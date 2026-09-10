package sshservice

import (
	"fmt"
	"net"
	"testing"

	"slider/pkg/conf"
	"slider/pkg/scrypt"
	"slider/pkg/slog"

	"golang.org/x/crypto/ssh"
)

type testOpener struct{}

func (testOpener) OpenChannel(string, []byte) (ssh.Channel, <-chan *ssh.Request, error) {
	return nil, nil, fmt.Errorf("not implemented")
}

func (testOpener) SendRequest(string, bool, []byte) (bool, []byte, error) {
	return false, nil, fmt.Errorf("not implemented")
}

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
		Opener:             testOpener{},
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
