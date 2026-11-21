package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testMonitorSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "act-sock-*")
	if err != nil {
		t.Fatalf("failed to create short temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return filepath.Join(dir, "daemon.sock")
}

func TestMonitorCmd_Setup(t *testing.T) {
	cmd := newMonitorCmd()
	if cmd.Use != "monitor" {
		t.Fatalf("expected command use 'monitor', got %s", cmd.Use)
	}
	if cmd.Flags().Lookup("sock") == nil {
		t.Fatal("expected --sock flag")
	}
	if cmd.Flags().Lookup("config-dir") == nil {
		t.Fatal("expected --config-dir flag")
	}
	if cmd.Flags().Lookup("demo") == nil {
		t.Fatal("expected --demo flag")
	}
	if cmd.Flags().Lookup("mouse") == nil {
		t.Fatal("expected --mouse flag")
	}
	if cmd.Flags().Lookup("no-mouse") == nil {
		t.Fatal("expected --no-mouse flag")
	}
}

func TestMonitorCmd_DemoFlag(t *testing.T) {
	var executed atomic.Bool
	origRunProgram := runProgram
	runProgram = func(m tea.Model, opts ...tea.ProgramOption) error {
		executed.Store(true)
		return nil
	}
	defer func() {
		runProgram = origRunProgram
	}()

	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"monitor", "--demo"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0 for monitor --demo, got %d. stderr: %s", code, stderr.String())
	}

	if !executed.Load() {
		t.Fatal("expected runProgram to be called with --demo")
	}
}

func TestMonitorCmd_DemoMode_DoesNotPolluteConfigDirHistoryDB(t *testing.T) {
	tempDir := t.TempDir()
	var executed atomic.Bool
	origRunProgram := runProgram
	runProgram = func(m tea.Model, opts ...tea.ProgramOption) error {
		executed.Store(true)
		if dash, ok := m.(*tui.DashboardModel); ok {
			assert.False(t, dash.IsOffline())
		}
		return nil
	}
	defer func() {
		runProgram = origRunProgram
	}()

	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"monitor", "--demo", "--config-dir", tempDir}, &stdout, &stderr)
	require.Equal(t, 0, code, "expected monitor --demo to exit 0")
	require.True(t, executed.Load())

	// Crucial check: verify that configDir/history.db was NEVER created
	prodDBPath := filepath.Join(tempDir, "history.db")
	_, err := os.Stat(prodDBPath)
	assert.True(t, os.IsNotExist(err), "demo mode must never write to config-dir/history.db")

	// Also verify that legacy runs/ was never created
	legacyRunsDir := filepath.Join(tempDir, "runs")
	_, err = os.Stat(legacyRunsDir)
	assert.True(t, os.IsNotExist(err), "demo mode must never write to config-dir/runs")
}

func TestMonitorCmd_SocketNotFound(t *testing.T) {
	missingSock := filepath.Join(t.TempDir(), "nonexistent.sock")
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	var executed atomic.Bool
	origRunProgram := runProgram
	runProgram = func(m tea.Model, opts ...tea.ProgramOption) error {
		executed.Store(true)
		if dash, ok := m.(*tui.DashboardModel); ok {
			assert.True(t, dash.IsOffline())
			assert.Equal(t, tui.StatusBadgeOffline, dash.StatusBadge())
		}
		return nil
	}
	defer func() {
		runProgram = origRunProgram
	}()

	code := dispatchWithContext(ctx, []string{"monitor", "--sock", missingSock}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0 when launching in offline mode, got %d. stderr: %s", code, stderr.String())
	}
	if !executed.Load() {
		t.Fatal("expected runProgram to be called for offline monitor")
	}
}

func TestMonitorCmd_ConfigDirSocketNotFound(t *testing.T) {
	emptyDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	var executed atomic.Bool
	origRunProgram := runProgram
	runProgram = func(m tea.Model, opts ...tea.ProgramOption) error {
		executed.Store(true)
		if dash, ok := m.(*tui.DashboardModel); ok {
			assert.True(t, dash.IsOffline())
		}
		return nil
	}
	defer func() {
		runProgram = origRunProgram
	}()

	code := dispatchWithContext(ctx, []string{"monitor", "--config-dir", emptyDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0 when launching in offline mode with missing socket in config-dir, got %d. stderr: %s", code, stderr.String())
	}
	if !executed.Load() {
		t.Fatal("expected runProgram to be called for offline monitor")
	}
}

func TestMonitorCmd_ExecuteWithSocket(t *testing.T) {
	sockPath := testMonitorSocketPath(t)

	// Create a dummy unix socket
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to create dummy unix socket: %v", err)
	}
	defer func() { _ = l.Close() }()

	var executed atomic.Bool
	origRunProgram := runProgram
	runProgram = func(m tea.Model, opts ...tea.ProgramOption) error {
		executed.Store(true)
		return nil
	}
	defer func() {
		runProgram = origRunProgram
	}()

	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"monitor", "--sock", sockPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d. stderr: %s", code, stderr.String())
	}

	if !executed.Load() {
		t.Fatal("expected runProgram to be called")
	}
}

