//go:build e2e

package e2e_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

type webConsole struct {
	conn      *websocket.Conn
	output    *lockedBuffer
	done      chan error
	writeMu   sync.Mutex
	closeOnce sync.Once
}

func openWebConsole(t *testing.T, port int, headers http.Header) *webConsole {
	t.Helper()

	url := fmt.Sprintf("ws://127.0.0.1:%d/console/ws", port)
	return openWebConsoleURL(t, url, websocket.DefaultDialer, headers)
}

func openWebConsoleURL(
	t *testing.T,
	url string,
	dialer *websocket.Dialer,
	headers http.Header,
) *webConsole {
	t.Helper()

	conn, response, err := dialer.Dial(url, headers)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("connect web console (status %d): %v", status, err)
	}

	console := &webConsole{
		conn:   conn,
		output: new(lockedBuffer),
		done:   make(chan error, 1),
	}
	go console.read()
	t.Cleanup(console.close)

	console.write(t, websocket.TextMessage, []byte(`{"type":"resize","cols":120,"rows":40}`))
	console.waitFor(t, 0, startupTimeout, "Slider#")
	return console
}

func (c *webConsole) read() {
	for {
		messageType, message, err := c.conn.ReadMessage()
		if err != nil {
			c.done <- err
			return
		}
		if messageType != websocket.BinaryMessage && messageType != websocket.TextMessage {
			continue
		}
		_, _ = c.output.Write(message)
		if bytes.Contains(message, []byte("\x1b[6n")) {
			c.writeRaw(websocket.TextMessage, []byte("\x1b[1;1R"))
		}
		if bytes.Contains(message, []byte("\x1b]11;?")) {
			c.writeRaw(websocket.TextMessage, []byte("\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\"))
		}
	}
}

func (c *webConsole) write(t *testing.T, messageType int, message []byte) {
	t.Helper()
	if err := c.writeRaw(messageType, message); err != nil {
		t.Fatalf("write web console: %v", err)
	}
}

func (c *webConsole) writeRaw(messageType int, message []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteMessage(messageType, message)
}

func (c *webConsole) run(t *testing.T, command string, expected ...string) string {
	t.Helper()
	offset := c.output.Len()
	c.write(t, websocket.TextMessage, []byte(command+"\r"))
	values := append(append([]string(nil), expected...), "Slider#")
	return c.waitFor(t, offset, commandTimeout, values...)
}

func (c *webConsole) runSFTP(t *testing.T, command string, expected ...string) string {
	t.Helper()
	return c.runSFTPWithTimeout(t, command, commandTimeout, expected...)
}

func (c *webConsole) runSFTPWithTimeout(
	t *testing.T,
	command string,
	timeout time.Duration,
	expected ...string,
) string {
	t.Helper()
	offset := c.output.Len()
	c.write(t, websocket.TextMessage, []byte(command+"\r"))
	values := append(append([]string(nil), expected...), "(S")
	return c.waitFor(t, offset, timeout, values...)
}

func (c *webConsole) waitFor(
	t *testing.T,
	offset int,
	timeout time.Duration,
	expected ...string,
) string {
	t.Helper()
	return waitForOutput(t, c.output, c.done, offset, timeout, expected...)
}

func (c *webConsole) close() {
	c.closeOnce.Do(func() {
		_ = c.conn.Close()
	})
}

func (c *webConsole) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(commandTimeout):
		t.Fatalf("web console did not close\n%s", cleanTerminal(c.output.String()))
	}
}

func TestWebConsoleCommandSurface(t *testing.T) {
	stack := startTestStack(t, false)
	console := openWebConsole(t, stack.port, nil)

	assertCommandSurface(t, console.run)
	console.run(t, "clear")

	console.write(t, websocket.TextMessage, []byte("bg\r"))
	console.waitClosed(t)

	reconnected := openWebConsole(t, stack.port, nil)
	reconnected.run(t, "help", "sessions")
	reconnected.write(t, websocket.TextMessage, []byte("exit\r"))
	reconnected.waitClosed(t)
}

