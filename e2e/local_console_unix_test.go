//go:build e2e && !windows

package e2e_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
)

type localConsole struct {
	cmd       *exec.Cmd
	terminal  *os.File
	output    *lockedBuffer
	done      chan error
	exited    atomic.Bool
	writeMu   sync.Mutex
	closeOnce sync.Once
}

func startLocalConsole(t *testing.T) *localConsole {
	t.Helper()
	return startLocalConsoleAt(t, t.TempDir())
}

func startLocalConsoleAt(
	t *testing.T,
	workDir string,
	additionalArgs ...string,
) *localConsole {
	t.Helper()
	port := reservePort(t)
	args := []string{
		"server",
		"--address", "127.0.0.1",
		"--port", fmt.Sprint(port),
		"--keepalive", "5s",
		"--colorless",
		"--verbose", "debug",
	}
	args = append(args, additionalArgs...)
	cmd := exec.Command(e2eBinary, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"NO_COLOR=1",
		"S_HOME="+filepath.Join(workDir, ".slider")+string(os.PathSeparator),
		"TERM=xterm-256color",
	)

	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatalf("start local console: %v", err)
	}
	console := &localConsole{
		cmd:      cmd,
		terminal: terminal,
		output:   new(lockedBuffer),
		done:     make(chan error, 1),
	}
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, readErr := terminal.Read(buffer)
			if n > 0 {
				chunk := append([]byte(nil), buffer[:n]...)
				_, _ = console.output.Write(chunk)
				if bytes.Contains(chunk, []byte("\x1b[6n")) {
					_ = console.writeRaw([]byte("\x1b[1;1R"))
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		console.exited.Store(true)
		console.done <- err
	}()
	t.Cleanup(console.close)

	console.waitFor(t, 0, startupTimeout, "Press CTR^C")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("open local console: %v", err)
	}
	console.waitFor(t, 0, startupTimeout, "Slider#")
	return console
}

func (c *localConsole) write(t *testing.T, value []byte) {
	t.Helper()
	if err := c.writeRaw(value); err != nil {
		t.Fatalf("write local console: %v", err)
	}
}

func (c *localConsole) writeRaw(value []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.terminal.Write(value)
	return err
}

func (c *localConsole) run(t *testing.T, command string, expected ...string) string {
	t.Helper()
	offset := c.output.Len()
	c.write(t, []byte(command+"\r"))
	values := append(append([]string(nil), expected...), "Slider#")
	return c.waitFor(t, offset, commandTimeout, values...)
}

func (c *localConsole) waitFor(
	t *testing.T,
	offset int,
	timeout time.Duration,
	expected ...string,
) string {
	t.Helper()
	return waitForOutput(t, c.output, c.done, offset, timeout, expected...)
}

func (c *localConsole) close() {
	c.closeOnce.Do(func() {
		_ = c.terminal.Close()
		if c.cmd.Process == nil {
			return
		}
		if c.exited.Load() {
			return
		}
		_ = c.cmd.Process.Signal(os.Interrupt)
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
			_ = c.cmd.Process.Kill()
		}
	})
}

func TestLocalConsoleCommandSurface(t *testing.T) {
	console := startLocalConsole(t)
	assertCommandSurface(t, console.run)

	offset := console.output.Len()
	console.write(t, []byte("clear\r"))
	waitForRaw(t, console, offset, "\x1b[2J")

	offset = console.output.Len()
	console.write(t, []byte("bg\r"))
	console.waitFor(t, offset, commandTimeout, "Logging...")

	offset = console.output.Len()
	if err := console.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("reopen local console: %v", err)
	}
	console.waitFor(t, offset, startupTimeout, "Slider#")

	console.exit(t)
}

func TestLocalConsoleCertificateCommands(t *testing.T) {
	workDir := t.TempDir()
	certJar := filepath.Join(workDir, "certs.json")
	console := startLocalConsoleAt(
		t,
		workDir,
		"--auth",
		"--ca-store",
		"--certs", certJar,
	)
	keyPair := readFirstKeyPair(t, certJar)
	console.run(t, "help", "certs")
	console.run(t, "certs", keyPair.FingerPrint)
	console.run(t, "certs --new", "Private Key:", "Fingerprint:")
	console.run(t, "certs --remove 2", "Certificate ID 2 successfully removed")
	console.exit(t)
}

func (c *localConsole) exit(t *testing.T) {
	t.Helper()
	c.write(t, []byte("exit\r"))
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatalf("server exit: %v\n%s", err, cleanTerminal(c.output.String()))
		}
	case <-time.After(commandTimeout):
		t.Fatalf("server did not exit\n%s", cleanTerminal(c.output.String()))
	}
}

func waitForRaw(t *testing.T, console *localConsole, offset int, expected string) {
	t.Helper()
	deadline := time.Now().Add(commandTimeout)
	for time.Now().Before(deadline) {
		if bytes.Contains([]byte(console.output.Since(offset)), []byte(expected)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for raw sequence %q\noutput:\n%s",
		expected, cleanTerminal(console.output.String()))
}
