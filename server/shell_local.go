package server

import (
	"fmt"
	"time"

	"slider/pkg/conf"
	"slider/pkg/session"
	"slider/pkg/types"

	"golang.org/x/term"
)

func (c *ShellCommand) handleLocalShellKill(
	sess *session.BidirectionalSession,
	ui UserInterface,
	killSessionID int,
) error {
	if sess.GetShellInstance().IsEnabled() {
		if err := sess.GetShellInstance().Stop(); err != nil {
			return fmt.Errorf("error stopping shell server: %w", err)
		}
		ui.PrintSuccess("Shell Endpoint gracefully stopped")
		return nil
	}
	ui.PrintInfo("No Shell Server on Session ID %d", killSessionID)
	return nil
}

func (c *ShellCommand) handleLocalShell(
	svr *server,
	sess *session.BidirectionalSession,
	unified UnifiedSession,
	ui UserInterface,
	options shellCommandOptions,
) error {
	if sess.GetShellInstance().IsEnabled() {
		if port, err := sess.GetShellInstance().GetEndpointPort(); err == nil {
			return fmt.Errorf("shell endpoint already running on port: %d", port)
		}
		return nil
	}

	tlsOn := options.tlsOn
	if options.interactive {
		if !sess.IsPtyOn() {
			return fmt.Errorf("target does not support shell in interactive mode")
		}
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

	width, height := 80, 24
	if console, ok := ui.(*Console); ok {
		if rw, ok := console.ReadWriter.(interface{ Fd() uintptr }); ok {
			if w, h, err := term.GetSize(int(rw.Fd())); err == nil {
				width, height = w, h
			}
		}
	}
	sess.SetInitTermSize(types.TermDimensions{
		Width:  uint32(width),
		Height: uint32(height),
	})

	go func() {
		_ = sess.EnableShell(
			options.port,
			options.expose,
			tlsOn,
			options.interactive,
			options.useAltShell,
			notifier,
		)
	}()

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
			port, err := sess.GetShellInstance().GetEndpointPort()
			if port == 0 || err != nil {
				ui.FlatPrintf(".")
				continue
			}
			ui.PrintSuccess("Shell Endpoint running on port: %d", port)
			if options.interactive {
				tlsConfig, err := svr.NewClientTlsConfig()
				if err != nil {
					return fmt.Errorf("failed to create client TLS certificate: %w", err)
				}
				console, ok := ui.(*Console)
				if !ok {
					return fmt.Errorf("UI is not a Console")
				}
				interactiveConfig := &InteractiveConsole{
					Console:              console,
					BidirectionalSession: sess,
					port:                 port,
					tlsConfig:            tlsConfig,
					ui:                   ui,
					targetSystem:         unified.BaseInfo.System,
				}
				ui.PrintInfo("Connecting to Shell...")
				if err := interactiveConfig.Run(); err != nil {
					return err
				}
			}
			return nil
		case <-timeout:
			return fmt.Errorf("shell endpoint reached timeout trying to start")
		}
	}
}
