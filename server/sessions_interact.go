package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"slider/pkg/conf"
	"slider/pkg/interpreter"
	"slider/pkg/remote"
	"slider/pkg/spath"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func (s *server) interactWithSession(ui UserInterface, sessionID int) error {
	unified, ok := s.ResolveUnifiedSessions()[int64(sessionID)]
	if !ok {
		return fmt.Errorf("session %d not found", sessionID)
	}
	if strings.HasPrefix(unified.Role, "operator") {
		return fmt.Errorf("interactive session not allowed against operator roles")
	}

	if unified.GatewayID == 0 {
		return s.interactWithLocalSession(ui, unified)
	}
	return s.interactWithRemoteSession(ui, unified)
}

func (s *server) interactWithLocalSession(
	ui UserInterface,
	unified UnifiedSession,
) error {
	sess, err := s.GetSession(int(unified.ActualID))
	if err != nil {
		return fmt.Errorf("session %d not found", unified.ActualID)
	}

	sftpClient, err := sess.NewSftpClient()
	if err != nil {
		return fmt.Errorf("failed to create SFTP client: %w", err)
	}
	defer func() { _ = sftpClient.Close() }()

	console, ok := ui.(*Console)
	if !ok {
		return fmt.Errorf("UI is not a Console")
	}
	s.newSftpConsoleWithInterpreter(console, SftpConsoleOptions{
		Session:    sess,
		SftpClient: sftpClient,
		LatestDir:  unified.WorkingDir,
		RemoteInfo: sess.GetPeerInfo(),
	})
	console.setConsoleAutoComplete(s.commandRegistry, s.serverInterpreter)
	return nil
}

func (s *server) interactWithRemoteSession(
	ui UserInterface,
	unified UnifiedSession,
) error {
	gatewaySession, err := s.GetSession(int(unified.GatewayID))
	if err != nil {
		return fmt.Errorf("gateway session %d not found", unified.GatewayID)
	}
	if gatewaySession.GetSSHClient() == nil {
		return fmt.Errorf("gateway session %d is not promiscuous", unified.GatewayID)
	}

	target := append([]int64{}, unified.Path...)
	target = append(target, unified.ActualID)
	payload, _ := json.Marshal(remote.ConnectRequest{
		Target:      target,
		ChannelType: conf.SSHChannelSFTP,
	})
	sftpChannel, requests, err := gatewaySession.GetSSHClient().OpenChannel(
		conf.SSHChannelSliderConnect,
		payload,
	)
	if err != nil {
		return fmt.Errorf("failed to open remote channel to %d: %v", target, err)
	}

	remoteInfo := interpreter.BaseInfo{
		User:      unified.BaseInfo.User,
		Hostname:  unified.BaseInfo.Hostname,
		HomeDir:   spath.NormalizeToSFTPPath(unified.BaseInfo.HomeDir, unified.BaseInfo.System),
		System:    unified.BaseInfo.System,
		Arch:      unified.BaseInfo.Arch,
		SliderDir: unified.BaseInfo.SliderDir,
		LaunchDir: unified.BaseInfo.LaunchDir,
	}
	go ssh.DiscardRequests(requests)

	sftpClient, err := sftp.NewClientPipe(sftpChannel, sftpChannel)
	if err != nil {
		_ = sftpChannel.Close()
		return fmt.Errorf("failed to create SFTP client: %v", err)
	}
	defer func() { _ = sftpClient.Close() }()

	console, ok := ui.(*Console)
	if !ok {
		return fmt.Errorf("UI is not a Console")
	}
	s.newSftpConsoleWithInterpreter(console, SftpConsoleOptions{
		Session:         gatewaySession,
		SftpClient:      sftpClient,
		RemoteInfo:      remoteInfo,
		targetSessionID: unified.UnifiedID,
		LatestDir:       unified.WorkingDir,
	})
	console.setConsoleAutoComplete(s.commandRegistry, s.serverInterpreter)
	return nil
}