func TestWebConsoleAgentWorkflow(t *testing.T) {
	stack := startTestStack(t, true)
	console := openWebConsole(t, stack.port, nil)
	echoAddress := startTCPEchoServer(t)
	udpEchoAddress := startUDPEchoServer(t)

	sessionsOutput := console.run(t, "sessions", "Active sessions: 1")
	sessionID := sessionIDFromOutput(t, sessionsOutput)
	console.runSFTP(t, fmt.Sprintf("sessions -i %d", sessionID), "Starting interactive session")

	console.runSFTP(t, "help",
		"cd, chdir",
		"get, download",
		"ls, dir, list",
		"mv, rename, move",
		"put, upload",
		"rm, del, delete",
		"stat, info",
		"sysinfo",
		"execute",
	)
	console.runSFTP(t, "sysinfo", "System", "Architecture", "Working Directory")
	console.runSFTP(t, "execute printf slider-e2e-remote-exec", "slider-e2e-remote-exec")
	console.runSFTP(t, "pwd", stack.clientDir)
	console.runSFTP(t, "getwd", stack.clientDir)
	console.runSFTP(t, "lpwd", stack.serverDir)
	console.runSFTP(t, "lgetwd", stack.serverDir)

	payload := []byte("slider e2e transfer payload\n")
	localUpload := filepath.Join(stack.serverDir, "upload.txt")
	if err := os.WriteFile(localUpload, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	spacedUpload := filepath.Join(stack.serverDir, "spaced file.txt")
	if err := os.WriteFile(spacedUpload, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	uploadTree := filepath.Join(stack.serverDir, "upload-tree", "nested")
	if err := os.MkdirAll(uploadTree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uploadTree, "tree.txt"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	console.runSFTP(t, "mkdir remote-e2e", "Created directory")
	console.runSFTP(t, "chdir remote-e2e", "Current cd path")
	console.runSFTP(t, "cd ..", "Current cd path")
	console.runSFTP(t, "put upload.txt", "Uploaded file:")
	remoteUpload := filepath.Join(stack.clientDir, "upload.txt")
	assertFileContent(t, remoteUpload, payload)
	console.runSFTP(t, `upload "spaced file.txt"`, "Uploaded file:")
	assertFileContent(t, filepath.Join(stack.clientDir, "spaced file.txt"), payload)

	console.runSFTP(t, "info upload.txt", "Regular File")
	if runtime.GOOS != "windows" {
		console.runSFTP(t, "chmod 0600 upload.txt", "Changed permissions")
	}
	console.runSFTP(t, "rename upload.txt renamed.txt", "Renamed file")
	console.runSFTP(t, "move renamed.txt moved.txt", "Renamed file")
	console.runSFTP(t, "get moved.txt", "Downloaded file:")
	assertFileContent(t, filepath.Join(stack.serverDir, "moved.txt"), payload)

	console.runSFTP(t, "dir", "moved.txt", "remote-e2e")
	console.runSFTP(t, "list", "spaced file.txt")
	console.runSFTP(t, "lls", "upload.txt")
	console.runSFTP(t, "llist", "spaced file.txt")
	console.runSFTP(t, "put -r upload-tree", "Uploaded directory")
	assertFileContent(t, filepath.Join(stack.clientDir, "upload-tree", "nested", "tree.txt"), payload)
	console.runSFTP(t, "lmkdir downloads", "Created directory")
	console.runSFTP(t, "lcd downloads", "Current lcd path")
	console.runSFTP(t, "download -r upload-tree", "Downloaded directory")
	assertFileContent(
		t,
		filepath.Join(stack.serverDir, "downloads", "upload-tree", "nested", "tree.txt"),
		payload,
	)
	console.runSFTP(t, "lcd ..", "Current lcd path")

	console.runSFTP(t, "get missing-file", "failed to access remote path")
	console.runSFTP(t, "mkdir", "exactly 1 argument required")
	console.runSFTP(t, "rm moved.txt", "Removed file")
	console.runSFTP(t, `del "spaced file.txt"`, "Removed file")
	console.runSFTP(t, "rm -r remote-e2e", "Removed directory")
	console.runSFTP(t, "delete -r upload-tree", "Removed directory")
	console.run(t, "exit")

	output := console.run(t, "socks --local --port 0", "Local listener started on port:")
	socksPort := portFromOutput(t, output, `Local listener started on port: (\d+)`)
	assertSOCKSRoundTrip(t, socksPort, echoAddress)
	console.run(t, "socks", "Active SOCKS servers: 1")
	console.run(t, "socks --local --kill", "Local SOCKS5 server stopped")

	output = console.run(t, fmt.Sprintf("socks --session %d --port 0", sessionID),
		"Socks Endpoint running on port:")
	socksPort = portFromOutput(t, output, `Socks Endpoint running on port: (\d+)`)
	assertSOCKSRoundTrip(t, socksPort, echoAddress)
	console.run(t, fmt.Sprintf("socks --session %d --kill", sessionID), "SOCKS server stopped")

	output = console.run(t, fmt.Sprintf("ssh --session %d --port 0", sessionID),
		"SSH Endpoint running on port:")
	sshPort := portFromOutput(t, output, `SSH Endpoint running on port: (\d+)`)
	assertSSHCommand(t, sshPort)
	console.run(t, fmt.Sprintf("ssh --kill %d", sessionID), "SSH Endpoint gracefully stopped")

	output = console.run(t, fmt.Sprintf("shell --session %d --port 0", sessionID),
		"Shell Endpoint running on port:")
	shellPort := portFromOutput(t, output, `Shell Endpoint running on port: (\d+)`)
	assertShellCommand(t, shellPort)
	console.run(t, fmt.Sprintf("shell --kill %d", sessionID), "Shell Endpoint gracefully stopped")

	localPort := reservePort(t)
	console.run(t, fmt.Sprintf(
		"portfwd --session %d --local 127.0.0.1:%d:%s",
		sessionID,
		localPort,
		echoAddress,
	), "Local Port Forward Endpoint running")
	assertTCPRoundTrip(t, fmt.Sprintf("127.0.0.1:%d", localPort))
	console.run(t, fmt.Sprintf(
		"portfwd --session %d --local --remove %d",
		sessionID,
		localPort,
	), "removed successfully")

	reversePort := reservePort(t)
	console.run(t, fmt.Sprintf(
		"portfwd --session %d --reverse 127.0.0.1:%d:%s",
		sessionID,
		reversePort,
		echoAddress,
	), "Remote Port Forward Endpoint running")
	assertTCPRoundTrip(t, fmt.Sprintf("127.0.0.1:%d", reversePort))
	console.run(t, fmt.Sprintf(
		"portfwd --session %d --reverse --remove %d",
		sessionID,
		reversePort,
	), "removed successfully")

	localUDPPort := reserveUDPPort(t)
	console.run(t, fmt.Sprintf(
		"portfwd --session %d --local --udp 127.0.0.1:%d:%s",
		sessionID,
		localUDPPort,
		udpEchoAddress,
	), "Local Port Forward Endpoint running")
	assertUDPRoundTrip(t, fmt.Sprintf("127.0.0.1:%d", localUDPPort))
	console.run(t, fmt.Sprintf(
		"portfwd --session %d --local --udp --remove %d",
		sessionID,
		localUDPPort,
	), "removed successfully")

	reverseUDPPort := reserveUDPPort(t)
	console.run(t, fmt.Sprintf(
		"portfwd --session %d --reverse --udp 127.0.0.1:%d:%s",
		sessionID,
		reverseUDPPort,
		udpEchoAddress,
	), "Remote Port Forward Endpoint running")
	assertUDPRoundTrip(t, fmt.Sprintf("127.0.0.1:%d", reverseUDPPort))
	console.run(t, fmt.Sprintf(
		"portfwd --session %d --reverse --udp --remove %d",
		sessionID,
		reverseUDPPort,
	), "removed successfully")

	console.run(t, "portfwd", "Active Port Forwards: 0")
	console.run(t, fmt.Sprintf("sessions --kill %d", sessionID), "terminated gracefully")
}

func TestSessionDisconnect(t *testing.T) {
	stack := startTestStack(t, true)
	console := openWebConsole(t, stack.port, nil)
	sessionsOutput := console.run(t, "sessions", "Active sessions: 1")
	sessionID := sessionIDFromOutput(t, sessionsOutput)
	console.run(t, fmt.Sprintf("sessions --disconnect %d", sessionID), "Closed connection")
	console.run(t, "sessions", "Active sessions: 0")
}

func TestLargeFileRoundTrip(t *testing.T) {
	if os.Getenv("SLIDER_E2E_LARGE") != "1" {
		t.Skip("set SLIDER_E2E_LARGE=1 to run the 1 GiB transfer scenario")
	}

	stack := startTestStack(t, true)
	console := openWebConsole(t, stack.port, nil)
	sessionsOutput := console.run(t, "sessions", "Active sessions: 1")
	sessionID := sessionIDFromOutput(t, sessionsOutput)
	console.runSFTP(t, fmt.Sprintf("sessions -i %d", sessionID), "Starting interactive session")

	source := filepath.Join(stack.serverDir, "large-upload.bin")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(1 << 30); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	expectedHash := fileSHA256(t, source)

	console.runSFTPWithTimeout(t, "put large-upload.bin", 10*time.Minute, "Uploaded file:")
	assertFileSize(t, filepath.Join(stack.clientDir, "large-upload.bin"), 1<<30)
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	console.runSFTPWithTimeout(t, "get large-upload.bin", 10*time.Minute, "Downloaded file:")
	assertFileSize(t, source, 1<<30)
	if actualHash := fileSHA256(t, source); actualHash != expectedHash {
		t.Fatalf("1 GiB round-trip hash = %x, want %x", actualHash, expectedHash)
	}
}

func TestInteractiveShellVim(t *testing.T) {
	if os.Getenv("SLIDER_E2E_INTERACTIVE") != "1" {
		t.Skip("set SLIDER_E2E_INTERACTIVE=1 to run the interactive Vim scenario")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Vim PTY scenario is Unix-specific")
	}
	if _, err := exec.LookPath("vim"); err != nil {
		t.Skip("vim is not installed")
	}

	stack := startTestStack(t, true)
	console := openWebConsole(t, stack.port, nil)
	sessionsOutput := console.run(t, "sessions", "Active sessions: 1")
	sessionID := sessionIDFromOutput(t, sessionsOutput)
	console.runSFTP(t, fmt.Sprintf("sessions -i %d", sessionID), "Starting interactive session")
	offset := console.output.Len()
	console.write(t, websocket.TextMessage, []byte("shell\r"))
	console.waitFor(t, offset, commandTimeout, "Authenticated with mTLS", "slider-e2e-shell>")

	console.write(t, websocket.TextMessage, []byte("vim slider-e2e-vim.txt\r"))
	time.Sleep(500 * time.Millisecond)
	offset = console.output.Len()
	console.write(t, websocket.TextMessage, []byte("islider-e2e-vim\x1b:wq\r"))
	waitForFileContent(t, filepath.Join(stack.clientDir, "slider-e2e-vim.txt"), []byte("slider-e2e-vim"))
	console.waitFor(t, offset, commandTimeout, "slider-e2e-shell>")

	offset = console.output.Len()
	console.write(t, websocket.TextMessage, []byte("exit\r"))
	console.waitFor(t, offset, commandTimeout, "Shell Endpoint gracefully stopped", "(S")

	console.runSFTP(t, "rm slider-e2e-vim.txt", "Removed file")
}

func assertCommandSurface(
	t *testing.T,
	run func(*testing.T, string, ...string) string,
) {
	t.Helper()

	help := run(t, "help", `Execute "command" in local shell`)
	expectedCommands := []string{
		"bg", "clear", "connect", "exit", "help",
		"portfwd", "sessions", "shell", "socks", "ssh",
	}
	for _, command := range expectedCommands {
		if !regexp.MustCompile(`(?m)\b` + regexp.QuoteMeta(command) + `\b`).MatchString(help) {
			t.Errorf("help output does not contain command %q:\n%s", command, help)
		}
	}
	if actual := commandsFromHelp(help); strings.Join(actual, ",") != strings.Join(expectedCommands, ",") {
		t.Errorf("registered commands = %v, want %v", actual, expectedCommands)
	}

	for _, check := range []struct {
		command string
		want    string
	}{
		{command: "connect --help", want: "--fingerprint"},
		{command: "portfwd --help", want: "--reverse"},
		{command: "sessions --help", want: "--disconnect"},
		{command: "shell --help", want: "--interactive"},
		{command: "socks --help", want: "--local"},
		{command: "ssh --help", want: "--alt-shell"},
		{command: "sessions", want: "Active sessions: 0"},
		{command: "socks", want: "Active SOCKS servers: 0"},
		{command: "portfwd", want: "Active Port Forwards: 0"},
		{command: "sessions --interactive 1 --kill 1", want: "cannot be used together"},
		{command: "socks --kill", want: "--kill requires"},
		{command: "ssh", want: "one of the flags"},
		{command: "shell", want: "one of the flags"},
		{command: "connect", want: "exactly 1 argument"},
		{command: "definitely-not-a-command", want: "unknown command"},
		{command: "!printf slider-e2e-local", want: "slider-e2e-local"},
	} {
		run(t, check.command, check.want)
	}
}

func commandsFromHelp(output string) []string {
	var commands []string
	inTable := false
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "-------" {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if fields[0] == "!command" {
			break
		}
		commands = append(commands, fields[0])
	}
	return commands
}

func sessionIDFromOutput(t *testing.T, output string) int {
	t.Helper()
	match := regexp.MustCompile(`(?m)^\s*(\d+)\s+LOCAL\s+`).FindStringSubmatch(output)
	if len(match) != 2 {
		t.Fatalf("session ID not found:\n%s", output)
	}
	id, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("parse session ID: %v", err)
	}
	return id
}

func portFromOutput(t *testing.T, output, expression string) int {
	t.Helper()
	match := regexp.MustCompile(expression).FindStringSubmatch(output)
	if len(match) != 2 {
		t.Fatalf("port not found with %q:\n%s", expression, output)
	}
	port, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return port
}

func startTCPEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start TCP echo server: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func startUDPEchoServer(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("start UDP echo server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	go func() {
		buffer := make([]byte, 64*1024)
		for {
			n, address, readErr := conn.ReadFromUDP(buffer)
			if readErr != nil {
				return
			}
			_, _ = conn.WriteToUDP(buffer[:n], address)
		}
	}()
	return conn.LocalAddr().String()
}

func reserveUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("reserve UDP port: %v", err)
	}
	defer func() { _ = conn.Close() }()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func assertSOCKSRoundTrip(t *testing.T, socksPort int, target string) {
	t.Helper()
	dialer, err := proxy.SOCKS5(
		"tcp",
		fmt.Sprintf("127.0.0.1:%d", socksPort),
		nil,
		&net.Dialer{Timeout: commandTimeout},
	)
	if err != nil {
		t.Fatalf("create SOCKS5 dialer: %v", err)
	}
	conn, err := dialer.Dial("tcp", target)
	if err != nil {
		t.Fatalf("dial %s through SOCKS5 port %d: %v", target, socksPort, err)
	}
	defer func() { _ = conn.Close() }()
	assertConnectionRoundTrip(t, conn)
}

func assertTCPRoundTrip(t *testing.T, address string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, commandTimeout)
	if err != nil {
		t.Fatalf("dial TCP endpoint %s: %v", address, err)
	}
	defer func() { _ = conn.Close() }()
	assertConnectionRoundTrip(t, conn)
}

