package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/engine"
	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
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

func (m *mockRunStore) GetStepLogs(ctx context.Context, stepID string) ([]string, error) {
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
}
