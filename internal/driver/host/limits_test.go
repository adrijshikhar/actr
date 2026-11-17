package host

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostLimiter_NilLimits(t *testing.T) {
	limiter := NewHostLimiter()
	require.NotNil(t, limiter)

	cleanup, err := limiter.Apply(context.Background(), os.Getpid(), nil)
	require.NoError(t, err)
	require.NotNil(t, cleanup)

	// Verify idempotency
	cleanup()
	cleanup()

	assert.False(t, limiter.IsOOMKilled(os.Getpid()))
}

func TestHostLimiter_ZeroLimits(t *testing.T) {
	limiter := NewHostLimiter()
	require.NotNil(t, limiter)

	cleanup, err := limiter.Apply(context.Background(), os.Getpid(), &config.LimitsConfig{
		CPU:    0,
		Memory: 0,
		Pids:   0,
	})
	require.NoError(t, err)
	require.NotNil(t, cleanup)

	cleanup()
	cleanup()

	assert.False(t, limiter.IsOOMKilled(os.Getpid()))
}

func TestHostLimiter_CleanupIdempotent(t *testing.T) {
	limiter := NewHostLimiter()
	require.NotNil(t, limiter)

	// Start a short-lived sleep process so we have a valid external child PID
	cmd := exec.Command("sleep", "1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	cleanup, err := limiter.Apply(context.Background(), cmd.Process.Pid, &config.LimitsConfig{
		CPU:    1.0,
		Memory: 512 * 1024 * 1024,
		Pids:   100,
	})
	require.NoError(t, err)
	require.NotNil(t, cleanup)

	// Call cleanup multiple times concurrently
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cleanup()
		}()
	}
	wg.Wait()

	assert.False(t, limiter.IsOOMKilled(cmd.Process.Pid))
}

func TestProcessWatchdog_MemoryExceededKillsProcess(t *testing.T) {
	cmd := exec.Command("sleep", "10")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())

	pid := cmd.Process.Pid
	var killedPID atomic.Int32

	// Watchdog with mock RSS check returning 200MB against 100MB limit
	watchdog := newProcessWatchdog(pid, 100*1024*1024, 20*time.Millisecond, func(p int) {
		killedPID.Store(int32(p))
	})
	watchdog.rssChecker = func(p int) (uint64, error) {
		return 200 * 1024 * 1024, nil
	}

	watchdog.Start()

	// Wait for process to be killed
	err := cmd.Wait()
	assert.Error(t, err, "process should have been killed by watchdog")
	assert.Equal(t, int32(pid), killedPID.Load())

	// Stop watchdog (should be safe and idempotent)
	watchdog.Stop()
	watchdog.Stop()
}

func TestProcessWatchdog_WithinLimitDoesNotKill(t *testing.T) {
	cmd := exec.Command("sleep", "0.2")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())

	pid := cmd.Process.Pid
	var killedPID atomic.Int32

	// Watchdog with mock RSS check returning 50MB against 100MB limit
	watchdog := newProcessWatchdog(pid, 100*1024*1024, 20*time.Millisecond, func(p int) {
		killedPID.Store(int32(p))
	})
	watchdog.rssChecker = func(p int) (uint64, error) {
		return 50 * 1024 * 1024, nil
	}

	watchdog.Start()

	err := cmd.Wait()
	assert.NoError(t, err, "process should have exited normally without being killed")
	assert.Equal(t, int32(0), killedPID.Load())

	watchdog.Stop()
}

type mockHostLimiter struct {
	BaseLimiter
	appliedPID int
	cleanupRun bool
}

func (m *mockHostLimiter) Apply(ctx context.Context, pid int, limits *config.LimitsConfig) (func(), error) {
	m.appliedPID = pid
	return func() {
		m.cleanupRun = true
	}, nil
}

func TestHostDriver_ExecuteStep_WithLimiterApplication(t *testing.T) {
	mockLimiter := &mockHostLimiter{}
	d := NewDriver().WithLimiter(mockLimiter)

	jobCtx := &driver.JobContext{
		JobID: "job-limiter-test",
		Limits: &config.LimitsConfig{
			CPU:    2.0,
			Memory: 1024 * 1024 * 1024,
		},
	}
	require.NoError(t, d.PrepareJob(context.Background(), jobCtx))

	spec := &driver.StepSpec{
		ID:               "step-test",
		Name:             "test step",
		WorkingDirectory: t.TempDir(),
		Script:           "echo limits-ok",
		Shell:            "sh",
	}

	var stdout, stderr bytes.Buffer
	res, err := d.ExecuteStep(context.Background(), spec, &stdout, &stderr)
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.False(t, res.OOMKilled)
	assert.True(t, mockLimiter.cleanupRun, "limiter cleanup should have run")
	assert.Greater(t, mockLimiter.appliedPID, 0, "limiter should have received valid PID")
}

func TestHostDriver_ExecuteStep_DetectsOOMKilled(t *testing.T) {
	mockLimiter := &mockHostLimiter{}
	d := NewDriver().WithLimiter(mockLimiter)

	jobCtx := &driver.JobContext{
		JobID: "job-oom-test",
		Limits: &config.LimitsConfig{
			Memory: 128 * 1024 * 1024,
		},
	}
	require.NoError(t, d.PrepareJob(context.Background(), jobCtx))

	// Script exits with 137 (SIGKILL)
	spec := &driver.StepSpec{
		ID:               "step-oom",
		Name:             "oom step",
		WorkingDirectory: t.TempDir(),
		Script:           "kill -9 $$",
		Shell:            "sh",
	}

	var stdout, stderr bytes.Buffer
	res, err := d.ExecuteStep(context.Background(), spec, &stdout, &stderr)
	assert.Error(t, err)
	assert.Equal(t, 137, res.ExitCode)
	assert.True(t, res.OOMKilled, "res.OOMKilled should be true when process killed with 137 and memory limits configured")
}

func TestHostDriver_ExecuteStep_LimiterMarkedOOM(t *testing.T) {
	mockLimiter := &mockHostLimiter{}
	d := NewDriver().WithLimiter(mockLimiter)

	jobCtx := &driver.JobContext{
		JobID: "job-oom-mock",
		Limits: &config.LimitsConfig{
			Memory: 128 * 1024 * 1024,
		},
	}
	require.NoError(t, d.PrepareJob(context.Background(), jobCtx))

	spec := &driver.StepSpec{
		ID:               "step-custom-oom",
		Name:             "custom oom",
		WorkingDirectory: t.TempDir(),
		Script:           "exit 1",
		Shell:            "sh",
		OnProcessStart: func(pid int, pgid int) {
			// Mark this pid as OOM killed in the limiter
			mockLimiter.MarkOOMKilled(pid)
		},
	}

	var stdout, stderr bytes.Buffer
	res, err := d.ExecuteStep(context.Background(), spec, &stdout, &stderr)
	assert.Error(t, err)
	assert.True(t, res.OOMKilled, "res.OOMKilled should be true when limiter marked pid as OOM killed")
	assert.Equal(t, 137, res.ExitCode, "exit code should be set to 137 on OOM kill")
}
