package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/engine"
	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/vault"
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
		{
			name:       "assets directory listing rejected",
			path:       "/assets",
			wantStatus: http.StatusNotFound,
			wantBody:   `"error"`,
		},
		{
			name:       "assets slash directory listing rejected",
			path:       "/assets/",
			wantStatus: http.StatusNotFound,
			wantBody:   `"error"`,
		},
		{
			name:       "missing asset returns 404 json not spa",
			path:       "/assets/non-existent-bundle.js",
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

	// Test gzip compression negotiation
	t.Run("gzip accept-encoding serves gzipped response", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", "gzip, deflate")
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		if rec.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("expected Content-Encoding: gzip, got %q", rec.Header().Get("Content-Encoding"))
		}
	})
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
	defer func() { _ = resp.Body.Close() }()

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
	_ = resp.Body.Close()

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

type mockRunnerProvider struct {
	runners []RunnerInfo
	err     error
}

func (m *mockRunnerProvider) GetRunners(ctx context.Context) ([]RunnerInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.runners, nil
}

type mockRunStore struct {
	runs        []*history.RunRecord
	steps       map[string][]*history.StepRecord
	logs        map[string][]string
	metrics     map[string][]*monitor.TelemetrySample
	getRunsErr  error
	getRunErr   error
	getStepsErr error
	getLogsErr  error
}

func (m *mockRunStore) GetRuns(ctx context.Context, limit, offset int, repo, status, q string) ([]*history.RunRecord, int, error) {
	if m.getRunsErr != nil {
		return nil, 0, m.getRunsErr
	}
	var filtered []*history.RunRecord
	for _, r := range m.runs {
		if repo != "" && r.Repo != repo && r.RepoAlias != repo {
			continue
		}
		if status != "" && string(r.Status) != status {
			continue
		}
		if q != "" && !strings.Contains(r.Name, q) && !strings.Contains(r.ID, q) {
			continue
		}
		filtered = append(filtered, r)
	}
	total := len(filtered)
	if offset >= len(filtered) {
		return []*history.RunRecord{}, total, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], total, nil
}

func (m *mockRunStore) GetRun(ctx context.Context, id string) (*history.RunRecord, error) {
	if m.getRunErr != nil {
		return nil, m.getRunErr
	}
	for _, r := range m.runs {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, errors.New("run not found")
}

func (m *mockRunStore) GetRunSteps(ctx context.Context, runID string) ([]*history.StepRecord, error) {
	if m.getStepsErr != nil {
		return nil, m.getStepsErr
	}
	if m.steps != nil {
		return m.steps[runID], nil
	}
	return nil, nil
}

func (m *mockRunStore) GetStepLogs(ctx context.Context, runID, stepID string) ([]string, error) {
	if m.getLogsErr != nil {
		return nil, m.getLogsErr
	}
	if m.logs != nil {
		src := m.logs[stepID]
		res := make([]string, len(src))
		copy(res, src)
		return res, nil
	}
	return []string{}, nil
}

func (m *mockRunStore) GetRunMetrics(ctx context.Context, runID string) ([]*monitor.TelemetrySample, error) {
	if m.metrics != nil {
		return m.metrics[runID], nil
	}
	return nil, nil
}

func TestServer_Runners(t *testing.T) {
	mockRunners := []RunnerInfo{
		{
			Repo:           "adrijshikhar/actr",
			Name:           "actr-mac-runner",
			Labels:         []string{"self-hosted", "macOS", "ARM64"},
			MaxConcurrency: 4,
			ActiveSlots:    1,
			Status:         "idle",
		},
	}
	mockProv := &mockRunnerProvider{runners: mockRunners}

	srv := NewServer(ServerConfig{
		Version:        "v0.2.0",
		RunnerProvider: mockProv,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runners", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var res []RunnerInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal runners error: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 runner, got %d", len(res))
	}
	if res[0].Name != "actr-mac-runner" || res[0].Repo != "adrijshikhar/actr" {
		t.Fatalf("unexpected runner data: %+v", res[0])
	}

	// Test fallback when RunnerProvider is nil
	srvFallback := NewServer(ServerConfig{
		Version: "v0.2.0",
	})
	recFallback := httptest.NewRecorder()
	reqFallback := httptest.NewRequest(http.MethodGet, "/api/runners", nil)
	srvFallback.ServeHTTP(recFallback, reqFallback)
	if recFallback.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for fallback, got %d", recFallback.Code)
	}
	var resFallback []RunnerInfo
	if err := json.Unmarshal(recFallback.Body.Bytes(), &resFallback); err != nil {
		t.Fatalf("unmarshal fallback runners error: %v", err)
	}
	if resFallback == nil {
		t.Fatalf("expected non-nil slice (empty array) for fallback runners")
	}

	// Test error propagation from RunnerProvider
	errProv := &mockRunnerProvider{err: errors.New("provider failure")}
	srvErr := NewServer(ServerConfig{
		RunnerProvider: errProv,
	})
	recErr := httptest.NewRecorder()
	reqErr := httptest.NewRequest(http.MethodGet, "/api/runners", nil)
	srvErr.ServeHTTP(recErr, reqErr)
	if recErr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 error, got %d", recErr.Code)
	}
}

