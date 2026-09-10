package cmd

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
	"net/url"
	"os"
	"sync"

	"slider/pkg/auth"
	"slider/pkg/listener"
	"slider/pkg/scrypt"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var hookCmd = &cobra.Command{
	Use:   "hook [flags] <server_url>",
	Short: "Connect to a Slider Server console via WebSocket",
	Long: `Connects to a Slider Server's web console endpoint (/console/ws)
and provides access to a remote slider console through your local terminal.
`,
	Args:         cobra.ExactArgs(1),
	RunE:         runHook,
	SilenceUsage: true,
}

// Hook flags
var (
	hookFingerprint string
	hookAuthKey     string
	hookClientCert  string
	hookClientKey   string
	hookCA          string
	hookServerName  string
)

func init() {
	rootCmd.AddCommand(hookCmd)

	// Define flags
	hookCmd.Flags().StringVar(&hookFingerprint, "fingerprint", "", "Certificate fingerprint for authentication")
	hookCmd.Flags().StringVar(&hookAuthKey, "auth-key", "", "Slider private key for challenge signing")
	hookCmd.Flags().StringVar(&hookClientCert, "client-cert", "", "Client certificate for mTLS")
	hookCmd.Flags().StringVar(&hookClientKey, "client-key", "", "Client private key for mTLS")
	hookCmd.Flags().StringVar(&hookCA, "ca", "", "CA certificate for server verification")
	hookCmd.Flags().StringVar(&hookServerName, "server-name", "", "Server name for TLS verification")

	// Mark flag dependencies
	hookCmd.MarkFlagsRequiredTogether("fingerprint", "auth-key")
	hookCmd.MarkFlagsRequiredTogether("client-cert", "client-key")
}

func runHook(_ *cobra.Command, args []string) error {
	serverURL := args[0]

	// Parse and validate server URL
	parsedURL, err := listener.ResolveURL(serverURL)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}

	// Get authentication token if certificate authentication is configured.
	var token string
	var tlsConfig *tls.Config
	if hookFingerprint != "" {
		token, tlsConfig, err = getAuthToken(
			parsedURL,
			hookFingerprint,
			hookAuthKey,
			hookClientCert,
			hookClientKey,
			hookCA,
			hookServerName,
		)
		if err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		}
	}

	// Connect to WebSocket console
	if err := connectToConsole(parsedURL, token, tlsConfig); err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}

	return nil
}

// getAuthToken proves possession of a Slider private key and obtains a JWT.
func getAuthToken(
	baseURL *url.URL,
	fingerprint string,
	authKey string,
	certPath string,
	keyPath string,
	caPath string,
	serverName string,
) (string, *tls.Config, error) {
	client := &http.Client{}
	tlsConfig := &tls.Config{}
	var err error
	if (certPath != "" && keyPath != "") || caPath != "" || serverName != "" {
		tlsConfig, err = buildTLSConfig(certPath, keyPath, caPath, serverName)
		if err != nil {
			return "", nil, err
		}
		client.Transport = &http.Transport{
			TLSClientConfig: tlsConfig,
		}
	}

	challengeURL := *baseURL
	challengeURL.Path = listener.AuthChallengePath
	challengeURL.RawQuery = ""
	var challengeResp struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	if err := doJSONPost(client, challengeURL.String(), map[string]string{
		"fingerprint": fingerprint,
	}, &challengeResp); err != nil {
		return "", nil, fmt.Errorf("failed to obtain challenge: %w", err)
	}

	challenge, err := base64.RawStdEncoding.DecodeString(challengeResp.Challenge)
	if err != nil {
		return "", nil, fmt.Errorf("failed to decode challenge: %w", err)
	}
	signer, err := scrypt.SignerFromKey(authKey)
	if err != nil {
		return "", nil, fmt.Errorf("failed to load authentication key: %w", err)
	}
	signature, err := signer.Sign(rand.Reader, auth.ChallengeMessage(challenge))
	if err != nil {
		return "", nil, fmt.Errorf("failed to sign challenge: %w", err)
	}

	authURL := *baseURL
	authURL.Path = listener.AuthLoginPath
	authURL.RawQuery = ""
	var tokenResp struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
		TokenType string `json:"token_type"`
	}
	if err := doJSONPost(client, authURL.String(), map[string]string{
		"fingerprint":  fingerprint,
		"challenge_id": challengeResp.ChallengeID,
		"signature":    base64.RawStdEncoding.EncodeToString(signature.Blob),
	}, &tokenResp); err != nil {
		return "", nil, fmt.Errorf("authentication failed: %w", err)
	}

	return tokenResp.Token, tlsConfig, nil
}

