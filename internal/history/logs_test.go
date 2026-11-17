package history_test

import (
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
