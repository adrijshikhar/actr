package history_test

import (
	"encoding/json"
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

func TestNewDiskStore(t *testing.T) {
	t.Run("creates base directory and empty index.json if not present", func(t *testing.T) {
		tempDir := filepath.Join(t.TempDir(), "nested", "runs")
		store, err := history.NewDiskStore(tempDir)
		require.NoError(t, err)
		require.NotNil(t, store)

		indexPath := filepath.Join(tempDir, "index.json")
		require.FileExists(t, indexPath)

		data, err := os.ReadFile(indexPath)
		require.NoError(t, err)

		var catalog history.IndexCatalog
		err = json.Unmarshal(data, &catalog)
		require.NoError(t, err)
		assert.Equal(t, 1, catalog.Version)
		assert.Empty(t, catalog.Runs)
		assert.False(t, catalog.UpdatedAt.IsZero())
	})

	t.Run("loads existing index.json without overwriting", func(t *testing.T) {
		tempDir := t.TempDir()
		indexPath := filepath.Join(tempDir, "index.json")

		existing := history.IndexCatalog{
			Version:   1,
			UpdatedAt: time.Now().Add(-1 * time.Hour).UTC(),
			Runs: []*history.RunSummary{
				{
					ID:        "job-existing",
					Name:      "test-run",
					Status:    monitor.StatusSuccess,
					StartedAt: time.Now().Add(-1 * time.Hour).UTC(),
				},
			},
		}
		data, err := json.Marshal(existing)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(indexPath, data, 0644))

		store, err := history.NewDiskStore(tempDir)
		require.NoError(t, err)
		require.NotNil(t, store)

		runs, err := store.ListRuns(0)
		require.NoError(t, err)
		require.Len(t, runs, 1)
		assert.Equal(t, "job-existing", runs[0].ID)
	})

	t.Run("returns error when baseDir is empty", func(t *testing.T) {
		store, err := history.NewDiskStore("")
		assert.Error(t, err)
		assert.Nil(t, store)
	})
}

func TestDiskStore_StartRun(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	job := &monitor.JobInfo{
		ID:        "job-101",
		Name:      "unit-tests",
		Workflow:  "CI Workflow",
		Repo:      "adrijshikhar/actr",
		Owner:     "adrijshikhar",
		Status:    monitor.StatusRunning,
		StartedAt: now,
		Steps: []*monitor.StepInfo{
			{
				ID:        "step-1",
				Name:      "Checkout",
				Number:    1,
				Status:    monitor.StatusSuccess,
				StartedAt: now,
				EndedAt:   now.Add(2 * time.Second),
			},
			{
				ID:        "step-2",
				Name:      "Run tests",
				Number:    2,
				Status:    monitor.StatusRunning,
				StartedAt: now.Add(2 * time.Second),
			},
		},
	}

	limits := &config.LimitsConfig{
		CPU:    2.0,
		Memory: 2147483648,
		Pids:   150,
	}

	err = store.StartRun(job, limits, "docker")
	require.NoError(t, err)

	// Check folder and steps directory creation
	jobDir := filepath.Join(tempDir, "job-101")
	assert.DirExists(t, jobDir)
	assert.DirExists(t, filepath.Join(jobDir, "steps"))

	// Check meta.json
	metaPath := filepath.Join(jobDir, "meta.json")
	require.FileExists(t, metaPath)

	metaData, err := os.ReadFile(metaPath)
	require.NoError(t, err)

	var meta history.RunMetadata
	err = json.Unmarshal(metaData, &meta)
	require.NoError(t, err)

	assert.Equal(t, "job-101", meta.ID)
	assert.Equal(t, "unit-tests", meta.Name)
	assert.Equal(t, "CI Workflow", meta.Workflow)
	assert.Equal(t, "adrijshikhar/actr", meta.Repo)
	assert.Equal(t, "adrijshikhar", meta.Owner)
	assert.Equal(t, monitor.StatusRunning, meta.Status)
	assert.Equal(t, "docker", meta.Driver)
	assert.Equal(t, now, meta.StartedAt)
	require.NotNil(t, meta.Limits)
	assert.Equal(t, 2.0, meta.Limits.CPU)
	assert.Equal(t, int64(2147483648), meta.Limits.Memory)
	assert.Equal(t, int64(150), meta.Limits.Pids)
	require.Len(t, meta.Steps, 2)
	assert.Equal(t, "step-1", meta.Steps[0].ID)
	assert.Equal(t, "Checkout", meta.Steps[0].Name)
	assert.Equal(t, 1, meta.Steps[0].Number)
	assert.Equal(t, monitor.StatusSuccess, meta.Steps[0].Status)
	assert.Equal(t, "step-2", meta.Steps[1].ID)

	// Check index.json updated
	runs, err := store.ListRuns(0)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "job-101", runs[0].ID)
	assert.Equal(t, "unit-tests", runs[0].Name)
	assert.Equal(t, "CI Workflow", runs[0].Workflow)
	assert.Equal(t, "adrijshikhar/actr", runs[0].Repo)
	assert.Equal(t, "adrijshikhar", runs[0].Owner)
	assert.Equal(t, monitor.StatusRunning, runs[0].Status)
	assert.Equal(t, "docker", runs[0].Driver)
	assert.Equal(t, 2, runs[0].StepCount)
}

