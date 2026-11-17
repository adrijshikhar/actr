package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver"
)

// Driver executes GitHub Actions steps on bare-metal host processes.
type Driver struct {
	mu            sync.Mutex
	currentLimits *config.LimitsConfig
	limiter       HostLimiter
}

// NewDriver constructs a host Driver using the default platform HostLimiter.
func NewDriver() *Driver {
	return &Driver{
		limiter: NewHostLimiter(),
	}
}

// WithLimiter allows injecting a custom HostLimiter (e.g. for testing or specialized governance).
func (d *Driver) WithLimiter(limiter HostLimiter) *Driver {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.limiter = limiter
	return d
}

// Limits returns the currently configured LimitsConfig for the active job.
func (d *Driver) Limits() *config.LimitsConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.currentLimits
}

// Name returns the driver identifier.
func (d *Driver) Name() string {
	return "host"
}

// PrepareJob provisions the host directories and captures job-level resource limits.
func (d *Driver) PrepareJob(ctx context.Context, job *driver.JobContext) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if job == nil {
		return errors.New("nil job context")
	}

	d.mu.Lock()
	d.currentLimits = job.Limits
	d.mu.Unlock()

	if job.WorkspaceDir != "" {
		if err := os.MkdirAll(job.WorkspaceDir, 0755); err != nil {
			return fmt.Errorf("prepare workspace dir: %w", err)
		}
	}
	if job.TempDir != "" {
		if err := os.MkdirAll(job.TempDir, 0755); err != nil {
			return fmt.Errorf("prepare temp dir: %w", err)
		}
	}
	return nil
}

// ExecuteStep runs a single GitHub Actions step script as a local OS process with resource limits applied.
func (d *Driver) ExecuteStep(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
	workDir := step.WorkingDirectory
	if workDir == "" {
		workDir = "."
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	if err := os.MkdirAll(absWorkDir, 0755); err != nil {
		return nil, fmt.Errorf("create working directory: %w", err)
	}

	scriptDir := absWorkDir
	if rt, ok := step.Environment["RUNNER_TEMP"]; ok && strings.TrimSpace(rt) != "" {
		if absRT, err := filepath.Abs(rt); err == nil {
			_ = os.MkdirAll(absRT, 0o755)
			scriptDir = absRT
		}
	} else if osTemp := os.TempDir(); osTemp != "" {
		scriptDir = osTemp
	}

	absScriptFile := filepath.Join(scriptDir, fmt.Sprintf("step_%s.sh", step.ID))
	if err := os.WriteFile(absScriptFile, []byte(step.Script), 0o755); err != nil {
		return nil, fmt.Errorf("write script file: %w", err)
	}
	defer func() { _ = os.Remove(absScriptFile) }()

	shell := step.Shell
	if shell == "" {
		shell = "sh"
	}

	cmd := exec.CommandContext(ctx, shell, absScriptFile)
	cmd.Dir = absWorkDir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil || cmd.Process.Pid <= 0 {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}

	// Environment layering
	cmd.Env = os.Environ()
	for k, v := range step.Environment {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	err = cmd.Start()
	if err != nil {
		return &driver.StepResult{
			ExitCode: 1,
			Error:    err,
		}, err
	}

	d.mu.Lock()
	limits := d.currentLimits
	limiter := d.limiter
	d.mu.Unlock()

	var cleanupLimits func()
	if limiter != nil && limits != nil {
		cleanupLimits, _ = limiter.Apply(ctx, cmd.Process.Pid, limits)
	}
	if cleanupLimits != nil {
		defer cleanupLimits()
	}

	if step.OnProcessStart != nil && cmd.Process != nil {
		step.OnProcessStart(cmd.Process.Pid, cmd.Process.Pid)
	}

	err = cmd.Wait()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				exitCode = 128 + int(ws.Signal())
			}
		} else {
			exitCode = 1
		}
	}

	oomKilled := false
	if limiter != nil && limiter.IsOOMKilled(cmd.Process.Pid) {
		oomKilled = true
	} else if exitCode == 137 && ctx.Err() == nil && limits != nil && limits.Memory > 0 {
		oomKilled = true
	}

	if oomKilled {
		exitCode = 137
	}

	var stepErr error
	if exitCode != 0 {
		if oomKilled {
			stepErr = fmt.Errorf("command terminated by OOM killer (exit code %d)", exitCode)
		} else if err != nil {
			stepErr = err
		} else {
			stepErr = fmt.Errorf("command failed with exit code %d", exitCode)
		}
	}

	return &driver.StepResult{
		ExitCode:  exitCode,
		OOMKilled: oomKilled,
		Error:     stepErr,
	}, stepErr
}

// CleanupJob terminates any job-level state and clears current limits.
func (d *Driver) CleanupJob(ctx context.Context) error {
	d.mu.Lock()
	d.currentLimits = nil
	d.mu.Unlock()
	return nil
}

// Cleanup is an alias for CleanupJob.
func (d *Driver) Cleanup(ctx context.Context) error {
	return d.CleanupJob(ctx)
}
