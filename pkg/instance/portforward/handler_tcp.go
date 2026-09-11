package portforward

import (
	"encoding/json"

	"slider/pkg/conf"
	"slider/pkg/sio"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

// HandleTCPIPForwardRequest handles incoming tcpip-forward SSH requests
// This is called when an SSH client requests a remote port forward
func (m *Manager) HandleTCPIPForwardRequest(
	req *ssh.Request,
	sshServerConn SSHServerConn,
	ownerID uint64,
) {
	srcReqPayload := &types.TcpIpFwdRequest{}
	if uErr := ssh.Unmarshal(req.Payload, srcReqPayload); uErr != nil {
		m.logger.ErrorWith("Failed to unmarshal TcpIpFwdRequest request",
			slog.F("session_id", m.sessionID),
			slog.F("request_type", conf.SSHRequestTcpIpForward),
			slog.F("err", uErr))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	customReqPayload := &types.CustomTcpIpFwdRequest{
		IsSshConn:       true,
		TcpIpFwdRequest: srcReqPayload,
	}

	reqPayload, mErr := json.Marshal(customReqPayload)
	if mErr != nil {
		m.logger.ErrorWith("Failed to marshal request",
			slog.F("session_id", m.sessionID),
			slog.F("request_type", conf.SSHRequestTcpIpForward),
			slog.F("err", mErr))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	rOk, sliderRespData, rErr := m.conn.SendRequest(conf.SSHRequestTcpIpForward, req.WantReply, reqPayload)
	if rErr != nil || !rOk {
		m.logger.ErrorWith("Failed to send slider client request",
			slog.F("session_id", m.sessionID),
			slog.F("request_type", conf.SSHRequestTcpIpForward),
			slog.F("err", rErr))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	sshRespPayload := &types.TcpIpReqSuccess{}
	if req.WantReply {
		if sliderRespData == nil && srcReqPayload.BindPort == 0 {
			m.logger.ErrorWith("Failed to bind to port",
				slog.F("session_id", m.sessionID),
				slog.F("request_type", conf.SSHRequestTcpIpForward),
				slog.F("bind_port", srcReqPayload.BindPort))
			_ = req.Reply(false, nil)
			return
		}

		respPayload := make([]byte, 0)
		if srcReqPayload.BindPort == 0 {
			if uErr := ssh.Unmarshal(sliderRespData, sshRespPayload); uErr != nil {
				m.logger.ErrorWith("Failed to unmarshal request",
					slog.F("session_id", m.sessionID),
					slog.F("request_type", conf.SSHRequestTcpIpForward),
					slog.F("err", uErr))
				_ = req.Reply(false, nil)
				return
			}

			respPayload = ssh.Marshal(sshRespPayload)
			srcReqPayload.BindPort = sshRespPayload.BoundPort
		}

		if wErr := req.Reply(true, respPayload); wErr != nil {
			m.logger.ErrorWith("Failed to reply to original request",
				slog.F("session_id", m.sessionID),
				slog.F("request_type", conf.SSHRequestTcpIpForward),
				slog.F("err", wErr))
			return
		}

		m.AddRemoteForward(&types.TcpIpChannelMsg{
			DstHost: "",
			DstPort: 0,
			SrcHost: srcReqPayload.BindAddress,
			SrcPort: srcReqPayload.BindPort,
		}, true, conf.ForwardingProtocolTCP, ownerID)
	}

	control, _ := m.GetRemoteMapping(conf.ForwardingProtocolTCP, srcReqPayload.BindPort)
	for {
		var channelMsg *types.CustomTcpIpChannelMsg
		select {
		case <-control.CancelChan:
			return
		case channelMsg = <-control.RcvChan:
		}

		channel, tcpIpFwdReq, oErr := sshServerConn.OpenChannel(
			conf.SSHChannelForwardedTCPIP,
			ssh.Marshal(channelMsg.TcpIpChannelMsg),
		)
		if oErr != nil {
			m.logger.ErrorWith("Failed to open channel to client",
				slog.F("session_id", m.sessionID),
				slog.F("request_channel", conf.SSHChannelForwardedTCPIP),
				slog.F("err", oErr))
			close(channelMsg.Done)
			continue
		}
		go ssh.DiscardRequests(tcpIpFwdReq)

		go func(channel ssh.Channel, forwarded *types.CustomTcpIpChannelMsg) {
			defer func() {
				_ = channel.Close()
				close(forwarded.Done)
			}()
			_, _ = sio.PipeWithCancel(channel, forwarded.Channel)
			m.logger.DebugWith("Completed SSH Port Forward channel from remote",
				slog.F("session_id", m.sessionID),
				slog.F("request_channel", conf.SSHChannelForwardedTCPIP),
				slog.F("src_host", srcReqPayload.BindAddress),
				slog.F("src_port", srcReqPayload.BindPort))
		}(channel, channelMsg)
	}
}

// HandleDirectTCPIPChannel handles incoming direct-tcpip channel requests
// This is called when an SSH client opens a local port forward channel
func (m *Manager) HandleDirectTCPIPChannel(nc ssh.NewChannel) error {
	sessionClientChannel, request, aErr := nc.Accept()
	if aErr != nil {
		return aErr
	}
	defer func() { _ = sessionClientChannel.Close() }()
	go ssh.DiscardRequests(request)

	var dti types.TcpIpChannelMsg
	if uErr := ssh.Unmarshal(nc.ExtraData(), &dti); uErr != nil {
		return uErr
	}

	m.logger.DebugWith("Direct TCPIP channel request",
		slog.F("session_id", m.sessionID),
		slog.F("request_channel", conf.SSHChannelDirectTCPIP),
		slog.F("dst_host", dti.DstHost),
		slog.F("dst_port", dti.DstPort),
		slog.F("src_host", dti.SrcHost),
		slog.F("src_port", dti.SrcPort))

	control, cErr := m.GetRemoteMapping(conf.ForwardingProtocolTCP, dti.DstPort)
	if cErr != nil {
		return cErr
	}

	forwarded := &types.CustomTcpIpChannelMsg{
		Protocol:        conf.ForwardingProtocolTCP,
		TcpIpChannelMsg: &dti,
		Channel:         sessionClientChannel,
		Done:            make(chan struct{}),
	}
	select {
	case control.RcvChan <- forwarded:
	case <-control.CancelChan:
		return nil
	}

	select {
	case <-forwarded.Done:
	case <-control.CancelChan:
	}

	return nil
}
