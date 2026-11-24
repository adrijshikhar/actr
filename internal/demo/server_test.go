package demo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartServer_Smoke(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "demo.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := StartServer(ctx, sockPath)
	if err != nil {
		t.Fatalf("failed to start demo server: %v", err)
	}
	defer func() { _ = srv.Stop() }()

	client := monitor.NewClient(sockPath)
	snap, err := client.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("failed to get snapshot from demo server: %v", err)
	}

	if snap.RunnerName != "m5-pro (macOS arm64)" {
		t.Fatalf("expected runner name 'm5-pro (macOS arm64)', got %q", snap.RunnerName)
	}

	require.NotNil(t, snap.SupervisorStatus)
	assert.Equal(t, 2, snap.SupervisorStatus.GlobalMaxJobs)
	assert.Equal(t, 1, snap.SupervisorStatus.GlobalActive)
	require.Len(t, snap.SupervisorStatus.RepoSlots, 2)
	assert.Equal(t, "aim", snap.SupervisorStatus.RepoSlots[0].Alias)
	assert.Equal(t, "cxstatusline", snap.SupervisorStatus.RepoSlots[1].Alias)

	if len(snap.ActiveJobs) == 0 {
		t.Fatal("expected at least 1 active job in demo snapshot")
	}
	assert.Equal(t, "cxstatusline", snap.ActiveJobs[0].RepoAlias)

	// Verify telemetry was populated
	if snap.Telemetry == nil {
		t.Fatal("expected telemetry in snapshot")
	}
	if snap.Telemetry.StepCPU <= 0 {
		t.Fatalf("expected positive StepCPU, got %f", snap.Telemetry.StepCPU)
	}

	// Wait briefly for simulation ticker to stream logs
	events, errs := client.StreamEvents(ctx)
	select {
	case ev := <-events:
		if ev.Type == "" {
			t.Fatal("received empty event from stream")
		}
	case err := <-errs:
		t.Fatalf("stream error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event from demo stream")
	}
}

func TestStartServer_DemoMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "demo.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := StartServer(ctx, sockPath)
	if err != nil {
		t.Fatalf("failed to start demo server: %v", err)
	}
	defer func() { _ = srv.Stop() }()

	client := monitor.NewClient(sockPath)
	snap, err := client.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("failed to get snapshot: %v", err)
	}

	if snap.Telemetry == nil {
		t.Fatal("expected telemetry in snapshot")
	}
	if snap.Telemetry.TraceID == "" {
		t.Fatal("expected non-empty TraceID in demo snapshot")
	}
	if snap.Telemetry.DiskTotalBytes == 0 {
		t.Fatal("expected non-zero DiskTotalBytes in demo snapshot")
	}
}

func TestSeedHistoryStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "demo_history.db")
	store, err := history.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	err = SeedHistoryStore(store)
	require.NoError(t, err)

	runs, err := store.ListRuns(10)
	require.NoError(t, err)
	require.Len(t, runs, 4)

	runMap := make(map[string]*history.RunSummary)
	for _, r := range runs {
		runMap[r.ID] = r
	}

	// Verify job-queued-1
	qRun, ok := runMap["job-queued-1"]
	require.True(t, ok, "missing job-queued-1")
	assert.Equal(t, monitor.StatusQueued, qRun.Status)
	assert.Equal(t, "actr", qRun.RepoAlias)
	assert.Equal(t, 2, qRun.StepCount)

	// Verify release-v0.1.0
	rel, ok := runMap["release-v0.1.0"]
	require.True(t, ok, "missing release-v0.1.0")
	assert.Equal(t, monitor.StatusSuccess, rel.Status)
	assert.Equal(t, "actr", rel.RepoAlias)
	assert.Equal(t, 3, rel.StepCount)

	// Verify nightly-security-scan
	sec, ok := runMap["nightly-security-scan"]
	require.True(t, ok, "missing nightly-security-scan")
	assert.Equal(t, monitor.StatusSuccess, sec.Status)
	assert.Equal(t, "aim", sec.RepoAlias)
	assert.Equal(t, 3, sec.StepCount)

	// Verify flaky-integration-test
	flk, ok := runMap["flaky-integration-test"]
	require.True(t, ok, "missing flaky-integration-test")
	assert.Equal(t, monitor.StatusFailed, flk.Status)
	assert.Equal(t, "cxstatusline", flk.RepoAlias)
	assert.Equal(t, 3, flk.StepCount)

	// Verify logs for flaky step
	logs, err := store.GetStepLogs("flaky-integration-test", "flk-3")
	require.NoError(t, err)
	assert.NotEmpty(t, logs)
	joined := ""
	for _, l := range logs {
		joined += l + "\n"
	}
	assert.Contains(t, joined, "FAIL: TestConcurrentTransactions_DeadlockRetry")
	assert.Contains(t, joined, "\x1b[1;31m")

	// Verify metadata peak vitals
	flkMeta, err := store.GetRun("flaky-integration-test")
	require.NoError(t, err)
	assert.Equal(t, 96.8, flkMeta.MetricsSummary.PeakCPUPercent)
	assert.Equal(t, uint64(512*1024*1024), flkMeta.MetricsSummary.PeakRSSBytes)
}