func TestMonitorCmd_ExecuteRunProgramError(t *testing.T) {
	sockPath := testMonitorSocketPath(t)

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to create dummy unix socket: %v", err)
	}
	defer func() { _ = l.Close() }()

	origRunProgram := runProgram
	runProgram = func(m tea.Model, opts ...tea.ProgramOption) error {
		return fmt.Errorf("simulated tui failure")
	}
	defer func() {
		runProgram = origRunProgram
	}()

	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"monitor", "--sock", sockPath}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit code when runProgram fails, got 0")
	}

	combined := stdout.String() + "\n" + stderr.String()
	if !strings.Contains(combined, "dashboard execution: simulated tui failure") {
		t.Fatalf("expected output to contain simulated tui failure, got: %s", combined)
	}
}

func TestRunCommand_StartsAndStopsMonitorServer(t *testing.T) {
	rawAESKey := make([]byte, 32)
	for i := range rawAESKey {
		rawAESKey[i] = byte(i + 1)
	}

	var encKeyStr string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionId": "mock-session-monitor",
				"encryptionKey": map[string]string{
					"value": encKeyStr,
				},
			})
		case r.URL.Path == "/session" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/message":
			<-r.Context().Done()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configDir, key := createTestConfigDir(t, server.URL)
	encAESKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &key.PublicKey, rawAESKey, nil)
	if err != nil {
		t.Fatalf("encrypt AES key: %v", err)
	}
	encKeyStr = base64.StdEncoding.EncodeToString(encAESKey)

	sockPath := filepath.Join(configDir, "daemon.sock")

	ctx, cancel := context.WithCancel(context.Background())

	// Check that socket appears while runner is running
	socketSeen := make(chan struct{})
	var onceClose sync.Once
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(sockPath); err == nil {
					onceClose.Do(func() { close(socketSeen) })
					return
				}
			}
		}
	}()

	go func() {
		select {
		case <-socketSeen:
			// Give a short pause then cancel context for graceful shutdown
			time.Sleep(50 * time.Millisecond)
			cancel()
		case <-time.After(3 * time.Second):
			cancel()
		}
	}()

	var stdout, stderr bytes.Buffer
	code := dispatchWithContext(ctx, []string{"run", "--config-dir", configDir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0 on context cancellation, got %d. stderr: %s", code, stderr.String())
	}

	select {
	case <-socketSeen:
		// Socket was successfully created during daemon execution
	default:
		t.Fatalf("expected socket %s to be created while daemon was running", sockPath)
	}

	// Verify socket was removed on shutdown
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Fatalf("expected socket %s to be removed on shutdown, but it still exists", sockPath)
	}
}

func TestMonitorCmd_DefaultConfigDirFlag(t *testing.T) {
	cmd := newMonitorCmd()
	f := cmd.Flags().Lookup("config-dir")
	if f == nil {
		t.Fatal("expected --config-dir flag")
	}
	expectedDefault := config.DefaultConfigDir()
	if f.DefValue != expectedDefault {
		t.Fatalf("expected default config-dir %q, got %q", expectedDefault, f.DefValue)
	}
}

func TestMonitorCmd_DefaultConfigDirResolution(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("ACT_RUNNER_CONFIG_DIR", tempDir)

	var executed atomic.Bool
	origRunProgram := runProgram
	runProgram = func(m tea.Model, opts ...tea.ProgramOption) error {
		executed.Store(true)
		if dash, ok := m.(*tui.DashboardModel); ok {
			assert.True(t, dash.IsOffline())
		}
		return nil
	}
	defer func() {
		runProgram = origRunProgram
	}()

	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"monitor"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0 when launching in offline mode with resolved default dir, got %d. stderr: %s", code, stderr.String())
	}
	if !executed.Load() {
		t.Fatal("expected runProgram to be called for offline monitor")
	}
}

