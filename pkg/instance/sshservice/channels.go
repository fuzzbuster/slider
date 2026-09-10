package sshservice

import (
	"fmt"

	"slider/pkg/conf"
	"slider/pkg/sio"
	"slider/pkg/slog"

	"golang.org/x/crypto/ssh"
)

func (s *Service) handleChannel(
	serverConn *ssh.ServerConn,
	newChannel ssh.NewChannel,
	ownerID uint64,
) {
	var err error
	switch newChannel.ChannelType() {
	case conf.SSHChannelSession:
		var channel ssh.Channel
		var requests <-chan *ssh.Request
		channel, requests, err = newChannel.Accept()
		if err == nil {
			s.handleSessionRequests(serverConn, channel, requests, ownerID)
		}
	case conf.SSHChannelDirectTCPIP, conf.SSHChannelForwardedTCPIP:
		if s.portFwdManager == nil {
			err = fmt.Errorf("port forwarding manager is not configured")
			break
		}
		err = s.portFwdManager.HandleDirectTCPIPChannel(newChannel)
	case conf.SSHChannelDirectUDP:
		if s.portFwdManager == nil {
			err = fmt.Errorf("port forwarding manager is not configured")
			break
		}
		err = s.portFwdManager.HandleDirectUDPChannel(newChannel)
	case conf.SSHChannelForwardedUDP:
		err = sio.HandleForwardedUDPChannel(
			newChannel,
			s.logger,
			s.sessionID,
			conf.ForwardingProtocolUDP,
		)
	default:
		err = newChannel.Reject(ssh.UnknownChannelType, "")
	}

	if err != nil {
		s.logger.ErrorWith("Failed to handle SSH endpoint channel",
			slog.F("session_id", s.sessionID),
			slog.F("channel_type", newChannel.ChannelType()),
			slog.F("err", err))
	}
}
