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
	histNode := dash.Tree().FindNode("historical-runs")
	require.NotNil(t, histNode)
	assert.False(t, histNode.Expanded, "historical runs should be collapsed by default")
	histNode.Expanded = true
	for _, ch := range histNode.Children {
		ch.Expanded = true
	}
	dash.Tree().Rebuild()
	view = dash.View()
	assert.Contains(t, view, "E2E Integration P")

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

func TestHistoryStore_PathTraversalRejection(t *testing.T) {
	tempBase := t.TempDir()
	runsDir := filepath.Join(tempBase, "runs")

	store, err := history.NewDiskStore(runsDir)
	require.NoError(t, err)

	traversalJobIDs := []string{
		"../escaped-job",
		"../../etc-job",
		"job/with/slash",
		"job\\with\\backslash",
		"..",
		".",
	}

	for _, badJobID := range traversalJobIDs {
		t.Run("jobID_"+badJobID, func(t *testing.T) {
			job := &monitor.JobInfo{
				ID:   badJobID,
				Name: "Bad Job",
			}
			err := store.StartRun(job, nil, "host")
			assert.Error(t, err, "StartRun should reject traversal jobID %q", badJobID)

			err = store.FinishRun(badJobID, monitor.StatusSuccess, nil)
			assert.Error(t, err, "FinishRun should reject traversal jobID %q", badJobID)

			_, err = store.GetRun(badJobID)
			assert.Error(t, err, "GetRun should reject traversal jobID %q", badJobID)

			err = store.AppendStepLog(badJobID, "valid-step", "some log")
			assert.Error(t, err, "AppendStepLog should reject traversal jobID %q", badJobID)

			_, err = store.GetStepLogs(badJobID, "valid-step")
			assert.Error(t, err, "GetStepLogs should reject traversal jobID %q", badJobID)

			_, err = store.GetStepLogReader(badJobID, "valid-step")
			assert.Error(t, err, "GetStepLogReader should reject traversal jobID %q", badJobID)
		})
	}

	traversalStepIDs := []string{
		"../escaped-step",
		"../../evil-step",
		"step/with/slash",
		"step\\backslash",
		"..",
		".",
	}

	for _, badStepID := range traversalStepIDs {
		t.Run("stepID_"+badStepID, func(t *testing.T) {
			err := store.AppendStepLog("valid-job", badStepID, "some log")
			assert.Error(t, err, "AppendStepLog should reject traversal stepID %q", badStepID)

			_, err = store.GetStepLogs("valid-job", badStepID)
			assert.Error(t, err, "GetStepLogs should reject traversal stepID %q", badStepID)

			_, err = store.GetStepLogReader("valid-job", badStepID)
			assert.Error(t, err, "GetStepLogReader should reject traversal stepID %q", badStepID)
		})
	}
}

func TestHistoryStore_CatalogCorruptNotOverwritten(t *testing.T) {
	tempBase := t.TempDir()
	runsDir := filepath.Join(tempBase, "runs")

	store, err := history.NewDiskStore(runsDir)
	require.NoError(t, err)

	indexPath := filepath.Join(runsDir, "index.json")
	corruptContent := []byte("{corrupted-json-content: not valid json")
	require.NoError(t, os.WriteFile(indexPath, corruptContent, 0600))

	job := &monitor.JobInfo{
		ID:   "job-corrupt-test",
		Name: "Test Run",
	}

	// StartRun should fail when index.json is corrupted
	err = store.StartRun(job, nil, "host")
	require.Error(t, err, "StartRun should fail when index.json is corrupted")

	// Corrupted index.json must be preserved as index.json.corrupt.* and not wiped
	matches, globErr := filepath.Glob(filepath.Join(runsDir, "index.json.corrupt.*"))
	require.NoError(t, globErr)
	require.NotEmpty(t, matches, "expected index.json.corrupt.* backup to exist")

	backupBytes, readErr := os.ReadFile(matches[0])
	require.NoError(t, readErr)
	assert.Equal(t, corruptContent, backupBytes, "backup should match original corrupted content")

	// Verify index.json was not overwritten with an empty catalog
	if currentData, err := os.ReadFile(indexPath); err == nil {
		assert.Equal(t, corruptContent, currentData, "index.json should retain corrupt data if not renamed, never overwritten with empty catalog")
	}
}

