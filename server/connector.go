package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"slider/pkg/conf"
	"slider/pkg/listener"
	"slider/pkg/scrypt"
	"slider/pkg/session"
	"slider/pkg/slog"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

type connectorSecurity struct {
	fingerprint string
	caPath      string
	serverName  string
	tlsCertPath string
	tlsKeyPath  string
}

func (s *server) newConnector(
	targetURL *url.URL,
	notifier chan error,
	certID int64,
	customDNS string,
	customProto string,
	security connectorSecurity,
	operation string,
) {
	targetHost := targetURL.Hostname()
	targetPort := targetURL.Port()

	resolvedHost := targetHost
	if customDNS != "" {
		ip, err := conf.CustomResolver(customDNS, targetHost)
		if err != nil {
			s.ErrorWith("Failed to resolve host:", slog.F("host", targetHost), slog.F("err", err))
			notifier <- err
			return
		}
		resolvedHost = ip
	}

	if s.isSelfConnection(resolvedHost, targetPort) {
		err := fmt.Errorf("cannot connect to self (target=%s:%s, server=%s:%d)", targetHost, targetPort, s.host, s.port)
		s.WarnWith("Self-connection attempt blocked",
			slog.F("target", targetURL.String()),
			slog.F("server_host", s.host),
			slog.F("server_port", s.port))
		notifier <- err
		return
	}

	wsURL, err := listener.FormatToWS(targetURL)
	if err != nil {
		notifier <- err
		return
	}
	if operation == conf.OperationOperator && security.fingerprint == "" {
		notifier <- fmt.Errorf("gateway connection requires an SSH fingerprint")
		return
	}
	if (operation == conf.OperationOperator || operation == conf.OperationCallback) &&
		(!s.authOn || certID == 0) {
		notifier <- fmt.Errorf("gateway and callback connections require authentication and a certificate ID")
		return
	}
	if operation != conf.OperationOperator && wsURL.Scheme != "wss" {
		notifier <- fmt.Errorf("listener and callback connections require HTTPS")
		return
	}

	wsURLString := wsURL.String()
	if customDNS != "" {
		ip, err := conf.CustomResolver(customDNS, targetURL.Hostname())
		if err != nil {
			notifier <- err
			return
		}
		wsURLString = strings.Replace(wsURL.String(), targetURL.Hostname(), ip, 1)
		s.DebugWith("Connecting to client", slog.F("url", wsURL), slog.F("resolved_ip", ip))
	}

	wsConfig := listener.NewWebSocketDialer()
	if wsURL.Scheme == "wss" {
		wsConfig.TLSClientConfig.ServerName = security.serverName
		if wsConfig.TLSClientConfig.ServerName == "" {
			wsConfig.TLSClientConfig.ServerName = targetURL.Hostname()
		}
		if security.caPath != "" {
			caPEM, err := os.ReadFile(security.caPath)
			if err != nil {
				notifier <- fmt.Errorf("failed to read server CA: %w", err)
				return
			}
			rootCAs := x509.NewCertPool()
			if !rootCAs.AppendCertsFromPEM(caPEM) {
				notifier <- fmt.Errorf("failed to parse server CA")
				return
			}
			wsConfig.TLSClientConfig.RootCAs = rootCAs
		}
		if security.tlsCertPath != "" && security.tlsKeyPath != "" {
			cert, err := tls.LoadX509KeyPair(security.tlsCertPath, security.tlsKeyPath)
			if err != nil {
				notifier <- err
				return
			}
			wsConfig.TLSClientConfig.Certificates = []tls.Certificate{cert}
		} else if security.tlsCertPath != "" || security.tlsKeyPath != "" {
			notifier <- fmt.Errorf("TLS client certificate and key must be provided together")
			return
		}
	}

	wsConn, _, err := wsConfig.DialContext(context.Background(), wsURLString, http.Header{
		"Sec-WebSocket-Protocol":  {customProto},
		"Sec-WebSocket-Operation": {operation},
	})
	if err != nil {
		s.ErrorWith("Failed to open WebSocket connection", slog.F("url", wsURL), slog.F("err", err))
		notifier <- err
		return
	}
	defer func() { _ = wsConn.Close() }()

	sshConfig := *s.sshConf
	var connectorSigner ssh.Signer
	if certID != 0 {
		keyPair, err := s.getCert(certID)
		if err != nil {
			notifier <- err
			return
		}
		connectorSigner, err = scrypt.SignerFromKey(keyPair.PrivateKey)
		if err != nil {
			notifier <- err
			return
		}
		sshConfig.AddHostKey(connectorSigner)
	}

	opts := &session.ServerSessionOptions{
		CertificateAuthority: s.CertificateAuthority,
		ServerKey:            s.serverKey,
		AuthOn:               s.authOn,
	}
	biSession := s.newConnectorSession(
		wsConn,
		&sshConfig,
		wsConn.RemoteAddr().String(),
		opts,
		operation,
	)

	if certID != 0 {
		keyPair, _ := s.getCert(certID)
		biSession.SetCertInfo(certID, keyPair.FingerPrint)
	}

	s.addSession(biSession)
	defer s.dropWebSocketSession(biSession)

	biSession.AddNotifier(notifier)
	biSession.SetSSHConfig(&sshConfig)

	if operation == conf.OperationOperator {
		s.NewSSHClient(
			biSession,
			hostKeyCallbackForFingerprint(security.fingerprint),
			connectorSigner,
		)
		return
	}
	s.NewSSHServer(biSession)
}

func (s *server) newConnectorSession(
	wsConn *websocket.Conn,
	sshConfig *ssh.ServerConfig,
	remoteAddr string,
	opts *session.ServerSessionOptions,
	operation string,
) *session.BidirectionalSession {
	switch operation {
	case conf.OperationOperator:
		result := session.NewServerToServerSession(
			s.Logger,
			wsConn,
			nil,
			s.serverInterpreter,
			remoteAddr,
			opts,
		)
		result.SetRole(session.OperatorConnector)
		result.SetPeerRole(session.GatewayListener)
		result.SetIsGateway(true)
		return result
	case conf.OperationCallback:
		result := session.NewServerToListenerSession(
			s.Logger,
			wsConn,
			nil,
			sshConfig,
			s.serverInterpreter,
			remoteAddr,
			opts,
		)
		result.SetRole(session.AgentConnector)
		result.SetPeerRole(session.OperatorListener)
		return result
	default:
		result := session.NewServerToListenerSession(
			s.Logger,
			wsConn,
			nil,
			sshConfig,
			s.serverInterpreter,
			remoteAddr,
			opts,
		)
		result.SetRole(session.OperatorConnector)
		result.SetPeerRole(session.AgentListener)
		return result
	}
}
