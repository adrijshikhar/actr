package host

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostDriverImplementsInterface(t *testing.T) {
	var _ driver.Driver = (*Driver)(nil)
	d := NewDriver()
	if d.Name() != "host" {
		t.Fatalf("expected name 'host', got %q", d.Name())
	}
	if err := d.CleanupJob(context.Background()); err != nil {
		t.Fatalf("expected cleanup nil, got %v", err)
	}
}

func TestHostDriverPrepareJob(t *testing.T) {
	d := NewDriver()

	t.Run("nil job context returns error", func(t *testing.T) {
		err := d.PrepareJob(context.Background(), nil)
		if err == nil {
			t.Fatal("expected error for nil job context, got nil")
		}
	})

	t.Run("creates workspace and temp dirs", func(t *testing.T) {
		tmpRoot := t.TempDir()
		ws := filepath.Join(tmpRoot, "workspace", "sub")
		temp := filepath.Join(tmpRoot, "temp", "sub")

		job := &driver.JobContext{
			JobID:          "test-job",
			WorkflowName:   "CI",
			JobDisplayName: "Build",
			WorkspaceDir:   ws,
			TempDir:        temp,
		}

		err := d.PrepareJob(context.Background(), job)
		if err != nil {
			t.Fatalf("expected PrepareJob to succeed, got %v", err)
		}

		// Verify directories were created
		info, err := filepath.Glob(ws)
		if err != nil || len(info) == 0 {
			t.Fatalf("workspace dir was not created at %s", ws)
		}
		info, err = filepath.Glob(temp)
		if err != nil || len(info) == 0 {
			t.Fatalf("temp dir was not created at %s", temp)
		}
	})

	t.Run("handles cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		job := &driver.JobContext{
			JobID:        "canceled-job",
			WorkspaceDir: filepath.Join(t.TempDir(), "ws"),
		}

		err := d.PrepareJob(ctx, job)
		if err == nil {
			t.Fatal("expected error with cancelled context, got nil")
		}
	})
}

