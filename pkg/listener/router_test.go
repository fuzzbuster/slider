package listener

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewConsolePaths(t *testing.T) {
	tests := []struct {
		name        string
		basePath    string
		wantBase    string
		wantRoot    string
		wantAuth    string
		wantConsole string
	}{
		{
			name:        "empty",
			wantRoot:    "/",
			wantAuth:    "/auth",
			wantConsole: "/console",
		},
		{
			name:        "slash",
			basePath:    "/",
			wantRoot:    "/",
			wantAuth:    "/auth",
			wantConsole: "/console",
		},
		{
			name:        "missing leading slash",
			basePath:    "admin",
			wantBase:    "/admin",
			wantRoot:    "/admin",
			wantAuth:    "/admin/auth",
			wantConsole: "/admin/console",
		},
		{
			name:        "trailing slash",
			basePath:    "/admin/",
			wantBase:    "/admin",
			wantRoot:    "/admin",
			wantAuth:    "/admin/auth",
			wantConsole: "/admin/console",
		},
		{
			name:        "nested",
			basePath:    "/ops/slider",
			wantBase:    "/ops/slider",
			wantRoot:    "/ops/slider",
			wantAuth:    "/ops/slider/auth",
			wantConsole: "/ops/slider/console",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paths, err := NewConsolePaths(tc.basePath)
			if err != nil {
				t.Fatal(err)
			}
			if paths.BasePath != tc.wantBase {
				t.Fatalf("BasePath = %q, want %q", paths.BasePath, tc.wantBase)
			}
			if paths.RootPath != tc.wantRoot {
				t.Fatalf("RootPath = %q, want %q", paths.RootPath, tc.wantRoot)
			}
			if paths.AuthPath != tc.wantAuth {
				t.Fatalf("AuthPath = %q, want %q", paths.AuthPath, tc.wantAuth)
			}
			if paths.ConsolePath != tc.wantConsole {
				t.Fatalf("ConsolePath = %q, want %q", paths.ConsolePath, tc.wantConsole)
			}
			if paths.AuthChallengePath != tc.wantAuth+"/challenge" {
				t.Fatalf("AuthChallengePath = %q, want %q", paths.AuthChallengePath, tc.wantAuth+"/challenge")
			}
			if paths.ConsoleWsPath != tc.wantConsole+"/ws" {
				t.Fatalf("ConsoleWsPath = %q, want %q", paths.ConsoleWsPath, tc.wantConsole+"/ws")
			}
			if paths.ConsoleAssetsPath != tc.wantConsole+"/assets/" {
				t.Fatalf("ConsoleAssetsPath = %q, want %q", paths.ConsoleAssetsPath, tc.wantConsole+"/assets/")
			}
		})
	}
}

func TestNewConsolePathsRejectsInvalidBasePath(t *testing.T) {
	for _, basePath := range []string{
		"https://example.com/admin",
		"/admin?debug=true",
		"/admin#fragment",
		`admin\console`,
		"/admin/../console",
		"/health",
		"/version",
	} {
		t.Run(basePath, func(t *testing.T) {
			if _, err := NewConsolePaths(basePath); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestTemplateHandlerRedirectsToConsoleBasePath(t *testing.T) {
	consolePaths, err := NewConsolePaths("/admin")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &RouterConfig{
		ConsoleOn:    true,
		AuthOn:       true,
		ConsolePaths: consolePaths,
	}
	mux := NewRouter(cfg)

	for _, requestPath := range []string{"/", "/admin", "/admin/"} {
		t.Run(requestPath, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, requestPath, nil)
			w := httptest.NewRecorder()

			mux.ServeHTTP(w, req)

			resp := w.Result()
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusFound)
			}
			if location := resp.Header.Get("Location"); location != "/admin/auth" {
				t.Fatalf("Location = %q, want %q", location, "/admin/auth")
			}
		})
	}
}

func TestHealthHandler(t *testing.T) {
	cfg := &RouterConfig{
		HealthOn:     true,
		ServerHeader: "test-server",
	}

	mux := NewRouter(cfg)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	if string(body) != "OK" {
		t.Errorf("Expected body 'OK', got '%s'", string(body))
	}

	if resp.Header.Get("server") != "test-server" {
		t.Errorf("Expected server header 'test-server', got '%s'", resp.Header.Get("server"))
	}
}

func TestHealthHandlerDisabled(t *testing.T) {
	cfg := &RouterConfig{
		HealthOn: false,
	}

	mux := NewRouter(cfg)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	resp := w.Result()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected status 404 when health disabled, got %d", resp.StatusCode)
	}
}

func TestVersionHandler(t *testing.T) {
	cfg := &RouterConfig{
		VersionOn:    true,
		ServerHeader: "test-server",
	}

	mux := NewRouter(cfg)

	req := httptest.NewRequest("GET", "/version", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type 'application/json', got '%s'", resp.Header.Get("Content-Type"))
	}

	if len(body) == 0 {
		t.Error("Expected non-empty JSON body")
	}
}

func TestVersionHandlerDisabled(t *testing.T) {
	cfg := &RouterConfig{
		VersionOn: false,
	}

	mux := NewRouter(cfg)

	req := httptest.NewRequest("GET", "/version", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	resp := w.Result()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected status 404 when version disabled, got %d", resp.StatusCode)
	}
}

func TestTemplateHandler(t *testing.T) {
	// Create a temporary template file
	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "test.html")
	templateContent := "<html><body>Test Template</body></html>"

	if err := os.WriteFile(templatePath, []byte(templateContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &RouterConfig{
		TemplatePath: templatePath,
		StatusCode:   http.StatusOK,
		ServerHeader: "test-server",
	}

	mux := NewRouter(cfg)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	if string(body) != templateContent {
		t.Errorf("Expected body '%s', got '%s'", templateContent, string(body))
	}
}

func TestTemplateHandlerNoTemplate(t *testing.T) {
	cfg := &RouterConfig{
		StatusCode: http.StatusNotFound,
	}

	mux := NewRouter(cfg)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	resp := w.Result()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", resp.StatusCode)
	}
}

func TestTemplateHandlerNonRootPath(t *testing.T) {
	cfg := &RouterConfig{
		StatusCode: http.StatusOK,
	}

	mux := NewRouter(cfg)

	req := httptest.NewRequest("GET", "/nonexistent", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	resp := w.Result()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected status 404 for non-root path, got %d", resp.StatusCode)
	}
}

func TestRouterMultipleEndpoints(t *testing.T) {
	cfg := &RouterConfig{
		HealthOn:     true,
		VersionOn:    true,
		StatusCode:   http.StatusOK,
		ServerHeader: "test-server",
	}

	mux := NewRouter(cfg)

	tests := []struct {
		path           string
		expectedStatus int
	}{
		{"/health", http.StatusOK},
		{"/version", http.StatusOK},
		{"/", http.StatusNotFound}, // No template
		{"/nonexistent", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()

			mux.ServeHTTP(w, req)

			resp := w.Result()

			if resp.StatusCode != tt.expectedStatus {
				t.Errorf("Path %s: expected status %d, got %d", tt.path, tt.expectedStatus, resp.StatusCode)
			}
		})
	}
}
