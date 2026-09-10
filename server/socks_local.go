package server

import (
	"fmt"

	"slider/pkg/instance/socks"
)

// killLocalSocksServer stops and removes the local SOCKS server
func killLocalSocksServer(svr *server, ui UserInterface) error {
	svr.localSocks.mu.Lock()
	if svr.localSocks.server == nil {
		svr.localSocks.mu.Unlock()
		return fmt.Errorf("no local SOCKS server running")
	}

	err := svr.localSocks.server.Stop()
	svr.localSocks.server = nil
	svr.localSocks.port = 0
	svr.localSocks.mu.Unlock()

	if err != nil {
		return fmt.Errorf("error stopping SOCKS5 server: %w", err)
	}

	ui.PrintSuccess("Local SOCKS5 server stopped")
	return nil
}

// createLocalSocksServer creates a standalone local SOCKS server
func createLocalSocksServer(svr *server, ui UserInterface, port int, expose bool) error {
	svr.localSocks.mu.Lock()
	if svr.localSocks.port != 0 {
		existingPort := svr.localSocks.port
		svr.localSocks.mu.Unlock()
		return fmt.Errorf("local SOCKS server already running on port: %d", existingPort)
	}
	svr.localSocks.mu.Unlock()

	localSvr, err := socks.NewLocalServer(port, expose, svr.Logger)
	if err != nil {
		return err
	}

	svr.localSocks.mu.Lock()
	svr.localSocks.server = localSvr
	svr.localSocks.port = localSvr.Port()
	svr.localSocks.mu.Unlock()

	ui.PrintSuccess("Local listener started on port: %d", localSvr.Port())

	go func() {
		localSvr.Start()

		svr.localSocks.mu.Lock()
		svr.localSocks.server = nil
		svr.localSocks.port = 0
		svr.localSocks.mu.Unlock()
	}()

	return nil
}