func doJSONPost(client *http.Client, endpoint string, requestBody any, responseBody any) error {
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	if err := json.NewDecoder(resp.Body).Decode(responseBody); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	return nil
}

// buildTLSConfig creates a TLS configuration for mTLS
func buildTLSConfig(certPath, keyPath, caPath, serverName string) (*tls.Config, error) {
	tlsConfig := &tls.Config{}

	if serverName != "" {
		tlsConfig.ServerName = serverName
	}

	// Load CA certificate if provided
	if caPath != "" {
		caCert, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		tlsConfig.RootCAs = caCertPool
	}

	if certPath != "" && keyPath != "" {
		// Load client certificate
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	return tlsConfig, nil
}

// connectToConsole establishes a WebSocket connection and bridges it with the local terminal
func connectToConsole(baseURL *url.URL, token string, tlsConfig *tls.Config) error {
	// Convert HTTP URL to WebSocket URL
	wsURL, err := listener.FormatToWS(baseURL)
	if err != nil {
		return fmt.Errorf("failed to convert URL to WebSocket: %w", err)
	}
	wsURL.Path = listener.ConsoleWsPath
	wsURL.RawQuery = ""

	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}

	dialer := *websocket.DefaultDialer
	if wsURL.Scheme == "wss" {
		dialer.TLSClientConfig = tlsConfig
	}

	fmt.Printf("Connecting to %s://%s%s...\n", wsURL.Scheme, wsURL.Host, wsURL.Path)
	wsConn, _, err := dialer.Dial(wsURL.String(), headers)
	if err != nil {
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}
	defer func() { _ = wsConn.Close() }()

	// Put terminal in raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set terminal to raw mode: %w", err)
	}

	// Ensure terminal is ALWAYS restored, even on panic
	defer func() {
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
		// Print newline after terminal is restored to ensure clean output
		fmt.Println()
	}()

	// Channel to signal goroutine shutdown
	done := make(chan struct{})

	// Channel to signal connection closed (from either side)
	connClosed := make(chan struct{})

	var writeMutex sync.Mutex
	writeMessage := func(messageType int, data []byte) error {
		writeMutex.Lock()
		defer writeMutex.Unlock()
		return wsConn.WriteMessage(messageType, data)
	}

	// Goroutine: WebSocket → Stdout (remote output)
	go func() {
		defer close(connClosed)
		for {
			select {
			case <-done:
				return
			default:
				msgType, msg, err := wsConn.ReadMessage()
				if err != nil {
					// Connection closed - this is normal on exit
					return
				}

				if msgType == websocket.BinaryMessage || msgType == websocket.TextMessage {
					_, _ = os.Stdout.Write(msg)
				}
			}
		}
	}()

	// Goroutine: Stdin → WebSocket (local input)
	go func() {
		buf := make([]byte, 1024)
		for {
			select {
			case <-done:
				return
			default:
				n, err := os.Stdin.Read(buf)
				if err != nil {
					// Stdin closed or error - exit gracefully
					return
				}

				if n > 0 {
					if err := writeMessage(websocket.TextMessage, buf[:n]); err != nil {
						// Connection closed - exit gracefully
						return
					}
				}
			}
		}
	}()

	// Goroutine: Handle terminal resize
	go monitorWindowResize(writeMessage, done)

	// Send initial terminal size
	sendTermSize(writeMessage)

	// Connection closed by server (e.g., exit command) - this is normal
	<-connClosed

	// Signal all goroutines to stop
	close(done)

	return nil
}

func sendTermSize(writeMessage func(int, []byte) error) {
	width, height, err := term.GetSize(int(os.Stdin.Fd()))
	if err == nil {
		// Use struct to ensure consistent JSON field ordering
		resizeMsg := struct {
			Type string `json:"type"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}{
			Type: "resize",
			Cols: width,
			Rows: height,
		}
		if data, err := json.Marshal(resizeMsg); err == nil {
			_ = writeMessage(websocket.TextMessage, data)
		}
	}
}