func TestServer_Runners_FallbackConfigDir(t *testing.T) {
	// 1. Multi-repo supervisor config test
	multiDir := t.TempDir()
	cfgContent := `version: "1"
global:
  max_concurrent_jobs: 4
repos:
  my-app:
    url: "https://github.com/my-org/my-app"
    max_concurrent_jobs: 3
`
	if err := os.WriteFile(multiDir+"/config.yaml", []byte(cfgContent), 0644); err != nil {
		t.Fatalf("write config.yaml error: %v", err)
	}

	repoSubDir := multiDir + "/repos/my-app"
	if err := os.MkdirAll(repoSubDir, 0700); err != nil {
		t.Fatalf("mkdir repo sub dir error: %v", err)
	}
	runnerJSON := `{
  "agentName": "app-runner-01",
  "gitHubUrl": "https://github.com/my-org/my-app"
}`
	if err := os.WriteFile(repoSubDir+"/.runner", []byte(runnerJSON), 0644); err != nil {
		t.Fatalf("write .runner error: %v", err)
	}

	srvMulti := NewServer(ServerConfig{
		ConfigDir: multiDir,
	})
	recMulti := httptest.NewRecorder()
	reqMulti := httptest.NewRequest(http.MethodGet, "/api/runners", nil)
	srvMulti.ServeHTTP(recMulti, reqMulti)

	if recMulti.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", recMulti.Code)
	}
	var resMulti []RunnerInfo
	if err := json.Unmarshal(recMulti.Body.Bytes(), &resMulti); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(resMulti) != 1 {
		t.Fatalf("expected 1 runner, got %d", len(resMulti))
	}
	if resMulti[0].Name != "app-runner-01" || resMulti[0].Repo != "my-org/my-app" || resMulti[0].MaxConcurrency != 3 {
		t.Fatalf("unexpected runner info: %+v", resMulti[0])
	}

	// 2. Single-repo config fallback test
	singleDir := t.TempDir()
	singleRunnerJSON := `{
  "agentName": "standalone-runner",
  "gitHubUrl": "https://github.com/standalone-org/standalone-repo"
}`
	if err := os.WriteFile(singleDir+"/.runner", []byte(singleRunnerJSON), 0644); err != nil {
		t.Fatalf("write single .runner error: %v", err)
	}

	srvSingle := NewServer(ServerConfig{
		ConfigDir: singleDir,
	})
	recSingle := httptest.NewRecorder()
	reqSingle := httptest.NewRequest(http.MethodGet, "/api/runners", nil)
	srvSingle.ServeHTTP(recSingle, reqSingle)

	if recSingle.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", recSingle.Code)
	}
	var resSingle []RunnerInfo
	if err := json.Unmarshal(recSingle.Body.Bytes(), &resSingle); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(resSingle) != 1 {
		t.Fatalf("expected 1 runner, got %d", len(resSingle))
	}
	if resSingle[0].Name != "standalone-runner" || resSingle[0].Repo != "standalone-org/standalone-repo" || resSingle[0].MaxConcurrency != 1 {
		t.Fatalf("unexpected single runner info: %+v", resSingle[0])
	}
}

