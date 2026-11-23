package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServer_StatusEndpoint(t *testing.T) {
	srv := NewServer(ServerConfig{
		Version: "v0.2.0",
		Port:    0, // random port
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal status error: %v", err)
	}
	if res["version"] != "v0.2.0" {
		t.Fatalf("expected v0.2.0, got %v", res["version"])
	}
	if _, ok := res["uptime_seconds"]; !ok {
		t.Fatalf("expected uptime_seconds field in response: %v", res)
	}
	if _, ok := res["daemon_connected"]; !ok {
		t.Fatalf("expected daemon_connected field in response: %v", res)
	}
}

func TestServer_SPAFallback(t *testing.T) {
	srv := NewServer(ServerConfig{
		Version: "v0.2.0",
	})

	testCases := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "root path",
			path:       "/",
			wantStatus: http.StatusOK,
			wantBody:   "actr web dashboard",
		},
		{
			name:       "index.html direct",
			path:       "/index.html",
			wantStatus: http.StatusOK,
			wantBody:   "actr web dashboard",
		},
		{
			name:       "spa unknown client route",
			path:       "/runs/run-1234",
			wantStatus: http.StatusOK,
			wantBody:   "actr web dashboard",
		},
		{
			name:       "api unknown route returns 404 json not spa",
			path:       "/api/not-found",
			wantStatus: http.StatusNotFound,
			wantBody:   `"error"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			srv.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("for path %s, expected status %d, got %d", tc.path, tc.wantStatus, rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.wantBody) {
				t.Fatalf("for path %s, expected body to contain %q, got %q", tc.path, tc.wantBody, body)
			}
		})
	}
}

func TestServer_DevProxy(t *testing.T) {
	// Start a mock Vite dev server
	devBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Dev-Proxy", "true")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mock vite dev server response"))
	}))
	defer devBackend.Close()

	srv := NewServer(ServerConfig{
		Version:     "v0.2.0",
		DevProxyURL: devBackend.URL,
	})

	// Non-API request should be proxied to devBackend
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from dev proxy, got %d", rec.Code)
	}
	if rec.Header().Get("X-Dev-Proxy") != "true" {
		t.Fatalf("expected X-Dev-Proxy header from proxy backend")
	}
	if !strings.Contains(rec.Body.String(), "mock vite dev server response") {
		t.Fatalf("expected proxy response body, got %q", rec.Body.String())
	}

	// API request should NOT be proxied
	apiRec := httptest.NewRecorder()
	apiReq := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	srv.ServeHTTP(apiRec, apiReq)

	if apiRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from API endpoint, got %d", apiRec.Code)
	}
	if apiRec.Header().Get("X-Dev-Proxy") == "true" {
		t.Fatalf("API endpoint should not be proxied")
	}
}

func TestServer_StartStopLifecycle(t *testing.T) {
	srv := NewServer(ServerConfig{
		Version: "v0.2.0",
		Port:    0, // ephemeral port
		Host:    "127.0.0.1",
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}

	addr := srv.Addr()
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("expected addr to start with 127.0.0.1:, got %q", addr)
	}

	// Query running server over TCP
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/status")
	if err != nil {
		t.Fatalf("failed to query running server: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK, got %d (body: %s)", resp.StatusCode, string(body))
	}

	// Stop server
	if err := srv.Stop(); err != nil {
		t.Fatalf("failed to stop server: %v", err)
	}

	// Double stop should be a no-op
	if err := srv.Stop(); err != nil {
		t.Fatalf("second stop should succeed: %v", err)
	}
}

func TestServer_StartStopLifecycle_BackgroundContext(t *testing.T) {
	// Tests that when context.Background() is used (never cancelled),
	// Stop() cleanly terminates the monitoring goroutine via stopCh without leaks.
	srv := NewServer(ServerConfig{
		Version: "v0.2.0",
		Port:    0,
		Host:    "127.0.0.1",
	})

	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}

	addr := srv.Addr()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/status")
	if err != nil {
		t.Fatalf("failed to query running server: %v", err)
	}
	resp.Body.Close()

	if err := srv.Stop(); err != nil {
		t.Fatalf("failed to stop server: %v", err)
	}

	// Verify server channel is closed
	select {
	case <-srv.stopCh:
		// success: stopCh was closed on Stop()
	case <-time.After(1 * time.Second):
		t.Fatalf("expected srv.stopCh to be closed upon Stop()")
	}
}
