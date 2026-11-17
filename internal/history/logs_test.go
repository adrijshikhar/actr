package history_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiskStore_AppendStepLog(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	t.Run("appends lines to step log file with newline termination", func(t *testing.T) {
		jobID := "job-append-1"
		stepID := "step-setup"

		err := store.AppendStepLog(jobID, stepID, "Initializing test runner...")
		require.NoError(t, err)

		err = store.AppendStepLog(jobID, stepID, "Checking out repository\n")
		require.NoError(t, err)

		expectedPath := filepath.Join(tempDir, jobID, "steps", stepID+".log")
		require.FileExists(t, expectedPath)

		content, err := os.ReadFile(expectedPath)
		require.NoError(t, err)
		assert.Equal(t, "Initializing test runner...\nChecking out repository\n", string(content))
	})

	t.Run("creates steps directory when it does not exist", func(t *testing.T) {
		jobID := "job-new-dir"
		stepID := "step-build"

		err := store.AppendStepLog(jobID, stepID, "go build ./...")
		require.NoError(t, err)

		expectedDir := filepath.Join(tempDir, jobID, "steps")
		assert.DirExists(t, expectedDir)

		expectedPath := filepath.Join(expectedDir, stepID+".log")
		require.FileExists(t, expectedPath)
	})

	t.Run("returns error on invalid arguments", func(t *testing.T) {
		assert.Error(t, store.AppendStepLog("", "step-1", "log line"))
		assert.Error(t, store.AppendStepLog("job-1", "", "log line"))
	})
}

func TestDiskStore_GetStepLogs_PreservesANSI(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	jobID := "job-ansi"
	stepID := "step-colors"

	lines := []string{
		"\x1b[32mPASS\x1b[0m test_case_1 (0.01s)",
		"\x1b[1;31mFAIL:\x1b[0m \x1b[4mexpected true, got false\x1b[0m",
		"\x1b[38;5;214mWARN:\x1b[0m 256 color sequence",
		"\x1b[2J\x1b[H\x1b[?25lCursor hide and clear screen",
	}

	for _, l := range lines {
		require.NoError(t, store.AppendStepLog(jobID, stepID, l))
	}

	retrieved, err := store.GetStepLogs(jobID, stepID)
	require.NoError(t, err)
	require.Len(t, retrieved, 4)

	for i, expected := range lines {
		assert.Equal(t, expected, retrieved[i])
	}
}

func TestDiskStore_GetStepLogs_NonExistent(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	t.Run("returns empty slice and nil error when step log does not exist", func(t *testing.T) {
		logs, err := store.GetStepLogs("non-existent-job", "missing-step")
		require.NoError(t, err)
		assert.Empty(t, logs)
		assert.NotNil(t, logs)
	})

	t.Run("returns error on invalid arguments", func(t *testing.T) {
		_, err := store.GetStepLogs("", "step-1")
		assert.Error(t, err)

		_, err = store.GetStepLogs("job-1", "")
		assert.Error(t, err)
	})
}

func TestDiskStore_GetStepLogReader(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	jobID := "job-reader"
	stepID := "step-test"

	err = store.AppendStepLog(jobID, stepID, "Streaming line 1")
	require.NoError(t, err)
	err = store.AppendStepLog(jobID, stepID, "Streaming line 2")
	require.NoError(t, err)

	t.Run("reads complete stream from step log file", func(t *testing.T) {
		reader, err := store.GetStepLogReader(jobID, stepID)
		require.NoError(t, err)
		require.NotNil(t, reader)
		defer func() {
			_ = reader.Close()
		}()

		data, err := io.ReadAll(reader)
		require.NoError(t, err)
		assert.Equal(t, "Streaming line 1\nStreaming line 2\n", string(data))
	})

	t.Run("returns error when file does not exist", func(t *testing.T) {
		reader, err := store.GetStepLogReader(jobID, "non-existent-step")
		assert.Error(t, err)
		assert.True(t, os.IsNotExist(err))
		assert.Nil(t, reader)
	})

	t.Run("returns error on invalid arguments", func(t *testing.T) {
		_, err := store.GetStepLogReader("", "step-1")
		assert.Error(t, err)

		_, err = store.GetStepLogReader("job-1", "")
		assert.Error(t, err)
	})
}