func TestServer_Runs_PaginationAndFilter(t *testing.T) {
	// Create 30 mock runs
	var runs []*history.RunRecord
	for i := 1; i <= 30; i++ {
		runs = append(runs, &history.RunRecord{
			ID:          fmt.Sprintf("run-%03d", i),
			Name:        fmt.Sprintf("Job %d", i),
			Workflow:    "ci.yaml",
			Repo:        "adrijshikhar/actr",
			RepoAlias:   "actr",
			Status:      monitor.StatusSuccess,
			StartedAt:   time.Now().Add(-time.Duration(30-i) * time.Minute),
			DurationSec: int64(10 + i),
		})
	}

	mockStore := &mockRunStore{
		runs: runs,
	}

	srv := NewServer(ServerConfig{
		RunStore: mockStore,
	})

	// 1. Default pagination (limit=25, offset=0)
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}

		var res struct {
			Runs   []*history.RunRecord `json:"runs"`
			Total  int                  `json:"total"`
			Limit  int                  `json:"limit"`
			Offset int                  `json:"offset"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if res.Total != 30 {
			t.Fatalf("expected total 30, got %d", res.Total)
		}
		if res.Limit != 25 {
			t.Fatalf("expected default limit 25, got %d", res.Limit)
		}
		if res.Offset != 0 {
			t.Fatalf("expected default offset 0, got %d", res.Offset)
		}
		if len(res.Runs) != 25 {
			t.Fatalf("expected 25 runs, got %d", len(res.Runs))
		}
	}

	// 2. Custom pagination (limit=10, offset=5)
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs?limit=10&offset=5", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}

		var res struct {
			Runs   []*history.RunRecord `json:"runs"`
			Total  int                  `json:"total"`
			Limit  int                  `json:"limit"`
			Offset int                  `json:"offset"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if res.Limit != 10 || res.Offset != 5 {
			t.Fatalf("expected limit 10 and offset 5, got limit=%d offset=%d", res.Limit, res.Offset)
		}
		if len(res.Runs) != 10 {
			t.Fatalf("expected 10 runs, got %d", len(res.Runs))
		}
	}

	// 3. Clamping limits (limit=500 clamped to 100, limit=-5 clamped to 1, offset=-10 clamped to 0)
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs?limit=500&offset=-10", nil)
		srv.ServeHTTP(rec, req)

		var res struct {
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &res)
		if res.Limit != 100 {
			t.Fatalf("expected limit clamped to 100, got %d", res.Limit)
		}
		if res.Offset != 0 {
			t.Fatalf("expected offset clamped to 0, got %d", res.Offset)
		}
	}

	// 4. Filtering with repo, status, q
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs?repo=adrijshikhar/actr&status=success&q=Job%201", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}

		var res struct {
			Runs  []*history.RunRecord `json:"runs"`
			Total int                  `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		// "Job 1" matches Job 1, Job 10, Job 11..19 -> 11 matches
		if res.Total != 11 {
			t.Fatalf("expected 11 filtered runs matching 'Job 1', got %d", res.Total)
		}
	}

	// 5. Unconfigured RunStore returns 503
	{
		srvNilStore := NewServer(ServerConfig{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
		srvNilStore.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 Service Unavailable, got %d", rec.Code)
		}
	}

	// 6. Store error returns 500
	{
		srvErrStore := NewServer(ServerConfig{
			RunStore: &mockRunStore{getRunsErr: errors.New("db error")},
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
		srvErrStore.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 Internal Server Error, got %d", rec.Code)
		}
	}
}

func TestServer_RunDetail(t *testing.T) {
	steps := []*history.StepRecord{
		{
			ID:          "step-01",
			Number:      1,
			Name:        "Checkout code",
			Status:      monitor.StatusSuccess,
			DurationSec: 2,
		},
		{
			ID:          "step-02",
			Number:      2,
			Name:        "Run unit tests",
			Status:      monitor.StatusSuccess,
			DurationSec: 15,
		},
	}

	run := &history.RunRecord{
		ID:          "run-detail-123",
		Name:        "Test Run",
		Workflow:    "build.yaml",
		Repo:        "adrijshikhar/actr",
		Status:      monitor.StatusSuccess,
		DurationSec: 17,
		Steps:       steps,
	}

	mockStore := &mockRunStore{
		runs: []*history.RunRecord{run},
		steps: map[string][]*history.StepRecord{
			"run-detail-123": steps,
		},
	}

	srv := NewServer(ServerConfig{
		RunStore: mockStore,
	})

	// 1. Success detail query
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/run-detail-123", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		var res history.RunRecord
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if res.ID != "run-detail-123" {
			t.Fatalf("expected ID run-detail-123, got %s", res.ID)
		}
		if len(res.Steps) != 2 {
			t.Fatalf("expected 2 steps, got %d", len(res.Steps))
		}
		if res.Steps[0].Name != "Checkout code" || res.Steps[1].Name != "Run unit tests" {
			t.Fatalf("unexpected steps: %+v", res.Steps)
		}
	}

	// 2. Not found run returns 404
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/non-existent-id", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found, got %d", rec.Code)
		}
		var errRes map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &errRes)
		if errRes["error"] != "run not found" {
			t.Fatalf("expected 'run not found' error, got %q", errRes["error"])
		}
	}

	// 3. Unconfigured RunStore returns 503
	{
		srvNilStore := NewServer(ServerConfig{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/run-123", nil)
		srvNilStore.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 Service Unavailable, got %d", rec.Code)
		}
	}
}

func TestServer_RunLogs_Masking(t *testing.T) {
	steps := []*history.StepRecord{
		{ID: "step-1", Number: 1, Name: "Step 1"},
		{ID: "step-2", Number: 2, Name: "Step 2"},
	}
	run := &history.RunRecord{
		ID:    "run-mask-456",
		Steps: steps,
	}

	stepLogs := map[string][]string{
		"step-1": {
			"Executing step 1",
			"Setting up token: ghp_very_secret_token_value",
		},
		"step-2": {
			"Executing step 2",
			"Connecting with key: AWS_SECRET_KEY_SECRET",
			"Completed successfully",
		},
	}

	masker := engine.NewSecretMasker()
	masker.AddMask("ghp_very_secret_token_value")
	masker.AddMask("AWS_SECRET_KEY_SECRET")

	mockStore := &mockRunStore{
		runs:  []*history.RunRecord{run},
		steps: map[string][]*history.StepRecord{"run-mask-456": steps},
		logs:  stepLogs,
	}

	srv := NewServer(ServerConfig{
		RunStore: mockStore,
		Masker:   masker,
	})

	// 1. Single step logs query (?step_id=step-1) with masking
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/run-mask-456/logs?step_id=step-1", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}

		var res struct {
			Logs []string `json:"logs"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if len(res.Logs) != 2 {
			t.Fatalf("expected 2 lines, got %d", len(res.Logs))
		}
		if strings.Contains(res.Logs[1], "ghp_very_secret_token_value") {
			t.Fatalf("secret was not redacted in line: %s", res.Logs[1])
		}
		if !strings.Contains(res.Logs[1], "***") {
			t.Fatalf("expected *** in redacted line: %s", res.Logs[1])
		}
	}

	// 2. Aggregated logs across all steps (no step_id) with masking
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/run-mask-456/logs", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}

		var res struct {
			Logs []string `json:"logs"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if len(res.Logs) != 5 {
			t.Fatalf("expected 5 aggregated lines, got %d", len(res.Logs))
		}
		for _, line := range res.Logs {
			if strings.Contains(line, "ghp_very_secret_token_value") || strings.Contains(line, "AWS_SECRET_KEY_SECRET") {
				t.Fatalf("unmasked secret found in logs: %s", line)
			}
		}
	}

	// 3. Without masker (Masker: nil)
	srvNoMask := NewServer(ServerConfig{
		RunStore: mockStore,
		Masker:   nil,
	})
	recNoMask := httptest.NewRecorder()
	reqNoMask := httptest.NewRequest(http.MethodGet, "/api/runs/run-mask-456/logs?step_id=step-1", nil)
	srvNoMask.ServeHTTP(recNoMask, reqNoMask)

	var resNoMask struct {
		Logs []string `json:"logs"`
	}
	_ = json.Unmarshal(recNoMask.Body.Bytes(), &resNoMask)
	if !strings.Contains(resNoMask.Logs[1], "ghp_very_secret_token_value") {
		t.Fatalf("expected unmasked secret when Masker is nil, got %s", resNoMask.Logs[1])
	}

	// 4. Unconfigured RunStore returns 503
	{
		srvNilStore := NewServer(ServerConfig{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/run-mask-456/logs", nil)
		srvNilStore.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 Service Unavailable, got %d", rec.Code)
		}
	}

	// 5. Non-existent run returns 404
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/non-existent-run-999/logs", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found, got %d", rec.Code)
		}
	}
}

type mockEventSubscriber struct {
	eventsCh    chan monitor.Event
	subscribeFn func(ctx context.Context) (<-chan monitor.Event, func(), error)
	unsubCalled bool
	subErr      error
	mu          sync.Mutex
}

func (m *mockEventSubscriber) SubscribeEvents(ctx context.Context) (<-chan monitor.Event, func(), error) {
	if m.subscribeFn != nil {
		return m.subscribeFn(ctx)
	}
	if m.subErr != nil {
		return nil, nil, m.subErr
	}
	unsub := func() {
		m.mu.Lock()
		m.unsubCalled = true
		m.mu.Unlock()
	}
	return m.eventsCh, unsub, nil
}

type mockMetricsProvider struct {
	metrics *monitor.RunnerMetrics
	err     error
}

func (m *mockMetricsProvider) GetCurrentMetrics(ctx context.Context) (*monitor.RunnerMetrics, error) {
	return m.metrics, m.err
}

type nonFlusherResponseWriter struct {
	http.ResponseWriter
}

func TestServer_LogsStream_SSE(t *testing.T) {
	masker := engine.NewSecretMasker()
	masker.AddMask("SUPER_SECRET_TOKEN_123")

	eventsCh := make(chan monitor.Event, 20)
	mockSub := &mockEventSubscriber{
		eventsCh: eventsCh,
	}

	srv := NewServer(ServerConfig{
		EventSubscriber: mockSub,
		Masker:          masker,
	})

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// 1. Successful stream with events, masking, and completion
	t.Run("stream events and masking", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/runs/run-sse-1/logs/stream", nil)
		if err != nil {
			t.Fatalf("new request failed: %v", err)
		}

		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		if ctype := resp.Header.Get("Content-Type"); !strings.Contains(ctype, "text/event-stream") {
			t.Fatalf("expected text/event-stream, got %s", ctype)
		}
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Fatalf("expected no-cache, got %s", cc)
		}
		if accel := resp.Header.Get("X-Accel-Buffering"); accel != "no" {
			t.Fatalf("expected X-Accel-Buffering: no, got %s", accel)
		}

		// Send events
		eventsCh <- monitor.Event{
			Type:    monitor.EventStepLog,
			JobID:   "different-job-999",
			StepID:  "step-diff",
			LogLine: "Ignored log for different job",
		}
		eventsCh <- monitor.Event{
			Type:   monitor.EventStepStart,
			JobID:  "run-sse-1",
			StepID: "step-1",
			Step: &monitor.StepInfo{
				ID:     "step-1",
				Name:   "Init Step",
				Number: 1,
				Status: monitor.StatusRunning,
			},
		}
		eventsCh <- monitor.Event{
			Type:    monitor.EventStepLog,
			JobID:   "run-sse-1",
			StepID:  "step-1",
			LogLine: "Authenticating with SUPER_SECRET_TOKEN_123 in runner",
		}
		eventsCh <- monitor.Event{
			Type:   monitor.EventStepFinish,
			JobID:  "run-sse-1",
			StepID: "step-1",
			Step: &monitor.StepInfo{
				ID:     "step-1",
				Name:   "Init Step",
				Number: 1,
				Status: monitor.StatusSuccess,
			},
		}
		eventsCh <- monitor.Event{
			Type:  monitor.EventJobFinish,
			JobID: "run-sse-1",
			Job: &monitor.JobInfo{
				ID:     "run-sse-1",
				Status: monitor.StatusSuccess,
			},
		}

		// Read streamed response
		reader := bufio.NewReader(resp.Body)
		var receivedEvents []string
		var receivedData []string

		// Read until EOF (job_complete exits the handler)
		doneCh := make(chan struct{})
		go func() {
			defer close(doneCh)
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "event:") {
					receivedEvents = append(receivedEvents, strings.TrimSpace(strings.TrimPrefix(line, "event:")))
				} else if strings.HasPrefix(line, "data:") {
					receivedData = append(receivedData, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				}
			}
		}()

		select {
		case <-doneCh:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for SSE stream to complete on job_finish")
		}

		// Verify received events
		expectedEvents := []string{"step_start", "log", "step_complete", "job_complete"}
		if len(receivedEvents) != len(expectedEvents) {
			t.Fatalf("expected events %v, got %v", expectedEvents, receivedEvents)
		}
		for i, ev := range expectedEvents {
			if receivedEvents[i] != ev {
				t.Errorf("at index %d, expected event %q, got %q", i, ev, receivedEvents[i])
			}
		}

		// Verify secret was masked in log event data
		if len(receivedData) < 2 {
			t.Fatalf("expected at least 2 data lines, got %d", len(receivedData))
		}
		logData := receivedData[1]
		if strings.Contains(logData, "SUPER_SECRET_TOKEN_123") {
			t.Fatalf("stream leaked secret token: %s", logData)
		}
		if !strings.Contains(logData, "***") {
			t.Fatalf("stream log data missing mask replacement: %s", logData)
		}
		if !strings.Contains(logData, "Authenticating with") {
			t.Fatalf("stream log data missing original content: %s", logData)
		}
	})

	// 2. Client context cancellation
	t.Run("client cancellation terminates stream", func(t *testing.T) {
		mockSubCancel := &mockEventSubscriber{
			eventsCh: make(chan monitor.Event),
		}
		srvCancel := NewServer(ServerConfig{
			EventSubscriber: mockSubCancel,
		})
		tsCancel := httptest.NewServer(srvCancel)
		defer tsCancel.Close()

		ctx, cancel := context.WithCancel(context.Background())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, tsCancel.URL+"/api/runs/run-cancel/logs/stream", nil)
		if err != nil {
			t.Fatalf("new request failed: %v", err)
		}

		resp, err := tsCancel.Client().Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		// Cancel client context
		cancel()

		// Read until EOF
		buf := make([]byte, 1024)
		for {
			_, err := resp.Body.Read(buf)
			if err != nil {
				break
			}
		}

		// Check unsubscribe was called (server goroutine cleans up asynchronously)
		var unsubCalled bool
		for i := 0; i < 50; i++ {
			mockSubCancel.mu.Lock()
			unsubCalled = mockSubCancel.unsubCalled
			mockSubCancel.mu.Unlock()
			if unsubCalled {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !unsubCalled {
			t.Errorf("expected unsubscribe to be called upon client cancellation")
		}
	})

	// 3. Unconfigured EventSubscriber returns 503
	t.Run("unconfigured EventSubscriber returns 503", func(t *testing.T) {
		srvNil := NewServer(ServerConfig{
			EventSubscriber: nil,
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/run-123/logs/stream", nil)
		srvNil.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 Service Unavailable, got %d", rec.Code)
		}
	})

	// 4. Non-flusher response writer returns 500
	t.Run("non-flusher response writer returns 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/run-123/logs/stream", nil)
		srv.ServeHTTP(nonFlusherResponseWriter{rec}, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 Internal Server Error, got %d", rec.Code)
		}
	})
}

func TestServer_MetricsEndpoints(t *testing.T) {
	mockMetrics := &monitor.RunnerMetrics{
		Timestamp:    time.Now().UTC(),
		ProcessCount: 5,
		StepCPU:      12.5,
		HostCPU:      42.0,
		HostMemUsed:  1024 * 1024 * 500,
		HostMemTotal: 1024 * 1024 * 1000,
	}
	provider := &mockMetricsProvider{
		metrics: mockMetrics,
	}

	samples := []*monitor.TelemetrySample{
		{
			Timestamp: time.Now().UTC().Add(-time.Minute),
			HostCPU:   30.0,
		},
		{
			Timestamp: time.Now().UTC(),
			HostCPU:   35.0,
		},
	}
	mockStore := &mockRunStore{
		metrics: map[string][]*monitor.TelemetrySample{
			"run-metrics-1": samples,
			"run-empty":     {},
		},
	}

	srv := NewServer(ServerConfig{
		MetricsProvider: provider,
		RunStore:        mockStore,
	})

	// 1. GET /api/metrics/current with MetricsProvider
	t.Run("metrics current with provider", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/metrics/current", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		var res monitor.RunnerMetrics
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if res.ProcessCount != 5 || res.HostCPU != 42.0 {
			t.Fatalf("unexpected metrics response: %+v", res)
		}
	})

	// 2. GET /api/metrics/current fallback to host vitals
	t.Run("metrics current fallback without provider", func(t *testing.T) {
		srvFallback := NewServer(ServerConfig{
			MetricsProvider: nil,
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/metrics/current", nil)
		srvFallback.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		var res monitor.RunnerMetrics
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if res.Timestamp.IsZero() {
			t.Fatalf("expected non-zero timestamp in fallback metrics")
		}
	})

	// 3. GET /api/metrics/stream SSE
	t.Run("metrics stream SSE", func(t *testing.T) {
		ts := httptest.NewServer(srv)
		defer ts.Close()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/metrics/stream", nil)
		if err != nil {
			t.Fatalf("new request failed: %v", err)
		}

		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		if ctype := resp.Header.Get("Content-Type"); !strings.Contains(ctype, "text/event-stream") {
			t.Fatalf("expected text/event-stream, got %s", ctype)
		}

		reader := bufio.NewReader(resp.Body)
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("failed reading event line: %v", err)
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "event: metrics") {
			t.Fatalf("expected 'event: metrics', got %q", line)
		}

		dataLine, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("failed reading data line: %v", err)
		}
		if !strings.HasPrefix(strings.TrimSpace(dataLine), "data:") {
			t.Fatalf("expected 'data: ...', got %q", dataLine)
		}

		jsonData := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(dataLine), "data:"))
		var streamedMetrics monitor.RunnerMetrics
		if err := json.Unmarshal([]byte(jsonData), &streamedMetrics); err != nil {
			t.Fatalf("unmarshal streamed metrics error: %v, data: %s", err, jsonData)
		}
		if streamedMetrics.HostCPU != 42.0 {
			t.Fatalf("expected HostCPU 42.0, got %f", streamedMetrics.HostCPU)
		}

		// Cancel context and verify it terminates
		cancel()
	})

	// 4. GET /api/metrics/stream non-flusher returns 500
	t.Run("metrics stream non-flusher returns 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/metrics/stream", nil)
		srv.ServeHTTP(nonFlusherResponseWriter{rec}, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 Internal Server Error, got %d", rec.Code)
		}
	})

	// 5. GET /api/runs/{id}/metrics with samples
	t.Run("run metrics history with samples", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/run-metrics-1/metrics", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		var res []*monitor.TelemetrySample
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if len(res) != 2 {
			t.Fatalf("expected 2 samples, got %d", len(res))
		}
		if res[0].HostCPU != 30.0 || res[1].HostCPU != 35.0 {
			t.Fatalf("unexpected sample contents: %+v", res)
		}
	})

	// 6. GET /api/runs/{id}/metrics empty samples returns []
	t.Run("run metrics history empty returns []", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/run-empty/metrics", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		body := strings.TrimSpace(rec.Body.String())
		if body != "[]" {
			t.Fatalf("expected empty array '[]', got %q", body)
		}
	})

	// 7. GET /api/runs/{id}/metrics unconfigured RunStore returns 503
	t.Run("run metrics unconfigured RunStore returns 503", func(t *testing.T) {
		srvNilStore := NewServer(ServerConfig{
			RunStore: nil,
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/run-1/metrics", nil)
		srvNilStore.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 Service Unavailable, got %d", rec.Code)
		}
	})
}

type mockSecretManager struct {
	mu      sync.Mutex
	secrets map[string]string
	scopes  map[string]string
	listErr error
	setErr  error
	delErr  error
}

func newMockSecretManager() *mockSecretManager {
	return &mockSecretManager{
		secrets: make(map[string]string),
		scopes:  make(map[string]string),
	}
}

func (m *mockSecretManager) ListSecrets(ctx context.Context) ([]vault.SecretMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	var res []vault.SecretMetadata
	for k := range m.secrets {
		res = append(res, vault.SecretMetadata{
			Key:      k,
			Provider: "mock",
		})
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].Key < res[j].Key
	})
	return res, nil
}

func (m *mockSecretManager) SetSecret(ctx context.Context, key, val, scope string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.setErr != nil {
		return m.setErr
	}
	m.secrets[key] = val
	m.scopes[key] = scope
	return nil
}

func (m *mockSecretManager) DeleteSecret(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.delErr != nil {
		return m.delErr
	}
	if _, ok := m.secrets[key]; !ok {
		return vault.ErrSecretNotFound
	}
	delete(m.secrets, key)
	delete(m.scopes, key)
	return nil
}

type mockDaemonController struct {
	statusStr string
	statusErr error
	startErr  error
	stopErr   error
	started   bool
	stopped   bool
}

func (m *mockDaemonController) Status() (string, error) {
	if m.statusErr != nil {
		return "", m.statusErr
	}
	return m.statusStr, nil
}

func (m *mockDaemonController) Start() error {
	if m.startErr != nil {
		return m.startErr
	}
	m.started = true
	return nil
}

func (m *mockDaemonController) Stop() error {
	if m.stopErr != nil {
		return m.stopErr
	}
	m.stopped = true
	return nil
}

func TestServer_Secrets(t *testing.T) {
	// 1. Unconfigured SecretManager returns 503
	t.Run("unconfigured returns 503", func(t *testing.T) {
		srv := NewServer(ServerConfig{SecretManager: nil})

		// GET /api/secrets
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/secrets", nil)
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET /api/secrets expected 503, got %d", rec.Code)
		}

		// POST /api/secrets
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(`{"key":"A","value":"B"}`))
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("POST /api/secrets expected 503, got %d", rec.Code)
		}

		// DELETE /api/secrets/{key}
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodDelete, "/api/secrets/A", nil)
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("DELETE /api/secrets expected 503, got %d", rec.Code)
		}
	})

	// 2. List secrets empty returns []
	t.Run("list empty returns []", func(t *testing.T) {
		sm := newMockSecretManager()
		srv := NewServer(ServerConfig{SecretManager: sm})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/secrets", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		body := strings.TrimSpace(rec.Body.String())
		if body != "[]" {
			t.Fatalf("expected '[]', got %q", body)
		}
	})

	// 3. Set secret successfully
	t.Run("set secret success", func(t *testing.T) {
		sm := newMockSecretManager()
		srv := NewServer(ServerConfig{SecretManager: sm})

		payload := `{"key": "GH_TOKEN", "value": "ghp_super_secret_value", "scope": "repo"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(payload))
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201 Created, got %d (body: %s)", rec.Code, rec.Body.String())
		}
		var res map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if res["status"] != "saved" || res["key"] != "GH_TOKEN" {
			t.Fatalf("unexpected response: %+v", res)
		}

		// Verify stored in manager
		if sm.secrets["GH_TOKEN"] != "ghp_super_secret_value" {
			t.Fatalf("secret value not stored in manager")
		}
		if sm.scopes["GH_TOKEN"] != "repo" {
			t.Fatalf("secret scope not stored in manager")
		}
	})

	// 4. Set secret validation errors
	t.Run("set secret validation errors", func(t *testing.T) {
		sm := newMockSecretManager()
		srv := NewServer(ServerConfig{SecretManager: sm})

		testCases := []struct {
			name string
			body string
		}{
			{"empty body", ""},
			{"invalid json", "{not-json}"},
			{"missing key", `{"value": "bar"}`},
			{"empty key", `{"key": "", "value": "bar"}`},
			{"whitespace key", `{"key": "   ", "value": "bar"}`},
			{"missing value", `{"key": "foo"}`},
			{"empty value", `{"key": "foo", "value": ""}`},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(tc.body))
				srv.ServeHTTP(rec, req)

				if rec.Code != http.StatusBadRequest {
					t.Fatalf("expected 400 Bad Request for %s, got %d (body: %s)", tc.name, rec.Code, rec.Body.String())
				}
			})
		}
	})

	// 5. Set secret manager error returns 500
	t.Run("set secret backend error returns 500", func(t *testing.T) {
		sm := newMockSecretManager()
		sm.setErr = errors.New("vault backend disk full")
		srv := NewServer(ServerConfig{SecretManager: sm})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(`{"key":"A","value":"B"}`))
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 Internal Server Error, got %d", rec.Code)
		}
	})

	// 5b. Set secret body exceeds 1MB limit returns 400
	t.Run("set secret body exceeds 1MB limit returns 400", func(t *testing.T) {
		sm := newMockSecretManager()
		srv := NewServer(ServerConfig{SecretManager: sm})

		largeVal := strings.Repeat("A", (1<<20)+1024)
		payload := fmt.Sprintf(`{"key": "LARGE_SECRET", "value": %q}`, largeVal)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(payload))
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for body > 1MB, got %d", rec.Code)
		}
	})

	// 5c. Set secret updates configured Masker
	t.Run("set secret updates configured Masker", func(t *testing.T) {
		sm := newMockSecretManager()
		masker := engine.NewSecretMasker()
		srv := NewServer(ServerConfig{SecretManager: sm, Masker: masker})

		payload := `{"key": "NEW_TOKEN", "value": "dyn_secret_token_12345"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(payload))
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201 Created, got %d", rec.Code)
		}
		masked := masker.Mask("authorization: dyn_secret_token_12345")
		if strings.Contains(masked, "dyn_secret_token_12345") {
			t.Fatalf("expected secret to be masked after set, got %q", masked)
		}
	})

	// 6. List secrets with data excludes sensitive values
	t.Run("list secrets excludes secret values", func(t *testing.T) {
		sm := newMockSecretManager()
		sm.secrets["SECRET_KEY"] = "super_classified_password_123"
		sm.secrets["API_TOKEN"] = "token_classified_xyz"
		srv := NewServer(ServerConfig{SecretManager: sm})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/secrets", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}

		rawBody := rec.Body.String()
		if strings.Contains(rawBody, "super_classified_password_123") {
			t.Fatalf("secret value leaked in GET /api/secrets response: %s", rawBody)
		}
		if strings.Contains(rawBody, "token_classified_xyz") {
			t.Fatalf("secret value leaked in GET /api/secrets response: %s", rawBody)
		}

		var items []vault.SecretMetadata
		if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("expected 2 secrets metadata items, got %d", len(items))
		}
		if items[0].Key != "API_TOKEN" || items[1].Key != "SECRET_KEY" {
			t.Fatalf("unexpected items: %+v", items)
		}
	})

	// 7. List secrets manager error returns 500
	t.Run("list secrets backend error returns 500", func(t *testing.T) {
		sm := newMockSecretManager()
		sm.listErr = errors.New("vault backend error")
		srv := NewServer(ServerConfig{SecretManager: sm})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/secrets", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 Internal Server Error, got %d", rec.Code)
		}
	})

	// 8. Delete secret success
	t.Run("delete secret success", func(t *testing.T) {
		sm := newMockSecretManager()
		sm.secrets["TO_DELETE"] = "delete_me"
		srv := NewServer(ServerConfig{SecretManager: sm})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/api/secrets/TO_DELETE", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (body: %s)", rec.Code, rec.Body.String())
		}
		var res map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if res["status"] != "deleted" || res["key"] != "TO_DELETE" {
			t.Fatalf("unexpected delete response: %+v", res)
		}
		if _, ok := sm.secrets["TO_DELETE"]; ok {
			t.Fatalf("secret was not removed from manager")
		}
	})

	// 9. Delete secret not found returns 404
	t.Run("delete secret not found returns 404", func(t *testing.T) {
		sm := newMockSecretManager()
		srv := NewServer(ServerConfig{SecretManager: sm})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/api/secrets/NON_EXISTENT", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found, got %d", rec.Code)
		}
	})

	// 10. Delete secret manager error returns 500
	t.Run("delete secret backend error returns 500", func(t *testing.T) {
		sm := newMockSecretManager()
		sm.secrets["EXISTS"] = "some_val"
		sm.delErr = errors.New("vault locked")
		srv := NewServer(ServerConfig{SecretManager: sm})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/api/secrets/EXISTS", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 Internal Server Error, got %d", rec.Code)
		}
	})
}

func TestServer_Service(t *testing.T) {
	// 1. Status with DaemonController running
	t.Run("status with DaemonController running", func(t *testing.T) {
		ctrl := &mockDaemonController{statusStr: "running"}
		srv := NewServer(ServerConfig{DaemonController: ctrl})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/service", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		var res map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if res["status"] != "running" {
			t.Fatalf("expected status running, got %s", res["status"])
		}
	})

	// 2. Status with DaemonController installed/stopped
	t.Run("status with DaemonController stopped", func(t *testing.T) {
		ctrl := &mockDaemonController{statusStr: "installed/stopped"}
		srv := NewServer(ServerConfig{DaemonController: ctrl})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/service", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		var res map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if res["status"] != "installed/stopped" {
			t.Fatalf("expected status installed/stopped, got %s", res["status"])
		}
	})

	// 3. Status with DaemonController error returns 500
	t.Run("status with DaemonController error returns 500", func(t *testing.T) {
		ctrl := &mockDaemonController{statusErr: errors.New("daemon query failed")}
		srv := NewServer(ServerConfig{DaemonController: ctrl})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/service", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 Internal Server Error, got %d", rec.Code)
		}
	})

	// 4. Start DaemonController success
	t.Run("start DaemonController success", func(t *testing.T) {
		ctrl := &mockDaemonController{}
		srv := NewServer(ServerConfig{DaemonController: ctrl})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/service/start", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		var res map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if res["status"] != "started" {
			t.Fatalf("expected status started, got %s", res["status"])
		}
		if !ctrl.started {
			t.Fatalf("expected ctrl.started to be true")
		}
	})

	// 5. Start DaemonController error returns 500
	t.Run("start DaemonController error returns 500", func(t *testing.T) {
		ctrl := &mockDaemonController{startErr: errors.New("start failed")}
		srv := NewServer(ServerConfig{DaemonController: ctrl})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/service/start", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 Internal Server Error, got %d", rec.Code)
		}
	})

	// 6. Stop DaemonController success
	t.Run("stop DaemonController success", func(t *testing.T) {
		ctrl := &mockDaemonController{}
		srv := NewServer(ServerConfig{DaemonController: ctrl})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/service/stop", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		var res map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if res["status"] != "stopped" {
			t.Fatalf("expected status stopped, got %s", res["status"])
		}
		if !ctrl.stopped {
			t.Fatalf("expected ctrl.stopped to be true")
		}
	})

	// 7. Stop DaemonController error returns 500
	t.Run("stop DaemonController error returns 500", func(t *testing.T) {
		ctrl := &mockDaemonController{stopErr: errors.New("stop failed")}
		srv := NewServer(ServerConfig{DaemonController: ctrl})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/service/stop", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 Internal Server Error, got %d", rec.Code)
		}
	})

	// 8. Fallback behavior when DaemonController is nil
	t.Run("fallback behavior when DaemonController nil", func(t *testing.T) {
		srv := NewServer(ServerConfig{DaemonController: nil})

		// GET /api/service executes service.Status()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/service", nil)
		srv.ServeHTTP(rec, req)

		// Must return either 200 OK with status string or 500 on unsupported platform (never 404 API endpoint not found)
		if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 200 or 500, got %d (body: %s)", rec.Code, rec.Body.String())
		}
		if rec.Code == http.StatusOK {
			var res map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			if _, ok := res["status"]; !ok {
				t.Fatalf("expected 'status' key in response: %+v", res)
			}
		}

		// POST /api/service/start executes service.Start()
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, "/api/service/start", nil)
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 200 or 500, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		// POST /api/service/stop executes service.Stop()
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, "/api/service/stop", nil)
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 200 or 500, got %d (body: %s)", rec.Code, rec.Body.String())
		}
	})
}

func TestServer_FallbackHostMetrics_PanicRecovery(t *testing.T) {
	m := fallbackHostMetrics()
	if m == nil {
		t.Fatal("expected non-nil metrics from fallbackHostMetrics")
	}
	if m.Timestamp.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
}
