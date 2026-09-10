package instance

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"slider/pkg/conf"
	"slider/pkg/instance/portforward"
	"slider/pkg/instance/shell"
	"slider/pkg/instance/socks"
	"slider/pkg/instance/sshservice"
	"slider/pkg/scrypt"
	"slider/pkg/sio"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

type ChannelOpener interface {
	OpenChannel(name string, payload []byte) (ssh.Channel, <-chan *ssh.Request, error)
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
	Wait() error
}

type endpointRun struct {
	listener    net.Listener
	done        chan struct{}
	stopOnce    sync.Once
	mutex       sync.Mutex
	stopped     bool
	connections map[net.Conn]struct{}
	wg          sync.WaitGroup
}

func newEndpointRun(listener net.Listener) *endpointRun {
	return &endpointRun{
		listener:    listener,
		done:        make(chan struct{}),
		connections: make(map[net.Conn]struct{}),
	}
}

func (r *endpointRun) addConnection(conn net.Conn) bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if r.stopped {
		_ = conn.Close()
		return false
	}
	r.connections[conn] = struct{}{}
	r.wg.Add(1)
	return true
}

func (r *endpointRun) removeConnection(conn net.Conn) {
	r.mutex.Lock()
	delete(r.connections, conn)
	r.mutex.Unlock()
	r.wg.Done()
}

func (r *endpointRun) stop() {
	r.stopOnce.Do(func() {
		r.mutex.Lock()
		r.stopped = true
		_ = r.listener.Close()
		for conn := range r.connections {
			_ = conn.Close()
		}
		r.mutex.Unlock()
	})
}

func (r *endpointRun) isStopped() bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.stopped
}

type Config struct {
	Logger               *slog.Logger
	SessionID            int64
	ServerKey            ssh.Signer
	AuthOn               bool
	EndpointType         EndpointType
	CertificateAuthority *scrypt.CertificateAuthority

	port               int
	allowedFingerprint string
	exposePort         bool
	ptyOn              bool
	enabled            bool
	tlsOn              bool
	interactiveOn      bool
	useAltShell        bool
	serverCertificate  *scrypt.GeneratedCertificate
	envVarList         []struct{ Key, Value string }
	initTermSize       *types.TermDimensions
	sshSessionConn     ChannelOpener
	portFwdManager     *portforward.Manager
	socksClient        *socks.Client
	shellService       *shell.Service
	sshService         *sshservice.Service
	serviceManager     *ServiceManager
	serviceErr         error
	run                *endpointRun
	instanceMutex      sync.RWMutex
}

func New(config *Config) *Config {
	config.envVarList = make([]struct{ Key, Value string }, 0)
	config.configureServices(config.sshSessionConn)
	return config
}

func (si *Config) configureServices(conn ChannelOpener) {
	manager := NewServiceManager()
	var serviceErrors []error

	si.portFwdManager = nil
	si.socksClient = nil
	si.shellService = nil
	si.sshService = nil

	if conn != nil && si.EndpointType != ExecEndpoint {
		si.socksClient = socks.NewClient(si.Logger, si.SessionID, conn)
		si.shellService = shell.NewService(si.Logger, si.SessionID, conn)
		si.shellService.SetEnvVarList(si.envVarList)
		si.shellService.SetUseAltShell(si.useAltShell)
		if si.initTermSize != nil {
			si.shellService.SetInitTermSize(*si.initTermSize)
		}
		serviceErrors = append(serviceErrors,
			manager.Register(SocksEndpoint, si.socksClient),
			manager.Register(ShellEndpoint, si.shellService),
		)
	}
	if conn != nil && si.EndpointType == SshEndpoint {
		si.portFwdManager = portforward.NewManager(si.Logger, si.SessionID, conn)
	}
	if conn != nil && si.EndpointType == SshEndpoint && si.ServerKey != nil {
		si.sshService = sshservice.NewService(&sshservice.Config{
			Logger:             si.Logger,
			SessionID:          si.SessionID,
			ServerKey:          si.ServerKey,
			AuthOn:             si.AuthOn,
			AllowedFingerprint: si.allowedFingerprint,
			PtyOn:              si.ptyOn,
			Opener:             conn,
			PortFwdManager:     si.portFwdManager,
		})
		si.sshService.SetEnvVarList(si.envVarList)
		si.sshService.SetUseAltShell(si.useAltShell)
		serviceErrors = append(serviceErrors, manager.Register(SshEndpoint, si.sshService))
	}
	si.serviceManager = manager
	si.serviceErr = errors.Join(serviceErrors...)
}

