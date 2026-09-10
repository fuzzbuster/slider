package portforward

import (
	"fmt"
	"net"
	"sync"
	"time"

	"slider/pkg/conf"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

// StartLocalUDPForward initiates a local port forward from a message.
func (m *Manager) StartLocalUDPForward(msg types.TcpIpChannelMsg, notifier chan error) {
	udpAddr, err := net.ResolveUDPAddr(
		conf.ForwardingProtocolUDP,
		fmt.Sprintf("%s:%d", msg.SrcHost, msg.SrcPort),
	)
	if err != nil {
		notifier <- fmt.Errorf(
			"failed to resolve UDP address %s:%d - %v",
			msg.SrcHost,
			msg.SrcPort,
			err,
		)
		return
	}

	conn, err := net.ListenUDP(conf.ForwardingProtocolUDP, udpAddr)
	if err != nil {
		notifier <- fmt.Errorf(
			"failed to listen on %s %s:%d - %v",
			conf.ForwardingProtocolUDP,
			msg.SrcHost,
			msg.SrcPort,
			err,
		)
		return
	}
	defer func() { _ = conn.Close() }()

	m.logger.DebugWith("UDP Endpoint listening",
		slog.F("session_id", m.sessionID),
		slog.F("channel_type", conf.SSHChannelDirectUDP),
		slog.F("src_host", msg.SrcHost),
		slog.F("src_port", msg.SrcPort))

	m.AddLocalForward(&msg, conn, false, conf.ForwardingProtocolUDP)
	mapping, err := m.GetLocalMapping(conf.ForwardingProtocolUDP, msg.SrcPort)
	if err != nil {
		notifier <- fmt.Errorf("failed to get local port mapping - %v", err)
		return
	}

	sessions := make(map[string]ssh.Channel)
	sessionsMutex := sync.Mutex{}
	buffer := make([]byte, conf.MaxUDPPacketSize)

	for {
		select {
		case <-mapping.DoneChan:
			m.logger.DebugWith("Endpoint listener stopped",
				slog.F("session_id", m.sessionID),
				slog.F("channel_type", conf.SSHChannelDirectUDP),
				slog.F("src_host", msg.SrcHost),
				slog.F("src_port", msg.SrcPort))
			m.RemoveLocalForward(conf.ForwardingProtocolUDP, msg.SrcPort)

			sessionsMutex.Lock()
			for _, channel := range sessions {
				_ = channel.Close()
			}
			sessionsMutex.Unlock()
			return
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(conf.EndpointTickerInterval))
		n, addr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			m.logger.ErrorWith("Failed to read from UDP connection",
				slog.F("session_id", m.sessionID),
				slog.F("err", err))
			continue
		}

		clientAddr := addr.String()
		sessionsMutex.Lock()
		channel, exists := sessions[clientAddr]
		sessionsMutex.Unlock()

		if !exists {
			newChannel, requests, err := m.conn.OpenChannel(
				conf.SSHChannelDirectUDP,
				ssh.Marshal(msg),
			)
			if err != nil {
				m.logger.ErrorWith("Failed to open \"direct-udp\" channel",
					slog.F("session_id", m.sessionID),
					slog.F("channel_type", conf.SSHChannelDirectUDP),
					slog.F("err", err))
				continue
			}
			go ssh.DiscardRequests(requests)

			channel = newChannel
			sessionsMutex.Lock()
			sessions[clientAddr] = channel
			sessionsMutex.Unlock()

			go func(channel ssh.Channel, targetAddr *net.UDPAddr) {
				defer func() {
					_ = channel.Close()
					sessionsMutex.Lock()
					delete(sessions, clientAddr)
					sessionsMutex.Unlock()
				}()

				response := make([]byte, 65535)
				for {
					n, err := channel.Read(response)
					if err != nil {
						return
					}
					if n > 0 {
						if _, err := conn.WriteToUDP(response[:n], targetAddr); err != nil {
							return
						}
					}
				}
			}(channel, addr)
		}

		if _, err := channel.Write(buffer[:n]); err != nil {
			m.logger.ErrorWith("Failed to write to SSH channel",
				slog.F("session_id", m.sessionID),
				slog.F("err", err))
			_ = channel.Close()
			sessionsMutex.Lock()
			delete(sessions, clientAddr)
			sessionsMutex.Unlock()
		}
	}
}
