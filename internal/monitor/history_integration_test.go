package monitor_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSocketPath(t *testing.T) string {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "actr-hist-test-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(tmpDir)
	})
	return filepath.Join(tmpDir, "test.sock")
}

func TestServer_WithHistoryStore_SnapshotHydration(t *testing.T) {
	tempRunsDir := t.TempDir()
	store, err := history.NewDiskStore(tempRunsDir)
	require.NoError(t, err)

	now := time.Now().UTC()
	// Pre-seed 2 completed historical runs in the store
	run1 := &monitor.JobInfo{
		ID:        "job-past-1",
		Name:      "CI Past Run 1",
		Workflow:  "ci.yml",
		Repo:      "test-repo",
		Owner:     "actr-org",
		Status:    monitor.StatusSuccess,
		StartedAt: now.Add(-10 * time.Minute),
		EndedAt:   now.Add(-8 * time.Minute),
		Steps: []*monitor.StepInfo{
			{
				ID:        "step-1-1",
				Name:      "Setup",
				Number:    1,
				Status:    monitor.StatusSuccess,
				StartedAt: now.Add(-10 * time.Minute),
				EndedAt:   now.Add(-9 * time.Minute),
			},
		},
	}
	require.NoError(t, store.StartRun(run1, nil, "docker"))
	require.NoError(t, store.FinishRun(run1.ID, monitor.StatusSuccess, &history.RunMetricsSummary{
		PeakCPUPercent: 42.5,
		PeakRSSBytes:   1024 * 1024 * 128,
	}))

	run2 := &monitor.JobInfo{
		ID:        "job-past-2",
		Name:      "CI Past Run 2",
		Workflow:  "build.yml",
		Repo:      "test-repo",
		Owner:     "actr-org",
		Status:    monitor.StatusFailed,
		StartedAt: now.Add(-5 * time.Minute),
		EndedAt:   now.Add(-4 * time.Minute),
		Steps: []*monitor.StepInfo{
			{
				ID:        "step-2-1",
				Name:      "Compile",
				Number:    1,
				Status:    monitor.StatusFailed,
				StartedAt: now.Add(-5 * time.Minute),
				EndedAt:   now.Add(-4 * time.Minute),
			},
		},
	}
	require.NoError(t, store.StartRun(run2, nil, "host"))
	require.NoError(t, store.FinishRun(run2.ID, monitor.StatusFailed, &history.RunMetricsSummary{
		PeakCPUPercent: 78.0,
		PeakRSSBytes:   1024 * 1024 * 256,
	}))

	sockPath := newTestSocketPath(t)
	server := monitor.NewServer(sockPath, "test-runner")
	server.WithHistoryStore(store)
	assert.Equal(t, store, server.HistoryStore())

	require.NoError(t, server.Start())
	defer func() { _ = server.Stop() }()

	client := monitor.NewClient(sockPath)
	snap, err := client.GetSnapshot(context.Background())
	require.NoError(t, err)
	require.NotNil(t, snap)

	// Verify that snap.RecentJobs is hydrated with the 2 pre-seeded historical runs
	require.Len(t, snap.RecentJobs, 2)
	assert.Equal(t, "job-past-2", snap.RecentJobs[0].ID)
	assert.Equal(t, monitor.StatusFailed, snap.RecentJobs[0].Status)
	assert.Equal(t, "CI Past Run 2", snap.RecentJobs[0].Name)
	require.NotEmpty(t, snap.RecentJobs[0].Steps)
	assert.Equal(t, "step-2-1", snap.RecentJobs[0].Steps[0].ID)

	assert.Equal(t, "job-past-1", snap.RecentJobs[1].ID)
	assert.Equal(t, monitor.StatusSuccess, snap.RecentJobs[1].Status)
	assert.Equal(t, "CI Past Run 1", snap.RecentJobs[1].Name)
	require.NotEmpty(t, snap.RecentJobs[1].Steps)
	assert.Equal(t, "step-1-1", snap.RecentJobs[1].Steps[0].ID)
}