func TestHostDriverCleanupJob(t *testing.T) {
	d := NewDriver()

	t.Run("returns nil on success", func(t *testing.T) {
		if err := d.CleanupJob(context.Background()); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("handles cancelled context gracefully", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := d.CleanupJob(ctx); err != nil {
			t.Fatalf("expected nil even if ctx cancelled, got %v", err)
		}
	})
}

func TestHostDriverExecuteStep(t *testing.T) {
	d := NewDriver()
	tmpDir := t.TempDir()

	spec := &driver.StepSpec{
		ID:               "step-1",
		Name:             "echo test",
		WorkingDirectory: tmpDir,
		Environment:      map[string]string{"FOO": "BAR"},
		Script:           "echo hello $FOO",
		Shell:            "sh",
	}

	var stdout, stderr bytes.Buffer
	res, err := d.ExecuteStep(context.Background(), spec, &stdout, &stderr)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(stdout.String(), "hello BAR") {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

func TestHostDriver_ScriptNotInWorkingDirectory(t *testing.T) {
	d := NewDriver()
	tmpWorkDir := t.TempDir()
	tmpRunnerTemp := t.TempDir()

	// Script lists files in current working directory.
	// It should find 0 files, because the script should NOT be stored in WorkingDirectory.
	spec := &driver.StepSpec{
		ID:               "step-clean-dir",
		Name:             "clean dir test",
		WorkingDirectory: tmpWorkDir,
		Environment: map[string]string{
			"RUNNER_TEMP": tmpRunnerTemp,
		},
		Script: "ls -1 | wc -l",
		Shell:  "sh",
	}

	var stdout, stderr bytes.Buffer
	res, err := d.ExecuteStep(context.Background(), spec, &stdout, &stderr)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d. stderr: %s", res.ExitCode, stderr.String())
	}
	output := strings.TrimSpace(stdout.String())
	if output != "0" {
		t.Errorf("expected working directory to have 0 files during execution, got count %q", output)
	}
}

func TestHostDriverCancellation(t *testing.T) {
	d := NewDriver()
	tmpDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	spec := &driver.StepSpec{
		ID:               "step-sleep",
		Name:             "sleep test",
		WorkingDirectory: tmpDir,
		Script:           "sleep 10",
		Shell:            "sh",
	}

	var stdout, stderr bytes.Buffer
	res, err := d.ExecuteStep(ctx, spec, &stdout, &stderr)
	if err == nil && res.ExitCode == 0 {
		t.Fatal("expected step to be cancelled and fail")
	}
}

func TestHostDriverNonZeroExit(t *testing.T) {
	d := NewDriver()
	tmpDir := t.TempDir()

	spec := &driver.StepSpec{
		ID:               "step-fail",
		Name:             "failure test",
		WorkingDirectory: tmpDir,
		Script:           "exit 42",
		Shell:            "", // Test default shell fallback
	}

	var stdout, stderr bytes.Buffer
	res, err := d.ExecuteStep(context.Background(), spec, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if res.ExitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", res.ExitCode)
	}
}

func TestHostDriverExecuteStep_RelativePath(t *testing.T) {
	d := NewDriver()
	relDir := filepath.Join(t.TempDir(), "nested", "subwork")

	spec := &driver.StepSpec{
		ID:               "step-rel",
		Name:             "rel path test",
		WorkingDirectory: relDir,
		Script:           "echo relative-ok",
		Shell:            "sh",
	}

	var stdout, stderr bytes.Buffer
	res, err := d.ExecuteStep(context.Background(), spec, &stdout, &stderr)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(stdout.String(), "relative-ok") {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

func TestHostDriverOnProcessStart(t *testing.T) {
	d := NewDriver()
	tmpDir := t.TempDir()

	var startedPID, startedPGID int
	called := false
	spec := &driver.StepSpec{
		ID:               "step-pid-test",
		Name:             "pid test",
		WorkingDirectory: tmpDir,
		Script:           "echo running",
		Shell:            "sh",
		OnProcessStart: func(pid int, pgid int) {
			called = true
			startedPID = pid
			startedPGID = pgid
		},
	}

	var stdout, stderr bytes.Buffer
	res, err := d.ExecuteStep(context.Background(), spec, &stdout, &stderr)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	if !called {
		t.Fatal("expected OnProcessStart to be called")
	}
	if startedPID <= 0 || startedPGID <= 0 {
		t.Fatalf("expected valid PID/PGID > 0, got pid=%d, pgid=%d", startedPID, startedPGID)
	}
}

func TestHostDriver_SanitizesSensitiveHostEnvAndGuardsNilStep(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "super-secret-host-key")
	t.Setenv("GITHUB_TOKEN", "ghp_host_secret")

	d := NewDriver()
	_, err := d.ExecuteStep(context.Background(), nil, nil, nil)
	require.Error(t, err)

	var out bytes.Buffer
	res, err := d.ExecuteStep(context.Background(), &driver.StepSpec{
		ID:               "check-env",
		WorkingDirectory: t.TempDir(),
		Script:           `printf "%s|%s" "$AWS_SECRET_ACCESS_KEY" "$GITHUB_TOKEN"`,
	}, &out, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "|", out.String())
}

type fakeOOMOnCleanupLimiter struct {
	HostLimiter
	mu     sync.Mutex
	marked map[int]bool
}

func (f *fakeOOMOnCleanupLimiter) Apply(_ context.Context, pid int, _ *config.LimitsConfig) (func(), error) {
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.marked == nil {
			f.marked = make(map[int]bool)
		}
		f.marked[pid] = true
	}, nil
}

func (f *fakeOOMOnCleanupLimiter) IsOOMKilled(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.marked[pid]
}

func TestHostDriver_DetectsOOMMarkedDuringCleanup(t *testing.T) {
	d := NewDriver().WithLimiter(&fakeOOMOnCleanupLimiter{})
	require.NoError(t, d.PrepareJob(context.Background(), &driver.JobContext{
		JobID:        "oom-job",
		WorkspaceDir: t.TempDir(),
		TempDir:      t.TempDir(),
		Limits:       &config.LimitsConfig{Memory: 1024},
	}))
	res, err := d.ExecuteStep(context.Background(), &driver.StepSpec{
		ID:     "s1",
		Script: "exit 1",
	}, io.Discard, io.Discard)
	require.Error(t, err)
	assert.True(t, res.OOMKilled)
	assert.Equal(t, 137, res.ExitCode)
}

func TestHostDriver_Clone(t *testing.T) {
	d := NewDriver()
	var cloneable driver.Cloneable = d
	require.NotNil(t, cloneable)

	clone := d.Clone()
	require.NotNil(t, clone)
	require.IsType(t, &Driver{}, clone)

	clonedDriver := clone.(*Driver)
	assert.Equal(t, d.limiter, clonedDriver.limiter)

	// Verify isolated limits state
	job1 := &driver.JobContext{
		Limits: &config.LimitsConfig{Memory: 100},
	}
	job2 := &driver.JobContext{
		Limits: &config.LimitsConfig{Memory: 200},
	}

	require.NoError(t, d.PrepareJob(context.Background(), job1))
	require.NoError(t, clonedDriver.PrepareJob(context.Background(), job2))

	assert.Equal(t, int64(100), d.Limits().Memory)
	assert.Equal(t, int64(200), clonedDriver.Limits().Memory)

	require.NoError(t, d.CleanupJob(context.Background()))
	assert.Nil(t, d.Limits())
	assert.NotNil(t, clonedDriver.Limits())
	assert.Equal(t, int64(200), clonedDriver.Limits().Memory)
}

type trackingLimiter struct {
	BaseLimiter
	mu         sync.Mutex
	clearedPID int
}

func (t *trackingLimiter) Apply(_ context.Context, pid int, _ *config.LimitsConfig) (func(), error) {
	return func() {}, nil
}

func (t *trackingLimiter) ClearPID(pid int) {
	t.mu.Lock()
	t.clearedPID = pid
	t.mu.Unlock()
	t.BaseLimiter.ClearPID(pid)
}

func TestHostDriver_ClearsPIDAfterExecution(t *testing.T) {
	limiter := &trackingLimiter{}
	d := NewDriver().WithLimiter(limiter)
	require.NoError(t, d.PrepareJob(context.Background(), &driver.JobContext{
		JobID:  "clear-job",
		Limits: &config.LimitsConfig{Memory: 1024},
	}))
	res, err := d.ExecuteStep(context.Background(), &driver.StepSpec{
		ID:               "s1",
		WorkingDirectory: t.TempDir(),
		Script:           "echo clear-pid-test",
	}, io.Discard, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)

	limiter.mu.Lock()
	cleared := limiter.clearedPID
	limiter.mu.Unlock()
	assert.Greater(t, cleared, 0)
}

func TestSanitizeHostEnviron(t *testing.T) {
	raw := []string{
		"PATH=/usr/bin:/bin",
		"AWS_ACCESS_KEY_ID=secret-key",
		"AWS_SECRET_ACCESS_KEY=secret-access",
		"aws_region=us-east-1",
		"GITHUB_TOKEN=token123",
		"GH_TOKEN=token456",
		"SSH_AUTH_SOCK=/tmp/ssh.sock",
		"NODE_OPTIONS=--max-old-space-size=4096",
		"LD_PRELOAD=/usr/lib/evil.so",
		"LD_LIBRARY_PATH=/usr/lib",
		"DYLD_INSERT_LIBRARIES=/usr/lib/evil.dylib",
		"DYLD_LIBRARY_PATH=/usr/lib",
		"HOMEBREW_GITHUB_API_TOKEN=token789",
		"SAFE_VAR=hello",
		"MALFORMED_NO_EQUALS",
	}

	sanitized := sanitizeHostEnviron(raw)

	joined := strings.Join(sanitized, "\n")
	assert.Contains(t, joined, "PATH=/usr/bin:/bin")
	assert.Contains(t, joined, "SAFE_VAR=hello")
	assert.NotContains(t, joined, "AWS_ACCESS_KEY_ID")
	assert.NotContains(t, joined, "AWS_SECRET_ACCESS_KEY")
	assert.NotContains(t, joined, "aws_region")
	assert.NotContains(t, joined, "GITHUB_TOKEN")
	assert.NotContains(t, joined, "GH_TOKEN")
	assert.NotContains(t, joined, "SSH_AUTH_SOCK")
	assert.NotContains(t, joined, "NODE_OPTIONS")
	assert.NotContains(t, joined, "LD_PRELOAD")
	assert.NotContains(t, joined, "LD_LIBRARY_PATH")
	assert.NotContains(t, joined, "DYLD_INSERT_LIBRARIES")
	assert.NotContains(t, joined, "DYLD_LIBRARY_PATH")
	assert.NotContains(t, joined, "HOMEBREW_GITHUB_API_TOKEN")
	assert.NotContains(t, joined, "MALFORMED_NO_EQUALS")
}