func assertUDPRoundTrip(t *testing.T, address string) {
	t.Helper()
	conn, err := net.DialTimeout("udp", address, commandTimeout)
	if err != nil {
		t.Fatalf("dial UDP endpoint %s: %v", address, err)
	}
	defer func() { _ = conn.Close() }()
	assertConnectionRoundTrip(t, conn)
}

func assertConnectionRoundTrip(t *testing.T, conn net.Conn) {
	t.Helper()
	payload := []byte("slider-e2e-network")
	if err := conn.SetDeadline(time.Now().Add(commandTimeout)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write endpoint payload: %v", err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatalf("read endpoint payload: %v", err)
	}
	if !bytes.Equal(response, payload) {
		t.Fatalf("endpoint response = %q, want %q", response, payload)
	}
}

func assertSSHCommand(t *testing.T, port int) {
	t.Helper()
	config := &ssh.ClientConfig{
		User:            "slider-e2e",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         commandTimeout,
	}
	client, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), config)
	if err != nil {
		t.Fatalf("dial SSH endpoint: %v", err)
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("create SSH session: %v", err)
	}
	defer func() { _ = session.Close() }()
	output, err := session.CombinedOutput("echo slider-e2e-ssh")
	if !bytes.Contains(output, []byte("slider-e2e-ssh")) {
		t.Fatalf("execute SSH command: %v\noutput: %q", err, output)
	}
}

func assertShellCommand(t *testing.T, port int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), commandTimeout)
	if err != nil {
		t.Fatalf("dial shell endpoint: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(commandTimeout)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "printf slider-e2e-shell\nexit\n"); err != nil {
		t.Fatalf("write shell command: %v", err)
	}
	output, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read shell output: %v", err)
	}
	if !strings.Contains(cleanTerminal(string(output)), "slider-e2e-shell") {
		t.Fatalf("shell output = %q", cleanTerminal(string(output)))
	}
}

func assertFileContent(t *testing.T, path string, expected []byte) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("%s content = %q, want %q", path, actual, expected)
	}
}

func assertFileSize(t *testing.T, path string, expected int64) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Size() != expected {
		t.Fatalf("%s size = %d, want %d", path, info.Size(), expected)
	}
}

func fileSHA256(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func waitForFileContent(t *testing.T, path string, expected []byte) {
	t.Helper()
	deadline := time.Now().Add(commandTimeout)
	for time.Now().Before(deadline) {
		actual, err := os.ReadFile(path)
		if err == nil && bytes.Contains(actual, expected) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s did not contain %q", path, expected)
}
