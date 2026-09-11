package portforward

import (
	"fmt"
	"net"
	"time"

	"slider/pkg/conf"
	"slider/pkg/sio"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

// StartLocalForward initiates a local port forward from a message.
func (m *Manager) StartLocalForward(msg types.TcpIpChannelMsg, notifier chan error) {
	listener, err := net.Listen(
		conf.ForwardingProtocolTCP,
		fmt.Sprintf("%s:%d", msg.SrcHost, msg.SrcPort),
	)
	if err != nil {
		notifier <- fmt.Errorf(
			"failed to listen on %s %s:%d - %v",
			conf.ForwardingProtocolTCP,
			msg.SrcHost,
			msg.SrcPort,
			err,
		)
		return
	}
	defer func() { _ = listener.Close() }()

	m.logger.DebugWith("Endpoint listening",
		slog.F("session_id", m.sessionID),
		slog.F("channel_type", conf.SSHChannelDirectTCPIP),
		slog.F("src_host", msg.SrcHost),
		slog.F("src_port", msg.SrcPort))

	m.AddLocalForward(&msg, listener, false, conf.ForwardingProtocolTCP)
	mapping, err := m.GetLocalMapping(conf.ForwardingProtocolTCP, msg.SrcPort)
	if err != nil {
		notifier <- fmt.Errorf("failed to get local port mapping - %v", err)
		return
	}

	for {
		select {
		case <-mapping.DoneChan:
			m.logger.DebugWith("Endpoint listener stopped",
				slog.F("session_id", m.sessionID),
				slog.F("channel_type", conf.SSHChannelDirectTCPIP),
				slog.F("src_host", msg.SrcHost),
				slog.F("src_port", msg.SrcPort))
			m.RemoveLocalForward(conf.ForwardingProtocolTCP, msg.SrcPort)
			return
		default:
		}

		_ = listener.(*net.TCPListener).SetDeadline(
			time.Now().Add(conf.EndpointTickerInterval),
		)
		conn, err := listener.Accept()
		if err != nil {
			continue
		}

		channel, requests, err := m.conn.OpenChannel(
			conf.SSHChannelDirectTCPIP,
			ssh.Marshal(msg),
		)
		if err != nil {
			m.logger.ErrorWith("Failed to open \"direct-tcpip\" channel",
				slog.F("session_id", m.sessionID),
				slog.F("channel_type", conf.SSHChannelDirectTCPIP),
				slog.F("err", err))
			_ = conn.Close()
			continue
		}
		go ssh.DiscardRequests(requests)

		_, _ = sio.PipeWithCancel(conn, channel)
	}
}
