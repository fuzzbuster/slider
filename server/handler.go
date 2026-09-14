package server

import (
	"net/http"
	"strings"

	"slider/pkg/conf"
	"slider/pkg/listener"
	"slider/pkg/session"
	"slider/pkg/slog"
)

// buildRouter creates the HTTP router with all configured endpoints
func (s *server) buildRouter() http.Handler {
	consolePaths := s.controlPaths()
	mux := listener.NewRouter(&listener.RouterConfig{
		TemplatePath: s.templatePath,
		ServerHeader: s.serverHeader,
		StatusCode:   s.statusCode,
		UrlRedirect:  s.urlRedirect,
		HealthOn:     s.httpHealth,
		VersionOn:    s.httpVersion,
		ConsoleOn:    s.httpConsoleOn,
		AuthOn:       s.authOn,
		ConsolePaths: consolePaths,
	})

	if s.httpConsoleOn {
		mux.HandleFunc(consolePaths.ConsoleAssetsPath, s.handleConsoleAsset)
		if s.authOn {
			mux.HandleFunc(consolePaths.AuthPath, s.handleAuthPage)
			mux.HandleFunc(consolePaths.AuthChallengePath, s.handleAuthChallenge)
			mux.HandleFunc(consolePaths.AuthLoginPath, s.handleAuthToken)
			mux.HandleFunc(consolePaths.AuthLogoutPath, s.handleLogout)
			mux.Handle(consolePaths.ConsolePath, s.authMiddleware(http.HandlerFunc(s.handleConsolePage)))
		} else {
			mux.Handle(consolePaths.ConsolePath, http.HandlerFunc(s.handleConsolePage))
		}

		mux.HandleFunc(consolePaths.ConsoleWsPath, func(w http.ResponseWriter, r *http.Request) {
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				_ = s.handleWebSocketConsole(w, r)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("This endpoint requires a WebSocket connection."))
		})
	}

	acceptedOps := []string{conf.OperationAgent}
	if s.authOn {
		acceptedOps = append(acceptedOps, conf.OperationCallback)
	}
	if s.gateway && s.authOn {
		acceptedOps = append(acceptedOps, conf.OperationOperator)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if listener.IsSliderWebSocket(r, s.customProto, acceptedOps) {
			s.handleWebSocket(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *server) controlPaths() listener.ConsolePaths {
	if s.consolePaths.AuthPath == "" {
		return listener.DefaultConsolePaths()
	}
	return s.consolePaths
}

func (s *server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	wsConn, err := listener.NewWebSocketUpgrader().Upgrade(w, r, nil)
	if err != nil {
		s.ErrorWith("Failed to upgrade client", slog.F("host", r.Host), slog.F("err", err))
		return
	}
	defer func() { _ = wsConn.Close() }()

	opts := &session.ServerSessionOptions{
		CertificateAuthority: s.CertificateAuthority,
		ServerKey:            s.serverKey,
		AuthOn:               s.authOn,
	}
	biSession := session.NewServerFromClientSession(
		s.Logger,
		wsConn,
		nil,
		s.sshConf,
		s.serverInterpreter,
		wsConn.RemoteAddr().String(),
		opts,
	)

	switch r.Header.Get("Sec-WebSocket-Operation") {
	case conf.OperationAgent:
		if s.gateway {
			biSession.SetRole(session.GatewayListener)
		} else {
			biSession.SetRole(session.OperatorListener)
		}
		biSession.SetPeerRole(session.AgentConnector)
	case conf.OperationCallback:
		biSession.SetRole(session.OperatorListener)
		biSession.SetPeerRole(session.AgentConnector)
		biSession.SetIsGateway(true)
	case conf.OperationGateway, conf.OperationOperator:
		biSession.SetRole(session.AgentListener)
		biSession.SetPeerRole(session.OperatorConnector)
	default:
		biSession.SetRole(session.OperatorListener)
		biSession.SetPeerRole(session.AgentConnector)
	}

	s.addSession(biSession)
	defer func() {
		s.dropWebSocketSession(biSession)
		s.NotifyUpstreamDisconnect(biSession.GetID())
	}()

	if r.Header.Get("Sec-WebSocket-Operation") == conf.OperationCallback {
		s.NewSSHClient(biSession, s.authorizedHostKey, nil)
		return
	}

	s.NewSSHServer(biSession)
}
