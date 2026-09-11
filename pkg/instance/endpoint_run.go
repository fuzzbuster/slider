package instance

import (
	"errors"
	"fmt"
	"net"
	"sync"

	"slider/pkg/slog"
)

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
	if si.run != nil {
		si.instanceMutex.Unlock()
		_ = listener.Close()
		return fmt.Errorf("endpoint is already running")
	}
	si.run = run
	endpointPort := listener.Addr().(*net.TCPAddr).Port
	si.port = endpointPort
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

func (si *Config) IsEnabled() bool {
	si.instanceMutex.RLock()
	defer si.instanceMutex.RUnlock()
	return si.run != nil
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
	if si.run == nil {
		return 0, fmt.Errorf("endpoint is not running")
	}
	return si.port, nil
}

func (si *Config) Stop() error {
	si.instanceMutex.RLock()
	run := si.run
	si.instanceMutex.RUnlock()
	if run == nil {
		return fmt.Errorf("endpoint is not running")
	}

	si.Logger.DebugWith("Stopping endpoint",
		slog.F("session_id", si.SessionID),
		slog.F("endpoint_type", si.EndpointType))
	run.stop()
	<-run.done
	return nil
}
