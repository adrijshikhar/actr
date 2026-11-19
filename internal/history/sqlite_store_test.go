package history

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/stretchr/testify/require"
)

func TestSQLiteStore_CompileTimeInterfaces(t *testing.T) {
	var _ Store = (*SQLiteStore)(nil)
	var _ LogStore = (*SQLiteStore)(nil)
	var _ Pruner = (*SQLiteStore)(nil)
}

func TestSQLiteStore_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_history.db")

	store, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	meta := &monitor.RunMetadata{
		ID:          "job-101",
		RepoAlias:   "aim",
		RepoURL:     "https://github.com/adrijshikhar/aim",
		Name:        "Test (macOS)",
		Workflow:    "CI",
		Owner:       "adrijshikhar",
		Repo:        "aim",
		Status:      monitor.StatusRunning,
		StartedAt:   now,
		EndedAt:     now.Add(10 * time.Second),
		DurationSec: 10,
		Driver:      "host",
		Steps: []*monitor.StepMetadata{
			{
				ID:        "step-1",
				Name:      "Checkout code",
				Status:    monitor.StatusSuccess,
				StartedAt: now,
			},
			{
				ID:        "step-2",
				Name:      "Run tests",
				Status:    monitor.StatusRunning,
				StartedAt: now.Add(2 * time.Second),
			},
		},
	}

	err = store.RecordRun(meta)
	require.NoError(t, err)

	// Append initial log batch
	err = store.AppendStepLogs("job-101", "step-1", []string{"line 1", "line 2", "line 3"})
	require.NoError(t, err)

	// Append second log batch to verify line numbering continuity
	err = store.AppendStepLogs("job-101", "step-1", []string{"line 4", "line 5"})
	require.NoError(t, err)

	// Update run to success
	meta.Status = monitor.StatusSuccess
	meta.Steps[1].Status = monitor.StatusSuccess
	err = store.UpdateRun(meta)
	require.NoError(t, err)

	// Retrieve run
	gotRun, err := store.GetRun("job-101")
	require.NoError(t, err)
	require.Equal(t, "job-101", gotRun.ID)
	require.Equal(t, "aim", gotRun.RepoAlias)
	require.Equal(t, "https://github.com/adrijshikhar/aim", gotRun.RepoURL)
	require.Equal(t, "Test (macOS)", gotRun.Name)
	require.Equal(t, "CI", gotRun.Workflow)
	require.Equal(t, "adrijshikhar", gotRun.Owner)
	require.Equal(t, "aim", gotRun.Repo)
	require.Equal(t, monitor.StatusSuccess, gotRun.Status)
	require.Equal(t, "host", gotRun.Driver)
	require.Len(t, gotRun.Steps, 2)
	require.Equal(t, "step-1", gotRun.Steps[0].ID)
	require.Equal(t, "step-2", gotRun.Steps[1].ID)

	// Retrieve logs
	logs, err := store.GetStepLogs("job-101", "step-1")
	require.NoError(t, err)
	require.Equal(t, []string{"line 1", "line 2", "line 3", "line 4", "line 5"}, logs)

	// Test GetStepLogReader
	reader, err := store.GetStepLogReader("job-101", "step-1")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	content, err := io.ReadAll(reader)
	require.NoError(t, err)
	expectedContent := "line 1\nline 2\nline 3\nline 4\nline 5\n"
	require.Equal(t, expectedContent, string(content))

	// List runs
	runs, err := store.ListRuns(10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, "job-101", runs[0].ID)
	require.Equal(t, "aim", runs[0].RepoAlias)

	// Nonexistent run lookup returns ErrRunNotFound
	missingRun, err := store.GetRun("job-nonexistent")
	require.Nil(t, missingRun)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrRunNotFound), "expected errors.Is ErrRunNotFound, got: %v", err)
}

func TestSQLiteStore_Pruning(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_prune.db")

	store, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("job-%d", i)
		stepID := fmt.Sprintf("step-%d", i)
		err := store.RecordRun(&monitor.RunMetadata{
			ID:        id,
			Name:      id,
			RepoAlias: "aim",
			Status:    monitor.StatusSuccess,
			StartedAt: now.Add(time.Duration(i) * time.Minute),
			EndedAt:   now.Add(time.Duration(i)*time.Minute + 5*time.Second),
			Steps: []*monitor.StepMetadata{
				{
					ID:        stepID,
					Name:      "step",
					Status:    monitor.StatusSuccess,
					StartedAt: now.Add(time.Duration(i) * time.Minute),
				},
			},
		})
		require.NoError(t, err)

		err = store.AppendStepLogs(id, stepID, []string{"log for " + id})
		require.NoError(t, err)
	}

	// Prune to keep 2 newest runs (job-4 and job-3)
	pruned, err := store.Prune(2)
	require.NoError(t, err)
	require.Equal(t, 3, pruned)

	runs, err := store.ListRuns(10)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	require.Equal(t, "job-4", runs[0].ID)
	require.Equal(t, "job-3", runs[1].ID)

	// Verify cascade deletion: oldest runs and their steps and logs are gone
	for _, id := range []string{"job-0", "job-1", "job-2"} {
		_, err := store.GetRun(id)
		require.True(t, errors.Is(err, ErrRunNotFound))

		logs, err := store.GetStepLogs(id, "step-"+id[4:])
		require.NoError(t, err)
		require.Empty(t, logs)
	}
}

