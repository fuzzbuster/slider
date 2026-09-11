//go:build e2e

package e2e_test

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"slider/pkg/auth"
	"slider/pkg/scrypt"

	"github.com/gorilla/websocket"
)

func TestAuthenticatedWebConsole(t *testing.T) {
	root := t.TempDir()
	serverHome := filepath.Join(root, ".slider") + string(os.PathSeparator)
	if err := os.MkdirAll(serverHome, 0o700); err != nil {
		t.Fatal(err)
	}

	tlsFiles := writeTestTLSCertificates(t, root)
	caPEM, err := os.ReadFile(tlsFiles.ca)
	if err != nil {
		t.Fatal(err)
	}

	port := reservePort(t)
	certJarPath := filepath.Join(root, "certs.json")
	server := startProcess(t, root, []string{
		"NO_COLOR=1",
		"S_HOME=" + serverHome,
		"TERM=xterm-256color",
	},
		"server",
		"--headless",
		"--auth",
		"--certs", certJarPath,
		"--ca-store",
		"--listener-cert", tlsFiles.serverCert,
		"--listener-key", tlsFiles.serverKey,
		"--address", "127.0.0.1",
		"--port", fmt.Sprint(port),
		"--colorless",
		"--verbose", "debug",
	)
	server.waitFor(t, startupTimeout, "Starting listener")

	keyPair := readFirstKeyPair(t, certJarPath)
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
	baseURL := fmt.Sprintf("https://localhost:%d", port)

	noRedirect := *client
	noRedirect.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	request, err := http.NewRequest(http.MethodGet, baseURL+"/console", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/html")
	response, err := noRedirect.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther ||
		response.Header.Get("Location") != "/auth" {
		t.Fatalf("unauthorized console response = %d %q",
			response.StatusCode, response.Header.Get("Location"))
	}

	invalidStatus, _ := postJSON(t, client, baseURL+"/auth/challenge", map[string]string{
		"fingerprint": "not-authorized",
	})
	if invalidStatus != http.StatusUnauthorized {
		t.Fatalf("invalid challenge status = %d, want %d",
			invalidStatus, http.StatusUnauthorized)
	}

	console := authenticateWebConsole(t, client, baseURL, tlsConfig, keyPair)
	console.run(t, "help", "certs")
	console.run(t, "certs", keyPair.FingerPrint)
	console.run(t, "certs --dump-ssh 1", "Private Key saved", "Public Key saved")
	assertFileSizeGreaterThanZero(t, filepath.Join(serverHome, "ssh", "id_ed25519_cert1"))
	assertFileSizeGreaterThanZero(t, filepath.Join(serverHome, "ssh", "id_ed25519_cert1.pub"))
	console.run(t, "certs --dump-ca", "CA Certificate saved", "CA Private Key saved")
	assertFileSizeGreaterThanZero(t, filepath.Join(serverHome, "ca_cert.pem"))
	assertFileSizeGreaterThanZero(t, filepath.Join(serverHome, "ca_key.pem"))
	console.run(t, "certs --new", "Private Key:", "Fingerprint:")
	console.run(t, "certs --remove 2", "Certificate ID 2 successfully removed")

	logoutRequest, err := http.NewRequest(http.MethodPost, baseURL+"/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	logoutResponse, err := client.Do(logoutRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = logoutResponse.Body.Close()
	if logoutResponse.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d", logoutResponse.StatusCode)
	}
}

func assertFileSizeGreaterThanZero(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Size() == 0 {
		t.Fatalf("%s is empty", path)
	}
}

func authenticateWebConsole(
	t *testing.T,
	client *http.Client,
	baseURL string,
	tlsConfig *tls.Config,
	keyPair *scrypt.KeyPair,
) *webConsole {
	t.Helper()

	_, challengeBody := postJSON(t, client, baseURL+"/auth/challenge", map[string]string{
		"fingerprint": keyPair.FingerPrint,
	})
	var challenge struct {
		ID    string `json:"challenge_id"`
		Value string `json:"challenge"`
	}
	if err := json.Unmarshal(challengeBody, &challenge); err != nil {
		t.Fatalf("decode challenge: %v\n%s", err, challengeBody)
	}
	challengeBytes, err := base64.RawStdEncoding.DecodeString(challenge.Value)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := scrypt.SignerFromKey(keyPair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := signer.Sign(rand.Reader, auth.ChallengeMessage(challengeBytes))
	if err != nil {
		t.Fatal(err)
	}
	status, tokenBody := postJSON(t, client, baseURL+"/auth/token", map[string]string{
		"fingerprint":  keyPair.FingerPrint,
		"challenge_id": challenge.ID,
		"signature":    base64.RawStdEncoding.EncodeToString(signature.Blob),
	})
	if status != http.StatusOK {
		t.Fatalf("token status = %d\n%s", status, tokenBody)
	}

	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{}
	for _, cookie := range client.Jar.Cookies(parsedURL) {
		headers.Add("Cookie", cookie.String())
	}
	dialer := *websocket.DefaultDialer
	dialer.TLSClientConfig = tlsConfig
	return openWebConsoleURL(
		t,
		"wss://"+parsedURL.Host+"/console/ws",
		&dialer,
		headers,
	)
}

type testTLSFiles struct {
	serverCert string
	serverKey  string
	clientCert string
	clientKey  string
	ca         string
}

func writeTestTLSCertificates(t *testing.T, directory string) testTLSFiles {
	t.Helper()
	ca, err := scrypt.CreateCA()
	if err != nil {
		t.Fatal(err)
	}
	serverCertificate, err := ca.CreateCertificate(true)
	if err != nil {
		t.Fatal(err)
	}
	clientCertificate, err := ca.CreateCertificate(false)
	if err != nil {
		t.Fatal(err)
	}
	files := testTLSFiles{
		serverCert: filepath.Join(directory, "listener.crt"),
		serverKey:  filepath.Join(directory, "listener.key"),
		clientCert: filepath.Join(directory, "client.crt"),
		clientKey:  filepath.Join(directory, "client.key"),
		ca:         filepath.Join(directory, "listener-ca.crt"),
	}
	for path, data := range map[string][]byte{
		files.serverCert: serverCertificate.CertPEM,
		files.serverKey:  serverCertificate.KeyPEM,
		files.clientCert: clientCertificate.CertPEM,
		files.clientKey:  clientCertificate.KeyPEM,
		files.ca:         ca.CertPEM,
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func readFirstKeyPair(t *testing.T, path string) *scrypt.KeyPair {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read certificate jar: %v", err)
	}
	var keyPairs map[string]*scrypt.KeyPair
	if err := json.Unmarshal(data, &keyPairs); err != nil {
		t.Fatalf("decode certificate jar: %v", err)
	}
	keyPair := keyPairs["1"]
	if keyPair == nil {
		t.Fatalf("certificate jar has no ID 1: %s", data)
	}
	return keyPair
}

func postJSON(
	t *testing.T,
	client *http.Client,
	endpoint string,
	body any,
) (int, []byte) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(endpoint, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST %s: %v", endpoint, err)
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, responseBody
}