func TestMonitor_OfflineMode_HydratesFromSQLiteStore(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "history.db")
	store, err := history.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	job1 := &monitor.JobInfo{
		ID:        "job-offline-1",
		Name:      "CI Test Suite",
		Status:    monitor.StatusRunning,
		StartedAt: time.Now().Add(-10 * time.Minute),
		Steps: []*monitor.StepInfo{
			{ID: "step-1", Name: "Setup runner", Status: monitor.StatusSuccess},
			{ID: "step-2", Name: "Run tests", Status: monitor.StatusSuccess},
		},
	}
	require.NoError(t, store.StartRun(job1, nil, "docker"))
	require.NoError(t, store.FinishRun(job1.ID, monitor.StatusSuccess, &monitor.RunMetricsSummary{
		PeakCPUPercent: 78.5,
		PeakRSSBytes:   512 * 1024 * 1024,
	}))

	job2 := &monitor.JobInfo{
		ID:        "job-offline-2",
		Name:      "Deploy Staging",
		Status:    monitor.StatusRunning,
		StartedAt: time.Now().Add(-5 * time.Minute),
		Steps: []*monitor.StepInfo{
			{ID: "step-3", Name: "Deploy", Status: monitor.StatusFailed},
		},
	}
	require.NoError(t, store.StartRun(job2, nil, "docker"))
	require.NoError(t, store.FinishRun(job2.ID, monitor.StatusFailed, &monitor.RunMetricsSummary{
		PeakCPUPercent: 42.0,
		PeakRSSBytes:   256 * 1024 * 1024,
	}))

	missingSock := filepath.Join(tempDir, "daemon.sock")

	var capturedModel tea.Model
	origRunProgram := runProgram
	runProgram = func(m tea.Model, opts ...tea.ProgramOption) error {
		capturedModel = m
		return nil
	}
	defer func() {
		runProgram = origRunProgram
	}()

	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"monitor", "--sock", missingSock, "--config-dir", tempDir}, &stdout, &stderr)
	require.Equal(t, 0, code, "expected monitor to succeed in offline mode, stderr: %s", stderr.String())

	require.NotNil(t, capturedModel, "expected runProgram to be called with dashboard model")
	dash, ok := capturedModel.(*tui.DashboardModel)
	require.True(t, ok, "expected capturedModel to be *tui.DashboardModel")

	assert.True(t, dash.IsOffline(), "expected dashboard to be in offline mode")
	assert.Equal(t, tui.StatusBadgeOffline, dash.StatusBadge())

	resModel, _ := dash.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	dash = resModel.(*tui.DashboardModel)
	view := dash.View()

	assert.Contains(t, view, "◉ OFFLINE (HISTORY)")
	assert.Contains(t, view, "HISTORICAL RUNS")
	histNode := dash.Tree().FindNode("historical-runs")
	require.NotNil(t, histNode)
	assert.False(t, histNode.Expanded, "historical runs should be collapsed by default")
	histNode.Expanded = true
	for _, ch := range histNode.Children {
		ch.Expanded = true
	}
	dash.Tree().Rebuild()
	view = dash.View()
	assert.Contains(t, view, "CI Test Suite")
	assert.Contains(t, view, "Deploy Staging")
}

func TestMonitor_OfflineMode_DirectStepLogReading(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "history.db")
	store, err := history.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	jobID := "job-offline-log-test"
	stepID := "step-offline-log-1"
	job := &monitor.JobInfo{
		ID:        jobID,
		Name:      "Offline Log Test",
		Status:    monitor.StatusRunning,
		StartedAt: time.Now().Add(-2 * time.Minute),
		Steps: []*monitor.StepInfo{
			{ID: stepID, Name: "Run offline unit tests", Status: monitor.StatusSuccess},
		},
	}
	require.NoError(t, store.StartRun(job, nil, "docker"))
	require.NoError(t, store.AppendStepLog(jobID, stepID, "PASS: test offline line 1"))
	require.NoError(t, store.AppendStepLog(jobID, stepID, "PASS: test offline line 2"))
	require.NoError(t, store.FinishRun(jobID, monitor.StatusSuccess, &monitor.RunMetricsSummary{
		PeakCPUPercent: 50.0,
		PeakRSSBytes:   128 * 1024 * 1024,
	}))

	snap, err := tui.HydrateSnapshotFromStore(store, 10)
	require.NoError(t, err)

	missingSock := filepath.Join(tempDir, "nonexistent.sock")
	model := tui.NewDashboardModel(missingSock)
	model.SetHistoryStore(store)
	model.SetOffline(true)
	model.ApplySnapshot(snap)

	resModel, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = resModel.(*tui.DashboardModel)

	// Navigate to the step node
	found := false
	for i := 0; i < 10; i++ {
		resModel, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
		model = resModel.(*tui.DashboardModel)
		resModel, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model = resModel.(*tui.DashboardModel)
		view := model.View()
		if strings.Contains(view, "PASS: test offline line 1") && strings.Contains(view, "PASS: test offline line 2") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected step logs to be read directly from SQLiteStore in offline mode")
}

