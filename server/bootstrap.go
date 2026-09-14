package server

import (
	"net/http"
	"net/url"
	"os"

	"slider/pkg/conf"
	"slider/pkg/interpreter"
	"slider/pkg/listener"
	"slider/pkg/scrypt"
	"slider/pkg/session"
	"slider/pkg/slog"

	"golang.org/x/crypto/ssh"
)

func newConfiguredServer(cfg *Config) *server {
	logger := slog.NewLogger("Server")
	localInterpreter, err := interpreter.NewInterpreter()
	if err != nil {
		logger.Fatalf("%v", err)
	}

	configureServerLogger(logger, localInterpreter, cfg)
	if !localInterpreter.PtyOn && !cfg.Headless {
		logger.Warnf("This System does not support PTY, headless mode is enforced")
		cfg.Headless = true
	}

	s := &server{
		Logger: logger,
		sshConf: &ssh.ServerConfig{
			NoClientAuth:  true,
			ServerVersion: "SSH-slider-server",
		},
		sessionTrack: &sessionTrack{
			Sessions: make(map[int64]*session.BidirectionalSession),
		},
		console: Console{
			FirstRun: true,
			History:  session.DefaultHistory,
		},
		serverInterpreter: localInterpreter,
		certTrack: &scrypt.CertTrack{
			Certs: make(map[int64]*scrypt.KeyPair),
		},
		certJarFile:       cfg.CertJarFile,
		authOn:            cfg.Auth,
		host:              cfg.Address,
		port:              cfg.Port,
		caStoreOn:         cfg.CaStore,
		urlRedirect:       &url.URL{},
		serverHeader:      cfg.ServerHeader,
		httpVersion:       cfg.HttpVersion,
		httpHealth:        cfg.HttpHealth,
		httpConsoleOn:     cfg.HttpConsole || cfg.Headless,
		consolePaths:      listener.DefaultConsolePaths(),
		gateway:           cfg.Gateway,
		customProto:       cfg.CustomProto,
		commandRegistry:   newServerCommandRegistry(cfg.Auth),
		remoteSessions:    make(map[remoteStateKey]*RemoteSessionState),
		unifiedSessionIDs: make(map[SessionKey]int64),
		authChallenges:    make(map[string]authChallenge),
	}

	configureServerHTTP(s, cfg)
	configureServerIdentity(s, cfg)
	return s
}

func configureServerLogger(
	logger *slog.Logger,
	localInterpreter *interpreter.Interpreter,
	cfg *Config,
) {
	if cfg.JsonLog {
		logger.WithJSON(true)
	} else {
		logger.WithColors(localInterpreter.ColorOn && !cfg.Colorless)
	}
	if cfg.CallerLog {
		logger.WithCallerInfo(true)
	}
	if err := logger.SetLevel(cfg.Verbose); err != nil {
		logger.Fatalf("Wrong log level (%s): %v", cfg.Verbose, err)
	}
}

func configureServerHTTP(s *server, cfg *Config) {
	consolePaths, err := listener.NewConsolePaths(cfg.HttpConsoleBasePath)
	if err != nil {
		s.FatalWith("Bad HTTP console base path",
			slog.F("path", cfg.HttpConsoleBasePath),
			slog.F("err", err))
	}
	s.consolePaths = consolePaths

	if cfg.TemplatePath != "" {
		if err := listener.CheckTemplate(cfg.TemplatePath); err != nil {
			s.Fatalf("Wrong template: %s", err)
		}
		s.templatePath = cfg.TemplatePath
	} else {
		s.templatePath = "templates"
	}

	s.statusCode = cfg.StatusCode
	if !listener.CheckStatusCode(cfg.StatusCode) {
		s.Warnf("Invalid status code \"%d\", will use \"%d\"", cfg.StatusCode, http.StatusOK)
		s.statusCode = http.StatusOK
	}

	if cfg.Keepalive < conf.MinKeepAlive {
		s.Debugf("Overriding KeepAlive to minimum allowed \"%v\"", conf.MinKeepAlive)
		cfg.Keepalive = conf.MinKeepAlive
	}
	s.keepalive = cfg.Keepalive

	if cfg.HttpRedirect != "" {
		redirect, err := listener.ResolveURL(cfg.HttpRedirect)
		if err != nil {
			s.FatalWith("Bad Redirect URL", slog.F("url", cfg.HttpRedirect), slog.F("err", err))
		}
		s.urlRedirect = redirect
	}
}

func configureServerIdentity(s *server, cfg *Config) {
	var keyPair *scrypt.ServerKeyPair
	var err error
	if cfg.CaStore || cfg.CaStorePath != "" {
		keyPath := conf.GetSliderHome() + serverCertFile
		if cfg.CaStorePath != "" {
			keyPath = cfg.CaStorePath
		}

		if _, statErr := os.Stat(keyPath); os.IsNotExist(statErr) && !cfg.CaStore {
			s.FatalWith("Failed to load Server Key", slog.F("ca_store", keyPath))
		}
		keyPair, err = scrypt.ServerKeyPairFromFile(keyPath)
	} else {
		keyPair, err = scrypt.NewServerKeyPair()
	}
	if err != nil {
		s.FatalWith("Failed to initialize Server Key", slog.F("err", err))
	}

	s.serverKey, err = scrypt.SignerFromKey(keyPair.PrivateKey)
	if err != nil {
		s.FatalWith("Failed to create signer", slog.F("err", err))
	}
	s.CertificateAuthority = keyPair.CertificateAuthority
	s.sshConf.AddHostKey(s.serverKey)

	s.fingerprint, err = scrypt.GenerateFingerprint(s.serverKey.PublicKey())
	if err != nil {
		s.Fatalf("Failed to generate server fingerprint")
	}
	s.InfoWith("Initializing server", slog.F("fingerprint", s.fingerprint))

	if !cfg.Auth {
		if s.certJarFile != "" {
			s.WarnWith("Client Authentication is disabled, certificates will be ignored",
				slog.F("cert_jar", s.certJarFile))
		}
		return
	}

	s.Warnf("Client Authentication enabled, a valid certificate will be required")
	if s.certJarFile == "" {
		s.certJarFile = conf.GetSliderHome() + clientCertsFile
	}
	if err := s.loadCertJar(); err != nil {
		s.Fatalf("%v", err)
	}
	s.sshConf.NoClientAuth = false
	s.sshConf.PublicKeyCallback = s.clientVerification
}
