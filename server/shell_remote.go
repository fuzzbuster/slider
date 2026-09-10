package server

import (
	"fmt"
	"time"

	"slider/pkg/conf"
	"slider/pkg/instance"
	"slider/pkg/remote"
	"slider/pkg/types"

	"golang.org/x/term"
)

func (c *ShellCommand) handleRemoteShellKill(
	s *server,
	unified UnifiedSession,
	ui UserInterface,
	killSessionID int,
) error {
	key := unified.stateKey(remoteStateShell)

	s.remoteSessionsMutex.Lock()
	state, exists := s.remoteSessions[key]
	s.remoteSessionsMutex.Unlock()
	if !exists || state.ShellInstance == nil || !state.ShellInstance.IsEnabled() {
		ui.PrintInfo("No Shell Server on Session ID %d", killSessionID)
		return nil
	}
	if err := state.ShellInstance.Stop(); err != nil {
		return fmt.Errorf("error stopping shell server: %w", err)
	}

	s.remoteSessionsMutex.Lock()
	if state.SocksInstance == nil && state.SSHInstance == nil {
		delete(s.remoteSessions, key)
	} else {
		state.ShellInstance = nil
	}
	s.remoteSessionsMutex.Unlock()

	ui.PrintSuccess("Shell Endpoint gracefully stopped")
	return nil
}

func (c *ShellCommand) createRemoteShellInstance(
	s *server,
	unified UnifiedSession,
	proxy *remote.Proxy,
) *instance.Config {
	shellInstance := instance.New(&instance.Config{
		Logger:               s.Logger,
		SessionID:            unified.UnifiedID,
		EndpointType:         instance.ShellEndpoint,
		CertificateAuthority: s.CertificateAuthority,
	})
	shellInstance.SetSSHConn(proxy)
	return shellInstance
}

func (c *ShellCommand) enableRemoteShell(
	shellInstance *instance.Config,
	port int,
	expose bool,
	tlsOn bool,
	interactiveOn bool,
	notifier chan error,
) {
	shellInstance.SetExpose(expose)

	if tlsOn {
		shellInstance.SetTLSOn(tlsOn)
		if interactiveOn {
			shellInstance.SetInteractiveOn(interactiveOn)
			defer shellInstance.SetInteractiveOn(false)
		}
		if err := shellInstance.StartTLSEndpoint(port); err != nil && notifier != nil {
			notifier <- err
		}
		return
	}
	if err := shellInstance.StartEndpoint(port); err != nil && notifier != nil {
		notifier <- err
	}
}

func (c *ShellCommand) handleRemoteShell(
	s *server,
	unified UnifiedSession,
	ui UserInterface,
	port int,
	interactive bool,
	tlsOn bool,
	expose bool,
	useAltShell bool,
) error {
	key := unified.stateKey(remoteStateShell)

	s.remoteSessionsMutex.Lock()
	if _, ok := s.remoteSessions[key]; !ok {
		s.remoteSessions[key] = &RemoteSessionState{}
	}
	state := s.remoteSessions[key]
	s.remoteSessionsMutex.Unlock()

	if state.ShellInstance != nil && state.ShellInstance.IsEnabled() {
		if existingPort, err := state.ShellInstance.GetEndpointPort(); err == nil {
			return fmt.Errorf("shell endpoint already running on port: %d", existingPort)
		}
	}

	gatewaySession, err := s.GetSession(int(unified.GatewayID))
	if err != nil {
		return fmt.Errorf("gateway session %d not found (disconnected?)", unified.GatewayID)
	}
	target := append([]int64{}, unified.Path...)
	target = append(target, unified.ActualID)
	proxy := remote.NewProxy(gatewaySession, target)
	remoteShellInstance := c.createRemoteShellInstance(s, unified, proxy)

	s.remoteSessionsMutex.Lock()
	state.ShellInstance = remoteShellInstance
	s.remoteSessionsMutex.Unlock()

	if interactive {
		width, height := 80, 24
		if console, ok := ui.(*Console); ok {
			if rw, ok := console.ReadWriter.(interface{ Fd() uintptr }); ok {
				if w, h, err := term.GetSize(int(rw.Fd())); err == nil {
					width, height = w, h
				}
			}
		}
		remoteShellInstance.SetInitTermSize(types.TermDimensions{
			Width:  uint32(width),
			Height: uint32(height),
		})
		remoteShellInstance.SetUseAltShell(useAltShell)
		ui.PrintInfo("Enabling Interactive Shell Endpoint in the background")
		tlsOn = true
	} else {
		ui.PrintInfo("Enabling Shell Endpoint in the background")
	}

	notifier := make(chan error, 1)
	defer close(notifier)
	shellTicker := time.NewTicker(conf.EndpointTickerInterval)
	defer shellTicker.Stop()
	timeout := time.After(conf.Timeout)

	go c.enableRemoteShell(
		remoteShellInstance,
		port,
		expose,
		tlsOn,
		interactive,
		notifier,
	)

	for {
		select {
		case err := <-notifier:
			if err != nil {
				return fmt.Errorf("endpoint error: %w", err)
			}
		default:
		}

		select {
		case <-shellTicker.C:
			actualPort, err := remoteShellInstance.GetEndpointPort()
			if actualPort == 0 || err != nil {
				ui.FlatPrintf(".")
				continue
			}
			ui.PrintSuccess("Shell Endpoint running on port: %d", actualPort)
			if interactive {
				tlsConfig, err := s.NewClientTlsConfig()
				if err != nil {
					return fmt.Errorf("failed to create client TLS certificate: %w", err)
				}
				console, ok := ui.(*Console)
				if !ok {
					return fmt.Errorf("UI is not a Console")
				}
				interactiveConfig := &InteractiveConsole{
					Console:              console,
					BidirectionalSession: nil,
					port:                 actualPort,
					tlsConfig:            tlsConfig,
					ui:                   ui,
					targetSystem:         unified.BaseInfo.System,
				}
				ui.PrintInfo("Connecting to Shell...")
				if err := c.runRemoteInteractiveShell(interactiveConfig, remoteShellInstance); err != nil {
					return err
				}
			}
			return nil
		case <-timeout:
			return fmt.Errorf("shell endpoint reached timeout trying to start")
		}
	}
}
