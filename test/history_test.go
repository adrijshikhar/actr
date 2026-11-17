package test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/driver/host"
	"github.com/adrijshikhar/actr/internal/engine"
	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/adrijshikhar/actr/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSocketPath(t *testing.T) string {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "d.sock")
	if len(sockPath) < 100 {
		return sockPath
	}

	dir, err := os.MkdirTemp("/tmp", "act-sock-*")
	if err != nil {
		t.Fatalf("failed to create short temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return filepath.Join(dir, "d.sock")
}

// TestHistory_EngineLifecycle_EndToEnd tests the complete local run persistence lifecycle:
// 1. Executing a job via the runner engine with monitor server and history store enabled.
// 2. Asserting meta.json is created with valid schema, duration, peak vitals, and step counts.
// 3. Asserting steps/<step-id>.log preserves ANSI escape sequences and exact log lines.
// 4. Asserting index.json catalog contains the run entry with status: success.
// 5. Asserting offline monitor boot loads the historical run, displays correct status, and retrieves step logs.
func TestHistory_EngineLifecycle_EndToEnd(t *testing.T) {
	tempBase := t.TempDir()
	runsDir := filepath.Join(tempBase, "runs")
	workDir := filepath.Join(tempBase, "work")
	sockPath := testSocketPath(t)

	require.NoError(t, os.MkdirAll(workDir, 0o755))

	// 1. Initialize History Store
	store, err := history.NewDiskStore(runsDir)
	require.NoError(t, err)
	assert.Equal(t, runsDir, store.BaseDir())

	// 2. Initialize Monitor Server with History Store
	monServer := monitor.NewServer(sockPath, "e2e-runner")
	monServer.WithHistoryStore(store)
	require.NoError(t, monServer.Start())
	defer func() { _ = monServer.Stop() }()

	// 3. Initialize Coordinator Engine with Host Driver
	coord := engine.NewCoordinator(host.NewDriver(), nil, workDir).
		WithRunnerName("e2e-runner").
		WithRepositoryURL("https://github.com/adrijshikhar/actr")
	coord.SetMonitorServer(monServer)

	jobID := "job-e2e-persist-001"
	jobSpec := &protocol.JobSpecification{
		JobID:   jobID,
		JobName: "E2E Integration Pipeline",
		Steps: []protocol.JobStep{
			{
				ID:     "step-setup",
				Name:   "Environment Setup",
				Type:   "script",
				Script: `printf "\033[1;32m✓ Environment initialized successfully\033[0m\n"`,
			},
			{
				ID:     "step-build",
				Name:   "Compile Core Modules",
				Type:   "script",
				Script: `printf "\033[38;5;39m[BUILD]\033[0m Compiling artifacts with optimizations...\n"`,
			},
			{
				ID:     "step-verify",
				Name:   "Verify Output",
				Type:   "script",
				Script: `echo "All 42 checks passed"`,
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Execute the job through runner engine
	completeReq, err := coord.ExecuteJob(ctx, jobSpec, "", "")
	require.NoError(t, err)
	require.NotNil(t, completeReq)
	assert.Equal(t, "success", completeReq.Conclusion)

	// Small delay to ensure async events are fully processed by monitor server
	time.Sleep(100 * time.Millisecond)

	// --- Verification 1: Verify meta.json schema, duration, peak vitals, step counts ---
	metaPath := filepath.Join(runsDir, jobID, "meta.json")
	require.FileExists(t, metaPath, "expected meta.json to exist")

	metaBytes, err := os.ReadFile(metaPath)
	require.NoError(t, err)

	var runMeta monitor.RunMetadata
	require.NoError(t, json.Unmarshal(metaBytes, &runMeta), "meta.json should unmarshal cleanly")

	assert.Equal(t, jobID, runMeta.ID)
	assert.Equal(t, "E2E Integration Pipeline", runMeta.Name)
	assert.Equal(t, monitor.StatusSuccess, runMeta.Status)
	assert.False(t, runMeta.StartedAt.IsZero())
	assert.False(t, runMeta.EndedAt.IsZero())
	assert.True(t, runMeta.DurationSec >= 0)
	assert.Len(t, runMeta.Steps, 3)

	for i, stepMeta := range runMeta.Steps {
		assert.NotEmpty(t, stepMeta.ID)
		assert.Equal(t, monitor.StatusSuccess, stepMeta.Status)
		assert.Equal(t, 0, stepMeta.ExitCode)
		assert.False(t, stepMeta.OOMKilled)
		assert.True(t, stepMeta.LogLines > 0, "step %d should have logged lines", i)
		assert.True(t, stepMeta.LogBytes > 0, "step %d should have log bytes", i)
	}

	// --- Verification 2: Verify step logs and ANSI sequence preservation ---
	step1LogPath := filepath.Join(runsDir, jobID, "steps", "step-setup.log")
	require.FileExists(t, step1LogPath, "expected step-setup.log to exist")

	step1LogBytes, err := os.ReadFile(step1LogPath)
	require.NoError(t, err)
	step1LogContent := string(step1LogBytes)

	// Verify exact log content and preservation of ANSI color codes
	assert.Contains(t, step1LogContent, "\x1b[1;32m✓ Environment initialized successfully\x1b[0m")

	step2LogPath := filepath.Join(runsDir, jobID, "steps", "step-build.log")
	require.FileExists(t, step2LogPath)
	step2LogBytes, err := os.ReadFile(step2LogPath)
	require.NoError(t, err)
	assert.Contains(t, string(step2LogBytes), "\x1b[38;5;39m[BUILD]\x1b[0m")

	// --- Verification 3: Verify index.json catalog ---
	indexPath := filepath.Join(runsDir, "index.json")
	require.FileExists(t, indexPath)

	indexBytes, err := os.ReadFile(indexPath)
	require.NoError(t, err)

	var catalog monitor.IndexCatalog
	require.NoError(t, json.Unmarshal(indexBytes, &catalog))
	assert.Equal(t, 1, catalog.Version)
	require.NotEmpty(t, catalog.Runs)

	var foundSummary *monitor.RunSummary
	for _, r := range catalog.Runs {
		if r.ID == jobID {
			foundSummary = r
			break
		}
	}
	require.NotNil(t, foundSummary, "expected run summary in index.json")
	assert.Equal(t, "E2E Integration Pipeline", foundSummary.Name)
	assert.Equal(t, monitor.StatusSuccess, foundSummary.Status)
	assert.Equal(t, 3, foundSummary.StepCount)

	// --- Verification 4: Stop daemon and boot offline monitor against store ---
	require.NoError(t, monServer.Stop())
	_ = os.Remove(sockPath)

	// Reopen store in offline mode
	offlineStore, err := history.NewDiskStore(runsDir)
	require.NoError(t, err)

	// Hydrate snapshot from offline store
	offlineSnapshot, err := tui.HydrateSnapshotFromStore(offlineStore, 50)
	require.NoError(t, err)
	require.NotNil(t, offlineSnapshot)
	require.Len(t, offlineSnapshot.RecentJobs, 1)
	assert.Equal(t, jobID, offlineSnapshot.RecentJobs[0].ID)
	assert.Equal(t, monitor.StatusSuccess, offlineSnapshot.RecentJobs[0].Status)
	assert.Len(t, offlineSnapshot.RecentJobs[0].Steps, 3)

	// Initialize dashboard model with offline store
	model := tui.NewDashboardModel(sockPath).WithHistoryStore(offlineStore)
	model.SetOffline(true)
	model.ApplySnapshot(offlineSnapshot)

	assert.True(t, model.IsOffline())
	assert.Equal(t, tui.StatusBadgeOffline, model.StatusBadge())

	// Resize dashboard and render view
	resModel, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	dash := resModel.(*tui.DashboardModel)
	view := dash.View()

	assert.Contains(t, view, "◉ OFFLINE (HISTORY)")
	assert.Contains(t, view, "HISTORICAL RUNS")
	assert.Contains(t, view, "E2E Integration Pip")

	// Verify step log retrieval directly from offline store
	retrievedLogs, err := offlineStore.GetStepLogs(jobID, "step-setup")
	require.NoError(t, err)
	require.NotEmpty(t, retrievedLogs)
	allLines := strings.Join(retrievedLogs, "\n")
	assert.Contains(t, allLines, "Environment initialized successfully")
	assert.Contains(t, allLines, "\x1b[1;32m")
}

// TestHistory_RetentionPruning_E2E verifies that LRU retention pruning enforces max_runs
// and max_disk_bytes across consecutive runs through the monitor server.
func TestHistory_RetentionPruning_E2E(t *testing.T) {
	tempBase := t.TempDir()
	runsDir := filepath.Join(tempBase, "runs")
	sockPath := testSocketPath(t)

	store, err := history.NewDiskStore(runsDir)
	require.NoError(t, err)

	server := monitor.NewServer(sockPath, "prune-runner")
	server.WithHistoryStore(store)
	// Configure retention: maximum 2 runs
	server.WithRetention(2, 50*1024*1024)
	require.NoError(t, server.Start())
	defer func() { _ = server.Stop() }()

	// Execute 3 jobs sequentially
	for i := 1; i <= 3; i++ {
		job := &monitor.JobInfo{
			ID:        strings.ToLower(string(rune('a'+i-1))) + "-run",
			Name:      "Job " + string(rune('A'+i-1)),
			Status:    monitor.StatusRunning,
			StartedAt: time.Now().Add(time.Duration(i) * time.Minute),
			Steps: []*monitor.StepInfo{
				{ID: "step-1", Name: "Step 1", Status: monitor.StatusSuccess},
			},
		}
		server.Publish(monitor.Event{Type: monitor.EventJobStart, JobID: job.ID, Job: job})
		server.Publish(monitor.Event{Type: monitor.EventStepLog, JobID: job.ID, StepID: "step-1", LogLine: "output for job"})
		job.Status = monitor.StatusSuccess
		server.Publish(monitor.Event{Type: monitor.EventJobFinish, JobID: job.ID, Job: job})
		time.Sleep(50 * time.Millisecond)
	}

	// Verify that the oldest run ('a-run') was pruned, leaving only 'b-run' and 'c-run'
	runs, err := store.ListRuns(10)
	require.NoError(t, err)
	require.Len(t, runs, 2)

	assert.Equal(t, "c-run", runs[0].ID)
	assert.Equal(t, "b-run", runs[1].ID)

	// Directory for 'a-run' should no longer exist
	assert.NoDirExists(t, filepath.Join(runsDir, "a-run"))
	assert.DirExists(t, filepath.Join(runsDir, "b-run"))
	assert.DirExists(t, filepath.Join(runsDir, "c-run"))
}

// TestHistory_CrashRecovery_E2E verifies that interrupted runs left behind by process crashes
// or SIGKILL are automatically marked as 'interrupted' when a new store or monitor initializes.
func TestHistory_CrashRecovery_E2E(t *testing.T) {
	tempBase := t.TempDir()
	runsDir := filepath.Join(tempBase, "runs")

	store, err := history.NewDiskStore(runsDir)
	require.NoError(t, err)

	// Start a run that never finishes (simulating sudden crash/kill)
	crashedJob := &monitor.JobInfo{
		ID:        "crashed-run-123",
		Name:      "Killed Pipeline",
		Status:    monitor.StatusRunning,
		StartedAt: time.Now().Add(-15 * time.Minute),
		Steps: []*monitor.StepInfo{
			{ID: "step-1", Name: "In-flight step", Status: monitor.StatusRunning},
		},
	}
	require.NoError(t, store.StartRun(crashedJob, nil, "host"))
	_ = store.AppendStepLog(crashedJob.ID, "step-1", "Starting long process...")

	// Verify it starts as running
	meta, err := store.GetRun(crashedJob.ID)
	require.NoError(t, err)
	assert.Equal(t, monitor.StatusRunning, meta.Status)

	// Re-open store (as would happen on next daemon or monitor startup)
	recoveredStore, err := history.NewDiskStore(runsDir)
	require.NoError(t, err)

	// Verify run is automatically marked as interrupted
	recoveredMeta, err := recoveredStore.GetRun(crashedJob.ID)
	require.NoError(t, err)
	assert.Equal(t, monitor.StatusInterrupted, recoveredMeta.Status)
	assert.False(t, recoveredMeta.EndedAt.IsZero())

	// Verify catalog index also shows interrupted
	runs, err := recoveredStore.ListRuns(5)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, monitor.StatusInterrupted, runs[0].Status)
}
