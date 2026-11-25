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
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/adrijshikhar/actr/internal/logger"
)

var _ driver.Driver = (*Driver)(nil)
var _ driver.Cloneable = (*Driver)(nil)

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

// Clone creates a shallow clone of the host driver with a shared limiter but isolated job limits state.
func (d *Driver) Clone() driver.Driver {
	d.mu.Lock()
	defer d.mu.Unlock()
	return &Driver{
		limiter: d.limiter,
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
	if step == nil {
		return nil, errors.New("nil step specification")
	}

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

	cleanStepID := strings.ReplaceAll(strings.ReplaceAll(step.ID, "/", "_"), "\\", "_")
	absScriptFile := filepath.Join(scriptDir, fmt.Sprintf("step_%s_%d.sh", cleanStepID, time.Now().UnixNano()))
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
	cmd.WaitDelay = 5 * time.Second
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		return killProcessGroup(cmd)
	}

	// Environment layering
	cmd.Env = sanitizeHostEnviron(os.Environ())
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

	var (
		cleanupLimits func()
		cleanupOnce   sync.Once
	)
	if limiter != nil && limits != nil {
		rawCleanup, applyErr := limiter.Apply(ctx, cmd.Process.Pid, limits)
		if applyErr != nil {
			logger.Warn("failed to apply host process limits", "pid", cmd.Process.Pid, "error", applyErr)
		}
		if rawCleanup != nil {
			cleanupLimits = func() {
				cleanupOnce.Do(rawCleanup)
			}
			defer cleanupLimits()
		}
	}

	if step.OnProcessStart != nil && cmd.Process != nil {
		step.OnProcessStart(cmd.Process.Pid, cmd.Process.Pid)
	}

	err = cmd.Wait()
	if cleanupLimits != nil {
		cleanupLimits()
	}

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				exitCode = 128 + int(ws.Signal())
			}
		} else {
			exitCode = 1
		}
	}

	oomKilled := false
	if limiter != nil && cmd.Process != nil && limiter.IsOOMKilled(cmd.Process.Pid) {
		oomKilled = true
	} else if exitCode == 137 && ctx.Err() == nil && limits != nil && limits.Memory > 0 {
		oomKilled = true
	}

	if limiter != nil && cmd.Process != nil {
		if c, ok := limiter.(interface{ ClearPID(int) }); ok {
			c.ClearPID(cmd.Process.Pid)
		}
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

func sanitizeHostEnviron(env []string) []string {
	sanitized := make([]string, 0, len(env))
	for _, kv := range env {
		idx := strings.IndexByte(kv, '=')
		if idx == -1 {
			continue
		}
		key := kv[:idx]
		if isBlockedHostEnv(key) {
			continue
		}
		sanitized = append(sanitized, kv)
	}
	return sanitized
}

func isBlockedHostEnv(key string) bool {
	upper := strings.ToUpper(key)
	if strings.HasPrefix(upper, "AWS_") ||
		strings.HasPrefix(upper, "LD_") ||
		strings.HasPrefix(upper, "DYLD_") {
		return true
	}
	switch upper {
	case "GITHUB_TOKEN", "GH_TOKEN", "SSH_AUTH_SOCK", "NODE_OPTIONS", "HOMEBREW_GITHUB_API_TOKEN":
		return true
	default:
		return false
	}
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
