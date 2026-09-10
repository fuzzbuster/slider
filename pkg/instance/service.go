package instance

import (
	"errors"
	"fmt"
	"net"
	"sync"
)

// EndpointType identifies the protocol served by an endpoint listener.
type EndpointType string

const (
	SocksEndpoint EndpointType = "socks-endpoint"
	ShellEndpoint EndpointType = "shell-endpoint"
	SshEndpoint   EndpointType = "ssh-endpoint"
	ExecEndpoint  EndpointType = "exec-endpoint"
)

// EndpointService handles accepted connections for one endpoint protocol.
type EndpointService interface {
	Serve(net.Conn) error
	Close() error
}

// ServiceManager dispatches connections to statically registered endpoint services.
type ServiceManager struct {
	mutex    sync.RWMutex
	services map[EndpointType]EndpointService
}

func NewServiceManager() *ServiceManager {
	return &ServiceManager{
		services: make(map[EndpointType]EndpointService),
	}
}

func (m *ServiceManager) Register(endpointType EndpointType, service EndpointService) error {
	if endpointType == "" {
		return fmt.Errorf("endpoint type is empty")
	}
	if service == nil {
		return fmt.Errorf("service %q is nil", endpointType)
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()
	if _, exists := m.services[endpointType]; exists {
		return fmt.Errorf("service %q is already registered", endpointType)
	}
	m.services[endpointType] = service
	return nil
}

func (m *ServiceManager) Has(endpointType EndpointType) bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	_, exists := m.services[endpointType]
	return exists
}

func (m *ServiceManager) Serve(endpointType EndpointType, conn net.Conn) error {
	m.mutex.RLock()
	service, exists := m.services[endpointType]
	m.mutex.RUnlock()
	if !exists {
		return fmt.Errorf("service %q is not registered", endpointType)
	}
	return service.Serve(conn)
}

func (m *ServiceManager) CloseAll() error {
	m.mutex.RLock()
	services := make([]EndpointService, 0, len(m.services))
	for _, service := range m.services {
		services = append(services, service)
	}
	m.mutex.RUnlock()

	var errs []error
	for _, service := range services {
		errs = append(errs, service.Close())
	}
	return errors.Join(errs...)
}
