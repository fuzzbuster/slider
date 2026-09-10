package portforward

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"slider/pkg/conf"
	"slider/pkg/sio"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

// StartRemoteForward initiates a remote (reverse) port forward from a message.
func (m *Manager) StartRemoteForward(msg types.CustomTcpIpChannelMsg, notifier chan error) {
	request := types.CustomTcpIpFwdRequest{
		Protocol:  msg.Protocol,
		IsSshConn: false,
		TcpIpFwdRequest: &types.TcpIpFwdRequest{
			BindAddress: msg.SrcHost,
			BindPort:    msg.SrcPort,
		},
		FwdHost: msg.DstHost,
		FwdPort: msg.DstPort,
	}

	payload, err := json.Marshal(request)
	if err != nil {
		notifier <- fmt.Errorf("failed to marshal TcpIpFwdRequest request - %v", err)
		return
	}

	var ok bool
	var response []byte
	if msg.Protocol == conf.ForwardingProtocolUDP {
		ok, response, err = m.conn.SendRequest(
			conf.SSHRequestSliderUDPForward,
			true,
			payload,
		)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("request was rejected, port likely in use")
			}
			notifier <- fmt.Errorf(
				"failed to send \"%s\" request - %v",
				conf.SSHRequestSliderUDPForward,
				err,
			)
			return
		}
	} else {
		ok, response, err = m.conn.SendRequest(
			conf.SSHRequestTcpIpForward,
			true,
			payload,
		)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("request was rejected, port likely in use")
			}
			notifier <- fmt.Errorf(
				"failed to send \"%s\" request - %v",
				conf.SSHRequestTcpIpForward,
				err,
			)
			return
		}
	}

	if len(response) > 0 {
		responsePort := &types.TcpIpReqSuccess{}
		if err := ssh.Unmarshal(response, responsePort); err == nil {
			msg.SrcPort = responsePort.BoundPort
		}
	}

	m.AddRemoteForward(&types.TcpIpChannelMsg{
		DstHost: msg.DstHost,
		DstPort: msg.DstPort,
		SrcHost: msg.SrcHost,
		SrcPort: msg.SrcPort,
	}, false, msg.Protocol)

	control, _ := m.GetRemoteMapping(msg.Protocol, msg.SrcPort)
	for {
		var forwarded *types.CustomTcpIpChannelMsg
		select {
		case <-control.CancelChan:
			return
		case forwarded = <-control.RcvChan:
		}

		conn, err := net.Dial(
			msg.Protocol,
			net.JoinHostPort(msg.DstHost, strconv.Itoa(int(msg.DstPort))),
		)
		if err != nil {
			m.logger.ErrorWith("Failed to connect to host",
				slog.F("session_id", m.sessionID),
				slog.F("dst_host", msg.DstHost),
				slog.F("dst_port", msg.DstPort),
				slog.F("err", err))
			close(forwarded.Done)
			continue
		}

		go func(conn net.Conn, forwarded *types.CustomTcpIpChannelMsg) {
			defer func() {
				_ = conn.Close()
				close(forwarded.Done)
			}()
			_, _ = sio.PipeWithCancel(conn, forwarded.Channel)
			m.logger.DebugWith("Completed MSG Forwarded channel",
				slog.F("session_id", m.sessionID),
				slog.F("src_host", msg.SrcHost),
				slog.F("src_port", msg.SrcPort),
				slog.F("dst_host", msg.DstHost),
				slog.F("dst_port", msg.DstPort))
		}(conn, forwarded)
	}
}

// CancelRemoteForward cancels a remote port forward.
func (m *Manager) CancelRemoteForward(protocol string, port uint32) error {
	m.mutex.Lock()
	key := newProtocolPortKey(protocol, port)
	control, ok := m.remoteMappings[key]
	if !ok {
		m.mutex.Unlock()
		return fmt.Errorf("mapping not found: %s", key)
	}
	m.mutex.Unlock()

	if control.IsSshConn {
		return fmt.Errorf("refusing to terminate ssh port forwarding, kill ssh endpoint instead")
	}

	payload := ssh.Marshal(&types.TcpIpFwdRequest{
		BindAddress: control.SrcHost,
		BindPort:    control.SrcPort,
	})
	ok, _, err := m.conn.SendRequest(
		conf.SSHRequestCancelTcpIpForward,
		true,
		payload,
	)
	if err != nil || !ok {
		return fmt.Errorf("failed to cancel reverse tcp forwarding - %v", err)
	}

	m.logger.DebugWith("Cancelled reverse tcp forwarding",
		slog.F("session_id", m.sessionID),
		slog.F("request_channel", conf.SSHRequestCancelTcpIpForward),
		slog.F("fwd_port", control.SrcPort))

	control.cancel()
	m.RemoveRemoteForward(protocol, port)
	return nil
}

// CancelAllSSHRemoteForwards cancels all SSH-initiated remote port forwards.
func (m *Manager) CancelAllSSHRemoteForwards() {
	m.mutex.Lock()
	mappings := make(map[string]*RemoteForward)
	for key, mapping := range m.remoteMappings {
		if mapping.IsSshConn {
			mappings[key] = mapping
		}
	}
	m.mutex.Unlock()

	for _, forward := range mappings {
		payload := ssh.Marshal(&types.TcpIpFwdRequest{
			BindAddress: forward.SrcHost,
			BindPort:    forward.SrcPort,
		})
		ok, _, err := m.conn.SendRequest(
			conf.SSHRequestCancelTcpIpForward,
			true,
			payload,
		)
		if err != nil || !ok {
			m.logger.ErrorWith("Failed to cancel reverse tcp forwarding",
				slog.F("session_id", m.sessionID),
				slog.F("request_channel", conf.SSHRequestCancelTcpIpForward),
				slog.F("fwd_host", forward.SrcHost),
				slog.F("fwd_port", forward.SrcPort),
				slog.F("err", err))
			continue
		}

		m.logger.DebugWith("Cancelled reverse tcp forwarding",
			slog.F("session_id", m.sessionID),
			slog.F("request_channel", conf.SSHRequestCancelTcpIpForward),
			slog.F("fwd_host", forward.SrcHost),
			slog.F("fwd_port", forward.SrcPort))
	}
}
