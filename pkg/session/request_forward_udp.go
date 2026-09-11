package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"slider/pkg/conf"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

// handleSliderUDPForwardRequest handles udp-forward requests (reverse port forwarding for UDP)
func (s *BidirectionalSession) handleSliderUDPForwardRequest(req *ssh.Request) {
	udpForward := &types.CustomTcpIpFwdRequest{}
	if uErr := json.Unmarshal(req.Payload, udpForward); uErr != nil {
		s.logger.ErrorWith("Failed to unmarshal UDP Forward request",
			slog.F("session_id", s.sessionID),
			slog.F("err", uErr))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	fwdAddress := fmt.Sprintf("%s:%d", udpForward.BindAddress, udpForward.BindPort)
	udpAddr, rErr := net.ResolveUDPAddr("udp", fwdAddress)
	if rErr != nil {
		s.logger.ErrorWith("Failed to resolve UDP address",
			slog.F("addr", fwdAddress),
			slog.F("err", rErr))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		s.logger.ErrorWith("Failed to start reverse udp forward listener",
			slog.F("session_id", s.sessionID),
			slog.F("fwd_address", fwdAddress),
			slog.F("err", err))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	finalBindPort := conn.LocalAddr().(*net.UDPAddr).Port
	s.logger.DebugWith("Reverse UDP Forward request binding",
		slog.F("session_id", s.sessionID),
		slog.F("bind_address", udpForward.BindAddress),
		slog.F("bind_port", udpForward.BindPort),
		slog.F("fwd_address", fwdAddress),
		slog.F("fwd_port", finalBindPort))

	if req.WantReply {
		dataBytes := make([]byte, 0)
		if udpForward.BindPort == 0 {
			data := types.TcpIpReqSuccess{BoundPort: uint32(finalBindPort)}
			dataBytes = ssh.Marshal(data)
		}
		_ = req.Reply(true, dataBytes)
	}

	udpForward.BindPort = uint32(finalBindPort)

	stopChan := make(chan bool, 1)
	if err := s.AddReversePortForward(udpForward.BindPort, udpForward.BindAddress, stopChan); err != nil {
		s.logger.ErrorWith("Failed to add reverse port forward",
			slog.F("session_id", s.sessionID),
			slog.F("bind_port", udpForward.BindPort),
			slog.F("err", err))
		_ = conn.Close()
		return
	}

	defer func() {
		_ = conn.Close()
		_ = s.RemoveReversePortForward(udpForward.BindPort)
	}()

	type udpSession struct {
		channel ssh.Channel
		addr    *net.UDPAddr
	}
	sessions := make(map[string]*udpSession)
	sessionsMutex := make(chan struct{}, 1)

	go func() {
		<-stopChan
		_ = conn.Close()
	}()

	buf := make([]byte, 65535)
	s.logger.DebugWith("Starting UDP listener loop",
		slog.F("session_id", s.sessionID),
		slog.F("bind_port", udpForward.BindPort))
	for {
		n, addr, rErr := conn.ReadFromUDP(buf)
		if rErr != nil {
			select {
			case <-stopChan:
				s.logger.DebugWith("UDP listener stopped via signal",
					slog.F("session_id", s.sessionID))
				return
			default:
				if !errors.Is(rErr, net.ErrClosed) {
					s.logger.DebugWith("Error reading from UDP listener",
						slog.F("err", rErr))
				}
				return
			}
		}

		s.logger.DebugWith("Received UDP packet",
			slog.F("session_id", s.sessionID),
			slog.F("bytes", n),
			slog.F("from", addr.String()))

		clientAddr := addr.String()

		sessionsMutex <- struct{}{}
		sess, exists := sessions[clientAddr]
		<-sessionsMutex

		if !exists {
			payload := &types.CustomTcpIpChannelMsg{
				Protocol:  udpForward.Protocol,
				IsSshConn: false,
				TcpIpChannelMsg: &types.TcpIpChannelMsg{
					DstHost: udpForward.FwdHost,
					DstPort: udpForward.FwdPort,
					SrcHost: addr.IP.String(),
					SrcPort: uint32(addr.Port),
				},
			}

			customMsgBytes, mErr := json.Marshal(payload)
			if mErr != nil {
				continue
			}

			channel, reqs, oErr := s.GetSSHConn().OpenChannel(conf.SSHChannelForwardedUDP, customMsgBytes)
			if oErr != nil {
				s.logger.DebugWith("Failed to open forwarded-udp channel",
					slog.F("err", oErr))
				continue
			}
			go ssh.DiscardRequests(reqs)

			sess = &udpSession{
				channel: channel,
				addr:    addr,
			}

			sessionsMutex <- struct{}{}
			sessions[clientAddr] = sess
			<-sessionsMutex

			go func(us *udpSession, key string) {
				defer func() {
					_ = us.channel.Close()
					sessionsMutex <- struct{}{}
					delete(sessions, key)
					<-sessionsMutex
				}()

				cBuf := make([]byte, conf.MaxUDPPacketSize)
				for {
					rn, cErr := us.channel.Read(cBuf)
					if cErr != nil {
						return
					}
					if rn > 0 {
						_, _ = conn.WriteToUDP(cBuf[:rn], us.addr)
					}
				}
			}(sess, clientAddr)
		}

		_, _ = sess.channel.Write(buf[:n])
	}
}