func TestServer_Publish_PersistsEvents(t *testing.T) {
	tempRunsDir := t.TempDir()
	store, err := history.NewDiskStore(tempRunsDir)
	require.NoError(t, err)

	sockPath := newTestSocketPath(t)
	server := monitor.NewServer(sockPath, "test-runner")
	server.WithHistoryStore(store)

	require.NoError(t, server.Start())
	defer func() { _ = server.Stop() }()

	now := time.Now().UTC()
	jobID := "job-live-persist"
	stepID := "step-compile"

	// 1. EventJobStart
	limits := &config.LimitsConfig{
		CPU:    2.0,
		Memory: 2 * 1024 * 1024 * 1024,
	}
	server.Publish(monitor.Event{
		Type:  monitor.EventJobStart,
		JobID: jobID,
		Job: &monitor.JobInfo{
			ID:        jobID,
			Name:      "Build and Test",
			Workflow:  "pipeline.yml",
			Repo:      "actr",
			Owner:     "adrijshikhar",
			Status:    monitor.StatusRunning,
			StartedAt: now,
			Steps: []*monitor.StepInfo{
				{
					ID:        stepID,
					Name:      "go build",
					Number:    1,
					Status:    monitor.StatusRunning,
					StartedAt: now,
				},
			},
		},
		Limits: limits,
		Driver: "docker",
	})

	// 2. EventStepLog
	server.Publish(monitor.Event{
		Type:    monitor.EventStepLog,
		JobID:   jobID,
		StepID:  stepID,
		LogLine: "Compiling packages...",
	})
	server.Publish(monitor.Event{
		Type:    monitor.EventStepLog,
		JobID:   jobID,
		StepID:  stepID,
		LogLine: "\x1b[32mBuild succeeded\x1b[0m",
	})

	// 3. EventTelemetry (Peak vitals accumulation)
	server.Publish(monitor.Event{
		Type:  monitor.EventTelemetry,
		JobID: jobID,
		Telemetry: &monitor.TelemetrySample{
			StepCPU:      35.0,
			StepRSSBytes: 100 * 1024 * 1024,
			NetRxRate:    10 * 1024,
			NetTxRate:    5 * 1024,
		},
	})
	server.Publish(monitor.Event{
		Type:  monitor.EventTelemetry,
		JobID: jobID,
		Telemetry: &monitor.TelemetrySample{
			StepCPU:      92.5,
			StepRSSBytes: 350 * 1024 * 1024,
			NetRxRate:    50 * 1024,
			NetTxRate:    20 * 1024,
		},
	})
	server.Publish(monitor.Event{
		Type:  monitor.EventTelemetry,
		JobID: jobID,
		Telemetry: &monitor.TelemetrySample{
			StepCPU:      15.0,
			StepRSSBytes: 200 * 1024 * 1024,
			NetRxRate:    5 * 1024,
			NetTxRate:    2 * 1024,
		},
	})

	// 4. EventJobFinish
	server.Publish(monitor.Event{
		Type:  monitor.EventJobFinish,
		JobID: jobID,
		Job: &monitor.JobInfo{
			ID:      jobID,
			Name:    "Build and Test",
			Status:  monitor.StatusSuccess,
			EndedAt: now.Add(45 * time.Second),
		},
	})

	// Verify persistence in history.Store
	meta, err := store.GetRun(jobID)
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.Equal(t, jobID, meta.ID)
	assert.Equal(t, monitor.StatusSuccess, meta.Status)
	assert.Equal(t, "docker", meta.Driver)
	assert.Equal(t, 92.5, meta.MetricsSummary.PeakCPUPercent)
	assert.Equal(t, uint64(350*1024*1024), meta.MetricsSummary.PeakRSSBytes)
	assert.Equal(t, uint64((10+50+5)*1024), meta.MetricsSummary.TotalNetRxBytes)
	assert.Equal(t, uint64((5+20+2)*1024), meta.MetricsSummary.TotalNetTxBytes)

	// Verify logs persisted to disk
	logs, err := store.GetStepLogs(jobID, stepID)
	require.NoError(t, err)
	assert.Equal(t, []string{"Compiling packages...", "\x1b[32mBuild succeeded\x1b[0m"}, logs)

	// Verify snapshot reflects the completed job
	client := monitor.NewClient(sockPath)
	snap, err := client.GetSnapshot(context.Background())
	require.NoError(t, err)
	require.Len(t, snap.RecentJobs, 1)
	assert.Equal(t, jobID, snap.RecentJobs[0].ID)
	assert.Equal(t, monitor.StatusSuccess, snap.RecentJobs[0].Status)
}

