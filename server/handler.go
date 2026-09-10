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
	mux := listener.NewRouter(&listener.RouterConfig{
		TemplatePath: s.templatePath,
		ServerHeader: s.serverHeader,
		StatusCode:   s.statusCode,
		UrlRedirect:  s.urlRedirect,
		HealthOn:     s.httpHealth,
		VersionOn:    s.httpVersion,
		ConsoleOn:    s.httpConsoleOn,
		AuthOn:       s.authOn,
	})

	if s.httpConsoleOn {
		if s.authOn {
			mux.HandleFunc(listener.AuthPath, s.handleAuthPage)
			mux.HandleFunc(listener.AuthChallengePath, s.handleAuthChallenge)
			mux.HandleFunc(listener.AuthLoginPath, s.handleAuthToken)
			mux.HandleFunc(listener.AuthLogoutPath, s.handleLogout)
			mux.Handle(listener.ConsolePath, s.authMiddleware(http.HandlerFunc(s.handleConsolePage)))
		} else {
			mux.Handle(listener.ConsolePath, http.HandlerFunc(s.handleConsolePage))
		}

		mux.HandleFunc(listener.ConsoleWsPath, func(w http.ResponseWriter, r *http.Request) {
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
