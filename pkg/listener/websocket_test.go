package listener

import (
	"net/http"
	"testing"
)

func TestWebSocketDefaultsAreIndependent(t *testing.T) {
	firstDialer := NewWebSocketDialer()
	secondDialer := NewWebSocketDialer()
	firstDialer.TLSClientConfig.ServerName = "first.example"
	if secondDialer.TLSClientConfig.ServerName != "" {
		t.Fatal("WebSocket dialers share TLS configuration")
	}

	firstUpgrader := NewWebSocketUpgrader()
	secondUpgrader := NewWebSocketUpgrader()
	firstUpgrader.CheckOrigin = func(_ *http.Request) bool { return true }
	if secondUpgrader.CheckOrigin != nil {
		t.Fatal("WebSocket upgraders share mutable configuration")
	}
}

func TestCheckURL(t *testing.T) {
	// Test cases for URL validation
	testCases := []struct {
		name        string
		url         string
		expectError bool
	}{
		{
			name:        "Valid HTTP URL",
			url:         "http://example.com",
			expectError: false,
		},
		{
			name:        "Valid HTTPS URL",
			url:         "https://example.com",
			expectError: false,
		},
		{
			name:        "Valid URL with port",
			url:         "http://example.com:8080",
			expectError: false,
		},
		{
			name:        "Valid URL with path",
			url:         "http://example.com/path",
			expectError: false,
		},
		{
			name:        "Missing scheme",
			url:         "example.com",
			expectError: false,
		},
		{
			name:        "Invalid scheme",
			url:         "ftp://example.com",
			expectError: true,
		},
		{
			name:        "Missing host",
			url:         "http://",
			expectError: true,
		},
		{
			name:        "Empty URL",
			url:         "",
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveURL(tc.url)

			if tc.expectError && err == nil {
				t.Errorf("Expected error for URL '%s', got nil", tc.url)
			} else if !tc.expectError && err != nil {
				t.Errorf("Unexpected error for URL '%s': %v", tc.url, err)
			}
		})
	}
}
