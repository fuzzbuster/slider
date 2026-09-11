package server

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"slider/pkg/conf"
	"slider/pkg/instance"
	"slider/pkg/session"
	"slider/pkg/sio"
)

type InteractiveConsole struct {
	*Console
	*session.BidirectionalSession
	port         int
	tlsConfig    *tls.Config
	ui           UserInterface
	targetSystem string
}

func (s *server) NewClientTlsConfig() (*tls.Config, error) {
	if s.CertificateAuthority == nil {
		return nil, errors.New("certificate authority not initialized")
	}

	cert, err := s.CertificateAuthority.CreateCertificate(false)
	if err != nil {
		return nil, err
	}
	return s.CertificateAuthority.GetTLSClientConfig(cert), nil
}

func (ic *InteractiveConsole) Run() error {
	conn, err := tls.Dial(
		"tcp",
		fmt.Sprintf("127.0.0.1:%d", ic.port),
		ic.tlsConfig,
	)
	if err != nil {
		return fmt.Errorf("failed to bind to port %d - %w", ic.port, err)
	}
	defer func() { _ = conn.Close() }()

	defer func() {
		if err := ic.GetShellInstance().Stop(); err != nil {
			ic.ui.PrintDebug("Failed to stop shell session: %v", err)
		}
		ic.ui.PrintInfo("Shell Endpoint gracefully stopped\n")
	}()

	ic.ui.PrintSuccess("Authenticated with mTLS")
	ic.ui.ScreenAlignment(os.Getenv(conf.SliderAlignConsoleShellEnvVar) == "true")

	done := make(chan struct{})
	if ic.ResizeChan != nil {
		go func() {
			for {
				select {
				case <-done:
					return
				case size := <-ic.ResizeChan:
					if shellInstance := ic.GetShellInstance(); shellInstance != nil && shellInstance.IsEnabled() {
						shellInstance.Resize(size.Width, size.Height)
					}
				}
			}
		}()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(ic.ReadWriter, conn)
		close(done)
	}()
	go func() {
		defer wg.Done()
		sio.CopyInteractiveCancellable(conn, ic.ReadWriter, done)
	}()
	wg.Wait()

	ic.ui.Reset()
	return nil
}

func (c *ShellCommand) runRemoteInteractiveShell(
	ic *InteractiveConsole,
	shellInstance *instance.Config,
) error {
	conn, err := tls.Dial(
		"tcp",
		fmt.Sprintf("127.0.0.1:%d", ic.port),
		ic.tlsConfig,
	)
	if err != nil {
		return fmt.Errorf("failed to bind to port %d - %w", ic.port, err)
	}
	defer func() { _ = conn.Close() }()

	defer func() {
		if err := shellInstance.Stop(); err != nil {
			ic.ui.PrintDebug("Failed to stop shell session: %v", err)
		}
		ic.ui.PrintInfo("Shell Endpoint gracefully stopped\n")
	}()

	ic.ui.PrintSuccess("Authenticated with mTLS")
	ic.ui.ScreenAlignment(os.Getenv(conf.SliderAlignConsoleShellEnvVar) == "true")

	done := make(chan struct{})
	if ic.ResizeChan != nil {
		go func() {
			for {
				select {
				case <-done:
					return
				case size := <-ic.ResizeChan:
					if shellInstance != nil && shellInstance.IsEnabled() {
						shellInstance.Resize(size.Width, size.Height)
					}
				}
			}
		}()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(ic.ReadWriter, conn)
		close(done)
	}()
	go func() {
		defer wg.Done()
		sio.CopyInteractiveCancellable(conn, ic.ReadWriter, done)
	}()
	wg.Wait()

	ic.ui.Reset()
	return nil
}