func TestHistoryStore_PruneSkipsActiveRuns(t *testing.T) {
	tempBase := t.TempDir()
	runsDir := filepath.Join(tempBase, "runs")

	store, err := history.NewDiskStore(runsDir)
	require.NoError(t, err)

	baseTime := time.Now().Add(-1 * time.Hour).UTC().Truncate(time.Second)

	// Create running job
	runningJob := &monitor.JobInfo{
		ID:        "job-active-running",
		Name:      "Active Running Job",
		Status:    monitor.StatusRunning,
		StartedAt: baseTime.Add(1 * time.Minute),
	}
	require.NoError(t, store.StartRun(runningJob, nil, "host"))

	// Create queued job
	queuedJob := &monitor.JobInfo{
		ID:        "job-active-queued",
		Name:      "Active Queued Job",
		Status:    monitor.StatusQueued,
		StartedAt: baseTime.Add(2 * time.Minute),
	}
	require.NoError(t, store.StartRun(queuedJob, nil, "host"))

	// Create 2 completed jobs
	completedJob1 := &monitor.JobInfo{
		ID:        "job-completed-1",
		Name:      "Completed Job 1",
		Status:    monitor.StatusRunning,
		StartedAt: baseTime.Add(3 * time.Minute),
	}
	require.NoError(t, store.StartRun(completedJob1, nil, "host"))
	require.NoError(t, store.FinishRun(completedJob1.ID, monitor.StatusSuccess, nil))

	completedJob2 := &monitor.JobInfo{
		ID:        "job-completed-2",
		Name:      "Completed Job 2",
		Status:    monitor.StatusRunning,
		StartedAt: baseTime.Add(4 * time.Minute),
	}
	require.NoError(t, store.StartRun(completedJob2, nil, "host"))
	require.NoError(t, store.FinishRun(completedJob2.ID, monitor.StatusSuccess, nil))

	// Prune with maxRuns=1 (1 completed run retention)
	prunedCount, err := store.Prune(1, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, prunedCount, "only older completed job should be pruned")

	runs, err := store.ListRuns(0)
	require.NoError(t, err)

	runMap := make(map[string]string)
	for _, r := range runs {
		runMap[r.ID] = string(r.Status)
	}

	// Active runs must NEVER be pruned
	assert.Contains(t, runMap, "job-active-running")
	assert.Contains(t, runMap, "job-active-queued")
	assert.DirExists(t, filepath.Join(runsDir, "job-active-running"))
	assert.DirExists(t, filepath.Join(runsDir, "job-active-queued"))

	// Only older completed job should be removed
	assert.NotContains(t, runMap, "job-completed-1")
	assert.Contains(t, runMap, "job-completed-2")
	assert.NoDirExists(t, filepath.Join(runsDir, "job-completed-1"))
	assert.DirExists(t, filepath.Join(runsDir, "job-completed-2"))
}

func TestHistoryStore_PruneEmptyRunIDDoesNotDeleteBaseDir(t *testing.T) {
	tempBase := t.TempDir()
	runsDir := filepath.Join(tempBase, "runs")

	store, err := history.NewDiskStore(runsDir)
	require.NoError(t, err)

	sentinelFile := filepath.Join(runsDir, "sentinel.txt")
	require.NoError(t, os.WriteFile(sentinelFile, []byte("preserve-me"), 0600))

	// Manually inject invalid runs with empty or whitespace IDs into index.json
	indexPath := filepath.Join(runsDir, "index.json")
	indexBytes, err := os.ReadFile(indexPath)
	require.NoError(t, err)

	var catalog monitor.IndexCatalog
	require.NoError(t, json.Unmarshal(indexBytes, &catalog))

	catalog.Runs = append(catalog.Runs,
		&monitor.RunSummary{
			ID:        "",
			Status:    monitor.StatusSuccess,
			StartedAt: time.Now().Add(-10 * time.Minute),
		},
		&monitor.RunSummary{
			ID:        "   ",
			Status:    monitor.StatusSuccess,
			StartedAt: time.Now().Add(-9 * time.Minute),
		},
	)

	newIndexBytes, err := json.Marshal(catalog)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(indexPath, newIndexBytes, 0600))

	// Prune should safely handle empty IDs without removing base directory
	_, err = store.Prune(1, 0)
	require.NoError(t, err)

	// Ensure base directory and sentinel file STILL exist
	assert.DirExists(t, runsDir, "base directory must NOT be deleted by empty run ID")
	assert.FileExists(t, sentinelFile, "sentinel file must remain untouched")
}
