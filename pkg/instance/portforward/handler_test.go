package portforward

import (
	"io"
	"testing"
	"time"

	"slider/pkg/conf"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

type testChannel struct{ id int }

func (*testChannel) Read([]byte) (int, error)    { return 0, io.EOF }
func (*testChannel) Write(p []byte) (int, error) { return len(p), nil }
func (*testChannel) Close() error                { return nil }
func (*testChannel) CloseWrite() error           { return nil }
func (*testChannel) SendRequest(string, bool, []byte) (bool, error) {
	return true, nil
}
func (*testChannel) Stderr() io.ReadWriter { return nil }

type testOpener struct{}

func (testOpener) OpenChannel(string, []byte) (ssh.Channel, <-chan *ssh.Request, error) {
	return nil, nil, nil
}

func (testOpener) SendRequest(string, bool, []byte) (bool, []byte, error) {
	return true, nil, nil
}

type testNewChannel struct {
	channel ssh.Channel
	payload []byte
}

func (c *testNewChannel) Accept() (ssh.Channel, <-chan *ssh.Request, error) {
	requests := make(chan *ssh.Request)
	close(requests)
	return c.channel, requests, nil
}
func (*testNewChannel) Reject(ssh.RejectionReason, string) error { return nil }
func (*testNewChannel) ChannelType() string                      { return conf.SSHChannelDirectTCPIP }
func (c *testNewChannel) ExtraData() []byte                      { return c.payload }

func TestDirectTCPIPKeepsChannelPerConnection(t *testing.T) {
	manager := NewManager(slog.NewLogger("portforward-test"), 1, nil)
	manager.AddRemoteForward(&types.TcpIpChannelMsg{
		SrcPort: 9000,
	}, false, conf.ForwardingProtocolTCP, 0)
	control, err := manager.GetRemoteMapping(conf.ForwardingProtocolTCP, 9000)
	if err != nil {
		t.Fatal(err)
	}

	channels := []*testChannel{{id: 1}, {id: 2}}
	errs := make(chan error, len(channels))
	for _, channel := range channels {
		request := &testNewChannel{
			channel: channel,
			payload: ssh.Marshal(types.TcpIpChannelMsg{DstPort: 9000}),
		}
		go func() {
			errs <- manager.HandleDirectTCPIPChannel(request)
		}()
	}

	received := make(map[ssh.Channel]bool)
	for range channels {
		select {
		case forwarded := <-control.RcvChan:
			received[forwarded.Channel] = true
			close(forwarded.Done)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for forwarded channel")
		}
	}

	for _, channel := range channels {
		if !received[channel] {
			t.Fatalf("channel %d was replaced by another forwarding connection", channel.id)
		}
	}
	for range channels {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCancelSSHRemoteForwardsOnlyCancelsOwner(t *testing.T) {
	manager := NewManager(slog.NewLogger("portforward-owner-test"), 1, testOpener{})
	manager.AddRemoteForward(&types.TcpIpChannelMsg{
		SrcHost: "127.0.0.1",
		SrcPort: 9001,
	}, true, conf.ForwardingProtocolTCP, 1)
	manager.AddRemoteForward(&types.TcpIpChannelMsg{
		SrcHost: "127.0.0.1",
		SrcPort: 9002,
	}, true, conf.ForwardingProtocolTCP, 2)

	ownerOne, err := manager.GetRemoteMapping(conf.ForwardingProtocolTCP, 9001)
	if err != nil {
		t.Fatal(err)
	}
	ownerTwo, err := manager.GetRemoteMapping(conf.ForwardingProtocolTCP, 9002)
	if err != nil {
		t.Fatal(err)
	}

	manager.CancelSSHRemoteForwards(1)

	if _, err := manager.GetRemoteMapping(conf.ForwardingProtocolTCP, 9001); err == nil {
		t.Fatal("owner one's mapping was not removed")
	}
	if _, err := manager.GetRemoteMapping(conf.ForwardingProtocolTCP, 9002); err != nil {
		t.Fatalf("owner two's mapping was removed: %v", err)
	}
	select {
	case <-ownerOne.CancelChan:
	default:
		t.Fatal("owner one's forwarding loop was not cancelled")
	}
	select {
	case <-ownerTwo.CancelChan:
		t.Fatal("owner two's forwarding loop was cancelled")
	default:
	}
}
