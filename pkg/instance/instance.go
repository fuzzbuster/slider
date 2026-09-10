package instance

import (
	"errors"
	"sync"

	"slider/pkg/instance/portforward"
	"slider/pkg/instance/shell"
	"slider/pkg/instance/socks"
	"slider/pkg/instance/sshservice"
	"slider/pkg/scrypt"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

type ChannelOpener interface {
	OpenChannel(name string, payload []byte) (ssh.Channel, <-chan *ssh.Request, error)
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
	Wait() error
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

func (si *Config) setServerCertificate(cert *scrypt.GeneratedCertificate) {
	si.instanceMutex.Lock()
	si.serverCertificate = cert
	si.instanceMutex.Unlock()
}
