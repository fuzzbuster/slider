//go:build e2e

package e2e_test

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"testing"

	"slider/pkg/scrypt"
)

func TestAgentConnectsAndReportsPlatform(t *testing.T) {
	stack := startTestStack(t, true)
	output := stack.client.output.String()
	if !regexp.MustCompile(`server_system="` + regexp.QuoteMeta(runtime.GOOS) + `"`).MatchString(output) {
		t.Fatalf("client did not complete platform exchange:\n%s", output)
	}
}

func TestReverseClientReconnects(t *testing.T) {
	stack := startTestStack(t, false)
	clientDir := filepath.Join(t.TempDir(), "retry-client")
	if err := os.MkdirAll(clientDir, 0o700); err != nil {
		t.Fatal(err)
	}
	client := startProcess(t, clientDir, []string{
		"NO_COLOR=1",
		"PS1=slider-e2e-shell> ",
		"S_HOME=" + filepath.Join(clientDir, ".slider") + string(os.PathSeparator),
		"SHELL=/bin/sh",
		"TERM=xterm-256color",
	},
		"client",
		fmt.Sprintf("http://127.0.0.1:%d", stack.port),
		"--fingerprint", stack.fingerprint,
		"--retry",
		"--keepalive", "5s",
		"--colorless",
		"--verbose", "debug",
	)
	client.waitFor(t, startupTimeout, "Server identification received")

	console := openWebConsole(t, stack.port, nil)
	output := console.run(t, "sessions", "Active sessions: 1")
	sessionID := sessionIDFromOutput(t, output)
	reconnectOffset := client.output.Len()
	console.run(t, fmt.Sprintf("sessions --disconnect %d", sessionID), "Closed connection")
	waitForOutput(
		t,
		client.output,
		client.done,
		reconnectOffset,
		startupTimeout,
		"Server identification received",
	)
	output = console.run(t, "sessions", "Active sessions: 1")
	reconnectedID := sessionIDFromOutput(t, output)
	if reconnectedID == sessionID {
		t.Fatalf("reconnected session reused ID %d", sessionID)
	}
	console.run(t, fmt.Sprintf("sessions --kill %d", reconnectedID), "terminated gracefully")
}

