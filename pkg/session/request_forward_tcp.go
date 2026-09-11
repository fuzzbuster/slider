package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"slider/pkg/conf"
	"slider/pkg/sio"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

// handleTcpIpForward handles tcpip-forward requests (reverse port forwarding)
// This is used by AgentRole and GatewayRole to set up reverse port forwarding (target offering fwd)
func (s *BidirectionalSession) handleTcpIpForward(req *ssh.Request) {
	tcpIpForward := &types.CustomTcpIpFwdRequest{}
	if uErr := json.Unmarshal(req.Payload, tcpIpForward); uErr != nil {
		s.logger.ErrorWith("Failed to unmarshal TcpIpFwdRequest request",
			slog.F("session_id", s.sessionID),
			slog.F("err", uErr))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	fwdAddress := fmt.Sprintf("%s:%d", tcpIpForward.BindAddress, tcpIpForward.BindPort)
	listen, err := net.Listen("tcp", fwdAddress)
	if err != nil {
		s.logger.ErrorWith("Failed to start reverse port forward listener",
			slog.F("session_id", s.sessionID),
			slog.F("fwd_address", fwdAddress),
			slog.F("err", err))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	finalBindPort := listen.Addr().(*net.TCPAddr).Port
	s.logger.DebugWith("Reverse Port Forward request binding",
		slog.F("session_id", s.sessionID),
		slog.F("bind_address", tcpIpForward.BindAddress),
		slog.F("bind_port", tcpIpForward.BindPort),
		slog.F("fwd_address", fwdAddress),
		slog.F("fwd_port", finalBindPort))

	if req.WantReply {
		dataBytes := make([]byte, 0)
		if tcpIpForward.BindPort == 0 {
			data := types.TcpIpReqSuccess{BoundPort: uint32(finalBindPort)}
			dataBytes = ssh.Marshal(data)
		}
		_ = req.Reply(true, dataBytes)
	}

	tcpIpForward.BindPort = uint32(finalBindPort)

	stopChan := make(chan bool, 1)
	if err := s.AddReversePortForward(tcpIpForward.BindPort, tcpIpForward.BindAddress, stopChan); err != nil {
		s.logger.ErrorWith("Failed to add reverse port forward",
			slog.F("session_id", s.sessionID),
			slog.F("bind_port", tcpIpForward.BindPort),
			slog.F("err", err))
		_ = listen.Close()
		return
	}

	defer func() {
		_ = listen.Close()
		_ = s.RemoveReversePortForward(tcpIpForward.BindPort)
	}()

	for {
		select {
		case <-stopChan:
			return
		default:
		}

		_ = listen.(*net.TCPListener).SetDeadline(time.Now().Add(conf.Timeout))
		conn, lErr := listen.Accept()
		if lErr != nil {
			var netErr net.Error
			if errors.As(lErr, &netErr) && netErr.Timeout() {
				continue
			}

			s.logger.DebugWith("Error accepting connection on reverse port",
				slog.F("session_id", s.sessionID),
				slog.F("bind_port", tcpIpForward.BindPort),
				slog.F("err", lErr))
			return
		}

		go func() {
			srcAddr := conn.RemoteAddr().(*net.TCPAddr)

			dstHost := tcpIpForward.BindAddress
			dstPort := tcpIpForward.BindPort
			if !tcpIpForward.IsSshConn {
				dstHost = tcpIpForward.FwdHost
				dstPort = tcpIpForward.FwdPort
			}

			payload := &types.CustomTcpIpChannelMsg{
				IsSshConn: tcpIpForward.IsSshConn,
				TcpIpChannelMsg: &types.TcpIpChannelMsg{
					DstHost: dstHost,
					DstPort: dstPort,
					SrcHost: srcAddr.IP.String(),
					SrcPort: uint32(srcAddr.Port),
				},
			}
			customMsgBytes, mErr := json.Marshal(payload)
			if mErr != nil {
				s.logger.DebugWith("Failed to marshal custom message",
					slog.F("session_id", s.sessionID),
					slog.F("bind_port", tcpIpForward.BindPort),
					slog.F("err", mErr))
				return
			}

			channel, reqs, oErr := s.GetSSHConn().OpenChannel(conf.SSHChannelForwardedTCPIP, customMsgBytes)
			if oErr != nil {
				s.logger.DebugWith("Failed to open forwarded-tcpip channel",
					slog.F("session_id", s.sessionID),
					slog.F("err", oErr))
				return
			}
			defer func() { _ = channel.Close() }()
			go ssh.DiscardRequests(reqs)

			_, _ = sio.PipeWithCancel(conn, channel)

			s.logger.DebugWith("Completed request to remote",
				slog.F("session_id", s.sessionID),
				slog.F("bind_address", tcpIpForward.BindAddress),
				slog.F("bind_port", tcpIpForward.BindPort),
				slog.F("src_addr", srcAddr.IP.String()),
				slog.F("src_port", srcAddr.Port))
		}()
	}
}

func (s *BidirectionalSession) handleCancelTcpIpForward(req *ssh.Request) {
	ok := false
	tcpIpForward := &types.TcpIpFwdRequest{}
	if uErr := ssh.Unmarshal(req.Payload, tcpIpForward); uErr == nil {
		revPortFwds := s.GetReversePortForwards()
		if pfc, found := revPortFwds[tcpIpForward.BindPort]; found {
			if pfc.BindAddress == tcpIpForward.BindAddress {
				select {
				case pfc.StopChan <- true:
				default:
				}
				ok = true
				s.logger.DebugWith("Cancelled reverse port forward",
					slog.F("session_id", s.sessionID),
					slog.F("bind_address", tcpIpForward.BindAddress),
					slog.F("bind_port", tcpIpForward.BindPort))
			}
		}
	}
	if req.WantReply {
		_ = req.Reply(ok, nil)
	}
}
