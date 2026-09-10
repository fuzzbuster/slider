package portforward

import (
	"slider/pkg/conf"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

// SSHServerConn represents the minimal SSH server connection interface needed
type SSHServerConn interface {
	OpenChannel(name string, data []byte) (ssh.Channel, <-chan *ssh.Request, error)
}

// HandleCancelTCPIPForwardRequest handles an OpenSSH cancel-tcpip-forward request.
func (m *Manager) HandleCancelTCPIPForwardRequest(req *ssh.Request, ownerID uint64) {
	var payload types.TcpIpFwdRequest
	if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	control, err := m.GetRemoteMapping(conf.ForwardingProtocolTCP, payload.BindPort)
	if err != nil ||
		!control.IsSshConn ||
		control.OwnerID != ownerID ||
		control.SrcHost != payload.BindAddress {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	ok, _, err := m.conn.SendRequest(conf.SSHRequestCancelTcpIpForward, true, req.Payload)
	if err == nil && ok {
		control.cancel()
		m.RemoveRemoteForward(conf.ForwardingProtocolTCP, payload.BindPort)
	}
	if req.WantReply {
		_ = req.Reply(err == nil && ok, nil)
	}
}