func (si *Config) SetExpose(expose bool) {
	si.instanceMutex.Lock()
	si.exposePort = expose
	si.instanceMutex.Unlock()
}

func (si *Config) SetPtyOn(ptyOn bool) {
	si.instanceMutex.Lock()
	si.ptyOn = ptyOn
	service := si.sshService
	si.instanceMutex.Unlock()
	if service != nil {
		service.SetPtyOn(ptyOn)
	}
}

func (si *Config) SetSSHConn(conn ChannelOpener) {
	si.instanceMutex.Lock()
	if si.serviceManager != nil {
		_ = si.serviceManager.CloseAll()
	}
	if si.portFwdManager != nil {
		si.portFwdManager.CloseAll()
	}
	si.sshSessionConn = conn
	si.configureServices(conn)
	si.instanceMutex.Unlock()
}

func (si *Config) GetSSHConn() ChannelOpener {
	si.instanceMutex.RLock()
	defer si.instanceMutex.RUnlock()
	return si.sshSessionConn
}

func (si *Config) GetPortForwardManager() *portforward.Manager {
	si.instanceMutex.RLock()
	defer si.instanceMutex.RUnlock()
	return si.portFwdManager
}

func (si *Config) SetTLSOn(tlsOn bool) {
	si.instanceMutex.Lock()
	si.tlsOn = tlsOn
	si.instanceMutex.Unlock()
}

func (si *Config) SetInteractiveOn(interactiveOn bool) {
	si.instanceMutex.Lock()
	si.interactiveOn = interactiveOn
	si.instanceMutex.Unlock()
}

func (si *Config) SetUseAltShell(useAltShell bool) {
	si.instanceMutex.Lock()
	si.useAltShell = useAltShell
	shellService := si.shellService
	sshService := si.sshService
	si.instanceMutex.Unlock()
	if shellService != nil {
		shellService.SetUseAltShell(useAltShell)
	}
	if sshService != nil {
		sshService.SetUseAltShell(useAltShell)
	}
}

func (si *Config) SetAllowedFingerprint(fingerprint string) {
	si.instanceMutex.Lock()
	si.allowedFingerprint = fingerprint
	service := si.sshService
	si.instanceMutex.Unlock()
	if service != nil {
		service.SetAllowedFingerprint(fingerprint)
	}
}

func (si *Config) SetEnvVarList(envVarList []struct{ Key, Value string }) {
	si.instanceMutex.Lock()
	si.envVarList = append(si.envVarList[:0], envVarList...)
	shellService := si.shellService
	sshService := si.sshService
	si.instanceMutex.Unlock()
	if shellService != nil {
		shellService.SetEnvVarList(envVarList)
	}
	if sshService != nil {
		sshService.SetEnvVarList(envVarList)
	}
}

func (si *Config) SetInitTermSize(size types.TermDimensions) {
	si.instanceMutex.Lock()
	si.initTermSize = &size
	service := si.shellService
	si.instanceMutex.Unlock()
	if service != nil {
		service.SetInitTermSize(size)
	}
}

func (si *Config) Resize(cols, rows uint32) {
	si.instanceMutex.RLock()
	service := si.shellService
	si.instanceMutex.RUnlock()
	if service != nil {
		service.Resize(cols, rows)
	}
}

func (si *Config) StartEndpoint(port int) error {
	if err := si.validateEndpoint(); err != nil {
		return err
	}
	si.SetTLSOn(false)
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("can not listen for connections: %w", err)
	}
	if !si.isExposed() {
		_ = listener.Close()
		listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return fmt.Errorf("can not listen for localhost connections: %w", err)
		}
	}
	return si.serveEndpoint(listener)
}

