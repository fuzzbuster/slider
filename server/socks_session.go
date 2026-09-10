package server

import (
	"fmt"
	"strings"
	"time"

	"slider/pkg/conf"
	"slider/pkg/instance"
	"slider/pkg/remote"
)

// killSessionSocksServer kills a SOCKS server for a specific session
func killSessionSocksServer(svr *server, ui UserInterface, sessionID int) error {
	var unified UnifiedSession
	var isRemote bool

	unifiedMap := svr.ResolveUnifiedSessions()
	if value, ok := unifiedMap[int64(sessionID)]; ok {
		if strings.HasPrefix(value.Role, "operator") {
			return fmt.Errorf("socks command not allowed against operator roles")
		}
		unified = value
		isRemote = unified.GatewayID != 0
	} else {
		return fmt.Errorf("unknown session ID %d", sessionID)
	}

	if !isRemote {
		session, err := svr.GetSession(int(unified.ActualID))
		if err != nil {
			return fmt.Errorf("local session %d not found", unified.ActualID)
		}

		if !session.GetSocksInstance().IsEnabled() {
			return fmt.Errorf("session %d does not have an active SOCKS server", sessionID)
		}

		if err := session.GetSocksInstance().Stop(); err != nil {
			return fmt.Errorf("failed to disable SOCKS: %w", err)
		}

		ui.PrintSuccess("SOCKS server stopped for session %d", sessionID)
		return nil
	}

	socksKey := unified.stateKey(remoteStateSocks)
	svr.remoteSessionsMutex.Lock()
	state, ok := svr.remoteSessions[socksKey]
	svr.remoteSessionsMutex.Unlock()

	if !ok || state.SocksInstance == nil || !state.SocksInstance.IsEnabled() {
		return fmt.Errorf("session %d does not have an active SOCKS server", sessionID)
	}

	if err := state.SocksInstance.Stop(); err != nil {
		return fmt.Errorf("failed to stop SOCKS: %w", err)
	}

	svr.remoteSessionsMutex.Lock()
	delete(svr.remoteSessions, socksKey)
	svr.remoteSessionsMutex.Unlock()

	ui.PrintSuccess("SOCKS server stopped for session %d", sessionID)
	return nil
}

// createSessionSocksServer creates a SOCKS server for a specific session
func createSessionSocksServer(
	svr *server,
	ui UserInterface,
	sessionID int,
	port int,
	expose bool,
) error {
	var unified UnifiedSession
	var isRemote bool

	unifiedMap := svr.ResolveUnifiedSessions()
	if value, ok := unifiedMap[int64(sessionID)]; ok {
		if strings.HasPrefix(value.Role, "operator") {
			return fmt.Errorf("socks command not allowed against operator roles")
		}
		unified = value
		isRemote = unified.GatewayID != 0
	} else {
		return fmt.Errorf("unknown session ID %d", sessionID)
	}

	if !isRemote {
		session, err := svr.GetSession(int(unified.ActualID))
		if err != nil {
			return fmt.Errorf("local session %d not found", unified.ActualID)
		}

		if session.GetSocksInstance().IsEnabled() {
			if port, pErr := session.GetSocksInstance().GetEndpointPort(); pErr == nil {
				return fmt.Errorf("socks endpoint already running on port: %d", port)
			}
			return nil
		}
		ui.PrintInfo("Enabling Socks Endpoint in the background")

		notifier := make(chan error, 1)
		defer close(notifier)
		socksTicker := time.NewTicker(conf.EndpointTickerInterval)
		defer socksTicker.Stop()
		timeout := time.After(conf.Timeout)

		go func() { _ = session.EnableSocks(port, expose, notifier) }()

		for {
			select {
			case err := <-notifier:
				if err != nil {
					return fmt.Errorf("endpoint error: %w", err)
				}
			case <-socksTicker.C:
				if session.GetSocksInstance() != nil {
					port, portErr := session.GetSocksInstance().GetEndpointPort()
					if port == 0 || portErr != nil {
						continue
					}
					ui.PrintSuccess("Socks Endpoint running on port: %d", port)
					return nil
				}
			case <-timeout:
				return fmt.Errorf("socks endpoint reached timeout trying to start")
			}
		}
	}

	key := unified.stateKey(remoteStateSocks)
	svr.remoteSessionsMutex.Lock()
	if _, ok := svr.remoteSessions[key]; !ok {
		svr.remoteSessions[key] = &RemoteSessionState{}
	}
	state := svr.remoteSessions[key]
	svr.remoteSessionsMutex.Unlock()

	if state.SocksInstance != nil && state.SocksInstance.IsEnabled() {
		if port, pErr := state.SocksInstance.GetEndpointPort(); pErr == nil {
			return fmt.Errorf("socks endpoint already running on port: %d", port)
		}
		return nil
	}

	gatewaySession, err := svr.GetSession(int(unified.GatewayID))
	if err != nil {
		return fmt.Errorf("gateway session %d not found", unified.GatewayID)
	}

	target := append([]int64{}, unified.Path...)
	target = append(target, unified.ActualID)
	remoteConn := remote.NewProxy(gatewaySession, target)

	config := instance.New(&instance.Config{
		Logger:       svr.Logger,
		SessionID:    unified.UnifiedID,
		EndpointType: instance.SocksEndpoint,
	})
	config.SetSSHConn(remoteConn)
	config.SetExpose(expose)

	ui.PrintInfo("Enabling Remote Socks Endpoint in the background")

	notifier := make(chan error, 1)
	defer close(notifier)
	go func() {
		if err := config.StartEndpoint(port); err != nil {
			notifier <- err
		}
	}()

	svr.remoteSessionsMutex.Lock()
	state.SocksInstance = config
	svr.remoteSessionsMutex.Unlock()

	socksTicker := time.NewTicker(conf.EndpointTickerInterval)
	defer socksTicker.Stop()
	timeout := time.After(conf.Timeout)

	for {
		select {
		case err := <-notifier:
			if err != nil {
				svr.remoteSessionsMutex.Lock()
				state.SocksInstance = nil
				svr.remoteSessionsMutex.Unlock()
				return fmt.Errorf("failed to start remote socks: %w", err)
			}
		case <-socksTicker.C:
			port, portErr := config.GetEndpointPort()
			if port == 0 || portErr != nil {
				continue
			}
			ui.PrintSuccess("Remote Socks Endpoint running on port: %d Target: %s", port, target)
			return nil
		case <-timeout:
			return fmt.Errorf("remote socks endpoint reached timeout trying to start")
		}
	}
}
