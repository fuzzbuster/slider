package sshservice

import (
	"encoding/json"

	"slider/pkg/conf"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

const (
	maxPendingEnvRequests = 128
	maxPendingEnvBytes    = 64 * 1024
)

func (s *Service) handleGlobalRequests(
	serverConn *ssh.ServerConn,
	requests <-chan *ssh.Request,
	ownerID uint64,
) {
	for request := range requests {
		switch request.Type {
		case conf.SSHRequestKeepAlive:
			if request.WantReply {
				_ = request.Reply(true, nil)
			}
		case conf.SSHRequestTcpIpForward, conf.SSHRequestSliderTCPIPForward:
			if s.portFwdManager == nil {
				s.rejectRequest(request)
				continue
			}
			go s.portFwdManager.HandleTCPIPForwardRequest(request, serverConn, ownerID)
		case conf.SSHRequestCancelTcpIpForward:
			if s.portFwdManager == nil {
				s.rejectRequest(request)
				continue
			}
			s.portFwdManager.HandleCancelTCPIPForwardRequest(request, ownerID)
		case conf.SSHRequestSliderUDPForward:
			if s.portFwdManager == nil {
				s.rejectRequest(request)
				continue
			}
			go s.portFwdManager.HandleUDPForwardRequest(request, serverConn, ownerID)
		default:
			s.rejectRequest(request)
		}
	}
}

func (s *Service) handleSessionRequests(
	serverConn *ssh.ServerConn,
	clientChannel ssh.Channel,
	requests <-chan *ssh.Request,
	ownerID uint64,
) {
	defer func() { _ = clientChannel.Close() }()

	winChange := make(chan []byte, 10)
	envChange := make(chan []byte, 10)
	defer close(winChange)
	defer close(envChange)

	externalPtyRequested := false
	started := false
	var pendingEnv [][]byte
	pendingEnvBytes := 0
	var pendingWindow []byte
	for request := range requests {
		ok := false
		switch request.Type {
		case conf.SSHRequestPTY:
			if s.isPtyOn() {
				ok = true
				externalPtyRequested = true
				s.sendInitTermSize(request.Payload)
			}
		case conf.SSHRequestEnv:
			if started {
				ok = tryEnqueuePayload(envChange, request.Payload)
				break
			}
			ok = appendPendingEnv(&pendingEnv, &pendingEnvBytes, request.Payload)
		case conf.SSHRequestShell, conf.SSHRequestExec:
			if started {
				break
			}
			ok = true
			started = true
			initialEnv := append([][]byte(nil), pendingEnv...)
			pendingEnv = nil
			pendingEnvBytes = 0
			for _, envVar := range s.getEnvVars(externalPtyRequested) {
				initialEnv = append(initialEnv, ssh.Marshal(envVar))
			}
			if pendingWindow != nil {
				winChange <- pendingWindow
				pendingWindow = nil
			}
			go s.interactiveChannelPipe(
				clientChannel,
				request.Type,
				request.Payload,
				initialEnv,
				winChange,
				envChange,
			)
		case conf.SSHRequestWindowChange:
			if s.isPtyOn() {
				ok = true
				if started {
					enqueueLatestPayload(winChange, request.Payload)
				} else {
					pendingWindow = append(pendingWindow[:0], request.Payload...)
				}
			}
		case conf.SSHRequestSubsystem:
			subsystem, err := types.ParseSSHString(request.Payload)
			if !started && err == nil && subsystem == conf.SSHChannelSFTP {
				ok = true
				started = true
				pendingEnv = nil
				pendingEnvBytes = 0
				pendingWindow = nil
				go s.channelPipe(clientChannel, conf.SSHChannelSFTP, nil)
			}
		case conf.SSHRequestTcpIpForward, conf.SSHRequestSliderTCPIPForward:
			if s.portFwdManager != nil {
				go s.portFwdManager.HandleTCPIPForwardRequest(request, serverConn, ownerID)
				continue
			}
		case conf.SSHRequestCancelTcpIpForward:
			if s.portFwdManager != nil {
				s.portFwdManager.HandleCancelTCPIPForwardRequest(request, ownerID)
				continue
			}
		case conf.SSHRequestSliderUDPForward:
			if s.portFwdManager != nil {
				go s.portFwdManager.HandleUDPForwardRequest(request, serverConn, ownerID)
				continue
			}
		case conf.SSHRequestKeepAlive:
			ok = true
		}

		if request.WantReply {
			_ = request.Reply(ok, nil)
		}
	}
}

func appendPendingEnv(pending *[][]byte, totalBytes *int, payload []byte) bool {
	if len(*pending) >= maxPendingEnvRequests ||
		*totalBytes+len(payload) > maxPendingEnvBytes {
		return false
	}
	copyPayload := append([]byte(nil), payload...)
	*pending = append(*pending, copyPayload)
	*totalBytes += len(copyPayload)
	return true
}

func tryEnqueuePayload(target chan<- []byte, payload []byte) bool {
	copyPayload := append([]byte(nil), payload...)
	select {
	case target <- copyPayload:
		return true
	default:
		return false
	}
}

func enqueueLatestPayload(target chan []byte, payload []byte) {
	if tryEnqueuePayload(target, payload) {
		return
	}
	select {
	case <-target:
	default:
	}
	_ = tryEnqueuePayload(target, payload)
}

func (s *Service) rejectRequest(request *ssh.Request) {
	if request.WantReply {
		_ = request.Reply(false, nil)
	}
}

func (s *Service) sendInitTermSize(payload []byte) {
	var ptyRequest types.PtyRequest
	if err := ssh.Unmarshal(payload, &ptyRequest); err != nil {
		return
	}

	initSize, err := json.Marshal(types.TermDimensions{
		Width:  ptyRequest.TermWidthCols,
		Height: ptyRequest.TermHeightRows,
		X:      ptyRequest.TermWidthPixels,
		Y:      ptyRequest.TermHeightPixels,
	})
	if err != nil {
		return
	}

	s.mutex.RLock()
	opener := s.opener
	s.mutex.RUnlock()
	channel, requests, err := opener.OpenChannel(conf.SSHChannelInitSize, initSize)
	if err != nil {
		return
	}
	defer func() { _ = channel.Close() }()
	go ssh.DiscardRequests(requests)
}

func (s *Service) getEnvVars(externalPtyRequested bool) []struct{ Key, Value string } {
	s.mutex.RLock()
	envVars := append([]struct{ Key, Value string }(nil), s.envVarList...)
	useAltShell := s.useAltShell
	s.mutex.RUnlock()

	if useAltShell {
		envVars = append(envVars, struct{ Key, Value string }{
			Key:   conf.SliderAltShellEnvVar,
			Value: "true",
		})
	}
	if externalPtyRequested {
		envVars = append(envVars, struct{ Key, Value string }{
			Key:   conf.SliderExecPtyEnvVar,
			Value: "true",
		})
	}
	return append(envVars, struct{ Key, Value string }{
		Key:   conf.SliderCloserEnvVar,
		Value: "true",
	})
}
