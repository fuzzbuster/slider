package instance

import (
	"net"
	"sync"
	"testing"

	"slider/pkg/slog"
)

type testEndpointService struct {
	mutex      sync.Mutex
	serveCount int
	closeCount int
}

func (s *testEndpointService) Serve(net.Conn) error {
	s.mutex.Lock()
	s.serveCount++
	s.mutex.Unlock()
	return nil
}

func (s *testEndpointService) Close() error {
	s.mutex.Lock()
	s.closeCount++
	s.mutex.Unlock()
	return nil
}

func TestServiceManagerLifecycle(t *testing.T) {
	manager := NewServiceManager()
	service := &testEndpointService{}

	if err := manager.Register("", service); err == nil {
		t.Fatal("empty endpoint type was accepted")
	}
	if err := manager.Register(ShellEndpoint, service); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(ShellEndpoint, service); err == nil {
		t.Fatal("duplicate service registration was accepted")
	}
	if err := manager.Serve(SocksEndpoint, nil); err == nil {
		t.Fatal("unregistered service lookup was accepted")
	}
	if !manager.Has(ShellEndpoint) {
		t.Fatal("registered service was not found")
	}

	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()
	if err := manager.Serve(ShellEndpoint, serverConn); err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseAll(); err != nil {
		t.Fatal(err)
	}

	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.serveCount != 1 || service.closeCount != 1 {
		t.Fatalf("serve=%d close=%d, want 1/1", service.serveCount, service.closeCount)
	}
}

func TestConfigRegistersFixedEndpointServices(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	config := New(&Config{
		Logger:         slog.NewLogger("service-registry-test"),
		EndpointType:   SocksEndpoint,
		sshSessionConn: &mockSSHConn{netConn: clientConn},
	})

	if !config.serviceManager.Has(ShellEndpoint) {
		t.Fatal("shell service is not registered")
	}
	if !config.serviceManager.Has(SocksEndpoint) {
		t.Fatal("SOCKS service is not registered")
	}
	if config.serviceManager.Has(SshEndpoint) {
		t.Fatal("SSH service registered without a server key")
	}
}
