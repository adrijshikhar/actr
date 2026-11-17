package test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitor_Publish_HighFrequencyLogs_SnapshotNotBlocked(t *testing.T) {
	tempDir := t.TempDir()
	sockPath := testSocketPath(t)

	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	server := monitor.NewServer(sockPath, "test-runner").WithHistoryStore(store)
	require.NoError(t, server.Start())
	defer func() {
		_ = server.Stop()
	}()

	client := monitor.NewClient(sockPath)
	jobID := "job-concurrent-stream"
	stepID := "step-build-heavy"

	// 1. Publish EventJobStart
	server.Publish(monitor.Event{
		Type:  monitor.EventJobStart,
		JobID: jobID,
		Job: &monitor.JobInfo{
			ID:        jobID,
			Name:      "Concurrent Stream Build",
			Status:    monitor.StatusRunning,
			StartedAt: time.Now(),
			Steps: []*monitor.StepInfo{
				{
					ID:        stepID,
					Name:      "Compile Modules",
					Number:    1,
					Status:    monitor.StatusRunning,
					StartedAt: time.Now(),
				},
			},
		},
	})

	const numWorkers = 4
	const linesPerWorker = 250
	totalExpectedLines := numWorkers * linesPerWorker

	var wg sync.WaitGroup
	startSignal := make(chan struct{})

	// Spin up log publishers
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			<-startSignal
			for i := 0; i < linesPerWorker; i++ {
				server.Publish(monitor.Event{
					Type:    monitor.EventStepLog,
					JobID:   jobID,
					StepID:  stepID,
					LogLine: fmt.Sprintf("worker-%d-log-line-%04d", workerID, i),
				})
			}
		}(w)
	}

	// Spin up snapshot readers concurrently to ensure Publish does not hold locks
	// during disk I/O and starve snapshot reads
	var readCount atomic.Int64
	var maxReadDuration atomic.Int64
	readerDone := make(chan struct{})

	go func() {
		<-startSignal
		for {
			select {
			case <-readerDone:
				return
			default:
				t0 := time.Now()
				ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				snap, err := client.GetSnapshot(ctx)
				cancel()
				elapsed := time.Since(t0)

				if err == nil && snap != nil {
					readCount.Add(1)
					if durMs := elapsed.Milliseconds(); durMs > maxReadDuration.Load() {
						maxReadDuration.Store(durMs)
					}
				}
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	// Release workers
	close(startSignal)
	wg.Wait()
	close(readerDone)

	// Verify that snapshot reads succeeded concurrently without being blocked or deadlocking
	assert.Greater(t, readCount.Load(), int64(5), "expected multiple successful snapshot reads during concurrent publishing")
	assert.Less(t, maxReadDuration.Load(), int64(250), "snapshot reads should not be blocked by disk I/O lock contention")

	// Finish step and job
	server.Publish(monitor.Event{
		Type:   monitor.EventStepFinish,
		JobID:  jobID,
		StepID: stepID,
		Step: &monitor.StepInfo{
			ID:      stepID,
			Name:    "Compile Modules",
			Status:  monitor.StatusSuccess,
			EndedAt: time.Now(),
		},
	})

	server.Publish(monitor.Event{
		Type:  monitor.EventJobFinish,
		JobID: jobID,
		Job: &monitor.JobInfo{
			ID:      jobID,
			Status:  monitor.StatusSuccess,
			EndedAt: time.Now(),
		},
	})

	// Verify step logs on disk
	logs, err := store.GetStepLogs(jobID, stepID)
	require.NoError(t, err)
	assert.Len(t, logs, totalExpectedLines)

	// Verify final metadata
	meta, err := store.GetRun(jobID)
	require.NoError(t, err)
	require.Len(t, meta.Steps, 1)
	assert.Equal(t, totalExpectedLines, meta.Steps[0].LogLines)
	assert.Greater(t, meta.Steps[0].LogBytes, int64(0))
}

func TestMonitor_Telemetry_CumulativeRateDeltaTime(t *testing.T) {
	tempDir := t.TempDir()
	sockPath := testSocketPath(t)

	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	server := monitor.NewServer(sockPath, "test-runner").WithHistoryStore(store)
	require.NoError(t, server.Start())
	defer func() {
		_ = server.Stop()
	}()

	jobID := "job-telemetry-rate-test"
	t0 := time.Now().UTC().Truncate(time.Second)

	// Start run
	server.Publish(monitor.Event{
		Type:  monitor.EventJobStart,
		JobID: jobID,
		Job: &monitor.JobInfo{
			ID:        jobID,
			Name:      "Rate Calculation Job",
			Status:    monitor.StatusRunning,
			StartedAt: t0,
		},
	})

	// Sample 1 at t0: NetRxRate = 2000 B/s, NetTxRate = 1000 B/s, DiskWriteRate = 4000 B/s
	// First sample defaults to 0.5s interval -> rate * 0.5
	server.Publish(monitor.Event{
		Type:  monitor.EventTelemetry,
		JobID: jobID,
		Telemetry: &monitor.TelemetrySample{
			Timestamp:     t0,
			NetRxRate:     2000,
			NetTxRate:     1000,
			DiskWriteRate: 4000,
		},
	})

	// Sample 2 at t0 + 500ms: delta = 0.5s -> rate * 0.5
	t1 := t0.Add(500 * time.Millisecond)
	server.Publish(monitor.Event{
		Type:  monitor.EventTelemetry,
		JobID: jobID,
		Telemetry: &monitor.TelemetrySample{
			Timestamp:     t1,
			NetRxRate:     2000,
			NetTxRate:     1000,
			DiskWriteRate: 4000,
		},
	})

	// Sample 3 at t0 + 1000ms: delta = 0.5s -> rate * 0.5
	t2 := t0.Add(1000 * time.Millisecond)
	server.Publish(monitor.Event{
		Type:  monitor.EventTelemetry,
		JobID: jobID,
		Telemetry: &monitor.TelemetrySample{
			Timestamp:     t2,
			NetRxRate:     4000,
			NetTxRate:     2000,
			DiskWriteRate: 8000,
		},
	})

	// Sample 4 with duplicate timestamp t2 (diff <= 0): deltaSeconds must be 0.0, accumulating 0 phantom bytes
	server.Publish(monitor.Event{
		Type:  monitor.EventTelemetry,
		JobID: jobID,
		Telemetry: &monitor.TelemetrySample{
			Timestamp:     t2,
			NetRxRate:     4000,
			NetTxRate:     2000,
			DiskWriteRate: 8000,
		},
	})

	// Expected cumulative calculations:
	// TotalNetRxBytes:
	// Sample 1: 2000 * 0.5 = 1000
	// Sample 2: 2000 * 0.5 = 1000
	// Sample 3: 4000 * 0.5 = 2000
	// Sum = 4000 bytes (without the bug fix, naive addition of raw rates yielded 8000 bytes)
	expectedNetRx := uint64(1000 + 1000 + 2000)

	// TotalNetTxBytes:
	// Sample 1: 1000 * 0.5 = 500
	// Sample 2: 1000 * 0.5 = 500
	// Sample 3: 2000 * 0.5 = 1000
	// Sum = 2000 bytes (without the bug fix, yielded 4000 bytes)
	expectedNetTx := uint64(500 + 500 + 1000)

	// TotalDiskWrittenBytes:
	// Sample 1: 4000 * 0.5 = 2000
	// Sample 2: 4000 * 0.5 = 2000
	// Sample 3: 8000 * 0.5 = 4000
	// Sum = 8000 bytes (without the bug fix, yielded 16000 bytes)
	expectedDiskWrite := uint64(2000 + 2000 + 4000)

	// Finish job
	server.Publish(monitor.Event{
		Type:  monitor.EventJobFinish,
		JobID: jobID,
		Job: &monitor.JobInfo{
			ID:      jobID,
			Status:  monitor.StatusSuccess,
			EndedAt: t2.Add(100 * time.Millisecond),
		},
	})

	meta, err := store.GetRun(jobID)
	require.NoError(t, err)
	require.NotNil(t, meta)

	assert.Equal(t, expectedNetRx, meta.MetricsSummary.TotalNetRxBytes, "cumulative NetRx bytes must account for interval delta time")
	assert.Equal(t, expectedNetTx, meta.MetricsSummary.TotalNetTxBytes, "cumulative NetTx bytes must account for interval delta time")
	assert.Equal(t, expectedDiskWrite, meta.MetricsSummary.TotalDiskWrittenBytes, "cumulative DiskWritten bytes must account for interval delta time")
}

func TestMonitor_StepDurationAndExitCode_RecordedInHistory(t *testing.T) {
	tempDir := t.TempDir()
	sockPath := testSocketPath(t)

	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	server := monitor.NewServer(sockPath, "test-runner").WithHistoryStore(store)
	require.NoError(t, server.Start())
	defer func() {
		_ = server.Stop()
	}()

	jobID := "job-duration-test"
	stepID := "step-build"
	t0 := time.Now().UTC().Add(-10 * time.Second)

	// 1. Job Start
	server.Publish(monitor.Event{
		Type:  monitor.EventJobStart,
		JobID: jobID,
		Job: &monitor.JobInfo{
			ID:        jobID,
			Name:      "Build & Test",
			Status:    monitor.StatusRunning,
			StartedAt: t0,
		},
	})

	// 2. Step Start at t0 + 2s
	stepStart := t0.Add(2 * time.Second)
	server.Publish(monitor.Event{
		Type:   monitor.EventStepStart,
		JobID:  jobID,
		StepID: stepID,
		Step: &monitor.StepInfo{
			ID:        stepID,
			Name:      "Build Target",
			Status:    monitor.StatusRunning,
			StartedAt: stepStart,
		},
	})

	// 3. Step Finish at t0 + 7s (5 seconds duration), failed with exit code 42
	stepFinish := t0.Add(7 * time.Second)
	server.Publish(monitor.Event{
		Type:   monitor.EventStepFinish,
		JobID:  jobID,
		StepID: stepID,
		Step: &monitor.StepInfo{
			ID:       stepID,
			Name:     "Build Target",
			Status:   monitor.StatusFailed,
			ExitCode: 42,
			EndedAt:  stepFinish,
		},
	})

	// 4. Job Finish
	server.Publish(monitor.Event{
		Type:  monitor.EventJobFinish,
		JobID: jobID,
		Job: &monitor.JobInfo{
			ID:      jobID,
			Status:  monitor.StatusFailed,
			EndedAt: stepFinish,
		},
	})

	meta, err := store.GetRun(jobID)
	require.NoError(t, err)
	require.NotNil(t, meta)
	require.Len(t, meta.Steps, 1)

	stepMeta := meta.Steps[0]
	assert.Equal(t, stepID, stepMeta.ID)
	assert.Equal(t, monitor.StatusFailed, stepMeta.Status)
	assert.Equal(t, 42, stepMeta.ExitCode, "ExitCode must be recorded from step finish")
	assert.Equal(t, int64(5), stepMeta.DurationSec, "DurationSec must be recorded based on step start and end times")
}
