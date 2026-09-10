package sshservice

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"

	"slider/pkg/conf"
	"slider/pkg/instance/portforward"
	"slider/pkg/scrypt"
	"slider/pkg/sio"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

type ChannelOpener interface {
	OpenChannel(name string, payload []byte) (ssh.Channel, <-chan *ssh.Request, error)
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
}

type Config struct {
	Logger             *slog.Logger
	SessionID          int64
	ServerKey          ssh.Signer
	AuthOn             bool
	AllowedFingerprint string
	PtyOn              bool
	Opener             ChannelOpener
	PortFwdManager     *portforward.Manager
}

// Service owns the protocol handling for one SSH endpoint.
type Service struct {
	logger             *slog.Logger
	sessionID          int64
	serverKey          ssh.Signer
	authOn             bool
	allowedFingerprint string
	ptyOn              bool
	useAltShell        bool
	envVarList         []struct{ Key, Value string }
	opener             ChannelOpener
	portFwdManager     *portforward.Manager
	mutex              sync.RWMutex
}

func NewService(cfg *Config) *Service {
	return &Service{
		logger:             cfg.Logger,
		sessionID:          cfg.SessionID,
		serverKey:          cfg.ServerKey,
		authOn:             cfg.AuthOn,
		allowedFingerprint: cfg.AllowedFingerprint,
		ptyOn:              cfg.PtyOn,
		opener:             cfg.Opener,
		portFwdManager:     cfg.PortFwdManager,
	}
}

func (s *Service) SetAllowedFingerprint(fingerprint string) {
	s.mutex.Lock()
	s.allowedFingerprint = fingerprint
	s.mutex.Unlock()
}

func (s *Service) SetPtyOn(ptyOn bool) {
	s.mutex.Lock()
	s.ptyOn = ptyOn
	s.mutex.Unlock()
}

func (s *Service) SetUseAltShell(useAltShell bool) {
	s.mutex.Lock()
	s.useAltShell = useAltShell
	s.mutex.Unlock()
}

func (s *Service) SetEnvVarList(envVarList []struct{ Key, Value string }) {
	s.mutex.Lock()
	s.envVarList = append(s.envVarList[:0], envVarList...)
	s.mutex.Unlock()
}

func (s *Service) Serve(conn net.Conn) error {
	defer func() { _ = conn.Close() }()

	s.mutex.RLock()
	serverKey := s.serverKey
	authOn := s.authOn
	opener := s.opener
	s.mutex.RUnlock()
	if serverKey == nil {
		return fmt.Errorf("SSH server key is not configured")
	}
	if opener == nil {
		return fmt.Errorf("SSH channel opener is not configured")
	}

	sshConfig := &ssh.ServerConfig{NoClientAuth: !authOn}
	if authOn {
		sshConfig.PublicKeyCallback = s.clientVerification
	}
	sshConfig.AddHostKey(serverKey)

	serverConn, channels, requests, err := ssh.NewServerConn(conn, sshConfig)
	if err != nil {
		return fmt.Errorf("SSH handshake failed: %w", err)
	}
	defer func() {
		_ = serverConn.Close()
		if s.portFwdManager != nil {
			s.portFwdManager.CancelAllSSHRemoteForwards()
		}
	}()

	go s.handleGlobalRequests(serverConn, requests)
	for channel := range channels {
		go s.handleChannel(serverConn, channel)
	}
	return nil
}

func (s *Service) Close() error {
	if s.portFwdManager != nil {
		s.portFwdManager.CloseAll()
	}
	return nil
}

func (s *Service) handleGlobalRequests(serverConn *ssh.ServerConn, requests <-chan *ssh.Request) {
	for request := range requests {
		switch request.Type {
		case conf.SSHRequestKeepAlive:
			if request.WantReply {
				_ = request.Reply(true, nil)
			}
		case conf.SSHRequestTcpIpForward, conf.SSHRequestSliderTCPIPForward:
			if s.portFwdManager == nil {
				s.rejectRequest(request)
				continue
			}
			go s.portFwdManager.HandleTCPIPForwardRequest(request, serverConn)
		case conf.SSHRequestCancelTcpIpForward:
			if s.portFwdManager == nil {
				s.rejectRequest(request)
				continue
			}
			s.portFwdManager.HandleCancelTCPIPForwardRequest(request)
		case conf.SSHRequestSliderUDPForward:
			if s.portFwdManager == nil {
				s.rejectRequest(request)
				continue
			}
			go s.portFwdManager.HandleUDPForwardRequest(request, serverConn)
		default:
			s.rejectRequest(request)
		}
	}
}