func TestDiskStore_ConcurrentAppends(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	jobID := "job-concurrent"
	stepID := "step-concurrent"

	const numGoroutines = 10
	const linesPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(gID int) {
			defer wg.Done()
			for l := 0; l < linesPerGoroutine; l++ {
				line := fmt.Sprintf("goroutine-%02d-line-%03d", gID, l)
				err := store.AppendStepLog(jobID, stepID, line)
				assert.NoError(t, err)
			}
		}(g)
	}

	wg.Wait()

	logs, err := store.GetStepLogs(jobID, stepID)
	require.NoError(t, err)
	require.Len(t, logs, numGoroutines*linesPerGoroutine)

	// Verify all expected lines are present and uncorrupted
	counts := make(map[string]int)
	for _, l := range logs {
		assert.True(t, strings.HasPrefix(l, "goroutine-"), "unexpected corrupted log line: %s", l)
		counts[l]++
	}

	for g := 0; g < numGoroutines; g++ {
		for l := 0; l < linesPerGoroutine; l++ {
			expected := fmt.Sprintf("goroutine-%02d-line-%03d", g, l)
			assert.Equal(t, 1, counts[expected], "expected line %s to appear exactly once", expected)
		}
	}
}

func TestDiskStore_StepLog_UpdatesMetadata(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	jobID := "job-meta-test"
	now := time.Now().UTC().Truncate(time.Second)

	job := &monitor.JobInfo{
		ID:        jobID,
		Name:      "test-run",
		Status:    monitor.StatusRunning,
		StartedAt: now,
		Steps: []*monitor.StepInfo{
			{
				ID:        "step-1",
				Name:      "Step 1",
				Number:    1,
				Status:    monitor.StatusRunning,
				StartedAt: now,
			},
			{
				ID:     "step-2",
				Name:   "Step 2",
				Number: 2,
				Status: monitor.StatusQueued,
			},
		},
	}

	require.NoError(t, store.StartRun(job, nil, "docker"))

	// Check initial step metadata
	meta, err := store.GetRun(jobID)
	require.NoError(t, err)
	require.Len(t, meta.Steps, 2)
	assert.Equal(t, int64(0), meta.Steps[0].LogBytes)
	assert.Equal(t, 0, meta.Steps[0].LogLines)
	assert.Equal(t, int64(0), meta.Steps[1].LogBytes)
	assert.Equal(t, 0, meta.Steps[1].LogLines)

	// Append logs to step-1
	line1 := "Line 1: starting container..."
	line2 := "Line 2: compilation finished\n"
	require.NoError(t, store.AppendStepLog(jobID, "step-1", line1))
	require.NoError(t, store.AppendStepLog(jobID, "step-1", line2))

	expectedBytes := int64(len(line1+"\n") + len(line2))
	expectedLines := 2

	meta, err = store.GetRun(jobID)
	require.NoError(t, err)
	assert.Equal(t, expectedBytes, meta.Steps[0].LogBytes)
	assert.Equal(t, expectedLines, meta.Steps[0].LogLines)
	assert.Equal(t, int64(0), meta.Steps[1].LogBytes)
	assert.Equal(t, 0, meta.Steps[1].LogLines)

	// Appending to an unknown step should not fail and should not corrupt meta.json
	require.NoError(t, store.AppendStepLog(jobID, "step-unknown", "unknown step log"))
	meta, err = store.GetRun(jobID)
	require.NoError(t, err)
	require.Len(t, meta.Steps, 2)
}

func TestDiskStore_AppendStepLog_DecoupledFromDiskFsync(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	jobID := "job-decoupled"
	stepID := "step-compile"
	now := time.Now().UTC()

	job := &monitor.JobInfo{
		ID:        jobID,
		Name:      "test-decoupled",
		Status:    monitor.StatusRunning,
		StartedAt: now,
		Steps: []*monitor.StepInfo{
			{
				ID:        stepID,
				Name:      "Compile",
				Number:    1,
				Status:    monitor.StatusRunning,
				StartedAt: now,
			},
		},
	}
	require.NoError(t, store.StartRun(job, nil, "docker"))

	metaFilePath := filepath.Join(tempDir, jobID, "meta.json")
	require.FileExists(t, metaFilePath)

	// Append 10 log lines
	var totalBytes int64
	for i := 1; i <= 10; i++ {
		line := fmt.Sprintf("Building module %d...\n", i)
		totalBytes += int64(len(line))
		require.NoError(t, store.AppendStepLog(jobID, stepID, line))
	}

	// Read meta.json DIRECTLY from disk without going through GetRun
	rawDiskMeta, err := os.ReadFile(metaFilePath)
	require.NoError(t, err)

	var diskMeta monitor.RunMetadata
	require.NoError(t, json.Unmarshal(rawDiskMeta, &diskMeta))
	require.Len(t, diskMeta.Steps, 1)

	// Crucial check: On disk, meta.json was NOT rewritten on each AppendStepLog call.
	// LogBytes and LogLines remain 0 on disk until FinishStep or FinishRun is executed.
	assert.Equal(t, int64(0), diskMeta.Steps[0].LogBytes, "disk meta.json must not be modified during hot log streaming")
	assert.Equal(t, 0, diskMeta.Steps[0].LogLines, "disk meta.json must not be modified during hot log streaming")

	// GetRun returns real-time in-memory counts
	inMemoryMeta, err := store.GetRun(jobID)
	require.NoError(t, err)
	assert.Equal(t, totalBytes, inMemoryMeta.Steps[0].LogBytes)
	assert.Equal(t, 10, inMemoryMeta.Steps[0].LogLines)

	// Finalize step with FinishStep
	finishedAt := now.Add(5 * time.Second)
	err = store.FinishStep(jobID, stepID, string(monitor.StatusSuccess), 0, finishedAt)
	require.NoError(t, err)

	// Now read meta.json from disk again - it must be finalized with exact stats
	rawDiskMetaAfter, err := os.ReadFile(metaFilePath)
	require.NoError(t, err)
	var diskMetaAfter monitor.RunMetadata
	require.NoError(t, json.Unmarshal(rawDiskMetaAfter, &diskMetaAfter))
	require.Len(t, diskMetaAfter.Steps, 1)

	assert.Equal(t, monitor.StatusSuccess, diskMetaAfter.Steps[0].Status)
	assert.Equal(t, 0, diskMetaAfter.Steps[0].ExitCode)
	assert.Equal(t, totalBytes, diskMetaAfter.Steps[0].LogBytes)
	assert.Equal(t, 10, diskMetaAfter.Steps[0].LogLines)
	assert.Equal(t, int64(5), diskMetaAfter.Steps[0].DurationSec)
}

