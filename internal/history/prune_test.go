package history_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiskStore_Prune_MaxRuns(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	baseTime := time.Now().Add(-1 * time.Hour).UTC().Truncate(time.Second)

	// Create 10 runs with increasing StartedAt times
	for i := 0; i < 10; i++ {
		jobID := fmt.Sprintf("job-%02d", i)
		job := &monitor.JobInfo{
			ID:        jobID,
			Name:      fmt.Sprintf("Run %d", i),
			Workflow:  "CI",
			Repo:      "adrijshikhar/actr",
			Owner:     "adrijshikhar",
			Status:    monitor.StatusRunning,
			StartedAt: baseTime.Add(time.Duration(i) * time.Minute),
		}
		require.NoError(t, store.StartRun(job, nil, "docker"))
		require.NoError(t, store.FinishRun(jobID, monitor.StatusSuccess, nil))
	}

	// Verify all 10 runs exist initially
	runs, err := store.ListRuns(0)
	require.NoError(t, err)
	require.Len(t, runs, 10)

	// Prune to keep 5 newest runs
	prunedCount, err := store.Prune(5, 0)
	require.NoError(t, err)
	assert.Equal(t, 5, prunedCount)

	// Verify only 5 runs remain in index.json
	remainingRuns, err := store.ListRuns(0)
	require.NoError(t, err)
	require.Len(t, remainingRuns, 5)

	// The 5 newest runs (job-05 through job-09) must remain
	expectedRemainingIDs := map[string]bool{
		"job-05": true,
		"job-06": true,
		"job-07": true,
		"job-08": true,
		"job-09": true,
	}
	for _, r := range remainingRuns {
		assert.True(t, expectedRemainingIDs[r.ID], "expected run %s to remain", r.ID)
		assert.DirExists(t, filepath.Join(tempDir, r.ID))
	}

	// The 5 oldest runs (job-00 through job-04) must be removed from disk
	for i := 0; i < 5; i++ {
		prunedDir := filepath.Join(tempDir, fmt.Sprintf("job-%02d", i))
		assert.NoDirExists(t, prunedDir)
	}

	// Calling Prune again with maxRuns >= len(runs) should prune 0
	prunedCount, err = store.Prune(5, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, prunedCount)

	// Calling Prune with maxRuns <= 0 should prune 0
	prunedCount, err = store.Prune(0, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, prunedCount)
}

func TestDiskStore_Prune_MaxDiskBytes(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	baseTime := time.Now().Add(-1 * time.Hour).UTC().Truncate(time.Second)

	// Helper to calculate total size of all run directories
	calcRunFoldersSize := func(runIDs []string) int64 {
		var total int64
		for _, id := range runIDs {
			runDir := filepath.Join(tempDir, id)
			_ = filepath.Walk(runDir, func(_ string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					total += info.Size()
				}
				return nil
			})
		}
		return total
	}

	// Create 4 runs with sizable logs (~2KB each)
	runIDs := []string{"job-size-0", "job-size-1", "job-size-2", "job-size-3"}
	logPayload := strings.Repeat("A", 2048)

	for i, id := range runIDs {
		job := &monitor.JobInfo{
			ID:        id,
			Name:      fmt.Sprintf("Size Run %d", i),
			Workflow:  "CI",
			Repo:      "adrijshikhar/actr",
			Owner:     "adrijshikhar",
			Status:    monitor.StatusRunning,
			StartedAt: baseTime.Add(time.Duration(i) * time.Minute),
			Steps: []*monitor.StepInfo{
				{
					ID:     "step-1",
					Name:   "Log step",
					Number: 1,
					Status: monitor.StatusRunning,
				},
			},
		}
		require.NoError(t, store.StartRun(job, nil, "docker"))
		require.NoError(t, store.AppendStepLog(id, "step-1", logPayload))
		require.NoError(t, store.FinishRun(id, monitor.StatusSuccess, nil))
	}

	totalBefore := calcRunFoldersSize(runIDs)
	require.Greater(t, totalBefore, int64(8000), "total run folders size should be > 8KB")

	// Set maxDiskBytes to trigger pruning (e.g. half of total size)
	maxDiskBytes := totalBefore / 2
	threshold90 := int64(float64(maxDiskBytes) * 0.90)

	prunedCount, err := store.Prune(0, maxDiskBytes)
	require.NoError(t, err)
	assert.Greater(t, prunedCount, 0, "should have pruned at least one run")

	// Verify remaining runs
	remainingRuns, err := store.ListRuns(0)
	require.NoError(t, err)

	var remainingIDs []string
	for _, r := range remainingRuns {
		remainingIDs = append(remainingIDs, r.ID)
		assert.DirExists(t, filepath.Join(tempDir, r.ID))
	}

	// Verify total size of remaining run folders is <= 90% of maxDiskBytes
	totalAfter := calcRunFoldersSize(remainingIDs)
	assert.LessOrEqual(t, totalAfter, threshold90, "total size of run folders after pruning must be <= 90%% of maxDiskBytes")

	// Oldest runs should have been pruned first (job-size-0, job-size-1...)
	for i := 0; i < prunedCount; i++ {
		prunedDir := filepath.Join(tempDir, runIDs[i])
		assert.NoDirExists(t, prunedDir, "oldest run directory %s should be deleted", runIDs[i])
	}

	// Newest run (job-size-3) should still exist
	assert.DirExists(t, filepath.Join(tempDir, "job-size-3"))
}