func (s *Service) handleChannel(serverConn *ssh.ServerConn, newChannel ssh.NewChannel) {
	var err error
	switch newChannel.ChannelType() {
	case conf.SSHChannelSession:
		var channel ssh.Channel
		var requests <-chan *ssh.Request
		channel, requests, err = newChannel.Accept()
		if err == nil {
			s.handleSessionRequests(serverConn, channel, requests)
		}
	case conf.SSHChannelDirectTCPIP, conf.SSHChannelForwardedTCPIP:
		if s.portFwdManager == nil {
			err = fmt.Errorf("port forwarding manager is not configured")
			break
		}
		err = s.portFwdManager.HandleDirectTCPIPChannel(newChannel)
	case conf.SSHChannelDirectUDP:
		if s.portFwdManager == nil {
			err = fmt.Errorf("port forwarding manager is not configured")
			break
		}
		err = s.portFwdManager.HandleDirectUDPChannel(newChannel)
	case conf.SSHChannelForwardedUDP:
		err = sio.HandleForwardedUDPChannel(
			newChannel,
			s.logger,
			s.sessionID,
			conf.ForwardingProtocolUDP,
		)
	default:
		err = newChannel.Reject(ssh.UnknownChannelType, "")
	}

	if err != nil {
		s.logger.ErrorWith("Failed to handle SSH endpoint channel",
			slog.F("session_id", s.sessionID),
			slog.F("channel_type", newChannel.ChannelType()),
			slog.F("err", err))
	}
}

func (s *Service) handleSessionRequests(
	serverConn *ssh.ServerConn,
	clientChannel ssh.Channel,
	requests <-chan *ssh.Request,
) {
	defer func() { _ = clientChannel.Close() }()

	winChange := make(chan []byte, 10)
	envChange := make(chan []byte, 10)
	defer close(winChange)
	defer close(envChange)

	externalPtyRequested := false
	started := false
	var pendingEnv [][]byte
	for request := range requests {
		ok := false
		switch request.Type {
		case conf.SSHRequestPTY:
			if s.isPtyOn() {
				ok = true
				externalPtyRequested = true
				s.sendInitTermSize(request.Payload)
			}
		case conf.SSHRequestEnv:
			ok = true
			if started {
				envChange <- request.Payload
			} else {
				pendingEnv = append(pendingEnv, request.Payload)
			}
		case conf.SSHRequestShell, conf.SSHRequestExec:
			if started {
				break
			}
			ok = true
			started = true
			initialEnv := append([][]byte(nil), pendingEnv...)
			for _, envVar := range s.getEnvVars(externalPtyRequested) {
				initialEnv = append(initialEnv, ssh.Marshal(envVar))
			}
			go s.interactiveChannelPipe(
				clientChannel,
				request.Type,
				request.Payload,
				initialEnv,
				winChange,
				envChange,
			)
		case conf.SSHRequestWindowChange:
			if s.isPtyOn() {
				ok = true
			}
			winChange <- request.Payload
		case conf.SSHRequestSubsystem:
			subsystem, err := types.ParseSSHString(request.Payload)
			if err == nil && subsystem == conf.SSHChannelSFTP {
				ok = true
				go s.channelPipe(clientChannel, conf.SSHChannelSFTP, nil)
			}
		case conf.SSHRequestTcpIpForward, conf.SSHRequestSliderTCPIPForward:
			if s.portFwdManager != nil {
				go s.portFwdManager.HandleTCPIPForwardRequest(request, serverConn)
				continue
			}
		case conf.SSHRequestCancelTcpIpForward:
			if s.portFwdManager != nil {
				s.portFwdManager.HandleCancelTCPIPForwardRequest(request)
				continue
			}
		case conf.SSHRequestSliderUDPForward:
			if s.portFwdManager != nil {
				go s.portFwdManager.HandleUDPForwardRequest(request, serverConn)
				continue
			}
		case conf.SSHRequestKeepAlive:
			ok = true
		}

		if request.WantReply {
			_ = request.Reply(ok, nil)
		}
	}
}

func (s *Service) rejectRequest(request *ssh.Request) {
	if request.WantReply {
		_ = request.Reply(false, nil)
	}
}

func (s *Service) sendInitTermSize(payload []byte) {
	var ptyRequest types.PtyRequest
	if err := ssh.Unmarshal(payload, &ptyRequest); err != nil {
		return
	}

	initSize, err := json.Marshal(types.TermDimensions{
		Width:  ptyRequest.TermWidthCols,
		Height: ptyRequest.TermHeightRows,
		X:      ptyRequest.TermWidthPixels,
		Y:      ptyRequest.TermHeightPixels,
	})
	if err != nil {
		return
	}

	s.mutex.RLock()
	opener := s.opener
	s.mutex.RUnlock()
	channel, requests, err := opener.OpenChannel(conf.SSHChannelInitSize, initSize)
	if err != nil {
		return
	}
	defer func() { _ = channel.Close() }()
	go ssh.DiscardRequests(requests)
}

