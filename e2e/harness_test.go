//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	commandTimeout = 15 * time.Second
	startupTimeout = 20 * time.Second
)

var (
	e2eBinary string
	ansiRE    = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
)

func TestMain(m *testing.M) {
	root, err := repositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	binary := os.Getenv("SLIDER_E2E_BINARY")
	var buildDir string
	if binary == "" {
		buildDir, err = os.MkdirTemp("", "slider-e2e-bin-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "create build directory: %v\n", err)
			os.Exit(1)
		}
		binary = filepath.Join(buildDir, "slider")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		build := exec.Command("go", "build", "-trimpath", "-o", binary, ".")
		build.Dir = root
		if output, buildErr := build.CombinedOutput(); buildErr != nil {
			fmt.Fprintf(os.Stderr, "build slider: %v\n%s", buildErr, output)
			_ = os.RemoveAll(buildDir)
			os.Exit(1)
		}
	}
	e2eBinary = binary

	code := m.Run()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

func repositoryRoot() (string, error) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("resolve e2e source path")
	}
	root := filepath.Dir(filepath.Dir(source))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	return root, nil
}

type lockedBuffer struct {
	mu sync.RWMutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.b.String()
}

func (b *lockedBuffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.b.Len()
}

func (b *lockedBuffer) Since(offset int) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	data := b.b.Bytes()
	if offset < 0 || offset > len(data) {
		offset = 0
	}
	return string(append([]byte(nil), data[offset:]...))
}

type runningProcess struct {
	cmd    *exec.Cmd
	output *lockedBuffer
	done   chan error
	exited atomic.Bool
}

func startProcess(t *testing.T, dir string, env []string, args ...string) *runningProcess {
	t.Helper()

	cmd := exec.Command(e2eBinary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	output := new(lockedBuffer)
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %q: %v", strings.Join(args, " "), err)
	}

	process := &runningProcess{
		cmd:    cmd,
		output: output,
		done:   make(chan error, 1),
	}
	go func() {
		err := cmd.Wait()
		process.exited.Store(true)
		process.done <- err
	}()
	t.Cleanup(process.stop)
	return process
}

func (p *runningProcess) stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	if p.exited.Load() {
		return
	}
	select {
	case <-p.done:
		return
	default:
	}

	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		_ = p.cmd.Process.Kill()
		<-p.done
		return
	}
	select {
	case <-p.done:
		return
	case <-time.After(2 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

func (p *runningProcess) waitFor(t *testing.T, timeout time.Duration, values ...string) string {
	t.Helper()
	return waitForOutput(t, p.output, p.done, 0, timeout, values...)
}

func waitForOutput(
	t *testing.T,
	output *lockedBuffer,
	done <-chan error,
	offset int,
	timeout time.Duration,
	values ...string,
) string {
	t.Helper()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		text := cleanTerminal(output.Since(offset))
		if containsAll(text, values) {
			return text
		}
		select {
		case err := <-done:
			text = cleanTerminal(output.Since(offset))
			if containsAll(text, values) {
				return text
			}
			t.Fatalf("process/connection ended while waiting for %q: %v\noutput:\n%s",
				values, err, cleanTerminal(output.String()))
		case <-timer.C:
			t.Fatalf("timeout waiting for %q\noutput:\n%s", values, cleanTerminal(output.String()))
		case <-ticker.C:
		}
	}
}

func containsAll(value string, expected []string) bool {
	for _, item := range expected {
		if !strings.Contains(value, item) {
			return false
		}
	}
	return true
}

func cleanTerminal(value string) string {
	value = ansiRE.ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "\r", "")
	return value
}

func reservePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

type testStack struct {
	serverDir   string
	clientDir   string
	server      *runningProcess
	client      *runningProcess
	port        int
	fingerprint string
}

func startTestStack(t *testing.T, withClient bool) *testStack {
	t.Helper()

	root := t.TempDir()
	serverDir := filepath.Join(root, "server")
	clientDir := filepath.Join(root, "client")
	for _, dir := range []string{serverDir, clientDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create fixture directory: %v", err)
		}
	}

	port := reservePort(t)
	env := []string{
		"NO_COLOR=1",
		"PS1=slider-e2e-shell> ",
		"S_HOME=" + filepath.Join(serverDir, ".slider") + string(os.PathSeparator),
		"SHELL=/bin/sh",
		"TERM=xterm-256color",
	}
	server := startProcess(t, serverDir, env,
		"server",
		"--headless",
		"--http-console",
		"--http-health",
		"--http-version",
		"--address", "127.0.0.1",
		"--port", fmt.Sprint(port),
		"--keepalive", "5s",
		"--colorless",
		"--verbose", "debug",
	)
	serverOutput := server.waitFor(t, startupTimeout, "Starting listener")
	match := regexp.MustCompile(`fingerprint="([^"]+)"`).FindStringSubmatch(serverOutput)
	if len(match) != 2 {
		t.Fatalf("server fingerprint not found:\n%s", serverOutput)
	}

	stack := &testStack{
		serverDir:   serverDir,
		clientDir:   clientDir,
		server:      server,
		port:        port,
		fingerprint: match[1],
	}
	if !withClient {
		return stack
	}

	clientEnv := []string{
		"NO_COLOR=1",
		"PS1=slider-e2e-shell> ",
		"S_HOME=" + filepath.Join(clientDir, ".slider") + string(os.PathSeparator),
		"SHELL=/bin/sh",
		"TERM=xterm-256color",
	}
	stack.client = startProcess(t, clientDir, clientEnv,
		"client",
		fmt.Sprintf("http://127.0.0.1:%d", port),
		"--fingerprint", stack.fingerprint,
		"--keepalive", "5s",
		"--colorless",
		"--verbose", "debug",
	)
	stack.client.waitFor(t, startupTimeout, "Server identification received")
	return stack
}

func waitForHTTP(t *testing.T, address string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	for {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.Close()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %s: %v", address, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}
