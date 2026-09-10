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
	}, false, conf.ForwardingProtocolTCP)
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
