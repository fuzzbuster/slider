package server

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"sync"

	"slider/pkg/listener"
	"slider/pkg/slog"
)

type consolePageData struct {
	AuthOn         bool
	AuthPath       string
	AuthLoginPath  string
	AuthLogoutPath string
	ConsoleWsPath  string
}

var (
	//go:embed web/dist
	webFS embed.FS

	templates     *template.Template
	templatesOnce sync.Once
	templatesErr  error

	webRoot, webRootErr = fs.Sub(webFS, "web/dist")
	webAssetHandler     = http.StripPrefix(listener.ConsolePath+"/", http.FileServer(http.FS(webRoot)))
)

// loadTemplates loads and parses all HTML templates
func loadTemplates() (*template.Template, error) {
	templatesOnce.Do(func() {
		templates, templatesErr = template.ParseFS(webFS, "web/dist/*.html")
	})
	return templates, templatesErr
}

func (s *server) pageData() consolePageData {
	return consolePageData{
		AuthOn:         s.authOn,
		AuthPath:       listener.AuthPath,
		AuthLoginPath:  listener.AuthLoginPath,
		AuthLogoutPath: listener.AuthLogoutPath,
		ConsoleWsPath:  listener.ConsoleWsPath,
	}
}

func (s *server) handleConsoleAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == listener.ConsoleAssetsPath {
		http.NotFound(w, r)
		return
	}
	if webRootErr != nil {
		s.ErrorWith("Failed to load web assets", slog.F("err", webRootErr))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	webAssetHandler.ServeHTTP(w, r)
}

// handleAuthPage serves the authentication/login page
func (s *server) handleAuthPage(w http.ResponseWriter, r *http.Request) {
	// Only allow GET requests
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// If user is already authenticated, redirect to console
	if token := extractTokenFromRequest(r); token != "" {
		// Validate the token
		if _, _, err := s.validateToken(token); err == nil {
			// Valid token, redirect to console
			http.Redirect(w, r, "/console", http.StatusSeeOther)
			return
		}
		// Invalid token, continue to show login page
	}

	// Load templates
	tmpl, err := loadTemplates()
	if err != nil {
		s.ErrorWith("Failed to load templates", slog.F("err", err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err := tmpl.ExecuteTemplate(w, "auth.html", s.pageData()); err != nil {
		s.ErrorWith("Failed to render auth template", slog.F("err", err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleConsolePage serves the web terminal console page
func (s *server) handleConsolePage(w http.ResponseWriter, r *http.Request) {
	// Only allow GET requests
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Load templates
	tmpl, err := loadTemplates()
	if err != nil {
		s.ErrorWith("Failed to load templates", slog.F("err", err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err := tmpl.ExecuteTemplate(w, "console.html", s.pageData()); err != nil {
		s.ErrorWith("Failed to render console template", slog.F("err", err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