func TestListenerClientTopology(t *testing.T) {
	stack := startTestStack(t, false)
	listenerDir := filepath.Join(t.TempDir(), "listener")
	if err := os.MkdirAll(listenerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tlsFiles := writeTestTLSCertificates(t, listenerDir)
	listenerPort := reservePort(t)
	listener := startProcess(t, listenerDir, []string{
		"NO_COLOR=1",
		"S_HOME=" + filepath.Join(listenerDir, ".slider") + string(os.PathSeparator),
		"TERM=xterm-256color",
	},
		"client",
		"--listener",
		"--address", "127.0.0.1",
		"--port", fmt.Sprint(listenerPort),
		"--fingerprint", stack.fingerprint,
		"--listener-cert", tlsFiles.serverCert,
		"--listener-key", tlsFiles.serverKey,
		"--listener-ca", tlsFiles.ca,
		"--keepalive", "5s",
		"--colorless",
		"--verbose", "debug",
	)
	listener.waitFor(t, startupTimeout, "Listening on tls://")

	console := openWebConsole(t, stack.port, nil)
	console.run(t, fmt.Sprintf(
		"connect --ca %s --server-name localhost --tls-cert %s --tls-key %s https://localhost:%d",
		tlsFiles.ca,
		tlsFiles.clientCert,
		tlsFiles.clientKey,
		listenerPort,
	), "Connection established")
	sessionsOutput := console.run(t, "sessions", "Active sessions: 1")
	sessionID := sessionIDFromOutput(t, sessionsOutput)
	console.runSFTP(t, fmt.Sprintf("sessions --interactive %d", sessionID), "Starting interactive session")
	console.runSFTP(t, "execute printf slider-e2e-listener", "slider-e2e-listener")
	console.run(t, "exit")
	console.run(t, fmt.Sprintf("sessions --kill %d", sessionID), "terminated gracefully")
}

func TestBeaconTopology(t *testing.T) {
	stack := startTestStack(t, false)
	beaconPort := reservePort(t)
	beaconDir := filepath.Join(t.TempDir(), "beacon")
	childDir := filepath.Join(t.TempDir(), "child")
	for _, directory := range []string{beaconDir, childDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	beacon := startProcess(t, beaconDir, []string{
		"NO_COLOR=1",
		"S_HOME=" + filepath.Join(beaconDir, ".slider") + string(os.PathSeparator),
		"TERM=xterm-256color",
	},
		"client",
		fmt.Sprintf("http://127.0.0.1:%d", stack.port),
		"--beacon",
		"--address", "127.0.0.1",
		"--port", fmt.Sprint(beaconPort),
		"--fingerprint", stack.fingerprint,
		"--keepalive", "5s",
		"--colorless",
		"--verbose", "debug",
	)
	beacon.waitFor(t, startupTimeout, "Listening on tcp://", "Server identification received")

	child := startProcess(t, childDir, []string{
		"NO_COLOR=1",
		"S_HOME=" + filepath.Join(childDir, ".slider") + string(os.PathSeparator),
		"TERM=xterm-256color",
	},
		"client",
		fmt.Sprintf("http://127.0.0.1:%d", beaconPort),
		"--fingerprint", stack.fingerprint,
		"--keepalive", "5s",
		"--colorless",
		"--verbose", "debug",
	)
	child.waitFor(t, startupTimeout, "Server identification received")

	console := openWebConsole(t, stack.port, nil)
	output := console.run(t, "sessions", "Active sessions: 2")
	sessionIDs := sessionIDsFromOutput(t, output)
	if len(sessionIDs) != 2 {
		t.Fatalf("session count = %d, want 2\n%s", len(sessionIDs), output)
	}
	for _, sessionID := range sessionIDs {
		marker := fmt.Sprintf("slider-e2e-beacon-%d", sessionID)
		console.runSFTP(t, fmt.Sprintf("sessions --interactive %d", sessionID),
			"Starting interactive session")
		console.runSFTP(t, "execute printf "+marker, marker)
		console.run(t, "exit")
	}
	for index := len(sessionIDs) - 1; index >= 0; index-- {
		console.run(t, fmt.Sprintf("sessions --kill %d", sessionIDs[index]),
			"terminated gracefully")
	}
}

func TestGatewayTopology(t *testing.T) {
	root := t.TempDir()
	sharedCertJar := filepath.Join(root, "shared-certs.json")

	gatewayDir := filepath.Join(root, "gateway")
	if err := os.MkdirAll(gatewayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	gatewayTLS := writeTestTLSCertificates(t, gatewayDir)
	gatewayPort := reservePort(t)
	gateway := startAuthenticatedServer(
		t,
		gatewayDir,
		gatewayPort,
		sharedCertJar,
		gatewayTLS,
		"--gateway",
	)
	gatewayOutput := gateway.waitFor(t, startupTimeout, "Starting listener")
	gatewayFingerprint := fingerprintFromOutput(t, gatewayOutput)
	keyPair := readFirstKeyPair(t, sharedCertJar)

	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	agent := startProcess(t, agentDir, []string{
		"NO_COLOR=1",
		"S_HOME=" + filepath.Join(agentDir, ".slider") + string(os.PathSeparator),
		"TERM=xterm-256color",
	},
		"client",
		fmt.Sprintf("https://localhost:%d", gatewayPort),
		"--key", keyPair.PrivateKey,
		"--server-ca", gatewayTLS.ca,
		"--server-name", "localhost",
		"--keepalive", "5s",
		"--colorless",
		"--verbose", "debug",
	)
	agent.waitFor(t, startupTimeout, "Server identification received")

	operatorDir := filepath.Join(root, "operator")
	if err := os.MkdirAll(operatorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	operatorTLS := writeTestTLSCertificates(t, operatorDir)
	operatorPort := reservePort(t)
	startAuthenticatedServer(
		t,
		operatorDir,
		operatorPort,
		sharedCertJar,
		operatorTLS,
	)

	console := openAuthenticatedConsole(
		t,
		operatorPort,
		operatorTLS.ca,
		keyPair,
	)
	console.run(t, fmt.Sprintf(
		"connect --gateway --cert-id 1 --fingerprint %s --ca %s --server-name localhost https://localhost:%d",
		gatewayFingerprint,
		gatewayTLS.ca,
		gatewayPort,
	), "Connection established")
	output := console.run(t, "sessions", "Active sessions: 1")
	remoteID := remoteSessionIDFromOutput(t, output)
	console.runSFTP(t, fmt.Sprintf("sessions --interactive %d", remoteID),
		"Starting interactive session")
	console.runSFTP(t, "execute printf slider-e2e-gateway", "slider-e2e-gateway")
	console.run(t, "exit")
	console.run(t, fmt.Sprintf("sessions --kill %d", remoteID), "terminated gracefully")
}

func TestGatewayCallbackTopology(t *testing.T) {
	root := t.TempDir()
	sharedCertJar := filepath.Join(root, "shared-certs.json")

	operatorDir := filepath.Join(root, "operator")
	if err := os.MkdirAll(operatorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	operatorTLS := writeTestTLSCertificates(t, operatorDir)
	operatorPort := reservePort(t)
	startAuthenticatedServer(
		t,
		operatorDir,
		operatorPort,
		sharedCertJar,
		operatorTLS,
	)
	keyPair := readFirstKeyPair(t, sharedCertJar)

	gatewayDir := filepath.Join(root, "gateway")
	if err := os.MkdirAll(gatewayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	gatewayTLS := writeTestTLSCertificates(t, gatewayDir)
	gatewayPort := reservePort(t)
	gateway := startAuthenticatedServer(
		t,
		gatewayDir,
		gatewayPort,
		sharedCertJar,
		gatewayTLS,
		"--gateway",
		"--callback", fmt.Sprintf("https://localhost:%d", operatorPort),
		"--callback-cert-id", "1",
		"--callback-ca", operatorTLS.ca,
		"--callback-server-name", "localhost",
	)
	gateway.waitFor(t, startupTimeout, "Established connection with client")

	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	agent := startProcess(t, agentDir, []string{
		"NO_COLOR=1",
		"S_HOME=" + filepath.Join(agentDir, ".slider") + string(os.PathSeparator),
		"TERM=xterm-256color",
	},
		"client",
		fmt.Sprintf("https://localhost:%d", gatewayPort),
		"--key", keyPair.PrivateKey,
		"--server-ca", gatewayTLS.ca,
		"--server-name", "localhost",
		"--keepalive", "5s",
		"--colorless",
		"--verbose", "debug",
	)
	agent.waitFor(t, startupTimeout, "Server identification received")

	console := openAuthenticatedConsole(
		t,
		operatorPort,
		operatorTLS.ca,
		keyPair,
	)
	output := console.run(t, "sessions", "Active sessions: 1")
	remoteID := remoteSessionIDFromOutput(t, output)
	console.runSFTP(t, fmt.Sprintf("sessions --interactive %d", remoteID),
		"Starting interactive session")
	console.runSFTP(t, "execute printf slider-e2e-callback", "slider-e2e-callback")
	console.run(t, "exit")
	console.run(t, fmt.Sprintf("sessions --kill %d", remoteID), "terminated gracefully")
}

func startAuthenticatedServer(
	t *testing.T,
	directory string,
	port int,
	certJar string,
	tlsFiles testTLSFiles,
	additionalArgs ...string,
) *runningProcess {
	t.Helper()
	args := []string{
		"server",
		"--headless",
		"--auth",
		"--ca-store",
		"--certs", certJar,
		"--listener-cert", tlsFiles.serverCert,
		"--listener-key", tlsFiles.serverKey,
		"--address", "127.0.0.1",
		"--port", fmt.Sprint(port),
		"--keepalive", "5s",
		"--colorless",
		"--verbose", "debug",
	}
	args = append(args, additionalArgs...)
	server := startProcess(t, directory, []string{
		"NO_COLOR=1",
		"S_HOME=" + filepath.Join(directory, ".slider") + string(os.PathSeparator),
		"TERM=xterm-256color",
	}, args...)
	server.waitFor(t, startupTimeout, "Starting listener")
	return server
}

func openAuthenticatedConsole(
	t *testing.T,
	port int,
	caPath string,
	keyPair *scrypt.KeyPair,
) *webConsole {
	t.Helper()
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(caPEM) {
		t.Fatal("append listener CA")
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    rootCAs,
		ServerName: "localhost",
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}
	return authenticateWebConsole(
		t,
		client,
		fmt.Sprintf("https://localhost:%d", port),
		tlsConfig,
		keyPair,
	)
}

func sessionIDsFromOutput(t *testing.T, output string) []int {
	t.Helper()
	matches := regexp.MustCompile(`(?m)^\s*(\d+)\s+(?:LOCAL|\d+)\s+`).FindAllStringSubmatch(output, -1)
	ids := make([]int, 0, len(matches))
	for _, match := range matches {
		id, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("parse session ID: %v", err)
		}
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

func fingerprintFromOutput(t *testing.T, output string) string {
	t.Helper()
	match := regexp.MustCompile(`fingerprint="([^"]+)"`).FindStringSubmatch(output)
	if len(match) != 2 {
		t.Fatalf("server fingerprint not found:\n%s", output)
	}
	return match[1]
}

func remoteSessionIDFromOutput(t *testing.T, output string) int {
	t.Helper()
	match := regexp.MustCompile(`(?m)^\s*(\d+)\s+\d+\s+`).FindStringSubmatch(output)
	if len(match) != 2 {
		t.Fatalf("remote session ID not found:\n%s", output)
	}
	id, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatal(err)
	}
	return id
}
