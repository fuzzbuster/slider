package portforward

import (
	"fmt"
	"io"
	"sync"

	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

// Manager handles port forwarding operations (both local and remote).
type Manager struct {
	logger         *slog.Logger
	sessionID      int64
	conn           ChannelOpener
	remoteMappings map[string]*RemoteForward
	localMappings  map[string]*LocalForward
	mutex          sync.Mutex
}

// ChannelOpener defines the interface for opening SSH channels and sending requests.
type ChannelOpener interface {
	OpenChannel(name string, payload []byte) (ssh.Channel, <-chan *ssh.Request, error)
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
}

// RemoteForward represents a remote (reverse) port forward.
type RemoteForward struct {
	RcvChan    chan *types.CustomTcpIpChannelMsg
	CancelChan chan struct{}
	cancelOnce sync.Once
	*types.CustomTcpIpChannelMsg
}

func (f *RemoteForward) cancel() {
	f.cancelOnce.Do(func() {
		close(f.CancelChan)
	})
}

// LocalForward represents a local port forward.
type LocalForward struct {
	RcvChan  chan *types.TcpIpChannelMsg
	DoneChan chan bool
	Listener io.Closer
	*types.CustomTcpIpChannelMsg
}

// NewManager creates a new port forwarding manager.
func NewManager(logger *slog.Logger, sessionID int64, conn ChannelOpener) *Manager {
	return &Manager{
		logger:         logger,
		sessionID:      sessionID,
		conn:           conn,
		remoteMappings: make(map[string]*RemoteForward),
		localMappings:  make(map[string]*LocalForward),
	}
}

func newProtocolPortKey(protocol string, port uint32) string {
	return fmt.Sprintf("%s:%d", protocol, port)
}

// AddRemoteForward adds a remote port forward mapping.
func (m *Manager) AddRemoteForward(
	message *types.TcpIpChannelMsg,
	isSSHConnection bool,
	protocol string,
) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	key := newProtocolPortKey(protocol, message.SrcPort)
	m.remoteMappings[key] = &RemoteForward{
		RcvChan:    make(chan *types.CustomTcpIpChannelMsg, 5),
		CancelChan: make(chan struct{}),
		CustomTcpIpChannelMsg: &types.CustomTcpIpChannelMsg{
			Protocol:        protocol,
			IsSshConn:       isSSHConnection,
			TcpIpChannelMsg: message,
		},
	}
}

// GetRemoteMappings returns all remote port forward mappings.
func (m *Manager) GetRemoteMappings() map[string]*RemoteForward {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	result := make(map[string]*RemoteForward, len(m.remoteMappings))
	for key, mapping := range m.remoteMappings {
		result[key] = mapping
	}
	return result
}

// GetRemoteMapping returns a specific remote port forward mapping.
func (m *Manager) GetRemoteMapping(protocol string, port uint32) (*RemoteForward, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	key := newProtocolPortKey(protocol, port)
	mapping, ok := m.remoteMappings[key]
	if !ok {
		return nil, fmt.Errorf("no remote mapping found for %s port %d", protocol, port)
	}
	return mapping, nil
}

// AddLocalForward adds a local port forward mapping.
func (m *Manager) AddLocalForward(
	message *types.TcpIpChannelMsg,
	listener io.Closer,
	isSSHConnection bool,
	protocol string,
) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	key := newProtocolPortKey(protocol, message.SrcPort)
	m.localMappings[key] = &LocalForward{
		DoneChan: make(chan bool, 1),
		Listener: listener,
		CustomTcpIpChannelMsg: &types.CustomTcpIpChannelMsg{
			Protocol:        protocol,
			IsSshConn:       isSSHConnection,
			TcpIpChannelMsg: message,
		},
	}
}

// GetLocalMappings returns all local port forward mappings.
func (m *Manager) GetLocalMappings() map[string]*LocalForward {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	result := make(map[string]*LocalForward, len(m.localMappings))
	for key, mapping := range m.localMappings {
		result[key] = mapping
	}
	return result
}

// GetLocalMapping returns a specific local port forward mapping.
func (m *Manager) GetLocalMapping(protocol string, port uint32) (*LocalForward, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	key := newProtocolPortKey(protocol, port)
	mapping, ok := m.localMappings[key]
	if !ok {
		return nil, fmt.Errorf("no local mapping found for %s port %d", protocol, port)
	}
	return mapping, nil
}

// RemoveLocalForward removes a local port forward mapping.
func (m *Manager) RemoveLocalForward(protocol string, port uint32) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	delete(m.localMappings, newProtocolPortKey(protocol, port))
}

// RemoveRemoteForward removes a remote port forward mapping.
func (m *Manager) RemoveRemoteForward(protocol string, port uint32) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	delete(m.remoteMappings, newProtocolPortKey(protocol, port))
}

// CloseAll closes all active port forwards (local and remote).
func (m *Manager) CloseAll() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for key, forward := range m.localMappings {
		if forward.Listener != nil {
			_ = forward.Listener.Close()
		}
		select {
		case forward.DoneChan <- true:
		default:
		}
		delete(m.localMappings, key)
	}

	for key, forward := range m.remoteMappings {
		forward.cancel()
		delete(m.remoteMappings, key)
	}
}
