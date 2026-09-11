package instance

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"slider/pkg/conf"
	"slider/pkg/sio"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

func (si *Config) ExecuteCommand(cmdBytes []byte, console io.ReadWriter) error {
	opener := si.GetSSHConn()
	if opener == nil {
		return fmt.Errorf("no active SSH connection available")
	}

	channel, requests, err := opener.OpenChannel(
		conf.SSHRequestExec,
		types.MarshalSSHString(string(cmdBytes)),
	)
	if err != nil {
		return fmt.Errorf("could not open ssh channel: %w", err)
	}
	defer func() { _ = channel.Close() }()
	go ssh.DiscardRequests(requests)

	go func() {
		for _, envVar := range si.executionEnvVars(true) {
			if _, err := channel.SendRequest(conf.SSHRequestEnv, true, ssh.Marshal(envVar)); err != nil {
				si.Logger.ErrorWith("Failed to send execution environment",
					slog.F("session_id", si.SessionID),
					slog.F("err", err))
			}
		}
	}()

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(console, channel)
		close(done)
	}()
	go func() {
		defer wg.Done()
		sio.CopyInteractiveCancellable(channel, console, done)
	}()
	wg.Wait()
	return nil
}

func (si *Config) executionEnvVars(withPty bool) []struct{ Key, Value string } {
	si.instanceMutex.RLock()
	envVars := append([]struct{ Key, Value string }(nil), si.envVarList...)
	useAltShell := si.useAltShell
	si.instanceMutex.RUnlock()

	if useAltShell {
		envVars = append(envVars, struct{ Key, Value string }{
			Key:   conf.SliderAltShellEnvVar,
			Value: "true",
		})
	}
	if withPty {
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

func ParseSizePayload(sizeBytes []byte) (uint32, uint32, error) {
	if len(sizeBytes) < 8 {
		return 0, 0, fmt.Errorf("invalid window-change payload length: %d", len(sizeBytes))
	}
	cols := binary.BigEndian.Uint32(sizeBytes)
	rows := binary.BigEndian.Uint32(sizeBytes[4:])
	return cols, rows, nil
}
