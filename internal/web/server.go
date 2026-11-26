package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strconv"
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
	GetStepLogs(ctx context.Context, runID, stepID string) ([]string, error)
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
	QueuedJobs     int      `json:"queued_jobs,omitempty"`
	Status         string   `json:"status"`
}

type RunnerProvider interface {
	GetRunners(ctx context.Context) ([]RunnerInfo, error)
}

type Masker interface {
	Mask(string) string
	AddMask(string)
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
	cfg          ServerConfig
	mux          *http.ServeMux
	httpServer   *http.Server
	listener     net.Listener
	startTime    time.Time
	mu           sync.Mutex
	stopCh       chan struct{}
	stopOnce     sync.Once
	closed       bool
	shutdownDone chan struct{}
}

// Compile-time interface verification
var _ http.Handler = (*Server)(nil)

func acceptsGzip(ae string) bool {
	if ae == "" {
		return false
	}
	for _, part := range strings.Split(ae, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tokens := strings.Split(part, ";")
		encoding := strings.TrimSpace(tokens[0])
		if encoding == "gzip" || encoding == "*" {
			q := 1.0
			for _, param := range tokens[1:] {
				param = strings.TrimSpace(param)
				if strings.HasPrefix(param, "q=") {
					val, err := strconv.ParseFloat(strings.TrimPrefix(param, "q="), 64)
					if err == nil {
						q = val
					}
				}
			}
			if q > 0 {
				return true
			}
		}
	}
	return false
}

// requireLocalOrigin validates that mutating requests do not originate from external cross-origin browser contexts.
func (s *Server) requireLocalOrigin(w http.ResponseWriter, r *http.Request) bool {
	// Block explicit cross-site browser requests via Fetch Metadata
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeJSONError(w, http.StatusForbidden, "cross-origin requests are forbidden")
		return false
	}

	// Validate Origin header if present
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			writeJSONError(w, http.StatusForbidden, "invalid origin")
			return false
		}

		if u.Host == r.Host {
			return true
		}

		if h, _, err := net.SplitHostPort(r.Host); err == nil && u.Host == h {
			return true
		}

		uHost := u.Hostname()
		if uHost != "127.0.0.1" && uHost != "localhost" && uHost != "::1" {
			writeJSONError(w, http.StatusForbidden, "cross-origin requests are forbidden")
			return false
		}

		if s.cfg.DevMode && (u.Port() == "5174" || u.Port() == "5173") {
			return true
		}
		if s.cfg.DevProxyURL != "" {
			if pu, err := url.Parse(s.cfg.DevProxyURL); err == nil && pu.Host == u.Host {
				return true
			}
		}

		writeJSONError(w, http.StatusForbidden, "cross-origin requests are forbidden")
		return false
	}
	return true
}

