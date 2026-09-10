package sshservice

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"slider/pkg/instance/portforward"
	"slider/pkg/scrypt"
	"slider/pkg/slog"

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
	connectionCounter  atomic.Uint64
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
	ownerID := s.connectionCounter.Add(1)
	defer func() {
		_ = serverConn.Close()
		if s.portFwdManager != nil {
			s.portFwdManager.CancelSSHRemoteForwards(ownerID)
		}
	}()

	go s.handleGlobalRequests(serverConn, requests, ownerID)
	for channel := range channels {
		go s.handleChannel(serverConn, channel, ownerID)
	}
	return nil
}

func (s *Service) Close() error {
	if s.portFwdManager != nil {
		s.portFwdManager.CloseAll()
	}
	return nil
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