func TestDiskStore_StartRun_InvalidArgs(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	assert.Error(t, store.StartRun(nil, nil, "docker"))
	assert.Error(t, store.StartRun(&monitor.JobInfo{ID: ""}, nil, "docker"))
}

func TestDiskStore_FinishRun(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	startedAt := time.Now().Add(-10 * time.Second).UTC().Truncate(time.Second)
	job := &monitor.JobInfo{
		ID:        "job-202",
		Name:      "integration-tests",
		Workflow:  "Nightly",
		Repo:      "adrijshikhar/actr",
		Owner:     "adrijshikhar",
		Status:    monitor.StatusRunning,
		StartedAt: startedAt,
	}

	err = store.StartRun(job, nil, "host")
	require.NoError(t, err)

	metrics := &history.RunMetricsSummary{
		PeakCPUPercent:        145.5,
		PeakRSSBytes:          536870912,
		TotalNetRxBytes:       1048576,
		TotalNetTxBytes:       524288,
		TotalDiskWrittenBytes: 2097152,
	}

	err = store.FinishRun("job-202", monitor.StatusSuccess, metrics)
	require.NoError(t, err)

	// Verify GetRun
	runMeta, err := store.GetRun("job-202")
	require.NoError(t, err)
	assert.Equal(t, monitor.StatusSuccess, runMeta.Status)
	assert.False(t, runMeta.EndedAt.IsZero())
	assert.True(t, runMeta.EndedAt.After(startedAt) || runMeta.EndedAt.Equal(startedAt))
	assert.GreaterOrEqual(t, runMeta.DurationSec, int64(10))
	assert.Equal(t, 145.5, runMeta.MetricsSummary.PeakCPUPercent)
	assert.Equal(t, uint64(536870912), runMeta.MetricsSummary.PeakRSSBytes)
	assert.Equal(t, uint64(1048576), runMeta.MetricsSummary.TotalNetRxBytes)
	assert.Equal(t, uint64(524288), runMeta.MetricsSummary.TotalNetTxBytes)
	assert.Equal(t, uint64(2097152), runMeta.MetricsSummary.TotalDiskWrittenBytes)

	// Verify index.json updated
	runs, err := store.ListRuns(0)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "job-202", runs[0].ID)
	assert.Equal(t, monitor.StatusSuccess, runs[0].Status)
	assert.Equal(t, runMeta.EndedAt, runs[0].EndedAt)
	assert.Equal(t, runMeta.DurationSec, runs[0].DurationSec)
	assert.Equal(t, 145.5, runs[0].PeakCPUPercent)
	assert.Equal(t, uint64(536870912), runs[0].PeakRSSBytes)

	// Test non-existent job
	err = store.FinishRun("non-existent-job", monitor.StatusFailed, nil)
	assert.Error(t, err)
}

