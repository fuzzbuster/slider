package portforward

import (
	"encoding/json"

	"slider/pkg/conf"
	"slider/pkg/sio"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

// HandleDirectUDPChannel handles incoming direct-udp channel requests
// This is called when the Console opens a local UDP port forward channel
func (m *Manager) HandleDirectUDPChannel(nc ssh.NewChannel) error {
	sessionClientChannel, request, aErr := nc.Accept()
	if aErr != nil {
		return aErr
	}
	defer func() { _ = sessionClientChannel.Close() }()
	go ssh.DiscardRequests(request)

	var dti types.TcpIpChannelMsg
	customMsg := &types.CustomTcpIpChannelMsg{}

	jErr := json.Unmarshal(nc.ExtraData(), customMsg)
	if jErr != nil || customMsg.TcpIpChannelMsg == nil {
		return jErr
	}
	dti = *customMsg.TcpIpChannelMsg

	m.logger.DebugWith("Direct UDP channel request",
		slog.F("session_id", m.sessionID),
		slog.F("request_channel", conf.SSHChannelDirectUDP),
		slog.F("dst_host", dti.DstHost),
		slog.F("dst_port", dti.DstPort),
		slog.F("src_host", dti.SrcHost),
		slog.F("src_port", dti.SrcPort))

	control, cErr := m.GetRemoteMapping(conf.ForwardingProtocolUDP, dti.DstPort)
	if cErr != nil {
		return cErr
	}

	customMsg.Channel = sessionClientChannel
	customMsg.Done = make(chan struct{})
	select {
	case control.RcvChan <- customMsg:
	case <-control.CancelChan:
		return nil
	}

	select {
	case <-customMsg.Done:
	case <-control.CancelChan:
	}

	return nil
}

// HandleUDPForwardRequest handles incoming udp-forward SSH requests
func (m *Manager) HandleUDPForwardRequest(
	req *ssh.Request,
	sshServerConn SSHServerConn,
	ownerID uint64,
) {
	srcReqPayload := &types.TcpIpFwdRequest{}
	if uErr := ssh.Unmarshal(req.Payload, srcReqPayload); uErr != nil {
		m.logger.ErrorWith("Failed to unmarshal TcpIpFwdRequest request",
			slog.F("session_id", m.sessionID),
			slog.F("request_type", conf.SSHRequestSliderUDPForward),
			slog.F("err", uErr))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	customReqPayload := &types.CustomTcpIpFwdRequest{
		Protocol:        conf.ForwardingProtocolUDP,
		IsSshConn:       true,
		TcpIpFwdRequest: srcReqPayload,
	}

	reqPayload, mErr := json.Marshal(customReqPayload)
	if mErr != nil {
		m.logger.ErrorWith("Failed to marshal request",
			slog.F("session_id", m.sessionID),
			slog.F("request_type", conf.SSHRequestSliderUDPForward),
			slog.F("err", mErr))
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	rOk, sliderRespData, rErr := m.conn.SendRequest(conf.SSHRequestSliderUDPForward, req.WantReply, reqPayload)
	if rErr != nil || !rOk {
		m.logger.ErrorWith("Failed to send slider client request",
			slog.F("session_id", m.sessionID),
			slog.F("request_type", conf.SSHRequestSliderUDPForward),
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
				slog.F("request_type", conf.SSHRequestSliderUDPForward),
				slog.F("bind_port", srcReqPayload.BindPort))
			_ = req.Reply(false, nil)
			return
		}

		respPayload := make([]byte, 0)
		if srcReqPayload.BindPort == 0 {
			if uErr := ssh.Unmarshal(sliderRespData, sshRespPayload); uErr != nil {
				m.logger.ErrorWith("Failed to unmarshal request",
					slog.F("session_id", m.sessionID),
					slog.F("request_type", conf.SSHRequestSliderUDPForward),
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
				slog.F("request_type", conf.SSHRequestSliderUDPForward),
				slog.F("err", wErr))
			return
		}

		m.AddRemoteForward(&types.TcpIpChannelMsg{
			DstHost: "",
			DstPort: 0,
			SrcHost: srcReqPayload.BindAddress,
			SrcPort: srcReqPayload.BindPort,
		}, true, conf.ForwardingProtocolUDP, ownerID)
	}

	control, _ := m.GetRemoteMapping(conf.ForwardingProtocolUDP, srcReqPayload.BindPort)
	for {
		var channelMsg *types.CustomTcpIpChannelMsg
		select {
		case <-control.CancelChan:
			return
		case channelMsg = <-control.RcvChan:
		}

		channel, tcpIpFwdReq, oErr := sshServerConn.OpenChannel(
			conf.SSHChannelDirectUDP,
			ssh.Marshal(channelMsg.TcpIpChannelMsg),
		)
		if oErr != nil {
			m.logger.ErrorWith("Failed to open channel to client",
				slog.F("session_id", m.sessionID),
				slog.F("request_channel", conf.SSHChannelDirectUDP),
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
				slog.F("request_channel", conf.SSHChannelDirectUDP),
				slog.F("src_host", srcReqPayload.BindAddress),
				slog.F("src_port", srcReqPayload.BindPort))
		}(channel, channelMsg)
	}
}
