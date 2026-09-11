package session

import (
	"encoding/json"
	"fmt"
	"os"

	"slider/pkg/conf"
	"slider/pkg/interpreter"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// handleSSHRequests processes incoming SSH requests (PTY, window-change, env)
// This is called as a goroutine to handle requests asynchronously
func (s *BidirectionalSession) handleSSHRequests(
	requests <-chan *ssh.Request,
	winChange chan<- []byte,
	envChange chan<- []byte,
) {
	defer close(winChange)
	defer close(envChange)
	for req := range requests {
		ok := false

		if req.Type != conf.SSHRequestEnv {
			s.logger.DebugWith("SSH Request",
				slog.F("session_id", s.sessionID),
				slog.F("request_type", req.Type),
				slog.F("payload", req.Payload))
		}

		switch req.Type {
		case conf.SSHRequestEnv:
			if req.WantReply {
				go func() { _ = req.Reply(ok, nil) }()
			}
			envChange <- req.Payload

		case conf.SSHRequestWindowChange:
			if s.peerBaseInfo.PtyOn {
				ok = true
			}
			if req.WantReply {
				go func() { _ = req.Reply(ok, nil) }()
			}
			winChange <- req.Payload

		case conf.SSHRequestPTY:
			var ptyReq types.PtyRequest
			if err := ssh.Unmarshal(req.Payload, &ptyReq); err != nil {
				s.logger.ErrorWith("Failed to unmarshal pty-req",
					slog.F("session_id", s.sessionID),
					slog.F("err", err))
			} else {
				s.logger.DebugWith("Received pty-req",
					slog.F("session_id", s.sessionID),
					slog.F("term", ptyReq.TermEnvVar),
					slog.F("cols", ptyReq.TermWidthCols),
					slog.F("rows", ptyReq.TermHeightRows))

				s.SetInitTermSize(types.TermDimensions{
					Width:  ptyReq.TermWidthCols,
					Height: ptyReq.TermHeightRows,
				})
				ok = true
			}

			if req.WantReply {
				go func() { _ = req.Reply(ok, nil) }()
			}

		default:
			s.logger.WarnWith("Rejected SSH request type",
				slog.F("session_id", s.sessionID),
				slog.F("request_type", req.Type))
			if req.WantReply {
				go func() { _ = req.Reply(ok, nil) }()
			}
		}
	}
}

func (s *BidirectionalSession) HandleIncomingRequests(requests <-chan *ssh.Request) {
	for req := range requests {
		go s.routeRequest(req)
	}
}

func (s *BidirectionalSession) routeRequest(req *ssh.Request) {
	s.logger.DebugWith("Routing request",
		slog.F("session_id", s.sessionID),
		slog.F("type", req.Type),
		slog.F("role", s.role.String()))

	switch req.Type {
	case conf.SSHRequestKeepAlive:
		s.handleKeepAlive(req)
	case conf.SSHRequestTcpIpForward:
		if s.role.IsAgent() || s.role.IsGateway() {
			go s.handleTcpIpForward(req)
		} else {
			s.rejectRequest(req, fmt.Sprintf("%s not supported in this role %s", conf.SSHRequestTcpIpForward, s.role.String()))
		}
	case conf.SSHRequestCancelTcpIpForward:
		if s.role.IsAgent() || s.role.IsGateway() {
			s.handleCancelTcpIpForward(req)
		} else {
			s.rejectRequest(req, fmt.Sprintf(
				"%s not supported in this role %s",
				conf.SSHRequestCancelTcpIpForward,
				s.role.String()))
		}
	case conf.SSHRequestClientInfo:
		s.handleClientInfo(req)
	case conf.SSHRequestWindowSize:
		s.handleWindowSize(req)
	case conf.SSHRequestSliderSessions:
		if (s.role.IsGateway() || s.role.IsAgent()) && s.applicationServer != nil {
			s.handleSliderSessions(req)
		} else {
			s.rejectRequest(req, "slider-sessions not supported in this configuration")
		}
	case conf.SSHRequestSliderTCPIPForward:
		if (s.role.IsGateway() || s.role.IsAgent()) && s.applicationServer != nil {
			s.handleSliderForwardRequest(req)
		} else {
			s.rejectRequest(req, fmt.Sprintf(
				"%s not supported in this role %s",
				conf.SSHRequestSliderTCPIPForward,
				s.role.String()))
		}
	case conf.SSHRequestSliderUDPForward:
		if s.role.IsAgent() || s.role.IsGateway() {
			go s.handleSliderUDPForwardRequest(req)
		} else {
			s.rejectRequest(req, fmt.Sprintf(
				"%s not supported in this role %s",
				conf.SSHRequestSliderUDPForward,
				s.role.String()))
		}
	case conf.SSHRequestSliderEvent:
		if (s.role.IsGateway() || s.role.IsAgent()) && s.applicationServer != nil {
			s.handleSliderEvent(req)
		} else {
			s.rejectRequest(req, fmt.Sprintf("%s not supported in this role %s", conf.SSHRequestSliderEvent, s.role.String()))
		}
	case conf.SSHRequestShutdown:
		s.handleShutdown(req)
	default:
		s.logger.DebugWith("Received unknown request type",
			slog.F("session_id", s.sessionID),
			slog.F("request_type", req.Type))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
	}
}

func (s *BidirectionalSession) rejectRequest(req *ssh.Request, reason string) {
	s.logger.WarnWith(reason,
		slog.F("session_id", s.sessionID),
		slog.F("request_type", req.Type),
		slog.F("role", s.role.String()))
	if req.WantReply {
		_ = req.Reply(false, nil)
	}
}

func (s *BidirectionalSession) handleKeepAlive(req *ssh.Request) {
	s.logger.DebugWith("Received keep-alive request",
		slog.F("session_id", s.sessionID))

	if err := s.ReplyConnRequest(req, true, nil); err != nil {
		s.logger.ErrorWith("Error sending keep-alive reply",
			slog.F("session_id", s.sessionID),
			slog.F("request_type", req.Type),
			slog.F("err", err))
	}
}

func (s *BidirectionalSession) handleClientInfo(req *ssh.Request) {
	ci := &interpreter.Info{}
	if err := json.Unmarshal(req.Payload, ci); err != nil {
		s.logger.DErrorWith("Failed to parse Client Info",
			slog.F("session_id", s.sessionID),
			slog.F("err", err))
		_ = s.ReplyConnRequest(req, true, nil)
		return
	}

	s.SetPeerInfo(ci.BaseInfo)
	if ci.Process != nil {
		s.SetPeerProcessInfo(*ci.Process)
	}
	if ci.Identity != "" {
		s.SetPeerIdentity(ci.Identity)
	}

	if s.applicationServer != nil {
		if serverInfo := s.applicationServer.GetServerInterpreter(); serverInfo != nil {
			processInfo := serverInfo.ProcessInfo
			replyInfo := &interpreter.Info{
				BaseInfo: serverInfo.BaseInfo,
				Process:  &processInfo,
				Identity: s.applicationServer.GetServerIdentity(),
			}
			interpreterPayload, err := json.Marshal(replyInfo)
			if err != nil {
				s.logger.DErrorWith("Error marshaling Server Info",
					slog.F("session_id", s.sessionID),
					slog.F("err", err))
				_ = s.ReplyConnRequest(req, true, nil)
			} else {
				_ = s.ReplyConnRequest(req, true, interpreterPayload)
			}
			return
		}
	}
	_ = s.ReplyConnRequest(req, true, nil)
}

func (s *BidirectionalSession) handleWindowSize(req *ssh.Request) {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		s.logger.Errorf("Failed to obtain terminal size")
	}
	payload, _ := json.Marshal(types.TermDimensions{
		Height: uint32(height),
		Width:  uint32(width),
	})
	_ = s.ReplyConnRequest(req, true, payload)
}

type eventRequest struct {
	Type      string `json:"type"`
	SessionID int64  `json:"session_id"`
	Timestamp int64  `json:"timestamp"`
}

func (s *BidirectionalSession) handleSliderEvent(req *ssh.Request) {
	var event eventRequest
	if err := json.Unmarshal(req.Payload, &event); err != nil {
		s.logger.DErrorWith("Failed to unmarshal event",
			slog.F("session_id", s.sessionID),
			slog.F("err", err))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	s.logger.InfoWith("Received Upstream Event",
		slog.F("type", event.Type),
		slog.F("remote_session_id", event.SessionID))

	if req.WantReply {
		_ = req.Reply(true, nil)
	}
}

func (s *BidirectionalSession) handleShutdown(req *ssh.Request) {
	s.logger.InfoWith("Received shutdown request, closing connection",
		slog.F("session_id", s.sessionID))

	if req.WantReply {
		_ = req.Reply(true, nil)
	}
	if s.role.IsAgent() && s.sshClient != nil {
		_ = s.sshClient.Close()
	}
	if s.wsConn != nil {
		_ = s.wsConn.Close()
	}
}