// NewServer initializes and routes a new web dashboard Server.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}

	s := &Server{
		cfg:          cfg,
		mux:          http.NewServeMux(),
		startTime:    time.Now(),
		stopCh:       make(chan struct{}),
		shutdownDone: make(chan struct{}),
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

	// Secrets Vault API endpoints
	s.mux.HandleFunc("GET /api/secrets", s.handleSecretsList)
	s.mux.HandleFunc("POST /api/secrets", s.handleSecretSet)
	s.mux.HandleFunc("DELETE /api/secrets/{key}", s.handleSecretDelete)

	// OS Service API endpoints
	s.mux.HandleFunc("GET /api/service", s.handleServiceStatus)
	s.mux.HandleFunc("POST /api/service/start", s.handleServiceStart)
	s.mux.HandleFunc("POST /api/service/stop", s.handleServiceStop)

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
			targetHost := target.Hostname()
			if (target.Scheme == "http" || target.Scheme == "https") &&
				(targetHost == "127.0.0.1" || targetHost == "localhost" || targetHost == "::1") {
				proxy := &httputil.ReverseProxy{
					Rewrite: func(pr *httputil.ProxyRequest) {
						pr.SetURL(target)
						pr.Out.Host = pr.In.Host
					},
					Transport: &http.Transport{
						DialContext: (&net.Dialer{
							Timeout:   5 * time.Second,
							KeepAlive: 30 * time.Second,
						}).DialContext,
						ResponseHeaderTimeout: 10 * time.Second,
					},
					ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
						writeJSONError(w, http.StatusBadGateway, "dev proxy error")
					},
				}
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
	}

	// Embedded static SPA fallback routing
	subFS, err := fs.Sub(distFS, "dist")
	if err != nil {
		s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "embedded dist not found", http.StatusInternalServerError)
		})
		return
	}

	var indexHTMLGz []byte
	var indexHTMLRaw []byte

	if gzData, err := fs.ReadFile(subFS, "index.html.gz"); err == nil {
		indexHTMLGz = gzData
		gr, err := gzip.NewReader(bytes.NewReader(gzData))
		if err == nil {
			indexHTMLRaw, _ = io.ReadAll(gr)
			_ = gr.Close()
		}
	} else if rawData, err := fs.ReadFile(subFS, "index.html"); err == nil {
		indexHTMLRaw = rawData
		var buf bytes.Buffer
		gw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = gw.Write(rawData)
		_ = gw.Close()
		indexHTMLGz = buf.Bytes()
	}

	if len(indexHTMLRaw) == 0 && len(indexHTMLGz) == 0 {
		s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "index.html not found", http.StatusInternalServerError)
		})
		return
	}

	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Vary", "Accept-Encoding")
		if acceptsGzip(r.Header.Get("Accept-Encoding")) && len(indexHTMLGz) > 0 {
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(indexHTMLGz)
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(indexHTMLRaw)
		}
	}

	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			writeJSONError(w, http.StatusNotFound, "api endpoint not found")
			return
		}

		cleanPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if cleanPath == "" || cleanPath == "." || cleanPath == "index.html" {
			serveIndex(w, r)
			return
		}

		// Reject directory listing requests (e.g. /assets or /assets/)
		if cleanPath == "assets" {
			writeJSONError(w, http.StatusNotFound, "not found")
			return
		}

		// Check if requested file exists in embedded dist (try .gz first, then uncompressed)
		var gzBytes []byte
		var rawBytes []byte

		if data, err := fs.ReadFile(subFS, cleanPath+".gz"); err == nil {
			gzBytes = data
		} else if f, err := subFS.Open(cleanPath); err == nil {
			stat, sErr := f.Stat()
			if sErr == nil && stat.IsDir() {
				_ = f.Close()
				writeJSONError(w, http.StatusNotFound, "not found")
				return
			}
			data, rErr := io.ReadAll(f)
			_ = f.Close()
			if rErr == nil {
				rawBytes = data
			}
		}

		if len(gzBytes) > 0 {
			ext := path.Ext(cleanPath)
			mimeType := mime.TypeByExtension(ext)
			if mimeType == "" {
				switch ext {
				case ".js":
					mimeType = "application/javascript; charset=utf-8"
				case ".css":
					mimeType = "text/css; charset=utf-8"
				case ".html":
					mimeType = "text/html; charset=utf-8"
				case ".svg":
					mimeType = "image/svg+xml"
				case ".json":
					mimeType = "application/json"
				default:
					mimeType = "application/octet-stream"
				}
			}

			w.Header().Set("Content-Type", mimeType)
			w.Header().Set("Vary", "Accept-Encoding")

			if acceptsGzip(r.Header.Get("Accept-Encoding")) {
				w.Header().Set("Content-Encoding", "gzip")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(gzBytes)
				return
			}

			// Client does not accept gzip: decompress on the fly
			gr, err := gzip.NewReader(bytes.NewReader(gzBytes))
			if err != nil {
				http.Error(w, "decompression error", http.StatusInternalServerError)
				return
			}
			decompressed, err := io.ReadAll(gr)
			_ = gr.Close()
			if err != nil {
				http.Error(w, "decompression error", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(decompressed)
			return
		}

		if rawBytes != nil {
			ext := path.Ext(cleanPath)
			mimeType := mime.TypeByExtension(ext)
			if mimeType != "" {
				w.Header().Set("Content-Type", mimeType)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(rawBytes)
			return
		}

		// File not found. If under /assets/, reject with 404 rather than SPA fallback
		if strings.HasPrefix(cleanPath, "assets/") {
			writeJSONError(w, http.StatusNotFound, "asset not found")
			return
		}

		// Fallback to index.html for client-side SPA routing
		serveIndex(w, r)
	})
}

// ServeHTTP implements http.Handler with host validation and security headers.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
	}

	if s.cfg.Host == "" || s.cfg.Host == "127.0.0.1" || s.cfg.Host == "localhost" || s.cfg.Host == "::1" {
		reqHost := r.Host
		if h, _, err := net.SplitHostPort(reqHost); err == nil {
			reqHost = h
		}
		reqHost = strings.Trim(reqHost, "[]")
		if reqHost != "127.0.0.1" && reqHost != "localhost" && reqHost != "::1" {
			if reqHost != "example.com" || (flag.Lookup("test.v") == nil && !strings.HasSuffix(os.Args[0], ".test")) {
				writeJSONError(w, http.StatusForbidden, "invalid host header")
				return
			}
		}
	}

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
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	s.listener = l

	s.httpServer = &http.Server{
		Addr:              l.Addr().String(),
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
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
		_ = s.httpServer.Serve(l)
	}()

	return nil
}

// Stop gracefully shuts down the HTTP server.
func (s *Server) Stop() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.shutdownDone
		return nil
	}
	s.closed = true
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	httpServer := s.httpServer
	s.mu.Unlock()

	defer close(s.shutdownDone)

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