func (si *Config) serveEndpoint(listener net.Listener) error {
	if err := si.validateEndpoint(); err != nil {
		_ = listener.Close()
		return err
	}

	run := newEndpointRun(listener)
	si.instanceMutex.Lock()
	if si.enabled {
		si.instanceMutex.Unlock()
		_ = listener.Close()
		return fmt.Errorf("endpoint is already running")
	}
	si.run = run
	endpointPort := listener.Addr().(*net.TCPAddr).Port
	si.port = endpointPort
	si.enabled = true
	opener := si.sshSessionConn
	serviceManager := si.serviceManager
	portFwdManager := si.portFwdManager
	endpointType := si.EndpointType
	si.instanceMutex.Unlock()

	if opener != nil {
		go func() {
			_ = opener.Wait()
			run.stop()
		}()
	}

	defer func() {
		run.stop()
		run.wg.Wait()
		if serviceManager != nil {
			_ = serviceManager.CloseAll()
		}
		if portFwdManager != nil {
			portFwdManager.CloseAll()
		}
		si.instanceMutex.Lock()
		if si.run == run {
			si.run = nil
			si.port = 0
			si.enabled = false
			si.interactiveOn = false
		}
		si.instanceMutex.Unlock()
		close(run.done)
	}()

	si.Logger.DebugWith("Endpoint listening",
		slog.F("session_id", si.SessionID),
		slog.F("endpoint_type", si.EndpointType),
		slog.F("port", endpointPort))

	for {
		conn, err := listener.Accept()
		if err != nil {
			if run.isStopped() || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("failed to accept endpoint connection: %w", err)
		}
		if !run.addConnection(conn) {
			continue
		}
		go func() {
			defer run.removeConnection(conn)
			si.handleEndpointConnection(serviceManager, endpointType, conn)
		}()
	}
}

func (si *Config) validateEndpoint() error {
	si.instanceMutex.RLock()
	defer si.instanceMutex.RUnlock()

	if si.EndpointType == ExecEndpoint {
		return fmt.Errorf("exec instances do not expose listeners")
	}
	if si.serviceErr != nil {
		return si.serviceErr
	}
	if si.serviceManager == nil || !si.serviceManager.Has(si.EndpointType) {
		if si.EndpointType == "" {
			return fmt.Errorf("endpoint type is not configured")
		}
		return fmt.Errorf("unknown endpoint type %q", si.EndpointType)
	}
	return nil
}

func (si *Config) handleEndpointConnection(
	manager *ServiceManager,
	endpointType EndpointType,
	conn net.Conn,
) {
	if err := manager.Serve(endpointType, conn); err != nil {
		si.Logger.ErrorWith("Failed to handle endpoint connection",
			slog.F("session_id", si.SessionID),
			slog.F("endpoint_type", endpointType),
			slog.F("err", err))
		_ = conn.Close()
	}
}

func (si *Config) TcpIpForwardFromMsg(msg types.CustomTcpIpChannelMsg, notifier chan error) {
	manager := si.GetPortForwardManager()
	if manager == nil {
		notifier <- fmt.Errorf("port forwarding manager not initialized")
		return
	}
	manager.StartRemoteForward(msg, notifier)
}

func (si *Config) GetLocalMappings() map[string]*portforward.LocalForward {
	manager := si.GetPortForwardManager()
	if manager == nil {
		return make(map[string]*portforward.LocalForward)
	}
	return manager.GetLocalMappings()
}

func (si *Config) GetLocalPortMapping(protocol string, port uint32) (*portforward.LocalForward, error) {
	manager := si.GetPortForwardManager()
	if manager == nil {
		return nil, fmt.Errorf("port forwarding manager not initialized")
	}
	return manager.GetLocalMapping(protocol, port)
}

func (si *Config) StartLocalForwardingFromMsg(msg types.CustomTcpIpChannelMsg, notifier chan error) {
	manager := si.GetPortForwardManager()
	if manager == nil {
		notifier <- fmt.Errorf("port forwarding manager not initialized")
		return
	}
	if msg.Protocol == conf.ForwardingProtocolUDP {
		manager.StartLocalUDPForward(*msg.TcpIpChannelMsg, notifier)
	} else {
		manager.StartLocalForward(*msg.TcpIpChannelMsg, notifier)
	}
}

func (si *Config) GetRemoteMappings() map[string]*portforward.RemoteForward {
	manager := si.GetPortForwardManager()
	if manager == nil {
		return make(map[string]*portforward.RemoteForward)
	}
	return manager.GetRemoteMappings()
}