func TestMonitor_AutoReconnect_TransitionsToLive(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "history.db")
	store, err := history.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	histJob := &monitor.JobInfo{
		ID:        "job-hist-reconnect-1",
		Name:      "Historic Build",
		Status:    monitor.StatusRunning,
		StartedAt: time.Now().Add(-1 * time.Hour),
		Steps: []*monitor.StepInfo{
			{ID: "step-hist-1", Name: "Build Artifact", Status: monitor.StatusSuccess},
		},
	}
	require.NoError(t, store.StartRun(histJob, nil, "docker"))
	require.NoError(t, store.FinishRun(histJob.ID, monitor.StatusSuccess, &monitor.RunMetricsSummary{
		PeakCPUPercent: 65.0,
		PeakRSSBytes:   200 * 1024 * 1024,
	}))

	snap, err := tui.HydrateSnapshotFromStore(store, 10)
	require.NoError(t, err)

	sockPath := testMonitorSocketPath(t)
	model := tui.NewDashboardModel(sockPath)
	model.SetHistoryStore(store)
	model.SetOffline(true)
	model.ApplySnapshot(snap)

	resModel, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = resModel.(*tui.DashboardModel)

	// Step 1: Verify initially offline
	assert.True(t, model.IsOffline())
	assert.Equal(t, tui.StatusBadgeOffline, model.StatusBadge())
	assert.Contains(t, model.View(), "◉ OFFLINE (HISTORY)")
	assert.Contains(t, model.View(), "HISTORICAL RUNS")

	// Step 2: Start live daemon server
	server := monitor.NewServer(sockPath, "live-reconnect-runner")
	require.NoError(t, server.Start())
	defer func() { _ = server.Stop() }()

	server.Publish(monitor.Event{
		Type: monitor.EventJobStart,
		Job: &monitor.JobInfo{
			ID:        "job-live-active-1",
			Name:      "Live Active Workflow",
			Status:    monitor.StatusRunning,
			StartedAt: time.Now(),
		},
	})
	time.Sleep(50 * time.Millisecond)

	// Step 3: Trigger auto-reconnect
	resModel, cmd := model.Update(tui.ReconnectMsg{})
	model = resModel.(*tui.DashboardModel)
	require.NotNil(t, cmd)

	msg := cmd()
	resModel, _ = model.Update(msg)
	model = resModel.(*tui.DashboardModel)

	// Step 4: Verify transitioned to live
	assert.False(t, model.IsOffline(), "expected model to transition from offline to live")
	assert.Equal(t, tui.StatusBadgeConnected, model.StatusBadge())

	view := model.View()
	assert.Contains(t, view, "● CONNECTED (LIVE)")
	assert.Contains(t, view, "Live Active Workflow")
	histNode := model.Tree().FindNode("historical-runs")
	require.NotNil(t, histNode, "historical runs branch must exist")
	assert.False(t, histNode.Expanded, "historical runs should be collapsed by default")
	histNode.Expanded = true
	for _, ch := range histNode.Children {
		ch.Expanded = true
	}
	model.Tree().Rebuild()
	view = model.View()
	assert.Contains(t, view, "Historic Build", "expected historical runs to be retained after reconnect")

	// Step 5: Deliver a live event
	resModel, _ = model.Update(tui.EventMsg(monitor.Event{
		Type:  monitor.EventStepStart,
		JobID: "job-live-active-1",
		Step: &monitor.StepInfo{
			ID:     "step-live-dyn-1",
			Name:   "Dynamic Live Step",
			Status: monitor.StatusRunning,
		},
	}))
	model = resModel.(*tui.DashboardModel)
	assert.Contains(t, model.View(), "Dynamic Live Step")
	assert.Contains(t, model.View(), "Historic Build")

	// Step 6: Simulate daemon disconnect
	resModel, tickCmd := model.Update(tui.ErrMsg(fmt.Errorf("stream connection lost")))
	model = resModel.(*tui.DashboardModel)
	assert.True(t, model.IsOffline(), "expected model to revert to offline upon disconnect")
	assert.Equal(t, tui.StatusBadgeOffline, model.StatusBadge())
	assert.Contains(t, model.View(), "◉ OFFLINE (HISTORY)")
	assert.Contains(t, model.View(), "Historic Build", "expected historical runs intact after disconnect")
	assert.NotNil(t, tickCmd, "expected reconnect ticker to be scheduled on disconnect")
}
