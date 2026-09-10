package listener

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
)

const (
	AuthPath          = "/auth"
	AuthChallengePath = AuthPath + "/challenge"
	AuthLoginPath     = AuthPath + "/token"
	AuthLogoutPath    = AuthPath + "/logout"
	ConsolePath       = "/console"
	ConsoleWsPath     = ConsolePath + "/ws"
)

// RouterConfig holds options for enabling/disabling endpoints
type RouterConfig struct {
	// Response settings
	TemplatePath string
	ServerHeader string
	StatusCode   int
	UrlRedirect  *url.URL

	// Toggleable endpoints
	HealthOn  bool
	VersionOn bool

	// Console features
	ConsoleOn bool
	AuthOn    bool
}

// NewRouter creates an http.ServeMux with configured handlers
func NewRouter(cfg *RouterConfig) *http.ServeMux {
	mux := http.NewServeMux()

	// Always register root handler (template or default response)
	mux.HandleFunc("/", templateHandler(cfg))

	// Conditional handlers
	if cfg.HealthOn {
		mux.HandleFunc("/health", healthHandler(cfg))
	}
	if cfg.VersionOn {
		mux.HandleFunc("/version", versionHandler(cfg))
	}

	return mux
}

// healthHandler serves the /health endpoint
func healthHandler(cfg *RouterConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.ServerHeader != "" {
			w.Header().Add("server", cfg.ServerHeader)
		}
		_, _ = w.Write([]byte("OK"))
	}
}

// versionHandler serves the /version endpoint
func versionHandler(cfg *RouterConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.ServerHeader != "" {
			w.Header().Add("server", cfg.ServerHeader)
		}
		w.Header().Add("Content-Type", "application/json")
		vRes, _ := json.Marshal(HttpVersionResponse)
		_, _ = w.Write(vRes)
	}
}

// templateHandler serves the root endpoint with template or default response
func templateHandler(cfg *RouterConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.ServerHeader != "" {
			w.Header().Add("server", cfg.ServerHeader)
		}

		// Only handle exact root path
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("Not found"))
			return
		}

		if cfg.UrlRedirect != nil && cfg.UrlRedirect.String() != "" {
			w.Header().Add("Location", cfg.UrlRedirect.String())
			w.WriteHeader(http.StatusFound)
			return
		}

		if cfg.TemplatePath != "" {
			// Double-checking just in case the template was changed after starting up
			tErr := CheckTemplate(cfg.TemplatePath)
			if tErr == nil {
				fb, rErr := os.ReadFile(cfg.TemplatePath)
				if rErr == nil {
					w.WriteHeader(cfg.StatusCode)
					_, _ = w.Write(fb)
					return
				}
			}
		}

		if cfg.ConsoleOn {
			location := ConsolePath
			if cfg.AuthOn {
				location = AuthPath
			}
			w.Header().Add("Location", location)
			w.WriteHeader(http.StatusFound)
			return
		}

		// No template configured or error reading it - return 404
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not found"))
	}
}
