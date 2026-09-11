package session

import (
	"encoding/json"
	"slices"

	"slider/pkg/conf"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

// handleSliderSessions handles slider-sessions requests (gateway servers only)
// Gathers all local and remote sessions and returns them to the requester
func (s *BidirectionalSession) handleSliderSessions(req *ssh.Request) {
	if s.applicationServer == nil {
		s.logger.ErrorWith("Application server not set",
			slog.F("session_id", s.sessionID))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	defer func() {
		if r := recover(); r != nil {
			s.logger.ErrorWith("Panic in handleSliderSessions", slog.F("panic", r))
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}()

	var request GetRemoteSessionsRequest
	if len(req.Payload) > 0 {
		if err := json.Unmarshal(req.Payload, &request); err != nil {
			s.logger.DErrorWith("Failed to unmarshal request",
				slog.F("session_id", s.sessionID),
				slog.F("err", err))
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			return
		}
	}

	currentIdentity := s.applicationServer.GetServerIdentity()
	if slices.Contains(request.Visited, currentIdentity) {
		s.logger.WarnWith("Loop/Self-connection detected in session listing",
			slog.F("session_id", s.sessionID),
			slog.F("identity", currentIdentity))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}
	visitedChain := append(request.Visited, currentIdentity)

	var sessions []RemoteSession
	fingerprint := s.applicationServer.GetFingerprint()
	localSessions := s.applicationServer.GetAllSessions()
	s.logger.DebugWith("Processing slider-sessions request",
		slog.F("session_id", s.sessionID),
		slog.F("total_sessions", len(localSessions)))

	gatewayClients := make([]*BidirectionalSession, 0)
	for _, sess := range localSessions {
		// Skip the session that is asking for the list (the caller)
		if sess.GetID() == s.sessionID {
			continue
		}

		s.logger.DebugWith("Checking session for list",
			slog.F("id", sess.GetID()),
			slog.F("is_client", sess.GetPeerInfo().User != ""),
			slog.F("is_gateway", sess.GetSSHClient() != nil))

		// Get the current SFTP working directory if available
		workingDir := sess.GetSftpWorkingDir()

		// Get connection address
		connectionAddr := ""
		if addr := sess.GetRemoteAddr(); addr != nil {
			connectionAddr = addr.String()
		}

		sessions = append(sessions, RemoteSession{
			ID:                sess.GetID(),
			ParentSessionID:   sess.GetParentSessionID(),
			ServerFingerprint: fingerprint,
			BaseInfo:          sess.GetPeerInfo(),
			Process:           processInfoPointer(sess.GetPeerProcessInfo()),
			Role:              sess.GetPeerRole().String(),
			WorkingDir:        workingDir,
			IsConnector:       sess.GetRole().IsConnector(),
			IsGateway:         sess.GetIsGateway(),
			ConnectionAddr:    connectionAddr,
		})

		if sess.GetSSHClient() != nil {
			gatewayClients = append(gatewayClients, sess)
		}
	}

	for _, clientSess := range gatewayClients {
		remoteSessions, err := clientSess.GetRemoteSessions(visitedChain)
		if err != nil {
			s.logger.WarnWith("Failed to fetch remote sessions",
				slog.F("session_id", s.sessionID),
				slog.F("err", err))
			continue
		}
		for i := range remoteSessions {
			remoteSessions[i].Process = sanitizeProcessInfoPointer(remoteSessions[i].Process)
			remoteSessions[i].Path = append([]int64{clientSess.GetID()}, remoteSessions[i].Path...)
		}
		sessions = append(sessions, remoteSessions...)
	}

	for i := range sessions {
		sessions[i].Process = sanitizeProcessInfoPointer(sessions[i].Process)
	}
	payload, err := json.Marshal(sessions)
	if err != nil {
		s.logger.DErrorWith("Failed to marshal sessions response",
			slog.F("session_id", s.sessionID),
			slog.F("err", err))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	if req.WantReply {
		_ = req.Reply(true, payload)
	}
}

// handleSliderForwardRequest handles slider-forward-request requests (gateway servers only)
// Routes forwarded requests through the mesh to their target session
func (s *BidirectionalSession) handleSliderForwardRequest(req *ssh.Request) {
	if s.applicationServer == nil {
		s.logger.ErrorWith("Application server not set",
			slog.F("session_id", s.sessionID))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	var payload types.ForwardRequestPayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		s.logger.DErrorWith("Failed to unmarshal forward payload",
			slog.F("session_id", s.sessionID),
			slog.F("err", err))
		return
	}

	target := payload.Target

	// Parse path to find next hop
	// Path format: [id1, id2, ...]
	if len(target) == 0 {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		s.logger.DErrorWith("Empty target path in forward request",
			slog.F("session_id", s.sessionID))
		return
	}

	nextID := int(target[0])

	// Lookup Session via the application server's registry
	nextHop, err := s.applicationServer.GetSession(nextID)
	if err != nil {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		s.logger.DErrorWith("Next hop session not found",
			slog.F("session_id", s.sessionID),
			slog.F("next_id", nextID),
			slog.F("err", err))
		return
	}

	s.logger.DebugWith("Forwarding Request",
		slog.F("req_type", payload.ReqType),
		slog.F("next_hop", nextID),
		slog.F("remaining_path", target[1:]))

	// Determine if Next Hop is Gateway or Leaf
	if nextHop.GetSSHClient() != nil && len(target) > 1 {
		// Gateway forwards as slider-forward-request
		remainingPath := target[1:]

		newPayload := types.ForwardRequestPayload{
			Target:  remainingPath,
			ReqType: payload.ReqType,
			Payload: payload.Payload,
		}
		newData, mErr := json.Marshal(newPayload)
		if mErr != nil {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			s.logger.DErrorWith("Failed to marshal new payload",
				slog.F("session_id", s.sessionID),
				slog.F("err", mErr))
			return
		}

		ok, reply, sErr := nextHop.GetSSHClient().SendRequest(
			conf.SSHRequestSliderTCPIPForward,
			req.WantReply,
			newData,
		)
		if sErr != nil {
			s.logger.DErrorWith("Failed to send forward request",
				slog.F("session_id", s.sessionID),
				slog.F("err", sErr))
			return
		}
		if req.WantReply {
			_ = req.Reply(ok, reply)
		}
		return
	}

	// End of path - Send request to target client via ServerConn
	if nextHop.GetSSHServerConn() == nil {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		s.logger.DErrorWith("Next hop session has no SSH connection",
			slog.F("session_id", s.sessionID),
			slog.F("next_id", nextID))
		return
	}

	ok, reply, sErr := nextHop.GetSSHServerConn().SendRequest(
		payload.ReqType,
		req.WantReply,
		payload.Payload,
	)
	if sErr != nil {
		s.logger.DErrorWith("Failed to send request to target",
			slog.F("session_id", s.sessionID),
			slog.F("err", sErr))
		return
	}
	if req.WantReply {
		_ = req.Reply(ok, reply)
	}
}