func TestServer_GetHistoricalStepLogs_Endpoint(t *testing.T) {
	tempRunsDir := t.TempDir()
	store, err := history.NewDiskStore(tempRunsDir)
	require.NoError(t, err)

	now := time.Now().UTC()
	jobID := "job-hist-logs"
	stepID := "step-test"

	run := &monitor.JobInfo{
		ID:        jobID,
		Name:      "Test Run",
		Workflow:  "test.yml",
		Repo:      "actr",
		Owner:     "adrijshikhar",
		Status:    monitor.StatusSuccess,
		StartedAt: now,
		Steps: []*monitor.StepInfo{
			{
				ID:     stepID,
				Name:   "Run unit tests",
				Number: 1,
				Status: monitor.StatusSuccess,
			},
		},
	}
	require.NoError(t, store.StartRun(run, nil, "docker"))
	require.NoError(t, store.AppendStepLog(jobID, stepID, "=== RUN   TestSample"))
	require.NoError(t, store.AppendStepLog(jobID, stepID, "--- PASS: TestSample (0.01s)"))
	require.NoError(t, store.AppendStepLog(jobID, stepID, "PASS"))
	require.NoError(t, store.FinishRun(jobID, monitor.StatusSuccess, nil))

	sockPath := newTestSocketPath(t)
	server := monitor.NewServer(sockPath, "test-runner")
	server.WithHistoryStore(store)

	require.NoError(t, server.Start())
	defer func() { _ = server.Stop() }()

	client := monitor.NewClient(sockPath)

	t.Run("successfully retrieves step logs for existing job and step", func(t *testing.T) {
		lines, err := client.GetHistoricalStepLogs(context.Background(), jobID, stepID)
		require.NoError(t, err)
		assert.Equal(t, []string{
			"=== RUN   TestSample",
			"--- PASS: TestSample (0.01s)",
			"PASS",
		}, lines)
	})

	t.Run("returns not found for non-existent job", func(t *testing.T) {
		lines, err := client.GetHistoricalStepLogs(context.Background(), "non-existent-job", stepID)
		assert.Error(t, err)
		assert.Nil(t, lines)
	})

	t.Run("returns not found for non-existent step", func(t *testing.T) {
		lines, err := client.GetHistoricalStepLogs(context.Background(), jobID, "non-existent-step")
		assert.Error(t, err)
		assert.Nil(t, lines)
	})

	t.Run("returns error on invalid arguments", func(t *testing.T) {
		_, err := client.GetHistoricalStepLogs(context.Background(), "", stepID)
		assert.Error(t, err)
		_, err = client.GetHistoricalStepLogs(context.Background(), jobID, "")
		assert.Error(t, err)
	})
}

func TestServer_Publish_TriggersBackgroundPruning(t *testing.T) {
	tempRunsDir := t.TempDir()
	store, err := history.NewDiskStore(tempRunsDir)
	require.NoError(t, err)

	sockPath := newTestSocketPath(t)
	server := monitor.NewServer(sockPath, "test-runner")
	server.WithHistoryStore(store)
	server.WithRetention(2, 0) // Max 2 runs

	require.NoError(t, server.Start())
	defer func() { _ = server.Stop() }()

	for i := 1; i <= 4; i++ {
		jID := fmt.Sprintf("job-prune-%d", i)
		server.Publish(monitor.Event{
			Type:  monitor.EventJobStart,
			JobID: jID,
			Job: &monitor.JobInfo{
				ID:        jID,
				Name:      fmt.Sprintf("Run %d", i),
				StartedAt: time.Now().UTC().Add(time.Duration(i) * time.Minute),
			},
		})
		server.Publish(monitor.Event{
			Type:  monitor.EventJobFinish,
			JobID: jID,
			Job: &monitor.JobInfo{
				ID:     jID,
				Status: monitor.StatusSuccess,
			},
		})
	}

	// Allow non-blocking background goroutine to execute Prune
	require.Eventually(t, func() bool {
		runs, err := store.ListRuns(0)
		return err == nil && len(runs) <= 2
	}, 1*time.Second, 20*time.Millisecond)

	runs, err := store.ListRuns(0)
	require.NoError(t, err)
	assert.Len(t, runs, 2)
	assert.Equal(t, "job-prune-4", runs[0].ID)
	assert.Equal(t, "job-prune-3", runs[1].ID)
}