func TestDiskStore_FinishStep_Detailed(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	jobID := "job-finish-step"
	stepID := "step-test-run"
	now := time.Now().UTC()

	job := &monitor.JobInfo{
		ID:        jobID,
		Name:      "test-run",
		Status:    monitor.StatusRunning,
		StartedAt: now,
		Steps: []*monitor.StepInfo{
			{
				ID:        stepID,
				Name:      "Run Tests",
				Number:    1,
				Status:    monitor.StatusRunning,
				StartedAt: now,
			},
		},
	}
	require.NoError(t, store.StartRun(job, nil, "docker"))

	// Stream log line
	line := "FAIL: test_case_failure\n"
	require.NoError(t, store.AppendStepLog(jobID, stepID, line))

	// Finish step with failed conclusion and exit code 1
	finishedAt := now.Add(12 * time.Second)
	err = store.FinishStep(jobID, stepID, "failed", 1, finishedAt)
	require.NoError(t, err)

	meta, err := store.GetRun(jobID)
	require.NoError(t, err)
	require.Len(t, meta.Steps, 1)

	step := meta.Steps[0]
	assert.Equal(t, monitor.StatusFailed, step.Status)
	assert.Equal(t, 1, step.ExitCode)
	assert.Equal(t, int64(12), step.DurationSec)
	assert.Equal(t, int64(len(line)), step.LogBytes)
	assert.Equal(t, 1, step.LogLines)

	// Validation on invalid inputs
	assert.Error(t, store.FinishStep("", stepID, "success", 0, time.Now()))
	assert.Error(t, store.FinishStep(jobID, "", "success", 0, time.Now()))
	assert.Error(t, store.FinishStep("../invalid", stepID, "success", 0, time.Now()))
}

func TestDiskStore_StepLocks_CleanedUp(t *testing.T) {
	tempDir := t.TempDir()
	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	jobID := "job-cleanup"
	now := time.Now().UTC()

	job := &monitor.JobInfo{
		ID:        jobID,
		Name:      "test-cleanup",
		Status:    monitor.StatusRunning,
		StartedAt: now,
		Steps: []*monitor.StepInfo{
			{ID: "step-1", Name: "Step 1", Number: 1, Status: monitor.StatusRunning, StartedAt: now},
			{ID: "step-2", Name: "Step 2", Number: 2, Status: monitor.StatusRunning, StartedAt: now},
		},
	}
	require.NoError(t, store.StartRun(job, nil, "docker"))

	// Append to step-1 and step-2
	require.NoError(t, store.AppendStepLog(jobID, "step-1", "log from step 1\n"))
	require.NoError(t, store.AppendStepLog(jobID, "step-2", "log from step 2\n"))

	assert.Equal(t, 2, store.StepLockCount(), "expected 2 active step locks")
	assert.Equal(t, 2, store.StepStatsCount(), "expected 2 active step stats")

	// Finish step-1
	require.NoError(t, store.FinishStep(jobID, "step-1", "success", 0, time.Now()))
	assert.Equal(t, 1, store.StepLockCount(), "expected 1 active step lock after step-1 finished")
	assert.Equal(t, 1, store.StepStatsCount(), "expected 1 active step stats after step-1 finished")

	// Finish entire run (should clean up step-2 and any remaining locks/stats)
	require.NoError(t, store.FinishRun(jobID, monitor.StatusSuccess, nil))
	assert.Equal(t, 0, store.StepLockCount(), "all step locks should be cleaned up after run finish")
	assert.Equal(t, 0, store.StepStatsCount(), "all step stats should be cleaned up after run finish")
}
