//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHTTPControlEndpoints(t *testing.T) {
	stack := startTestStack(t, false)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", stack.port)
	client := &http.Client{
		Timeout: commandTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	assertHTTPResponse(t, client, baseURL+"/", http.StatusFound, "")
	assertHTTPResponse(t, client, baseURL+"/health", http.StatusOK, "OK")
	_, versionBody := assertHTTPResponse(t, client, baseURL+"/version", http.StatusOK, "")
	var version struct {
		ProtoVersion string
		Version      string
	}
	if err := json.Unmarshal(versionBody, &version); err != nil {
		t.Fatalf("decode version response: %v", err)
	}
	if version.ProtoVersion == "" || version.Version == "" {
		t.Fatalf("incomplete version response: %s", versionBody)
	}
	assertHTTPResponse(t, client, baseURL+"/missing", http.StatusNotFound, "Not found")
	assertHTTPResponse(t, client, baseURL+"/console/ws", http.StatusBadRequest,
		"This endpoint requires a WebSocket connection.")

	request, err := http.NewRequest(http.MethodPost, baseURL+"/console", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /console status = %d, want %d",
			response.StatusCode, http.StatusMethodNotAllowed)
	}
}

func TestCLIValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "client requires address",
			args: []string{"client"},
			want: "requires exactly one valid server address",
		},
		{
			name: "listener rejects address",
			args: []string{"client", "--listener", "http://127.0.0.1:1"},
			want: "server address cannot be provided",
		},
		{
			name: "listener-only HTTP flag",
			args: []string{"client", "--http-health", "http://127.0.0.1:1"},
			want: "requires --listener",
		},
		{
			name: "listener and beacon conflict",
			args: []string{"client", "--listener", "--beacon"},
			want: "none of the others can be",
		},
		{
			name: "server rejects arguments",
			args: []string{"server", "unexpected"},
			want: "unknown command",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := runBinary(test.args...)
			if err == nil {
				t.Fatalf("%q unexpectedly succeeded:\n%s", test.args, output)
			}
			if !strings.Contains(output, test.want) {
				t.Fatalf("%q output does not contain %q:\n%s",
					test.args, test.want, output)
			}
		})
	}

	output, err := runBinary("--help")
	if err != nil {
		t.Fatalf("root help: %v\n%s", err, output)
	}
	for _, command := range []string{"client", "hook", "server", "version"} {
		if !strings.Contains(output, command) {
			t.Fatalf("root help does not expose %q:\n%s", command, output)
		}
	}

	for _, check := range []struct {
		args []string
		want []string
	}{
		{
			args: []string{"server", "--help"},
			want: []string{"--http-console", "--gateway", "--callback", "--auth"},
		},
		{
			args: []string{"client", "--help"},
			want: []string{"--listener", "--beacon", "--fingerprint", "--retry"},
		},
		{
			args: []string{"hook", "--help"},
			want: []string{"--auth-key", "--fingerprint", "--client-cert", "--ca"},
		},
	} {
		output, err := runBinary(check.args...)
		if err != nil {
			t.Fatalf("%q help: %v\n%s", check.args, err, output)
		}
		for _, expected := range check.want {
			if !strings.Contains(output, expected) {
				t.Fatalf("%q help does not contain %q:\n%s",
					check.args, expected, output)
			}
		}
	}
}

func TestServerAndClientHTTPPresentation(t *testing.T) {
	for _, mode := range []string{"server", "client"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			templatePath := filepath.Join(directory, "index.html")
			const templateBody = "<html><body>slider-e2e-template</body></html>"
			if err := os.WriteFile(templatePath, []byte(templateBody), 0o600); err != nil {
				t.Fatal(err)
			}
			port := reservePort(t)
			args := []string{
				mode,
				"--address", "127.0.0.1",
				"--port", fmt.Sprint(port),
				"--http-template", templatePath,
				"--http-server-header", "slider-e2e",
				"--http-status-code", "503",
				"--http-health",
				"--http-version",
				"--colorless",
				"--verbose", "debug",
			}
			if mode == "server" {
				args = append(args, "--headless")
			} else {
				args = append(args, "--listener")
			}
			process := startProcess(t, directory, []string{
				"NO_COLOR=1",
				"S_HOME=" + filepath.Join(directory, ".slider") + string(os.PathSeparator),
				"TERM=xterm-256color",
			}, args...)
			startedText := "Starting listener"
			if mode == "client" {
				startedText = "Listening on"
			}
			process.waitFor(t, startupTimeout, startedText)

			client := &http.Client{Timeout: commandTimeout}
			response, body := assertHTTPResponse(
				t,
				client,
				fmt.Sprintf("http://127.0.0.1:%d/", port),
				http.StatusServiceUnavailable,
				"slider-e2e-template",
			)
			if response.Header.Get("Server") != "slider-e2e" {
				t.Fatalf("Server header = %q", response.Header.Get("Server"))
			}
			if string(body) != templateBody {
				t.Fatalf("template body = %q", body)
			}
			assertHTTPResponse(t, client,
				fmt.Sprintf("http://127.0.0.1:%d/health", port),
				http.StatusOK, "OK")
			assertHTTPResponse(t, client,
				fmt.Sprintf("http://127.0.0.1:%d/version", port),
				http.StatusOK, "ProtoVersion")
		})
	}
}

func TestHTTPRedirect(t *testing.T) {
	directory := t.TempDir()
	port := reservePort(t)
	process := startProcess(t, directory, []string{
		"NO_COLOR=1",
		"S_HOME=" + filepath.Join(directory, ".slider") + string(os.PathSeparator),
		"TERM=xterm-256color",
	},
		"server",
		"--headless",
		"--address", "127.0.0.1",
		"--port", fmt.Sprint(port),
		"--http-redirect", "https://example.test/redirected",
		"--colorless",
		"--verbose", "debug",
	)
	process.waitFor(t, startupTimeout, "Starting listener")

	client := &http.Client{
		Timeout: commandTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, _ := assertHTTPResponse(
		t,
		client,
		fmt.Sprintf("http://127.0.0.1:%d/", port),
		http.StatusFound,
		"",
	)
	if response.Header.Get("Location") != "https://example.test/redirected" {
		t.Fatalf("redirect location = %q", response.Header.Get("Location"))
	}
}

func assertHTTPResponse(
	t *testing.T,
	client *http.Client,
	endpoint string,
	wantStatus int,
	wantBody string,
) (*http.Response, []byte) {
	t.Helper()
	response, err := client.Get(endpoint)
	if err != nil {
		t.Fatalf("GET %s: %v", endpoint, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("GET %s status = %d, want %d\n%s",
			endpoint, response.StatusCode, wantStatus, body)
	}
	if wantBody != "" && !strings.Contains(string(body), wantBody) {
		t.Fatalf("GET %s body does not contain %q:\n%s",
			endpoint, wantBody, body)
	}
	return response, body
}

func runBinary(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, e2eBinary, args...)
	output, err := command.CombinedOutput()
	return string(output), err
}
