package instance

import (
	"crypto/tls"
	"fmt"

	"slider/pkg/slog"
)

func (si *Config) StartTLSEndpoint(port int) error {
	if err := si.validateEndpoint(); err != nil {
		return err
	}
	si.instanceMutex.RLock()
	certificateAuthority := si.CertificateAuthority
	cert := si.serverCertificate
	verifyClient := si.interactiveOn
	si.instanceMutex.RUnlock()

	if certificateAuthority == nil {
		return fmt.Errorf("certificate authority is not configured")
	}
	if cert == nil {
		var err error
		cert, err = certificateAuthority.CreateCertificate(true)
		if err != nil {
			return fmt.Errorf("failed to create server TLS certificate: %w", err)
		}
		si.setServerCertificate(cert)
		si.Logger.DebugWith("Created new TLS server certificate",
			slog.F("session_id", si.SessionID))
	}

	tlsConfig := certificateAuthority.GetTLSServerConfig(cert, verifyClient)
	listener, err := tls.Listen("tcp", fmt.Sprintf(":%d", port), tlsConfig)
	if err != nil {
		return fmt.Errorf("can not listen for connections: %w", err)
	}
	if !si.isExposed() {
		_ = listener.Close()
		listener, err = tls.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port), tlsConfig)
		if err != nil {
			return fmt.Errorf("can not listen for localhost connections: %w", err)
		}
	}

	si.SetTLSOn(true)
	return si.serveEndpoint(listener)
}