func TestServer_HistoricalStepLogs_DirectHTTP404WhenNoStore(t *testing.T) {
	sockPath := newTestSocketPath(t)
	server := monitor.NewServer(sockPath, "test-runner")
	require.NoError(t, server.Start())
	defer func() { _ = server.Stop() }()

	client := monitor.NewClient(sockPath)
	_, err := client.GetHistoricalStepLogs(context.Background(), "any-job", "any-step")
	assert.Error(t, err)
}

type orderRecordingStore struct {
	monitor.HistoryStore
	mu     sync.Mutex
	ops    []string
	closed bool
}

func (o *orderRecordingStore) record(op string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		panic("store operation after Close: " + op)
	}
	o.ops = append(o.ops, op)
}

func (o *orderRecordingStore) StartRun(job *monitor.JobInfo, limits *config.LimitsConfig, driver string) error {
	time.Sleep(15 * time.Millisecond)
	o.record("StartRun:" + job.ID)
	return nil
}

func (o *orderRecordingStore) AppendStepLog(jobID, stepID, line string) error {
	o.record("AppendStepLog:" + jobID + "/" + stepID)
	return nil
}

func (o *orderRecordingStore) FinishStep(jobID, stepID, conclusion string, exitCode int, finishedAt time.Time, startedAt ...time.Time) error {
	o.record("FinishStep:" + jobID + "/" + stepID)
	return nil
}

func (o *orderRecordingStore) FinishRun(jobID string, status monitor.StepStatus, metrics *monitor.RunMetricsSummary) error {
	o.record("FinishRun:" + jobID)
	return nil
}

func (o *orderRecordingStore) Prune(maxRuns int, maxDiskBytes ...int64) (int, error) {
	time.Sleep(10 * time.Millisecond)
	o.record("Prune")
	return 0, nil
}

func (o *orderRecordingStore) ListRuns(limit int) ([]*monitor.RunSummary, error) {
	return nil, nil
}

func (o *orderRecordingStore) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	return nil
}

func TestServer_SequentialPersistenceOrderingAndStopDrain(t *testing.T) {
	store := &orderRecordingStore{}
	sockPath := newTestSocketPath(t)
	server := monitor.NewServer(sockPath, "order-runner")
	server.WithHistoryStore(store)
	require.NoError(t, server.Start())

	server.Publish(monitor.Event{
		Type:  monitor.EventJobStart,
		JobID: "job-ord-1",
		Job:   &monitor.JobInfo{ID: "job-ord-1", Name: "Order Test", Status: monitor.StatusRunning},
	})
	server.Publish(monitor.Event{
		Type:    monitor.EventStepLog,
		JobID:   "job-ord-1",
		StepID:  "step-1",
		LogLine: "line 1",
	})
	server.Publish(monitor.Event{
		Type:   monitor.EventStepFinish,
		JobID:  "job-ord-1",
		StepID: "step-1",
		Step:   &monitor.StepInfo{ID: "step-1", Status: monitor.StatusSuccess, EndedAt: time.Now()},
	})
	server.Publish(monitor.Event{
		Type:  monitor.EventJobFinish,
		JobID: "job-ord-1",
		Job:   &monitor.JobInfo{ID: "job-ord-1", Status: monitor.StatusSuccess},
	})

	require.NoError(t, server.Stop())
	require.NoError(t, store.Close())

	store.mu.Lock()
	ops := append([]string(nil), store.ops...)
	store.mu.Unlock()

	assert.Equal(t, []string{
		"StartRun:job-ord-1",
		"AppendStepLog:job-ord-1/step-1",
		"FinishStep:job-ord-1/step-1",
		"FinishRun:job-ord-1",
		"Prune",
	}, ops)
}
