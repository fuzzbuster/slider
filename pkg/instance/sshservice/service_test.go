package sshservice

import (
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"slider/pkg/conf"
	"slider/pkg/scrypt"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

type testOpener struct {
	openCount atomic.Int32
}

func (o *testOpener) OpenChannel(string, []byte) (ssh.Channel, <-chan *ssh.Request, error) {
	o.openCount.Add(1)
	return nil, nil, fmt.Errorf("not implemented")
}

func (*testOpener) SendRequest(string, bool, []byte) (bool, []byte, error) {
	return false, nil, fmt.Errorf("not implemented")
}

type testChannel struct{}

func (*testChannel) Read([]byte) (int, error)    { return 0, io.EOF }
func (*testChannel) Write(p []byte) (int, error) { return len(p), nil }
func (*testChannel) Close() error                { return nil }
func (*testChannel) CloseWrite() error           { return nil }
func (*testChannel) SendRequest(string, bool, []byte) (bool, error) {
	return true, nil
}
func (*testChannel) Stderr() io.ReadWriter { return nil }

func TestServiceHandlesConcurrentAuthenticatedClients(t *testing.T) {
	serverKeyPair, err := scrypt.NewEd25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	serverSigner, err := scrypt.SignerFromKey(serverKeyPair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	clientKeyPair, err := scrypt.NewEd25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := scrypt.SignerFromKey(clientKeyPair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}

	service := NewService(&Config{
		Logger:             slog.NewLogger("sshservice-test"),
		SessionID:          1,
		ServerKey:          serverSigner,
		AuthOn:             true,
		AllowedFingerprint: clientKeyPair.FingerPrint,
		Opener:             &testOpener{},
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	results := make(chan error, 4)
	go func() {
		for range 2 {
			conn, err := listener.Accept()
			if err != nil {
				results <- err
				continue
			}
			go func() {
				results <- service.Serve(conn)
			}()
		}
	}()

	for range 2 {
		go func() {
			clientConn, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				results <- err
				return
			}
			config := &ssh.ClientConfig{
				User:            "test",
				Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
				HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			}
			conn, channels, requests, err := ssh.NewClientConn(clientConn, "test", config)
			if err != nil {
				results <- err
				return
			}
			client := ssh.NewClient(conn, channels, requests)
			ok, _, err := client.SendRequest(conf.SSHRequestKeepAlive, true, nil)
			_ = client.Close()
			if err != nil {
				results <- err
				return
			}
			if !ok {
				results <- fmt.Errorf("keepalive request was rejected")
				return
			}
			results <- nil
		}()
	}

	for range 4 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPendingEnvironmentIsBounded(t *testing.T) {
	var pending [][]byte
	totalBytes := 0
	payload := make([]byte, 1024)

	for range maxPendingEnvBytes / len(payload) {
		if !appendPendingEnv(&pending, &totalBytes, payload) {
			t.Fatal("environment payload rejected before byte limit")
		}
	}
	if appendPendingEnv(&pending, &totalBytes, payload) {
		t.Fatal("environment payload accepted beyond byte limit")
	}
	if totalBytes != maxPendingEnvBytes {
		t.Fatalf("pending environment uses %d bytes, want %d", totalBytes, maxPendingEnvBytes)
	}
}

func TestWindowChangesBeforeStartDoNotBlock(t *testing.T) {
	service := NewService(&Config{
		Logger: slog.NewLogger("sshservice-window-test"),
		PtyOn:  true,
		Opener: &testOpener{},
	})
	requests := make(chan *ssh.Request, 32)
	for range cap(requests) {
		requests <- &ssh.Request{
			Type:    conf.SSHRequestWindowChange,
			Payload: []byte("resize"),
		}
	}
	close(requests)

	done := make(chan struct{})
	go func() {
		service.handleSessionRequests(nil, &testChannel{}, requests, 1)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("window changes blocked the request handler before session start")
	}
}

func TestSubsystemCanOnlyStartOnce(t *testing.T) {
	opener := &testOpener{}
	service := NewService(&Config{
		Logger: slog.NewLogger("sshservice-subsystem-test"),
		Opener: opener,
	})
	requests := make(chan *ssh.Request, 2)
	for range 2 {
		requests <- &ssh.Request{
			Type:    conf.SSHRequestSubsystem,
			Payload: types.MarshalSSHString(conf.SSHChannelSFTP),
		}
	}
	close(requests)

	service.handleSessionRequests(nil, &testChannel{}, requests, 1)
	deadline := time.Now().Add(time.Second)
	for opener.openCount.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := opener.openCount.Load(); got != 1 {
		t.Fatalf("opened %d subsystem channels, want 1", got)
	}
}
