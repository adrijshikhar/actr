package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/vault"
)

// Consumer-defined interfaces for loose coupling
type RunStore interface {
	GetRuns(ctx context.Context, limit, offset int, repo, status, q string) ([]*history.RunRecord, int, error)
	GetRun(ctx context.Context, id string) (*history.RunRecord, error)
	GetRunSteps(ctx context.Context, runID string) ([]*history.StepRecord, error)
	GetStepLogs(ctx context.Context, stepID string) ([]string, error)
	GetRunMetrics(ctx context.Context, runID string) ([]*monitor.TelemetrySample, error)
}

type SecretManager interface {
	ListSecrets(ctx context.Context) ([]vault.SecretMetadata, error)
	SetSecret(ctx context.Context, key, val, scope string) error
	DeleteSecret(ctx context.Context, key string) error
}

type EventSubscriber interface {
	SubscribeEvents(ctx context.Context) (<-chan monitor.Event, func(), error)
}

type DaemonController interface {
	Status() (string, error)
	Start() error
	Stop() error
}

// ServerConfig configures the Web Dashboard HTTP server.
type ServerConfig struct {
	Port             int
	Host             string
	DevMode          bool
	DevProxyURL      string
	Version          string
	ConfigDir        string
	RunStore         RunStore
	SecretManager    SecretManager
	EventSubscriber  EventSubscriber
	DaemonController DaemonController
}

// Server represents the web dashboard HTTP server.
type Server struct {
	cfg        ServerConfig
	mux        *http.ServeMux
	httpServer *http.Server
	listener   net.Listener
	startTime  time.Time
	mu         sync.Mutex
	closed     bool
}

// Compile-time interface verification
var _ http.Handler = (*Server)(nil)

// NewServer initializes and routes a new web dashboard Server.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}

	s := &Server{
		cfg:       cfg,
		mux:       http.NewServeMux(),
		startTime: time.Now(),
	}

	s.routes()
	return s
}

// routes sets up API endpoints and static SPA / dev proxy routing.
func (s *Server) routes() {
	// Status API endpoint
	s.mux.HandleFunc("GET /api/status", s.handleStatus)

	// Catch-all API 404 to ensure API routes never return HTML SPA fallback
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotFound, "api endpoint not found")
	})

	// Dev proxy vs Embedded SPA routing
	proxyURL := s.cfg.DevProxyURL
	if proxyURL == "" {
		proxyURL = os.Getenv("ACTR_DEV_PROXY_URL")
	}
	if proxyURL == "" && s.cfg.DevMode {
		proxyURL = "http://127.0.0.1:5174"
	}

	if proxyURL != "" {
		if target, err := url.Parse(proxyURL); err == nil {
			proxy := httputil.NewSingleHostReverseProxy(target)
			s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				// Don't proxy API routes
				if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
					writeJSONError(w, http.StatusNotFound, "api endpoint not found")
					return
				}
				proxy.ServeHTTP(w, r)
			})
			return
		}
	}

	// Embedded static SPA fallback routing
	subFS, err := fs.Sub(distFS, "dist")
	if err != nil {
		s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "embedded dist not found", http.StatusInternalServerError)
		})
		return
	}

	indexHTML, err := fs.ReadFile(subFS, "index.html")
	if err != nil {
		s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "index.html not found", http.StatusInternalServerError)
		})
		return
	}

	fileServer := http.FileServer(http.FS(subFS))
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			writeJSONError(w, http.StatusNotFound, "api endpoint not found")
			return
		}

		cleanPath := strings.TrimPrefix(filepath.Clean(r.URL.Path), "/")
		if cleanPath == "" || cleanPath == "." || cleanPath == "index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(indexHTML)
			return
		}

		// Check if requested file exists in embedded dist
		f, err := subFS.Open(cleanPath)
		if err != nil {
			// Fallback to index.html for client-side SPA routing
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(indexHTML)
			return
		}
		_ = f.Close()

		fileServer.ServeHTTP(w, r)
	})
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Start binds to the configured host:port and begins serving HTTP requests.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.httpServer != nil {
		s.mu.Unlock()
		return errors.New("server already started")
	}

	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	s.listener = l

	s.httpServer = &http.Server{
		Addr:              l.Addr().String(),
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.mu.Unlock()

	if ctx != nil {
		go func() {
			<-ctx.Done()
			_ = s.Stop()
		}()
	}

	go func() {
		if err := s.httpServer.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Server closed or listener error
		}
	}()

	return nil
}

// Stop gracefully shuts down the HTTP server.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

// Addr returns the network address the server is listening on or configured for.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
}

// Uniform JSON response helpers
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}