func (si *Config) GetRemotePortMapping(protocol string, port uint32) (*portforward.RemoteForward, error) {
	manager := si.GetPortForwardManager()
	if manager == nil {
		return nil, fmt.Errorf("port forwarding manager not initialized")
	}
	return manager.GetRemoteMapping(protocol, port)
}

func (si *Config) CancelMsgRemoteFwd(protocol string, port uint32) error {
	manager := si.GetPortForwardManager()
	if manager == nil {
		return fmt.Errorf("port forwarding manager not initialized")
	}
	return manager.CancelRemoteForward(protocol, port)
}

func (si *Config) ExecuteCommand(cmdBytes []byte, console io.ReadWriter) error {
	opener := si.GetSSHConn()
	if opener == nil {
		return fmt.Errorf("no active SSH connection available")
	}

	channel, requests, err := opener.OpenChannel(
		conf.SSHRequestExec,
		types.MarshalSSHString(string(cmdBytes)),
	)
	if err != nil {
		return fmt.Errorf("could not open ssh channel: %w", err)
	}
	defer func() { _ = channel.Close() }()
	go ssh.DiscardRequests(requests)

	go func() {
		for _, envVar := range si.executionEnvVars(true) {
			if _, err := channel.SendRequest(conf.SSHRequestEnv, true, ssh.Marshal(envVar)); err != nil {
				si.Logger.ErrorWith("Failed to send execution environment",
					slog.F("session_id", si.SessionID),
					slog.F("err", err))
			}
		}
	}()

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(console, channel)
		close(done)
	}()
	go func() {
		defer wg.Done()
		sio.CopyInteractiveCancellable(channel, console, done)
	}()
	wg.Wait()
	return nil
}

func (si *Config) executionEnvVars(withPty bool) []struct{ Key, Value string } {
	si.instanceMutex.RLock()
	envVars := append([]struct{ Key, Value string }(nil), si.envVarList...)
	useAltShell := si.useAltShell
	si.instanceMutex.RUnlock()

	if useAltShell {
		envVars = append(envVars, struct{ Key, Value string }{
			Key:   conf.SliderAltShellEnvVar,
			Value: "true",
		})
	}
	if withPty {
		envVars = append(envVars, struct{ Key, Value string }{
			Key:   conf.SliderExecPtyEnvVar,
			Value: "true",
		})
	}
	return append(envVars, struct{ Key, Value string }{
		Key:   conf.SliderCloserEnvVar,
		Value: "true",
	})
}

func (si *Config) IsEnabled() bool {
	si.instanceMutex.RLock()
	defer si.instanceMutex.RUnlock()
	return si.enabled
}

func (si *Config) isExposed() bool {
	si.instanceMutex.RLock()
	defer si.instanceMutex.RUnlock()
	return si.exposePort
}

func (si *Config) IsTLSOn() bool {
	si.instanceMutex.RLock()
	defer si.instanceMutex.RUnlock()
	return si.tlsOn
}

func (si *Config) GetEndpointPort() (int, error) {
	si.instanceMutex.RLock()
	defer si.instanceMutex.RUnlock()
	if !si.enabled {
		return 0, fmt.Errorf("endpoint is not running")
	}
	return si.port, nil
}

func (si *Config) Stop() error {
	si.instanceMutex.RLock()
	run := si.run
	enabled := si.enabled
	si.instanceMutex.RUnlock()
	if !enabled || run == nil {
		return fmt.Errorf("endpoint is not running")
	}

	si.Logger.DebugWith("Stopping endpoint",
		slog.F("session_id", si.SessionID),
		slog.F("endpoint_type", si.EndpointType))
	run.stop()
	<-run.done
	return nil
}

func (si *Config) setServerCertificate(cert *scrypt.GeneratedCertificate) {
	si.instanceMutex.Lock()
	si.serverCertificate = cert
	si.instanceMutex.Unlock()
}

func ParseSizePayload(sizeBytes []byte) (uint32, uint32, error) {
	if len(sizeBytes) < 8 {
		return 0, 0, fmt.Errorf("invalid window-change payload length: %d", len(sizeBytes))
	}
	cols := binary.BigEndian.Uint32(sizeBytes)
	rows := binary.BigEndian.Uint32(sizeBytes[4:])
	return cols, rows, nil
}