func TestSQLiteStore_ConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_concurrent.db")

	store, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	const numGoroutines = 10
	const logsPerGoroutine = 20

	now := time.Now().UTC()
	var wg sync.WaitGroup
	for i := 0; i < numGoroutines; i++ {
		jobID := fmt.Sprintf("job-concurrent-%d", i)
		stepID := "step-1"

		meta := &monitor.RunMetadata{
			ID:        jobID,
			RepoAlias: "concurrency-test",
			Name:      "test",
			Status:    monitor.StatusRunning,
			StartedAt: now,
			Steps: []*monitor.StepMetadata{
				{
					ID:        stepID,
					Name:      "step-1",
					Status:    monitor.StatusRunning,
					StartedAt: now,
				},
			},
		}
		require.NoError(t, store.RecordRun(meta))

		wg.Add(1)
		go func(jID, sID string) {
			defer wg.Done()
			for l := 0; l < logsPerGoroutine; l++ {
				appendErr := store.AppendStepLogs(jID, sID, []string{fmt.Sprintf("log entry %d", l)})
				if appendErr != nil {
					t.Errorf("AppendStepLogs failed for %s: %v", jID, appendErr)
					return
				}
			}
		}(jobID, stepID)
	}

	wg.Wait()

	// Verify all logs were recorded accurately
	for i := 0; i < numGoroutines; i++ {
		jobID := fmt.Sprintf("job-concurrent-%d", i)
		logs, err := store.GetStepLogs(jobID, "step-1")
		require.NoError(t, err)
		require.Len(t, logs, logsPerGoroutine)
	}
}

func TestSQLiteStore_RecoverInterruptedRuns(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_recovery.db")

	store, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	now := time.Now().UTC().Add(-10 * time.Minute)
	err = store.RecordRun(&monitor.RunMetadata{
		ID:        "job-crash-1",
		RepoAlias: "aim",
		Name:      "Crashed Run",
		Status:    monitor.StatusRunning,
		StartedAt: now,
		Steps: []*monitor.StepMetadata{
			{
				ID:        "step-crashed",
				Name:      "Build",
				Status:    monitor.StatusRunning,
				StartedAt: now,
			},
		},
	})
	require.NoError(t, err)

	recovered, err := store.RecoverInterruptedRuns()
	require.NoError(t, err)
	require.Equal(t, 1, recovered)

	run, err := store.GetRun("job-crash-1")
	require.NoError(t, err)
	require.Equal(t, StatusInterrupted, run.Status)
	require.False(t, run.EndedAt.IsZero())
	require.GreaterOrEqual(t, run.DurationSec, int64(600))
	require.Len(t, run.Steps, 1)
	require.Equal(t, StatusInterrupted, run.Steps[0].Status)
}

func TestSQLiteStore_HistoryStoreInterfaceMethods(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_historystore.db")

	store, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	job := &monitor.JobInfo{
		ID:        "job-hs-1",
		RepoAlias: "aim",
		RepoURL:   "https://github.com/adrijshikhar/aim",
		Name:      "HistoryStore Test",
		Workflow:  "build.yml",
		Owner:     "adrijshikhar",
		Repo:      "aim",
		Status:    monitor.StatusRunning,
		StartedAt: now,
		Steps: []*monitor.StepInfo{
			{
				ID:        "step-1",
				Number:    1,
				Name:      "Setup",
				Status:    monitor.StatusRunning,
				StartedAt: now,
			},
		},
	}

	// 1. StartRun
	err = store.StartRun(job, nil, "docker")
	require.NoError(t, err)

	// 2. AppendStepLog (single line)
	err = store.AppendStepLog("job-hs-1", "step-1", "Starting setup...")
	require.NoError(t, err)

	// 3. FinishStep
	stepEnd := now.Add(5 * time.Second)
	err = store.FinishStep("job-hs-1", "step-1", "success", 0, stepEnd)
	require.NoError(t, err)

	// 4. FinishRun
	metrics := &monitor.RunMetricsSummary{
		PeakCPUPercent:  45.5,
		PeakRSSBytes:    1024 * 1024 * 128,
		TotalNetRxBytes: 5000,
		TotalNetTxBytes: 2000,
	}
	err = store.FinishRun("job-hs-1", monitor.StatusSuccess, metrics)
	require.NoError(t, err)

	// Verify run metadata
	run, err := store.GetRun("job-hs-1")
	require.NoError(t, err)
	require.Equal(t, monitor.StatusSuccess, run.Status)
	require.Equal(t, "docker", run.Driver)
	require.Equal(t, 45.5, run.MetricsSummary.PeakCPUPercent)
	require.Equal(t, uint64(1024*1024*128), run.MetricsSummary.PeakRSSBytes)
	require.Len(t, run.Steps, 1)
	require.Equal(t, monitor.StatusSuccess, run.Steps[0].Status)
	require.Equal(t, int64(5), run.Steps[0].DurationSec)

	// Verify logs
	logs, err := store.GetStepLogs("job-hs-1", "step-1")
	require.NoError(t, err)
	require.Equal(t, []string{"Starting setup..."}, logs)
}
