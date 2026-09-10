package sshservice

import (
	"io"
	"sync"

	"slider/pkg/conf"
	"slider/pkg/sio"
	"slider/pkg/slog"

	"golang.org/x/crypto/ssh"
)

func (s *Service) channelPipe(clientChannel ssh.Channel, channelType string, payload []byte) {
	s.mutex.RLock()
	opener := s.opener
	s.mutex.RUnlock()
	serverChannel, requests, err := opener.OpenChannel(channelType, payload)
	if err != nil {
		s.logger.ErrorWith("Failed to open proxied SSH channel",
			slog.F("session_id", s.sessionID),
			slog.F("channel_type", channelType),
			slog.F("err", err))
		_ = clientChannel.Close()
		return
	}
	defer func() { _ = serverChannel.Close() }()
	go ssh.DiscardRequests(requests)
	_, _ = sio.PipeWithCancel(clientChannel, serverChannel)
}

func (s *Service) interactiveChannelPipe(
	clientChannel ssh.Channel,
	channelType string,
	payload []byte,
	initialEnv [][]byte,
	winChange <-chan []byte,
	envChange <-chan []byte,
) {
	s.mutex.RLock()
	opener := s.opener
	s.mutex.RUnlock()
	serverChannel, requests, err := opener.OpenChannel(channelType, payload)
	if err != nil {
		s.logger.ErrorWith("Failed to open proxied SSH channel",
			slog.F("session_id", s.sessionID),
			slog.F("channel_type", channelType),
			slog.F("err", err))
		_ = clientChannel.Close()
		return
	}
	defer func() { _ = serverChannel.Close() }()

	for _, envPayload := range initialEnv {
		if _, err := serverChannel.SendRequest(conf.SSHRequestEnv, true, envPayload); err != nil {
			return
		}
	}
	go forwardRequests(serverChannel, conf.SSHRequestWindowChange, winChange)
	go forwardRequests(serverChannel, conf.SSHRequestEnv, envChange)
	s.pipeChannelWithStatus(clientChannel, serverChannel, requests)
}

func forwardRequests(channel ssh.Channel, requestType string, payloads <-chan []byte) {
	for payload := range payloads {
		_, _ = channel.SendRequest(requestType, true, payload)
	}
}

func (s *Service) pipeChannelWithStatus(
	clientChannel ssh.Channel,
	serverChannel ssh.Channel,
	serverRequests <-chan *ssh.Request,
) (int64, int64) {
	var bytesToServer int64
	var bytesToClient int64
	var wg sync.WaitGroup
	copyDone := make(chan struct{}, 2)
	wg.Add(2)

	go func() {
		defer wg.Done()
		bytesToClient, _ = io.Copy(clientChannel, serverChannel)
		copyDone <- struct{}{}
	}()
	go func() {
		defer wg.Done()
		bytesToServer, _ = io.Copy(serverChannel, clientChannel)
		copyDone <- struct{}{}
	}()

	for {
		select {
		case <-copyDone:
			_ = clientChannel.Close()
			_ = serverChannel.Close()
			wg.Wait()
			return bytesToServer, bytesToClient
		case request, ok := <-serverRequests:
			if !ok {
				_ = clientChannel.Close()
				_ = serverChannel.Close()
				wg.Wait()
				return bytesToServer, bytesToClient
			}
			switch request.Type {
			case conf.SSHRequestExitStatus, conf.SSHRequestExitSignal:
				_, _ = clientChannel.SendRequest(request.Type, false, request.Payload)
				_ = clientChannel.Close()
				_ = serverChannel.Close()
				wg.Wait()
				return bytesToServer, bytesToClient
			}
			if request.WantReply {
				_ = request.Reply(true, nil)
			}
		}
	}
}
