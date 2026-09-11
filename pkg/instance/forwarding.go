package instance

import (
	"fmt"

	"slider/pkg/conf"
	"slider/pkg/instance/portforward"
	"slider/pkg/types"
)

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

func (si *Config) GetLocalPortMapping(
	protocol string,
	port uint32,
) (*portforward.LocalForward, error) {
	manager := si.GetPortForwardManager()
	if manager == nil {
		return nil, fmt.Errorf("port forwarding manager not initialized")
	}
	return manager.GetLocalMapping(protocol, port)
}

func (si *Config) StartLocalForwardingFromMsg(
	msg types.CustomTcpIpChannelMsg,
	notifier chan error,
) {
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

func (si *Config) GetRemotePortMapping(
	protocol string,
	port uint32,
) (*portforward.RemoteForward, error) {
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