func (s *Service) getEnvVars(externalPtyRequested bool) []struct{ Key, Value string } {
	s.mutex.RLock()
	envVars := append([]struct{ Key, Value string }(nil), s.envVarList...)
	useAltShell := s.useAltShell
	s.mutex.RUnlock()

	if useAltShell {
		envVars = append(envVars, struct{ Key, Value string }{
			Key:   conf.SliderAltShellEnvVar,
			Value: "true",
		})
	}
	if externalPtyRequested {
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

func (s *Service) channelPipe(clientChannel ssh.Channel, channelType string, payload []byte) {
	s.mutex.RLock()
	opener := s.opener
	s.mutex.RUnlock()
	serverChannel, requests, err := opener.OpenChannel(channelType, payload)
	if err != nil {
		s.logger.ErrorWith("Failed to open proxied SSH channel",
			slog.F("session_id", s.sessionID),
			slog.F("channel_type", channelType),
			slog.F("err", err))
		_ = clientChannel.Close()
		return
	}
	defer func() { _ = serverChannel.Close() }()
	go ssh.DiscardRequests(requests)
	_, _ = sio.PipeWithCancel(clientChannel, serverChannel)
}

func (s *Service) interactiveChannelPipe(
	clientChannel ssh.Channel,
	channelType string,
	payload []byte,
	initialEnv [][]byte,
	winChange <-chan []byte,
	envChange <-chan []byte,
) {
	s.mutex.RLock()
	opener := s.opener
	s.mutex.RUnlock()
	serverChannel, requests, err := opener.OpenChannel(channelType, payload)
	if err != nil {
		s.logger.ErrorWith("Failed to open proxied SSH channel",
			slog.F("session_id", s.sessionID),
			slog.F("channel_type", channelType),
			slog.F("err", err))
		_ = clientChannel.Close()
		return
	}
	defer func() { _ = serverChannel.Close() }()

	for _, envPayload := range initialEnv {
		if _, err := serverChannel.SendRequest(conf.SSHRequestEnv, true, envPayload); err != nil {
			return
		}
	}
	go forwardRequests(serverChannel, conf.SSHRequestWindowChange, winChange)
	go forwardRequests(serverChannel, conf.SSHRequestEnv, envChange)
	s.pipeChannelWithStatus(clientChannel, serverChannel, requests)
}

func forwardRequests(channel ssh.Channel, requestType string, payloads <-chan []byte) {
	for payload := range payloads {
		_, _ = channel.SendRequest(requestType, true, payload)
	}
}

func (s *Service) pipeChannelWithStatus(
	clientChannel ssh.Channel,
	serverChannel ssh.Channel,
	serverRequests <-chan *ssh.Request,
) (int64, int64) {
	var bytesToServer int64
	var bytesToClient int64
	var wg sync.WaitGroup
	copyDone := make(chan struct{}, 2)
	wg.Add(2)

	go func() {
		defer wg.Done()
		bytesToClient, _ = io.Copy(clientChannel, serverChannel)
		copyDone <- struct{}{}
	}()
	go func() {
		defer wg.Done()
		bytesToServer, _ = io.Copy(serverChannel, clientChannel)
		copyDone <- struct{}{}
	}()

	for {
		select {
		case <-copyDone:
			_ = clientChannel.Close()
			_ = serverChannel.Close()
			wg.Wait()
			return bytesToServer, bytesToClient
		case request, ok := <-serverRequests:
			if !ok {
				_ = clientChannel.Close()
				_ = serverChannel.Close()
				wg.Wait()
				return bytesToServer, bytesToClient
			}
			switch request.Type {
			case conf.SSHRequestExitStatus, conf.SSHRequestExitSignal:
				_, _ = clientChannel.SendRequest(request.Type, false, request.Payload)
				_ = clientChannel.Close()
				_ = serverChannel.Close()
				wg.Wait()
				return bytesToServer, bytesToClient
			}
			if request.WantReply {
				_ = request.Reply(true, nil)
			}
		}
	}
}

func (s *Service) isPtyOn() bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.ptyOn
}

func (s *Service) clientVerification(
	conn ssh.ConnMetadata,
	key ssh.PublicKey,
) (*ssh.Permissions, error) {
	fingerprint, err := scrypt.GenerateFingerprint(key)
	if err != nil {
		return nil, err
	}

	s.mutex.RLock()
	allowedFingerprint := s.allowedFingerprint
	s.mutex.RUnlock()
	if fingerprint != allowedFingerprint {
		return nil, fmt.Errorf("client key not authorized")
	}

	s.logger.DebugWith("Authenticated SSH endpoint client",
		slog.F("session_id", s.sessionID),
		slog.F("remote_addr", conn.RemoteAddr()),
		slog.F("fingerprint", fingerprint))
	return &ssh.Permissions{
		Extensions: map[string]string{"fingerprint": fingerprint},
	}, nil
}
