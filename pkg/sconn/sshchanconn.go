package sconn

import (
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

type ChannelConn struct {
	ssh.Channel
}

func (cc *ChannelConn) Network() string {
	return "tcp"
}

func (cc *ChannelConn) String() string {
	return ""
}

func (cc *ChannelConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

func (cc *ChannelConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

func (cc *ChannelConn) SetDeadline(_ time.Time) error {
	// SSH channels don't support deadlines, so we just ignore this
	return nil
}

func (cc *ChannelConn) SetReadDeadline(_ time.Time) error {
	// SSH channels don't support deadlines, so we just ignore this
	return nil
}

func (cc *ChannelConn) SetWriteDeadline(_ time.Time) error {
	// SSH channels don't support deadlines, so we just ignore this
	return nil
}

// SSHChannelToNetConn converts an SSH channel to a net.Conn interface
// This is used to adapt SSH channels to be used with code that expects net.Conn
func SSHChannelToNetConn(channel ssh.Channel) net.Conn {
	return &ChannelConn{
		Channel: channel,
	}
}
