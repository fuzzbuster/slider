package listener

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
)

const (
	HealthPath        = "/health"
	VersionPath       = "/version"
	AuthPath          = "/auth"
	AuthChallengePath = AuthPath + "/challenge"
	AuthLoginPath     = AuthPath + "/token"
	AuthLogoutPath    = AuthPath + "/logout"
	ConsolePath       = "/console"
	ConsoleAssetsPath = ConsolePath + "/assets/"
	ConsoleWsPath     = ConsolePath + "/ws"
)

// ConsolePaths contains every HTTP console path derived from one base path.
type ConsolePaths struct {
	BasePath          string
	RootPath          string
	AuthPath          string
	AuthChallengePath string
	AuthLoginPath     string
	AuthLogoutPath    string
	ConsolePath       string
	ConsoleAssetsPath string
	ConsoleWsPath     string
}

// NewConsolePaths returns normalized HTTP console paths for a base path.
func NewConsolePaths(basePath string) (ConsolePaths, error) {
	basePath, err := NormalizeConsoleBasePath(basePath)
	if err != nil {
		return ConsolePaths{}, err
	}

	join := func(suffix string) string {
		if basePath == "" {
			return suffix
		}
		return basePath + suffix
	}

	rootPath := "/"
	if basePath != "" {
		rootPath = basePath
	}

	return ConsolePaths{
		BasePath:          basePath,
		RootPath:          rootPath,
		AuthPath:          join(AuthPath),
		AuthChallengePath: join(AuthChallengePath),
		AuthLoginPath:     join(AuthLoginPath),
		AuthLogoutPath:    join(AuthLogoutPath),
		ConsolePath:       join(ConsolePath),
		ConsoleAssetsPath: join(ConsoleAssetsPath),
		ConsoleWsPath:     join(ConsoleWsPath),
	}, nil
}

// DefaultConsolePaths returns console paths without a base path prefix.
func DefaultConsolePaths() ConsolePaths {
	paths, _ := NewConsolePaths("")
	return paths
}

// NormalizeConsoleBasePath canonicalizes the optional HTTP console base path.
func NormalizeConsoleBasePath(rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" || rawPath == "/" {
		return "", nil
	}
	if strings.Contains(rawPath, "://") {
		return "", fmt.Errorf("base path must not be a URL")
	}
	if strings.ContainsAny(rawPath, "?#\\") {
		return "", fmt.Errorf("base path must be a URL path without query, fragment, or backslash")
	}
	if !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	for _, segment := range strings.Split(strings.Trim(rawPath, "/"), "/") {
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("base path cannot contain %q segments", segment)
		}
	}

	cleaned := path.Clean(rawPath)
	if cleaned == "/" {
		return "", nil
	}
	if cleaned == HealthPath || cleaned == VersionPath {
		return "", fmt.Errorf("base path conflicts with reserved path %q", cleaned)
	}
	return cleaned, nil
}

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
	ConsoleOn    bool
	AuthOn       bool
	ConsolePaths ConsolePaths
}

// NewRouter creates an http.ServeMux with configured handlers
func NewRouter(cfg *RouterConfig) *http.ServeMux {
	mux := http.NewServeMux()
	paths := cfg.consolePaths()

	// Always register root handler (template or default response)
	mux.HandleFunc("/", templateHandler(cfg, paths))

	// Conditional handlers
	if cfg.HealthOn {
		mux.HandleFunc(HealthPath, healthHandler(cfg))
	}
	if cfg.VersionOn {
		mux.HandleFunc(VersionPath, versionHandler(cfg))
	}

	return mux
}

func (cfg *RouterConfig) consolePaths() ConsolePaths {
	if cfg.ConsolePaths.AuthPath == "" {
		return DefaultConsolePaths()
	}
	return cfg.ConsolePaths
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
func templateHandler(cfg *RouterConfig, consolePaths ConsolePaths) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.ServerHeader != "" {
			w.Header().Add("server", cfg.ServerHeader)
		}

		// Only handle exact root path
		if r.URL.Path != "/" &&
			r.URL.Path != consolePaths.RootPath &&
			r.URL.Path != consolePaths.RootPath+"/" {
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
			location := consolePaths.ConsolePath
			if cfg.AuthOn {
				location = consolePaths.AuthPath
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
