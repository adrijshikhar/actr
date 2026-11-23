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
	"path"
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

type RunnerInfo struct {
	Repo           string   `json:"repo"`
	Name           string   `json:"name"`
	Labels         []string `json:"labels"`
	MaxConcurrency int      `json:"max_concurrency"`
	ActiveSlots    int      `json:"active_slots"`
	Status         string   `json:"status"`
}

type RunnerProvider interface {
	GetRunners(ctx context.Context) ([]RunnerInfo, error)
}

type Masker interface {
	Mask(string) string
}

type MetricsProvider interface {
	GetCurrentMetrics(ctx context.Context) (*monitor.RunnerMetrics, error)
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
	RunnerProvider   RunnerProvider
	MetricsProvider  MetricsProvider
	Masker           Masker
}

// Server represents the web dashboard HTTP server.
type Server struct {
	cfg        ServerConfig
	mux        *http.ServeMux
	httpServer *http.Server
	listener   net.Listener
	startTime  time.Time
	mu         sync.Mutex
	stopCh     chan struct{}
	stopOnce   sync.Once
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
		stopCh:    make(chan struct{}),
	}

	s.routes()
	return s
}

// routes sets up API endpoints and static SPA / dev proxy routing.
func (s *Server) routes() {
	// Status API endpoint
	s.mux.HandleFunc("GET /api/status", s.handleStatus)

	// Runners and Run history API endpoints
	s.mux.HandleFunc("GET /api/runners", s.handleRunners)
	s.mux.HandleFunc("GET /api/runs", s.handleRuns)
	s.mux.HandleFunc("GET /api/runs/{id}", s.handleRunDetail)
	s.mux.HandleFunc("GET /api/runs/{id}/logs", s.handleRunLogs)
	s.mux.HandleFunc("GET /api/runs/{id}/logs/stream", s.handleRunLogsStream)
	s.mux.HandleFunc("GET /api/runs/{id}/metrics", s.handleRunMetrics)

	// Live Metrics API endpoints
	s.mux.HandleFunc("GET /api/metrics/current", s.handleMetricsCurrent)
	s.mux.HandleFunc("GET /api/metrics/stream", s.handleMetricsStream)

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

		cleanPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
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
	if s.closed {
		s.mu.Unlock()
		return errors.New("server is stopped")
	}
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
			select {
			case <-ctx.Done():
				_ = s.Stop()
			case <-s.stopCh:
			}
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
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	httpServer := s.httpServer
	s.mu.Unlock()

	if httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(ctx)
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
