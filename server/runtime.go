package server

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"slider/pkg/conf"
	"slider/pkg/listener"
	"slider/pkg/scrypt"
	"slider/pkg/slog"
)

func (s *server) startHTTPListener(cfg *Config) {
	address := fmt.Sprintf("%s:%d", cfg.Address, cfg.Port)
	serverAddr, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		s.Fatalf("Not a valid IP address \"%s\"", address)
	}

	tlsOn, tlsConfig := s.listenerTLSConfig(cfg)
	protocol := "tcp"
	if tlsOn {
		protocol = "tls"
	}

	s.Infof("Starting listener %s://%s", protocol, serverAddr.String())
	go func() {
		httpServer := &http.Server{
			Addr:      serverAddr.String(),
			Handler:   s.buildRouter(),
			TLSConfig: tlsConfig,
			ErrorLog:  slog.NewDummyLog(),
		}
		var serveErr error
		if tlsOn {
			serveErr = httpServer.ListenAndServeTLS(cfg.ListenerCert, cfg.ListenerKey)
		} else {
			serveErr = httpServer.ListenAndServe()
		}
		if serveErr != nil {
			s.FatalWith("Listener error", slog.F("err", serveErr))
		}
	}()
}

func (s *server) listenerTLSConfig(cfg *Config) (bool, *tls.Config) {
	if cfg.ListenerCert == "" || cfg.ListenerKey == "" {
		if s.authOn && s.httpConsoleOn {
			s.Fatalf("HTTP Console with authentication requires TLS")
		}
		return false, nil
	}

	tlsConfig := &tls.Config{}
	if cfg.ListenerCA != "" {
		caPEM, err := os.ReadFile(cfg.ListenerCA)
		if err != nil {
			s.FatalWith("Failed to read CA file",
				slog.F("ca", cfg.ListenerCA),
				slog.F("err", err))
		}
		if len(caPEM) == 0 {
			s.FatalWith("CA file is empty", slog.F("ca", cfg.ListenerCA))
		}
		tlsConfig = scrypt.GetTLSClientVerifiedConfig(caPEM)
	}
	return true, tlsConfig
}

func (s *server) startStartupCallback(cfg *Config) {
	if cfg.CallbackURL == "" {
		return
	}
	if !cfg.Gateway {
		s.Fatalf("--callback requires --gateway mode")
	}
	if !cfg.Auth || cfg.CallbackCertID == 0 {
		s.Fatalf("--callback requires --auth and --callback-cert-id")
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		callbackURL, err := listener.ResolveURL(cfg.CallbackURL)
		if err != nil {
			s.ErrorWith("Failed to resolve callback URL",
				slog.F("url", cfg.CallbackURL),
				slog.F("err", err))
			return
		}

		for {
			s.InfoWith("Initiating callback connection", slog.F("target", callbackURL.String()))
			notifier := make(chan error, 1)
			s.newConnector(
				callbackURL,
				notifier,
				cfg.CallbackCertID,
				"",
				s.customProto,
				connectorSecurity{
					caPath:      cfg.CallbackCA,
					serverName:  cfg.CallbackServerName,
					tlsCertPath: cfg.CallbackTLSCert,
					tlsKeyPath:  cfg.CallbackTLSKey,
				},
				conf.OperationCallback,
			)

			if err := <-notifier; err != nil {
				s.ErrorWith("Callback connection failed",
					slog.F("target", callbackURL.String()),
					slog.F("err", err))
			} else {
				s.InfoWith("Callback connection disconnected",
					slog.F("target", callbackURL.String()))
			}
			if !cfg.CallbackRetry {
				return
			}
			time.Sleep(s.keepalive)
		}
	}()
}

func (s *server) runUntilShutdown(headless bool) {
	if headless {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
		<-signals
		s.Infof("Received interrupt signal, shutting down...")
		return
	}

	s.Printf("Press CTR^C to access the Slider Console")
	for {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
		<-signals
		signal.Stop(signals)

		s.LogToBuffer()
		command := s.NewConsole()
		s.BufferOut()
		s.LogToStdout()
		if command == "exit" {
			return
		}
	}
}