func TestDiskStore_RecoverInterruptedRuns(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)

	// Run 1: Interrupted run (started 10 minutes ago, still running)
	interruptedJob := &monitor.JobInfo{
		ID:        "job-interrupted",
		Name:      "Interrupted Job",
		Workflow:  "CI",
		Repo:      "adrijshikhar/actr",
		Owner:     "adrijshikhar",
		Status:    monitor.StatusRunning,
		StartedAt: now.Add(-10 * time.Minute),
		Steps: []*monitor.StepInfo{
			{
				ID:        "step-1",
				Name:      "Completed Step",
				Number:    1,
				Status:    monitor.StatusSuccess,
				StartedAt: now.Add(-10 * time.Minute),
				EndedAt:   now.Add(-9 * time.Minute),
			},
			{
				ID:        "step-2",
				Name:      "Running Step",
				Number:    2,
				Status:    monitor.StatusRunning,
				StartedAt: now.Add(-9 * time.Minute),
			},
		},
	}
	require.NoError(t, store.StartRun(interruptedJob, nil, "docker"))

	// Run 2: Clean completed run
	completedJob := &monitor.JobInfo{
		ID:        "job-completed",
		Name:      "Completed Job",
		Workflow:  "CI",
		Repo:      "adrijshikhar/actr",
		Owner:     "adrijshikhar",
		Status:    monitor.StatusRunning,
		StartedAt: now.Add(-5 * time.Minute),
	}
	require.NoError(t, store.StartRun(completedJob, nil, "docker"))
	require.NoError(t, store.FinishRun("job-completed", monitor.StatusSuccess, nil))

	// Verify state before recovery
	beforeRuns, err := store.ListRuns(0)
	require.NoError(t, err)
	require.Len(t, beforeRuns, 2)
	for _, r := range beforeRuns {
		if r.ID == "job-interrupted" {
			assert.Equal(t, monitor.StatusRunning, r.Status)
		}
	}

	// Call RecoverInterruptedRuns
	recoveredCount, err := store.RecoverInterruptedRuns()
	require.NoError(t, err)
	assert.Equal(t, 1, recoveredCount)

	// Verify index.json after recovery
	afterRuns, err := store.ListRuns(0)
	require.NoError(t, err)
	require.Len(t, afterRuns, 2)

	var recoveredSummary *history.RunSummary
	var completedSummary *history.RunSummary
	for _, r := range afterRuns {
		switch r.ID {
		case "job-interrupted":
			recoveredSummary = r
		case "job-completed":
			completedSummary = r
		}
	}

	require.NotNil(t, recoveredSummary)
	assert.Equal(t, history.StatusInterrupted, recoveredSummary.Status)
	assert.False(t, recoveredSummary.EndedAt.IsZero())
	assert.GreaterOrEqual(t, recoveredSummary.DurationSec, int64(600)) // ~10 minutes

	require.NotNil(t, completedSummary)
	assert.Equal(t, monitor.StatusSuccess, completedSummary.Status)

	// Verify meta.json after recovery
	recoveredMeta, err := store.GetRun("job-interrupted")
	require.NoError(t, err)
	assert.Equal(t, history.StatusInterrupted, recoveredMeta.Status)
	assert.False(t, recoveredMeta.EndedAt.IsZero())
	assert.GreaterOrEqual(t, recoveredMeta.DurationSec, int64(600))
	require.Len(t, recoveredMeta.Steps, 2)
	assert.Equal(t, monitor.StatusSuccess, recoveredMeta.Steps[0].Status)
	assert.Equal(t, history.StatusInterrupted, recoveredMeta.Steps[1].Status)
	assert.False(t, recoveredMeta.Steps[1].EndedAt.IsZero())

	// Calling RecoverInterruptedRuns again should recover 0 runs
	secondRecoveryCount, err := store.RecoverInterruptedRuns()
	require.NoError(t, err)
	assert.Equal(t, 0, secondRecoveryCount)
}

func TestDiskStore_RecoverInterruptedRuns_OnStoreInit(t *testing.T) {
	tempDir := t.TempDir()

	// Seed an index.json with a running job
	now := time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Second)
	catalog := history.IndexCatalog{
		Version:   1,
		UpdatedAt: now,
		Runs: []*history.RunSummary{
			{
				ID:        "job-crashed",
				Name:      "Crashed Run",
				Status:    monitor.StatusRunning,
				StartedAt: now,
			},
		},
	}
	catalogBytes, err := json.Marshal(catalog)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "index.json"), catalogBytes, 0644))

	// Also write meta.json for the crashed job
	jobDir := filepath.Join(tempDir, "job-crashed")
	require.NoError(t, os.MkdirAll(jobDir, 0755))
	meta := history.RunMetadata{
		ID:        "job-crashed",
		Name:      "Crashed Run",
		Status:    monitor.StatusRunning,
		StartedAt: now,
	}
	metaBytes, err := json.Marshal(meta)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(jobDir, "meta.json"), metaBytes, 0644))

	// Initialize NewDiskStore - should auto-recover on init
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)
	require.NotNil(t, store)

	runs, err := store.ListRuns(0)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, history.StatusInterrupted, runs[0].Status)
	assert.False(t, runs[0].EndedAt.IsZero())

	metaRecovered, err := store.GetRun("job-crashed")
	require.NoError(t, err)
	assert.Equal(t, history.StatusInterrupted, metaRecovered.Status)
	assert.False(t, metaRecovered.EndedAt.IsZero())
}
