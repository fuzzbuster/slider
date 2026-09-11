package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"slider/pkg/conf"
	"slider/pkg/listener"
	"slider/pkg/slog"
)

// TestHandler_AuthRoutes verifies that auth routes are only registered when AuthOn is true
func TestHandler_AuthRoutes(t *testing.T) {
	tests := []struct {
		name         string
		authOn       bool
		path         string
		expectedCode int
	}{
		{
			name:         "AuthOn_AuthPage",
			authOn:       true,
			path:         listener.AuthPath,
			expectedCode: http.StatusOK, // Assuming templates are not loaded, might be 500 or 200 depending on handler
		},
		{
			name:         "AuthOff_AuthPage",
			authOn:       false,
			path:         listener.AuthPath,
			expectedCode: http.StatusNotFound,
		},
		{
			name:         "AuthOn_AuthLogin",
			authOn:       true,
			path:         listener.AuthLoginPath,
			expectedCode: http.StatusMethodNotAllowed, // GET on POST-only endpoint
		},
		{
			name:         "AuthOn_AuthChallenge",
			authOn:       true,
			path:         listener.AuthChallengePath,
			expectedCode: http.StatusMethodNotAllowed,
		},
		{
			name:         "AuthOff_AuthLogin",
			authOn:       false,
			path:         listener.AuthLoginPath,
			expectedCode: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup minimal server
			logger := slog.NewLogger("test-auth-routes")
			s := &server{
				Logger:        logger,
				authOn:        tc.authOn,
				httpConsoleOn: true, // Must be enabled for auth routes to be considered
			}

			// Mock template path to avoid file errors if possible, or expect them
			// For this test we only care if the route IS REGISTERED, so 404 vs anything else is key.
			// However, handleAuthPage reads templates. Let's see.
			// handleAuthToken (AuthLoginPath) doesn't use templates, so that's a safer check for registration.

			// We need to build the router
			handler := s.buildRouter()

			req := httptest.NewRequest("GET", tc.path, nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if tc.expectedCode == http.StatusNotFound {
				if w.Code != http.StatusNotFound {
					t.Errorf("Expected 404 for path %s when authOn=%v, got %d", tc.path, tc.authOn, w.Code)
				}
			} else {
				// If we expect it to exist, we just want NOT 404
				if w.Code == http.StatusNotFound {
					t.Errorf("Expected route to exist for path %s when authOn=%v, but got 404", tc.path, tc.authOn)
				}
			}
		})
	}
}

func TestGatewayOperatorRequiresAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name       string
		authOn     bool
		wantRouted bool
	}{
		{name: "authentication disabled", authOn: false, wantRouted: false},
		{name: "authentication enabled", authOn: true, wantRouted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &server{
				Logger:      slog.NewLogger("gateway-route-test"),
				authOn:      tc.authOn,
				gateway:     true,
				customProto: conf.Proto,
				urlRedirect: &url.URL{},
			}
			request := httptest.NewRequest(http.MethodGet, "/gateway", nil)
			request.Header.Set("Upgrade", "websocket")
			request.Header.Set("Sec-WebSocket-Protocol", conf.Proto)
			request.Header.Set("Sec-WebSocket-Operation", conf.OperationOperator)
			recorder := httptest.NewRecorder()

			s.buildRouter().ServeHTTP(recorder, request)
			routed := recorder.Code != http.StatusNotFound
			if routed != tc.wantRouted {
				t.Fatalf("routed = %v, want %v (status %d)", routed, tc.wantRouted, recorder.Code)
			}
		})
	}
}

func TestWebFrontendRoutes(t *testing.T) {
	s := &server{
		Logger:        slog.NewLogger("web-frontend-test"),
		httpConsoleOn: true,
		urlRedirect:   &url.URL{},
	}
	handler := s.buildRouter()

	pageRequest := httptest.NewRequest(http.MethodGet, listener.ConsolePath, nil)
	pageRecorder := httptest.NewRecorder()
	handler.ServeHTTP(pageRecorder, pageRequest)
	if pageRecorder.Code != http.StatusOK {
		t.Fatalf("console page returned %d", pageRecorder.Code)
	}
	if cacheControl := pageRecorder.Header().Get("Cache-Control"); cacheControl != "no-cache" {
		t.Fatalf("console cache control = %q, want no-cache", cacheControl)
	}

	body := pageRecorder.Body.String()
	if strings.Contains(body, "cdn.jsdelivr.net") {
		t.Fatal("console page references the legacy CDN")
	}
	assetPath := regexp.MustCompile(`/console/assets/[^"]+\.js`).FindString(body)
	if assetPath == "" {
		t.Fatal("console page does not reference an embedded JavaScript asset")
	}

	assetRequest := httptest.NewRequest(http.MethodGet, assetPath, nil)
	assetRecorder := httptest.NewRecorder()
	handler.ServeHTTP(assetRecorder, assetRequest)
	if assetRecorder.Code != http.StatusOK {
		t.Fatalf("console asset returned %d", assetRecorder.Code)
	}
	if cacheControl := assetRecorder.Header().Get("Cache-Control"); cacheControl !=
		"public, max-age=31536000, immutable" {
		t.Fatalf("asset cache control = %q", cacheControl)
	}
	if contentType := assetRecorder.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("asset content type = %q, want JavaScript", contentType)
	}

	methodRequest := httptest.NewRequest(http.MethodPost, assetPath, nil)
	methodRecorder := httptest.NewRecorder()
	handler.ServeHTTP(methodRecorder, methodRequest)
	if methodRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST console asset returned %d, want 405", methodRecorder.Code)
	}

	disabled := (&server{
		Logger:      slog.NewLogger("web-frontend-disabled-test"),
		urlRedirect: &url.URL{},
	}).buildRouter()
	disabledRecorder := httptest.NewRecorder()
	disabled.ServeHTTP(disabledRecorder, assetRequest)
	if disabledRecorder.Code != http.StatusNotFound {
		t.Fatalf("disabled console asset returned %d, want 404", disabledRecorder.Code)
	}
}
