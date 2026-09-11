package server

import "time"

// Config holds all configuration for a server instance.
type Config struct {
	Verbose            string
	Address            string
	Port               int
	Keepalive          time.Duration
	Colorless          bool
	Auth               bool
	CertJarFile        string
	CaStore            bool
	CaStorePath        string
	TemplatePath       string
	ServerHeader       string
	HttpRedirect       string
	StatusCode         int
	HttpVersion        bool
	HttpHealth         bool
	CustomProto        string
	ListenerCert       string
	ListenerKey        string
	ListenerCA         string
	JsonLog            bool
	CallerLog          bool
	Headless           bool
	HttpConsole        bool
	Gateway            bool
	CallbackURL        string
	CallbackRetry      bool
	CallbackCertID     int64
	CallbackCA         string
	CallbackServerName string
	CallbackTLSCert    string
	CallbackTLSKey     string
}

func RunServer(cfg *Config) {
	s := newConfiguredServer(cfg)
	s.startHTTPListener(cfg)
	s.startStartupCallback(cfg)
	s.runUntilShutdown(cfg.Headless)
	s.Infof("Server down...")
}
