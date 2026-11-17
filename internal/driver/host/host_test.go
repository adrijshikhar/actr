package host

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/driver"
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