func TestDiskStore_ListRuns(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	baseTime := time.Now().UTC().Truncate(time.Minute)

	runs := []*monitor.JobInfo{
		{
			ID:        "run-1",
			Name:      "First",
			StartedAt: baseTime.Add(1 * time.Minute),
		},
		{
			ID:        "run-2",
			Name:      "Second",
			StartedAt: baseTime.Add(3 * time.Minute),
		},
		{
			ID:        "run-3",
			Name:      "Third",
			StartedAt: baseTime.Add(2 * time.Minute),
		},
	}

	for _, r := range runs {
		require.NoError(t, store.StartRun(r, nil, "docker"))
	}

	// List all runs (limit 0 or negative)
	allRuns, err := store.ListRuns(0)
	require.NoError(t, err)
	require.Len(t, allRuns, 3)

	// Sorted descending by StartedAt: run-2 (3m), run-3 (2m), run-1 (1m)
	assert.Equal(t, "run-2", allRuns[0].ID)
	assert.Equal(t, "run-3", allRuns[1].ID)
	assert.Equal(t, "run-1", allRuns[2].ID)

	// Test limit
	limitedRuns, err := store.ListRuns(2)
	require.NoError(t, err)
	require.Len(t, limitedRuns, 2)
	assert.Equal(t, "run-2", limitedRuns[0].ID)
	assert.Equal(t, "run-3", limitedRuns[1].ID)
}

func TestDiskStore_GetRun(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	t.Run("returns error when run does not exist", func(t *testing.T) {
		meta, err := store.GetRun("missing-job")
		assert.Error(t, err)
		assert.True(t, os.IsNotExist(err) || err != nil)
		assert.Nil(t, meta)
	})

	t.Run("returns full run metadata", func(t *testing.T) {
		job := &monitor.JobInfo{
			ID:       "run-full",
			Name:     "Full Meta Test",
			Workflow: "Release",
		}
		require.NoError(t, store.StartRun(job, nil, "docker"))

		meta, err := store.GetRun("run-full")
		require.NoError(t, err)
		require.NotNil(t, meta)
		assert.Equal(t, "run-full", meta.ID)
		assert.Equal(t, "Full Meta Test", meta.Name)
		assert.Equal(t, "Release", meta.Workflow)
	})
}

func TestDiskStore_AtomicWrite(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	// Verify no temporary files remain after operations
	job := &monitor.JobInfo{
		ID:   "run-atomic",
		Name: "Atomic Test",
	}
	require.NoError(t, store.StartRun(job, nil, "docker"))
	require.NoError(t, store.FinishRun("run-atomic", monitor.StatusSuccess, nil))

	// Search for any .tmp files
	err = filepath.Walk(tempDir, func(path string, info os.FileInfo, walkErr error) error {
		require.NoError(t, walkErr)
		if !info.IsDir() {
			assert.NotContains(t, filepath.Base(path), ".tmp")
		}
		return nil
	})
	require.NoError(t, err)

	// Concurrency test: multiple goroutines starting and finishing runs simultaneously
	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		id := fmt.Sprintf("concurrent-%d", i)
		go func(runID string) {
			defer wg.Done()
			j := &monitor.JobInfo{
				ID:        runID,
				Name:      "Concurrent Run",
				StartedAt: time.Now().UTC(),
			}
			startErr := store.StartRun(j, nil, "docker")
			assert.NoError(t, startErr)

			finishErr := store.FinishRun(runID, monitor.StatusSuccess, &history.RunMetricsSummary{
				PeakCPUPercent: 50.0,
			})
			assert.NoError(t, finishErr)

			_, listErr := store.ListRuns(5)
			assert.NoError(t, listErr)

			_, getErr := store.GetRun(runID)
			assert.NoError(t, getErr)
		}(id)
	}

	wg.Wait()

	runs, err := store.ListRuns(0)
	require.NoError(t, err)
	// 1 initial run + 10 concurrent runs = 11 runs
	assert.Len(t, runs, 11)
}
