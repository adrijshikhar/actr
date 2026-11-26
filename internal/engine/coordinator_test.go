package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/action"
	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/adrijshikhar/actr/internal/driver/host"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/adrijshikhar/actr/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDriver is a test double implementing driver.Driver.
type mockDriver struct {
	mu           sync.Mutex
	stepsRun     []*driver.StepSpec
	executeFunc  func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error)
	cleanupFunc  func(ctx context.Context) error
	cleanupCalls int
	prepareFunc  func(ctx context.Context, job *driver.JobContext) error
	prepareCalls int
	preparedJob  *driver.JobContext
}

func (m *mockDriver) Name() string {
	return "mock"
}

func (m *mockDriver) PrepareJob(ctx context.Context, job *driver.JobContext) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepareCalls++
	m.preparedJob = job
	if m.prepareFunc != nil {
		return m.prepareFunc(ctx, job)
	}
	return nil
}

func (m *mockDriver) ExecuteStep(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
	m.mu.Lock()
	m.stepsRun = append(m.stepsRun, step)
	fn := m.executeFunc
	m.mu.Unlock()

	if fn != nil {
		return fn(ctx, step, stdout, stderr)
	}
	return &driver.StepResult{ExitCode: 0}, nil
}

func (m *mockDriver) CleanupJob(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupCalls++
	if m.cleanupFunc != nil {
		return m.cleanupFunc(ctx)
	}
	return nil
}

func (m *mockDriver) Cleanup(ctx context.Context) error {
	return m.CleanupJob(ctx)
}

func TestCoordinator_CleansWorkspaceOnJobStart(t *testing.T) {
	d := &mockDriver{}
	tmpWorkDir := t.TempDir()
	c := NewCoordinator(d, nil, tmpWorkDir)

	// Pre-create dirty workspace with a leftover file
	jobWorkspace := filepath.Join(tmpWorkDir, "clean-job")
	if err := os.MkdirAll(jobWorkspace, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	leftover := filepath.Join(jobWorkspace, "leftover.txt")
	if err := os.WriteFile(leftover, []byte("dirty"), 0o644); err != nil {
		t.Fatalf("write leftover failed: %v", err)
	}

	job := &protocol.JobSpecification{
		JobID:   "clean-job",
		JobName: "clean-job",
		Steps: []protocol.JobStep{
			{ID: "s1", Name: "step 1", Script: "echo test"},
		},
	}

	_, err := c.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("expected leftover file to be deleted on job start, but it still exists")
	}
}

func TestCoordinator_AbsolutePaths(t *testing.T) {
	d := &mockDriver{}
	// relative workDir
	c := NewCoordinator(d, nil, "relative_work_dir")

	job := &protocol.JobSpecification{
		JobID:   "job-abs",
		JobName: "test-abs",
		Steps: []protocol.JobStep{
			{ID: "s1", Name: "step 1", Script: "echo test"},
		},
	}

	defer func() { _ = os.RemoveAll("relative_work_dir") }()

	_, err := c.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(d.stepsRun) != 1 {
		t.Fatalf("expected 1 step, got %d", len(d.stepsRun))
	}

	step := d.stepsRun[0]
	envPath := step.Environment["GITHUB_ENV"]
	if !filepath.IsAbs(envPath) {
		t.Errorf("expected GITHUB_ENV to be an absolute path, got %q", envPath)
	}
	workspacePath := step.Environment["GITHUB_WORKSPACE"]
	if !filepath.IsAbs(workspacePath) {
		t.Errorf("expected GITHUB_WORKSPACE to be an absolute path, got %q", workspacePath)
	}
	runnerTemp := step.Environment["RUNNER_TEMP"]
	if !filepath.IsAbs(runnerTemp) {
		t.Errorf("expected RUNNER_TEMP to be an absolute path, got %q", runnerTemp)
	}
}

func TestCoordinator_MultiStep_Success(t *testing.T) {
	workDir := t.TempDir()

	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			switch step.ID {
			case "step-1":
				// Step 1 writes to GITHUB_ENV, GITHUB_OUTPUT, and GITHUB_PATH
				envFile := step.Environment["GITHUB_ENV"]
				if err := os.WriteFile(envFile, []byte("EXPORTED_VAR=hello_world\n"), 0o600); err != nil {
					return nil, err
				}
				outFile := step.Environment["GITHUB_OUTPUT"]
				if err := os.WriteFile(outFile, []byte("step1_out=value1\n"), 0o600); err != nil {
					return nil, err
				}
				pathFile := step.Environment["GITHUB_PATH"]
				if err := os.WriteFile(pathFile, []byte("/custom/tool/bin\n"), 0o600); err != nil {
					return nil, err
				}
				return &driver.StepResult{ExitCode: 0}, nil

			case "step-2":
				// Step 2 verifies it received Step 1's env and path updates
				if step.Environment["EXPORTED_VAR"] != "hello_world" {
					return nil, fmt.Errorf("expected EXPORTED_VAR=hello_world, got %q", step.Environment["EXPORTED_VAR"])
				}
				pathVal := step.Environment["PATH"]
				if !strings.HasPrefix(pathVal, "/custom/tool/bin") {
					return nil, fmt.Errorf("expected PATH to start with /custom/tool/bin, got %q", pathVal)
				}
				outFile := step.Environment["GITHUB_OUTPUT"]
				if err := os.WriteFile(outFile, []byte("step2_out=value2\n"), 0o600); err != nil {
					return nil, err
				}
				return &driver.StepResult{ExitCode: 0}, nil

			default:
				return &driver.StepResult{ExitCode: 0}, nil
			}
		},
	}

	coord := NewCoordinator(mock, nil, workDir)

	job := &protocol.JobSpecification{
		JobID:   "job-101",
		JobName: "test-job",
		PlanID:  "plan-001",
		Variables: map[string]string{
			"BASE_VAR": "base_val",
		},
		Steps: []protocol.JobStep{
			{
				ID:   "step-1",
				Name: "First Step",
			},
			{
				ID:   "step-2",
				Name: "Second Step",
			},
		},
	}

	completeReq, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("ExecuteJob failed: %v", err)
	}

	if completeReq.Conclusion != "success" {
		t.Errorf("expected conclusion 'success', got %q", completeReq.Conclusion)
	}
	if completeReq.JobID != "job-101" {
		t.Errorf("expected JobID 'job-101', got %q", completeReq.JobID)
	}
	if completeReq.PlanID != "plan-001" {
		t.Errorf("expected PlanID 'plan-001', got %q", completeReq.PlanID)
	}

	// Verify outputs aggregated from steps
	if completeReq.Outputs["step1_out"] != "value1" {
		t.Errorf("expected step1_out=value1, got %q", completeReq.Outputs["step1_out"])
	}
	if completeReq.Outputs["step2_out"] != "value2" {
		t.Errorf("expected step2_out=value2, got %q", completeReq.Outputs["step2_out"])
	}

	// Verify step results
	if len(completeReq.StepResults) != 2 {
		t.Fatalf("expected 2 step results, got %d", len(completeReq.StepResults))
	}
	for i, sr := range completeReq.StepResults {
		if sr.Conclusion != "success" || sr.ExitCode != 0 {
			t.Errorf("stepResult[%d] = %+v, expected success with exitCode 0", i, sr)
		}
	}

	// Verify cleanup was called on driver
	if mock.cleanupCalls != 1 {
		t.Errorf("expected driver.Cleanup to be called once, got %d", mock.cleanupCalls)
	}
}

func TestCoordinator_StepFailure_HaltsPipeline(t *testing.T) {
	workDir := t.TempDir()

	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			if step.ID == "step-fail" {
				return &driver.StepResult{ExitCode: 42, Error: errors.New("command failed")}, errors.New("exit code 42")
			}
			return &driver.StepResult{ExitCode: 0}, nil
		},
	}

	coord := NewCoordinator(mock, nil, workDir)

	job := &protocol.JobSpecification{
		JobID:   "job-fail",
		JobName: "fail-job",
		PlanID:  "plan-fail",
		Steps: []protocol.JobStep{
			{ID: "step-fail", Name: "Failing Step"},
			{ID: "step-unreached", Name: "Should Not Run"},
		},
	}

	completeReq, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("ExecuteJob returned error: %v", err)
	}

	if completeReq.Conclusion != "failure" {
		t.Errorf("expected conclusion 'failure', got %q", completeReq.Conclusion)
	}
	if len(completeReq.StepResults) != 1 {
		t.Fatalf("expected 1 step result recorded, got %d", len(completeReq.StepResults))
	}
	if completeReq.StepResults[0].ID != "step-fail" {
		t.Errorf("expected failed step ID 'step-fail', got %q", completeReq.StepResults[0].ID)
	}
	if completeReq.StepResults[0].Conclusion != "failure" {
		t.Errorf("expected failed step conclusion 'failure', got %q", completeReq.StepResults[0].Conclusion)
	}
	if completeReq.StepResults[0].ExitCode != 42 {
		t.Errorf("expected failed step exit code 42, got %d", completeReq.StepResults[0].ExitCode)
	}

	// Verify only 1 step was executed
	if len(mock.stepsRun) != 1 {
		t.Errorf("expected exactly 1 step executed, got %d", len(mock.stepsRun))
	}
}

func TestCoordinator_ContextCancellation(t *testing.T) {
	workDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())

	mock := &mockDriver{
		executeFunc: func(c context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			// Cancel context during first step execution
			cancel()
			return &driver.StepResult{ExitCode: 1, Error: context.Canceled}, context.Canceled
		},
	}

	coord := NewCoordinator(mock, nil, workDir)

	job := &protocol.JobSpecification{
		JobID:   "job-cancel",
		JobName: "cancel-job",
		PlanID:  "plan-cancel",
		Steps: []protocol.JobStep{
			{ID: "step-1", Name: "Cancelled Step"},
			{ID: "step-2", Name: "Never Reached"},
		},
	}

	completeReq, err := coord.ExecuteJob(ctx, job, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if completeReq.Conclusion != "cancelled" {
		t.Errorf("expected conclusion 'cancelled', got %q", completeReq.Conclusion)
	}
	if len(mock.stepsRun) != 1 {
		t.Errorf("expected 1 step executed before cancel, got %d", len(mock.stepsRun))
	}
}

func TestCoordinator_StandardEnvironmentAndWorkspace(t *testing.T) {
	workDir := t.TempDir()

	var recordedStep *driver.StepSpec

	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			recordedStep = step
			return &driver.StepResult{ExitCode: 0}, nil
		},
	}

	coord := NewCoordinator(mock, nil, workDir)

	job := &protocol.JobSpecification{
		JobID:   "job-env-test",
		JobName: "my-build-job",
		PlanID:  "plan-env",
		Steps: []protocol.JobStep{
			{
				ID:               "step-env",
				Name:             "Inspect Env",
				WorkingDirectory: "custom-subdir",
				Environment: map[string]string{
					"STEP_LOCAL": "local_val",
				},
			},
		},
	}

	_, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("ExecuteJob failed: %v", err)
	}

	if recordedStep == nil {
		t.Fatal("recordedStep was nil")
	}

	// WorkingDirectory should resolve relative to per-job GITHUB_WORKSPACE
	expectedWorkspace := filepath.Join(workDir, "my-build-job-job-env-test")
	expectedWorkDir := filepath.Join(expectedWorkspace, "custom-subdir")
	if recordedStep.WorkingDirectory != expectedWorkDir {
		t.Errorf("expected WorkingDirectory %q, got %q", expectedWorkDir, recordedStep.WorkingDirectory)
	}

	// Verify working directory was created
	if fi, err := os.Stat(expectedWorkDir); err != nil || !fi.IsDir() {
		t.Errorf("expected step working directory %q to exist as dir", expectedWorkDir)
	}

	// Standard variables
	env := recordedStep.Environment
	if env["GITHUB_WORKSPACE"] != expectedWorkspace {
		t.Errorf("expected GITHUB_WORKSPACE %q, got %q", expectedWorkspace, env["GITHUB_WORKSPACE"])
	}
	if env["RUNNER_WORKSPACE"] != workDir {
		t.Errorf("expected RUNNER_WORKSPACE %q, got %q", workDir, env["RUNNER_WORKSPACE"])
	}
	expectedTemp := filepath.Join(workDir, "_temp", "job-env-test")
	if env["RUNNER_TEMP"] != expectedTemp {
		t.Errorf("expected RUNNER_TEMP %q, got %q", expectedTemp, env["RUNNER_TEMP"])
	}
	if env["RUNNER_NAME"] != "actr" {
		t.Errorf("expected RUNNER_NAME 'actr', got %q", env["RUNNER_NAME"])
	}

	// OS and Arch checks
	expectedOS := "macOS"
	if runtime.GOOS == "linux" {
		expectedOS = "Linux"
	}
	if env["RUNNER_OS"] != expectedOS {
		t.Errorf("expected RUNNER_OS %q, got %q", expectedOS, env["RUNNER_OS"])
	}

	expectedArch := "ARM64"
	if runtime.GOARCH == "amd64" {
		expectedArch = "X64"
	}
	if env["RUNNER_ARCH"] != expectedArch {
		t.Errorf("expected RUNNER_ARCH %q, got %q", expectedArch, env["RUNNER_ARCH"])
	}

	if env["CI"] != "true" || env["GITHUB_ACTIONS"] != "true" {
		t.Errorf("expected CI and GITHUB_ACTIONS to be 'true'")
	}
	if env["GITHUB_JOB"] != "my-build-job" {
		t.Errorf("expected GITHUB_JOB 'my-build-job', got %q", env["GITHUB_JOB"])
	}
	if env["STEP_LOCAL"] != "local_val" {
		t.Errorf("expected STEP_LOCAL 'local_val', got %q", env["STEP_LOCAL"])
	}
	if env["GITHUB_ENV"] == "" || env["GITHUB_OUTPUT"] == "" || env["GITHUB_PATH"] == "" {
		t.Errorf("missing command files in step environment: %+v", env)
	}
}

func TestCoordinator_LeaseRenewalTicker(t *testing.T) {
	var renewCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/renewjob" {
			atomic.AddInt32(&renewCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"lockedUntil": time.Now().Add(60 * time.Second).Format(time.RFC3339),
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	runClient := protocol.NewRunServiceClient(server.Client())
	workDir := t.TempDir()

	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			// Sleep long enough for multiple ticks of the renew interval
			time.Sleep(120 * time.Millisecond)
			return &driver.StepResult{ExitCode: 0}, nil
		},
	}

	coord := NewCoordinator(mock, runClient, workDir).WithRenewInterval(30 * time.Millisecond)

	job := &protocol.JobSpecification{
		JobID:   "renew-job-1",
		JobName: "renew-job",
		PlanID:  "plan-renew-1",
		Steps: []protocol.JobStep{
			{ID: "step-sleep", Name: "Sleeping Step"},
		},
	}

	completeReq, err := coord.ExecuteJob(context.Background(), job, server.URL, "test-token")
	if err != nil {
		t.Fatalf("ExecuteJob failed: %v", err)
	}

	if completeReq.Conclusion != "success" {
		t.Errorf("expected success conclusion, got %q", completeReq.Conclusion)
	}

	calls := atomic.LoadInt32(&renewCalls)
	if calls < 2 {
		t.Errorf("expected at least 2 renew calls during step execution, got %d", calls)
	}
}

func TestCoordinator_InputValidation(t *testing.T) {
	workDir := t.TempDir()
	mock := &mockDriver{}
	coord := NewCoordinator(mock, nil, workDir)

	// nil job
	_, err := coord.ExecuteJob(context.Background(), nil, "", "")
	if err == nil {
		t.Error("expected error for nil job, got nil")
	}

	// nil driver
	nilDriverCoord := NewCoordinator(nil, nil, workDir)
	_, err = nilDriverCoord.ExecuteJob(context.Background(), &protocol.JobSpecification{JobID: "j"}, "", "")
	if err == nil {
		t.Error("expected error for nil driver, got nil")
	}
}

func TestCoordinator_WithRunnerNameAndOutput(t *testing.T) {
	workDir := t.TempDir()
	var capturedEnv map[string]string
	var outBuf, errBuf bytes.Buffer

	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			capturedEnv = step.Environment
			_, _ = stdout.Write([]byte("custom output\n"))
			_, _ = stderr.Write([]byte("custom error\n"))
			return &driver.StepResult{ExitCode: 0}, nil
		},
	}

	coord := NewCoordinator(mock, nil, workDir).
		WithRunnerName("my-bespoke-runner").
		WithStepLogger(false).
		WithOutput(&outBuf, &errBuf)

	job := &protocol.JobSpecification{
		JobID: "job-opts",
		Steps: []protocol.JobStep{
			{ID: "step-1", Name: "Step 1"},
		},
	}

	req, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("ExecuteJob failed: %v", err)
	}
	if req.Conclusion != "success" {
		t.Errorf("expected success, got %q", req.Conclusion)
	}

	if capturedEnv["RUNNER_NAME"] != "my-bespoke-runner" {
		t.Errorf("expected RUNNER_NAME 'my-bespoke-runner', got %q", capturedEnv["RUNNER_NAME"])
	}
	if capturedEnv["GITHUB_JOB"] != "job-opts" {
		t.Errorf("expected GITHUB_JOB 'job-opts', got %q", capturedEnv["GITHUB_JOB"])
	}

	if outBuf.String() != "custom output\n" {
		t.Errorf("expected stdout %q, got %q", "custom output\n", outBuf.String())
	}
	if errBuf.String() != "custom error\n" {
		t.Errorf("expected stderr %q, got %q", "custom error\n", errBuf.String())
	}
}

func TestCoordinator_StepLogger_FormattedExecution(t *testing.T) {
	workDir := t.TempDir()
	var outBuf, errBuf bytes.Buffer

	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			_, _ = stdout.Write([]byte("compiling assets\n"))
			return &driver.StepResult{ExitCode: 0}, nil
		},
	}

	coord := NewCoordinator(mock, nil, workDir).
		WithColor(false).
		WithOutput(&outBuf, &errBuf)

	job := &protocol.JobSpecification{
		JobID:   "deploy-job",
		JobName: "Deploy Assets",
		Steps: []protocol.JobStep{
			{ID: "build-step", Name: "Build Assets"},
		},
	}

	req, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("ExecuteJob failed: %v", err)
	}
	if req.Conclusion != "success" {
		t.Errorf("expected success, got %q", req.Conclusion)
	}

	output := outBuf.String()
	if !strings.Contains(output, "[Deploy Assets] ⭐ Run Build Assets") {
		t.Errorf("expected step start header, got:\n%s", output)
	}
	if !strings.Contains(output, "[Deploy Assets]   | compiling assets") {
		t.Errorf("expected prefixed stdout line, got:\n%s", output)
	}
	if !strings.Contains(output, "[Deploy Assets] ✓ Success Build Assets") {
		t.Errorf("expected step success header, got:\n%s", output)
	}
}

func TestCoordinator_ContextCancelledBeforeExecution(t *testing.T) {
	workDir := t.TempDir()
	mock := &mockDriver{}
	coord := NewCoordinator(mock, nil, workDir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	job := &protocol.JobSpecification{
		JobID: "job-pre-cancelled",
		Steps: []protocol.JobStep{
			{ID: "step-1", Name: "Step 1"},
		},
	}

	req, err := coord.ExecuteJob(ctx, job, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Conclusion != "cancelled" {
		t.Errorf("expected conclusion 'cancelled', got %q", req.Conclusion)
	}
	if len(mock.stepsRun) != 0 {
		t.Errorf("expected 0 steps executed, got %d", len(mock.stepsRun))
	}
}

func TestCoordinator_DefaultWorkDir(t *testing.T) {
	mock := &mockDriver{}
	coord := NewCoordinator(mock, nil, "")
	if coord.workDir != "_work" {
		t.Errorf("expected default workDir '_work', got %q", coord.workDir)
	}
}

func TestCoordinator_EmptyStepID_Fallback(t *testing.T) {
	workDir := t.TempDir()
	var executedSpec *driver.StepSpec

	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			executedSpec = step
			return &driver.StepResult{ExitCode: 0}, nil
		},
	}

	coord := NewCoordinator(mock, nil, workDir)

	job := &protocol.JobSpecification{
		JobID: "job-fallback-id",
		Steps: []protocol.JobStep{
			{ID: "", Name: "Unnamed Step"},
		},
	}

	completeReq, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("ExecuteJob failed: %v", err)
	}

	if executedSpec == nil {
		t.Fatal("expected executedSpec to be non-nil")
	}
	if executedSpec.ID != "step_1" {
		t.Errorf("expected spec.ID 'step_1', got %q", executedSpec.ID)
	}
	if len(completeReq.StepResults) != 1 {
		t.Fatalf("expected 1 step result, got %d", len(completeReq.StepResults))
	}
	if completeReq.StepResults[0].ID != "step_1" {
		t.Errorf("expected stepResult.ID 'step_1', got %q", completeReq.StepResults[0].ID)
	}
}

func TestCoordinator_CleanupOnCancelledContext(t *testing.T) {
	workDir := t.TempDir()
	var cleanupCtxErr error

	mock := &mockDriver{
		cleanupFunc: func(ctx context.Context) error {
			cleanupCtxErr = ctx.Err()
			return nil
		},
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			return &driver.StepResult{ExitCode: 0}, nil
		},
	}

	coord := NewCoordinator(mock, nil, workDir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel context

	job := &protocol.JobSpecification{
		JobID: "job-cancel-cleanup",
		Steps: []protocol.JobStep{
			{ID: "step-1", Name: "Step 1"},
		},
	}

	_, err := coord.ExecuteJob(ctx, job, "", "")
	if err != nil {
		t.Fatalf("ExecuteJob returned error: %v", err)
	}

	if mock.cleanupCalls != 1 {
		t.Errorf("expected cleanupCalls == 1, got %d", mock.cleanupCalls)
	}
	if cleanupCtxErr != nil {
		t.Errorf("expected cleanup context to not be cancelled, got %v", cleanupCtxErr)
	}
}

func testSocketPath(t *testing.T) string {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "mon.sock")
	if len(sockPath) < 100 {
		return sockPath
	}

	dir, err := os.MkdirTemp("/tmp", "act-sock-*")
	if err != nil {
		t.Fatalf("failed to create short temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return filepath.Join(dir, "mon.sock")
}

func TestCoordinator_MonitorEvents(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := testSocketPath(t)
	server := monitor.NewServer(sockPath, "test-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("start monitor server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	coord := NewCoordinator(&mockDriver{}, nil, filepath.Join(tmpDir, "work"))
	coord.SetMonitorServer(server)

	received := make(chan monitor.Event, 10)
	client := monitor.NewClient(sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, _ := client.StreamEvents(ctx)
	go func() {
		for ev := range events {
			received <- ev
		}
	}()

	// Wait briefly for client stream connection to establish
	time.Sleep(50 * time.Millisecond)

	// Simulate notifying step start & log
	coord.NotifyStepStart("job-1", "step-1", "Test Step")
	coord.NotifyStepLog("step-1", "Hello from step")
	coord.NotifyStepFinish("job-1", "step-1", "Test Step", monitor.StatusSuccess)

	select {
	case ev := <-received:
		if ev.StepID != "step-1" {
			t.Fatalf("expected step-1, got %s", ev.StepID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for coordinator monitor event")
	}
}

func TestCoordinator_ExecuteJob_WithMonitor(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := testSocketPath(t)
	server := monitor.NewServer(sockPath, "test-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("start monitor server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			_, _ = stdout.Write([]byte("log line 1\nlog line 2\n"))
			_, _ = stderr.Write([]byte("err line 1\n"))
			// sleep long enough for 500ms ticker to tick at least once
			time.Sleep(550 * time.Millisecond)
			return &driver.StepResult{ExitCode: 0}, nil
		},
	}

	coord := NewCoordinator(mock, nil, filepath.Join(tmpDir, "work")).WithStepLogger(true).WithRepoAlias("cxstatusline")
	coord.SetMonitorServer(server)

	client := monitor.NewClient(sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, _ := client.StreamEvents(ctx)
	var mu sync.Mutex
	received := make([]monitor.Event, 0)
	doneStream := make(chan struct{})
	go func() {
		defer close(doneStream)
		for ev := range events {
			mu.Lock()
			received = append(received, ev)
			mu.Unlock()
		}
	}()

	time.Sleep(50 * time.Millisecond)

	job := &protocol.JobSpecification{
		JobID:   "job-mon-test",
		JobName: "Monitor Job",
		Steps: []protocol.JobStep{
			{ID: "step-1", Name: "Run Build", Script: "echo build"},
		},
	}

	req, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("ExecuteJob failed: %v", err)
	}
	if req.Conclusion != "success" {
		t.Fatalf("expected conclusion success, got %s", req.Conclusion)
	}

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-doneStream

	mu.Lock()
	evs := make([]monitor.Event, len(received))
	copy(evs, received)
	mu.Unlock()

	var hasJobStart, hasJobFinish, hasStepStart, hasStepLog, hasTelemetry, hasStepFinish bool
	var hasRepoAlias, hasStepTelemetry, hasTraceID bool
	for _, ev := range evs {
		switch ev.Type {
		case monitor.EventJobStart:
			if ev.JobID == "job-mon-test" {
				hasJobStart = true
				if ev.RepoAlias == "cxstatusline" {
					hasRepoAlias = true
				}
			}
		case monitor.EventJobFinish:
			if ev.JobID == "job-mon-test" {
				hasJobFinish = true
			}
		case monitor.EventStepStart:
			if ev.JobID == "job-mon-test" {
				hasStepStart = true
			}
		case monitor.EventStepLog:
			if strings.Contains(ev.LogLine, "log line 1") || strings.Contains(ev.LogLine, "err line 1") {
				hasStepLog = true
			}
		case monitor.EventTelemetry:
			hasTelemetry = true
			if ev.Telemetry != nil {
				if ev.Telemetry.ActiveStepName == "Run Build" {
					hasStepTelemetry = true
				}
				if ev.Telemetry.TraceID != "" {
					hasTraceID = true
				}
			}
		case monitor.EventStepFinish:
			if ev.JobID == "job-mon-test" {
				hasStepFinish = true
			}
		}
	}

	if !hasJobStart {
		t.Error("missing EventJobStart")
	}
	if !hasRepoAlias {
		t.Error("missing RepoAlias cxstatusline in EventJobStart")
	}
	if !hasJobFinish {
		t.Error("missing EventJobFinish")
	}
	if !hasStepStart {
		t.Error("missing EventStepStart")
	}
	if !hasStepLog {
		t.Error("missing EventStepLog with output content")
	}
	if !hasTelemetry {
		t.Error("missing EventTelemetry during step execution")
	}
	if !hasStepTelemetry {
		t.Error("missing StepTelemetry with ActiveStepName='Run Build'")
	}
	if !hasTraceID {
		t.Error("missing TraceID in step telemetry")
	}
	if !hasStepFinish {
		t.Error("missing EventStepFinish")
	}

	// Verify server snapshot contains logs
	snap, err := client.GetSnapshot(context.Background())
	if err != nil {
		t.Fatalf("failed to fetch snapshot: %v", err)
	}
	logs, ok := snap.StepLogs["job-mon-test/step-1"]
	if !ok {
		logs, ok = snap.StepLogs["step-1"]
	}
	if !ok || len(logs) == 0 {
		t.Errorf("expected step-1 logs in snapshot, got: %+v", snap.StepLogs)
	}
}

func TestCoordinator_ExecuteJob_WithMonitor_NoStepLogger(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := testSocketPath(t)
	server := monitor.NewServer(sockPath, "test-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("start monitor server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			_, _ = stdout.Write([]byte("raw output line\n"))
			return &driver.StepResult{ExitCode: 0}, nil
		},
	}

	coord := NewCoordinator(mock, nil, filepath.Join(tmpDir, "work")).WithStepLogger(false)
	coord.SetMonitorServer(server)

	job := &protocol.JobSpecification{
		JobID:   "job-no-logger",
		JobName: "No Logger Job",
		Steps: []protocol.JobStep{
			{ID: "step-raw", Name: "Raw Step", Script: "echo raw"},
		},
	}

	req, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("ExecuteJob failed: %v", err)
	}
	if req.Conclusion != "success" {
		t.Fatalf("expected conclusion success, got %s", req.Conclusion)
	}

	client := monitor.NewClient(sockPath)
	snap, err := client.GetSnapshot(context.Background())
	if err != nil {
		t.Fatalf("fetch snapshot failed: %v", err)
	}
	logs := snap.StepLogs["job-no-logger/step-raw"]
	if len(logs) == 0 {
		logs = snap.StepLogs["step-raw"]
	}
	if len(logs) != 1 || logs[0] != "raw output line" {
		t.Errorf("expected ['raw output line'], got %v", logs)
	}
}

func TestCoordinator_StepFailure_WithMonitor(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := testSocketPath(t)
	server := monitor.NewServer(sockPath, "test-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("start monitor server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			_, _ = stderr.Write([]byte("fatal execution error\n"))
			return &driver.StepResult{ExitCode: 1, Error: errors.New("command failed")}, errors.New("failed")
		},
	}

	coord := NewCoordinator(mock, nil, filepath.Join(tmpDir, "work"))
	coord.SetMonitorServer(server)

	client := monitor.NewClient(sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, _ := client.StreamEvents(ctx)
	var mu sync.Mutex
	var finishEvent *monitor.Event
	doneStream := make(chan struct{})
	go func() {
		defer close(doneStream)
		for ev := range events {
			if ev.Type == monitor.EventStepFinish {
				mu.Lock()
				e := ev
				finishEvent = &e
				mu.Unlock()
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)

	job := &protocol.JobSpecification{
		JobID: "job-fail-mon",
		Steps: []protocol.JobStep{
			{ID: "step-err", Name: "Failing Step"},
		},
	}

	req, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("ExecuteJob returned error: %v", err)
	}
	if req.Conclusion != "failure" {
		t.Errorf("expected failure conclusion, got %s", req.Conclusion)
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-doneStream

	mu.Lock()
	defer mu.Unlock()
	if finishEvent == nil {
		t.Fatal("expected EventStepFinish event for failed step")
	}
	if finishEvent.Step == nil || finishEvent.Step.Status != monitor.StatusFailed {
		t.Errorf("expected StatusFailed in EventStepFinish, got %+v", finishEvent.Step)
	}
}

func TestCoordinator_SetMonitorServer_Nil(t *testing.T) {
	coord := NewCoordinator(&mockDriver{}, nil, "")
	// Should not panic on nil server
	coord.SetMonitorServer(nil)
	coord.NotifyStepStart("j", "s", "s")
	coord.NotifyStepLog("s", "l")
	coord.NotifyStepFinish("j", "s", "s", monitor.StatusSuccess)
}

func TestCoordinator_ExecuteJob_ProcessStartPIDAndJobEvents(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := testSocketPath(t)
	server := monitor.NewServer(sockPath, "test-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("start monitor server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			if step.ID == "step-pid" {
				if step.OnProcessStart != nil {
					step.OnProcessStart(12345, 12345)
				}
			}
			// wait briefly for telemetry sampler (ticker runs every 500ms)
			time.Sleep(550 * time.Millisecond)
			return &driver.StepResult{ExitCode: 0}, nil
		},
	}

	coord := NewCoordinator(mock, nil, filepath.Join(tmpDir, "work")).WithStepLogger(true).WithRepoAlias("cxstatusline")
	coord.SetMonitorServer(server)

	client := monitor.NewClient(sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, _ := client.StreamEvents(ctx)
	var mu sync.Mutex
	received := make([]monitor.Event, 0)
	doneStream := make(chan struct{})
	go func() {
		defer close(doneStream)
		for ev := range events {
			mu.Lock()
			received = append(received, ev)
			mu.Unlock()
		}
	}()

	time.Sleep(50 * time.Millisecond)

	job := &protocol.JobSpecification{
		JobID:   "job-pid-verify",
		JobName: "Verify PID & Job Events",
		Steps: []protocol.JobStep{
			{ID: "step-pid", Name: "PID Step", Script: "echo test"},
			{ID: "step-no-pid", Name: "No PID Step", Script: "echo after"},
		},
	}

	req, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("ExecuteJob failed: %v", err)
	}
	if req.Conclusion != "success" {
		t.Fatalf("expected conclusion success, got %s", req.Conclusion)
	}

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-doneStream

	mu.Lock()
	evs := make([]monitor.Event, len(received))
	copy(evs, received)
	mu.Unlock()

	var gotJobStart, gotJobFinish, gotStepStart, gotStepFinish, gotTelemetryWithPID, gotResetTelemetry bool
	for _, ev := range evs {
		switch ev.Type {
		case monitor.EventJobStart:
			if ev.JobID == "job-pid-verify" && ev.Job != nil && ev.Job.Status == monitor.StatusRunning {
				gotJobStart = true
			}
		case monitor.EventJobFinish:
			if ev.JobID == "job-pid-verify" && ev.Job != nil && ev.Job.Status == monitor.StatusSuccess {
				gotJobFinish = true
			}
		case monitor.EventStepStart:
			if ev.JobID == "job-pid-verify" && ev.StepID == "step-pid" {
				gotStepStart = true
			}
		case monitor.EventStepFinish:
			if ev.JobID == "job-pid-verify" && ev.StepID == "step-pid" && ev.Step != nil && ev.Step.Status == monitor.StatusSuccess {
				gotStepFinish = true
			}
		case monitor.EventTelemetry:
			if ev.Telemetry != nil && ev.Telemetry.PID == 12345 {
				gotTelemetryWithPID = true
			}
			if ev.Telemetry != nil && ev.Telemetry.ActiveStepName == "No PID Step" && ev.Telemetry.PID == 0 {
				gotResetTelemetry = true
			}
		}
	}

	if !gotJobStart {
		t.Error("expected EventJobStart with JobID 'job-pid-verify'")
	}
	if !gotJobFinish {
		t.Error("expected EventJobFinish with JobID 'job-pid-verify'")
	}
	if !gotStepStart {
		t.Error("expected EventStepStart with JobID 'job-pid-verify'")
	}
	if !gotStepFinish {
		t.Error("expected EventStepFinish with JobID 'job-pid-verify'")
	}
	if !gotTelemetryWithPID {
		t.Error("expected EventTelemetry with PID 12345")
	}
	if !gotResetTelemetry {
		t.Error("expected EventTelemetry for subsequent step to have PID reset to 0")
	}

	// Verify shared coordinator sampler was not mutated by per-job execution
	if coord.Sampler() != nil {
		sample := coord.Sampler().Sample()
		if sample.PID != 0 {
			t.Errorf("expected shared coordinator sampler PID to remain 0, got %d", sample.PID)
		}
	}
}

func TestCoordinator_GitHubVariablesAndCheckoutSynthesis(t *testing.T) {
	tmpDir := t.TempDir()
	d := &mockDriver{}
	coord := NewCoordinator(d, nil, tmpDir)

	job := &protocol.JobSpecification{
		JobID:   "job-checkout-test",
		JobName: "Checkout Job",
		Variables: map[string]string{
			"system.github.repository": "adrijshikhar/cxstatusline",
			"system.github.sha":        "abcdef123456",
			"system.github.ref":        "refs/heads/main",
			"system.github.event_name": "schedule",
		},
		Steps: []protocol.JobStep{
			{
				ID:   "checkout-step",
				Name: "__actions_checkout",
				Type: "uses",
			},
			{
				ID:     "custom-step",
				Name:   "Run custom script",
				Type:   "script",
				Script: "echo hello",
			},
		},
	}

	res, err := coord.ExecuteJob(context.Background(), job, "", "test-token-123")
	if err != nil {
		t.Fatalf("ExecuteJob failed: %v", err)
	}
	if res.Conclusion != "success" {
		t.Errorf("expected success conclusion, got %q", res.Conclusion)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.stepsRun) != 2 {
		t.Fatalf("expected 2 steps run, got %d", len(d.stepsRun))
	}

	checkoutStep := d.stepsRun[0]
	if !strings.Contains(checkoutStep.Script, "git clone") || !strings.Contains(checkoutStep.Script, "$ACTR_CHECKOUT_REPO") {
		t.Errorf("checkout step script missing parameterized git clone logic: %s", checkoutStep.Script)
	}
	if strings.Contains(checkoutStep.Script, "git checkout -f \"$TARGET_REF\" 2>/dev/null || true") {
		t.Errorf("checkout step script should not contain '|| true' on checkout: %s", checkoutStep.Script)
	}
	if !strings.Contains(checkoutStep.Script, "git fetch --depth=1 \"$CLONE_URL\" \"$TARGET_REF\"") {
		t.Errorf("checkout step script should fetch target ref explicitly: %s", checkoutStep.Script)
	}
	if checkoutStep.Environment["ACTR_CHECKOUT_REPO"] != "adrijshikhar/cxstatusline" {
		t.Errorf("expected ACTR_CHECKOUT_REPO 'adrijshikhar/cxstatusline', got %q", checkoutStep.Environment["ACTR_CHECKOUT_REPO"])
	}
	if checkoutStep.Environment["GITHUB_REPOSITORY"] != "adrijshikhar/cxstatusline" {
		t.Errorf("expected GITHUB_REPOSITORY 'adrijshikhar/cxstatusline', got %q", checkoutStep.Environment["GITHUB_REPOSITORY"])
	}
	if checkoutStep.Environment["GITHUB_SHA"] != "abcdef123456" {
		t.Errorf("expected GITHUB_SHA 'abcdef123456', got %q", checkoutStep.Environment["GITHUB_SHA"])
	}
	if checkoutStep.Environment["GITHUB_EVENT_NAME"] != "schedule" {
		t.Errorf("expected GITHUB_EVENT_NAME 'schedule', got %q", checkoutStep.Environment["GITHUB_EVENT_NAME"])
	}
	if checkoutStep.Environment["GITHUB_TOKEN"] != "test-token-123" {
		t.Errorf("expected GITHUB_TOKEN 'test-token-123', got %q", checkoutStep.Environment["GITHUB_TOKEN"])
	}

	// Verify PATH was enriched
	if !strings.Contains(checkoutStep.Environment["PATH"], "/usr/bin") {
		t.Errorf("PATH does not contain /usr/bin: %s", checkoutStep.Environment["PATH"])
	}
}

func TestCoordinator_CheckoutScriptParameterization_SafeInputs(t *testing.T) {
	step := protocol.JobStep{
		ID:   "checkout",
		Name: "actions/checkout",
		Inputs: map[string]string{
			"repository": "owner/repo; rm -rf /",
			"ref":        "feature/branch$(whoami)",
			"path":       "sub/dir`id`",
		},
	}
	env := make(map[string]string)

	script := generateCheckoutScript(step, env)

	// User inputs must NOT be interpolated into the script text
	if strings.Contains(script, "rm -rf") {
		t.Errorf("dangerous repo input leaked into script string: %s", script)
	}
	if strings.Contains(script, "whoami") {
		t.Errorf("dangerous ref input leaked into script string: %s", script)
	}
	if strings.Contains(script, "sub/dir`id`") {
		t.Errorf("dangerous path input leaked into script string: %s", script)
	}

	// Must be passed via environment variables
	if env["ACTR_CHECKOUT_REPO"] != "owner/repo; rm -rf /" {
		t.Errorf("expected repo in ACTR_CHECKOUT_REPO, got %q", env["ACTR_CHECKOUT_REPO"])
	}
	if env["ACTR_CHECKOUT_REF"] != "feature/branch$(whoami)" {
		t.Errorf("expected ref in ACTR_CHECKOUT_REF, got %q", env["ACTR_CHECKOUT_REF"])
	}
	if env["ACTR_CHECKOUT_PATH"] != "sub/dir`id`" {
		t.Errorf("expected path in ACTR_CHECKOUT_PATH, got %q", env["ACTR_CHECKOUT_PATH"])
	}

	// Verify checkout line does not have || true
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "checkout") && strings.Contains(line, "|| true") {
			t.Errorf("checkout command must not ignore errors with || true: %s", line)
		}
	}

	// Verify fetch uses CLONE_URL to support authenticated private repository checkout
	if !strings.Contains(script, `git fetch --depth=1 "$CLONE_URL"`) {
		t.Errorf("expected script to fetch using $CLONE_URL for private repo auth, got: %s", script)
	}

	// Verify ref stripping and separator usage
	if !strings.Contains(script, `CLEAN_REF="${TARGET_REF#refs/heads/}"`) {
		t.Errorf("expected script to normalize refs/heads/, got: %s", script)
	}
	if !strings.Contains(script, `git checkout -f -- "$TARGET_REF"`) {
		t.Errorf("expected script to use -- separator for checkout, got: %s", script)
	}
}

func TestCoordinator_CommandFileParseError_FailsStep(t *testing.T) {
	tmpDir := t.TempDir()

	// Mock driver that writes a line exceeding 10MB to GITHUB_ENV
	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			envPath := step.Environment["GITHUB_ENV"]
			// Write a line larger than 10MB to trigger scanner.Buffer error (bufio.ErrTooLong)
			f, err := os.OpenFile(envPath, os.O_APPEND|os.O_WRONLY, 0644)
			if err != nil {
				return nil, err
			}
			defer func() { _ = f.Close() }()

			largeLine := strings.Repeat("X", 11*1024*1024) + "\n"
			if _, err := f.WriteString(largeLine); err != nil {
				return nil, err
			}
			return &driver.StepResult{ExitCode: 0}, nil
		},
	}

	coord := NewCoordinator(mock, nil, filepath.Join(tmpDir, "work"))

	job := &protocol.JobSpecification{
		JobID:   "job-parse-err",
		JobName: "Parse Error Job",
		Steps: []protocol.JobStep{
			{
				ID:     "step-1",
				Name:   "Step writing huge env",
				Type:   "script",
				Script: "echo huge",
			},
			{
				ID:     "step-2",
				Name:   "Step that should not run",
				Type:   "script",
				Script: "echo skipped",
			},
		},
	}

	res, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("unexpected coordinator error: %v", err)
	}

	if res.Conclusion != "failure" {
		t.Errorf("expected job conclusion 'failure' on parse error, got %q", res.Conclusion)
	}

	if len(res.StepResults) != 1 {
		t.Fatalf("expected 1 step result before break, got %d", len(res.StepResults))
	}

	if res.StepResults[0].Conclusion != "failure" {
		t.Errorf("expected step conclusion 'failure', got %q", res.StepResults[0].Conclusion)
	}
	if res.StepResults[0].ExitCode != 1 {
		t.Errorf("expected step exit code 1, got %d", res.StepResults[0].ExitCode)
	}
}

func TestEvaluateExpressionAndParseContextData(t *testing.T) {
	rawContextJSON := `{
		"github": {
			"d": [
				{"k": "event_name", "v": "workflow_dispatch"},
				{"k": "repository", "v": "adrijshikhar/cxstatusline"},
				{"k": "server_url", "v": "https://github.com"},
				{"k": "token", "v": "ghs_secret_123"}
			],
			"t": 2
		},
		"runner": {
			"d": [
				{"k": "temp", "v": "/tmp/runner/temp"}
			],
			"t": 2
		},
		"inputs": {
			"d": [
				{"k": "codex_version", "v": "auto"},
				{"k": "platforms", "v": "darwin-arm64,linux-arm64"},
				{"k": "publish", "v": false}
			],
			"t": 2
		},
		"needs": {
			"d": [
				{
					"k": "detect",
					"v": {
						"d": [
							{"k": "result", "v": "success"},
							{
								"k": "outputs",
								"v": {
									"d": [
										{"k": "platforms", "v": "darwin-arm64"},
										{"k": "should_build", "v": "false"}
									],
									"t": 2
								}
							}
						],
						"t": 2
					}
				}
			],
			"t": 2
		}
	}`

	var rawMap map[string]any
	if err := json.Unmarshal([]byte(rawContextJSON), &rawMap); err != nil {
		t.Fatalf("unmarshal rawContextJSON: %v", err)
	}

	parsed := parseContextData(rawMap).(map[string]any)

	tests := []struct {
		expr     string
		expected string
	}{
		{"inputs.platforms || 'darwin-arm64,linux-arm64'", "darwin-arm64,linux-arm64"},
		{"inputs.missing || 'fallback-value'", "fallback-value"},
		{"inputs.codex_version || 'auto'", "auto"},
		{"github.event_name == 'schedule' || inputs.publish == true", "false"},
		{"github.event_name == 'workflow_dispatch' || inputs.publish == true", "true"},
		{"needs.detect.outputs.platforms", "darwin-arm64"},
		{"needs.detect.outputs.should_build", "false"},
		{"needs.detect.result", "success"},
		{"github.token", "ghs_secret_123"},
		{"${{ github.token }}", "ghs_secret_123"},
		{"${{ github.server_url == 'https://github.com' && github.token || '' }}", "ghs_secret_123"},
		{"${{ github.server_url == 'https://other.com' && github.token || '' }}", ""},
		{"github.repository", "adrijshikhar/cxstatusline"},
		{"prefix-${{ github.repository }}-suffix", "prefix-adrijshikhar/cxstatusline-suffix"},
		{"${{ runner.temp }}/prebuilt-detect.log", "/tmp/runner/temp/prebuilt-detect.log"},
		{"format('{0}/prebuilt-detect.log', runner.temp)", "/tmp/runner/temp/prebuilt-detect.log"},
		{"format('logs-detect-{0}', github.token)", "logs-detect-ghs_secret_123"},
		{"'plain literal'", "plain literal"},
	}

	for _, tt := range tests {
		got := evaluateExpression(tt.expr, parsed)
		if got != tt.expected {
			t.Errorf("evaluateExpression(%q) = %q; want %q", tt.expr, got, tt.expected)
		}
	}
}

func TestEvaluateExpression_FormatWithEscapedQuotes(t *testing.T) {
	input := `format('IFS='','' read -ra ADDR <<< "$PLATFORMS"
for p in "${ADDR[@]}"; do
  part="parts/release-$TAG-$p"
  if [ ! -f "$part/manifest.json" ]; then
    echo "::warning::$part/manifest.json missing; falling back to gh run download"
    rm -rf "$part"
    mkdir -p "$part"
    gh run download "{0}" --repo "{1}" -n "release-$TAG-$p" -D "$part"
  fi
done
', github.run_id, github.repository)`

	ctx := map[string]any{
		"github": map[string]any{
			"run_id":     "44657469251",
			"repository": "adrijshikhar/cxstatusline",
		},
	}
	res := evaluateExpression(input, ctx)
	if strings.HasPrefix(res, "format(") {
		t.Fatalf("FAILED: evaluateExpression did not evaluate format(): %q", res)
	}
	if !strings.Contains(res, "IFS=',' read") {
		t.Errorf("expected IFS=',' read with single quotes unescaped, got:\n%s", res)
	}
	if !strings.Contains(res, `gh run download "44657469251" --repo "adrijshikhar/cxstatusline"`) {
		t.Errorf("expected placeholder substitution for run_id and repository, got:\n%s", res)
	}

	// Test single quote escaping in string literals and format
	escapedTests := []struct {
		expr     string
		expected string
	}{
		{"''''", "'"},
		{"'a''b'", "a'b"},
		{"format('hello ''{0}''', 'world')", "hello 'world'"},
		{"format('a'',''b')", "a','b"},
	}
	for _, tt := range escapedTests {
		got := evaluateExpression(tt.expr, nil)
		if got != tt.expected {
			t.Errorf("evaluateExpression(%q) = %q; want %q", tt.expr, got, tt.expected)
		}
	}
}

func TestCoordinator_Lifecycle_PrepareJobAndCleanupJob(t *testing.T) {
	workDir := t.TempDir()
	mock := &mockDriver{}
	coord := NewCoordinator(mock, nil, workDir)

	job := &protocol.JobSpecification{
		JobID:   "job-lifecycle-1",
		JobName: "Build & Test",
		Variables: map[string]string{
			"GITHUB_WORKFLOW": "ci-workflow",
		},
		Steps: []protocol.JobStep{
			{ID: "step-1", Name: "Echo", Type: "script", Script: "echo ok"},
		},
	}

	res, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("unexpected error executing job: %v", err)
	}
	if res.Conclusion != "success" {
		t.Fatalf("expected conclusion success, got %s", res.Conclusion)
	}

	if mock.prepareCalls != 1 {
		t.Errorf("expected 1 prepareCall, got %d", mock.prepareCalls)
	}
	if mock.cleanupCalls != 1 {
		t.Errorf("expected 1 cleanupCall, got %d", mock.cleanupCalls)
	}
	if mock.preparedJob == nil {
		t.Fatal("expected preparedJob to be non-nil")
	}
	if mock.preparedJob.JobID != "job-lifecycle-1" {
		t.Errorf("expected JobID 'job-lifecycle-1', got %q", mock.preparedJob.JobID)
	}
	if mock.preparedJob.WorkflowName != "ci-workflow" {
		t.Errorf("expected WorkflowName 'ci-workflow', got %q", mock.preparedJob.WorkflowName)
	}
	if mock.preparedJob.JobDisplayName != "Build & Test" {
		t.Errorf("expected JobDisplayName 'Build & Test', got %q", mock.preparedJob.JobDisplayName)
	}
	if mock.preparedJob.WorkspaceDir == "" {
		t.Error("expected non-empty WorkspaceDir")
	}
	if mock.preparedJob.TempDir == "" {
		t.Error("expected non-empty TempDir")
	}
	if mock.preparedJob.Environment["GITHUB_WORKSPACE"] != mock.preparedJob.WorkspaceDir {
		t.Errorf("expected GITHUB_WORKSPACE to match WorkspaceDir %q, got %q", mock.preparedJob.WorkspaceDir, mock.preparedJob.Environment["GITHUB_WORKSPACE"])
	}
	if mock.preparedJob.Environment["RUNNER_TEMP"] != mock.preparedJob.TempDir {
		t.Errorf("expected RUNNER_TEMP to match TempDir %q, got %q", mock.preparedJob.TempDir, mock.preparedJob.Environment["RUNNER_TEMP"])
	}
}

func TestCoordinator_Lifecycle_PrepareJobFailure_AbortsJob(t *testing.T) {
	workDir := t.TempDir()
	mock := &mockDriver{
		prepareFunc: func(ctx context.Context, job *driver.JobContext) error {
			return errors.New("disk full")
		},
	}
	coord := NewCoordinator(mock, nil, workDir)

	job := &protocol.JobSpecification{
		JobID: "job-prep-fail",
		Steps: []protocol.JobStep{
			{ID: "step-1", Name: "Echo", Type: "script", Script: "echo ok"},
		},
	}

	_, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err == nil {
		t.Fatal("expected error from ExecuteJob when PrepareJob fails, got nil")
	}
	if !strings.Contains(err.Error(), "prepare job: disk full") {
		t.Errorf("expected error to contain 'prepare job: disk full', got %v", err)
	}
	if mock.prepareCalls != 1 {
		t.Errorf("expected 1 prepareCall, got %d", mock.prepareCalls)
	}
	if mock.cleanupCalls != 1 {
		t.Errorf("expected CleanupJob to be called on failure via defer, got %d", mock.cleanupCalls)
	}
	if len(mock.stepsRun) != 0 {
		t.Errorf("expected 0 steps run when prepare fails, got %d", len(mock.stepsRun))
	}
}

func TestCoordinator_DriverSelection_HostJob(t *testing.T) {
	workDir := t.TempDir()
	hostMock := &mockDriver{}
	dockerMock := &mockDriver{}
	limits := &config.LimitsConfig{
		CPU:    2.0,
		Memory: 1024 * 1024 * 1024,
		Pids:   512,
	}

	coord := NewCoordinator(hostMock, nil, workDir).
		WithDockerDriver(dockerMock).
		WithLimits(limits)

	job := &protocol.JobSpecification{
		JobID:     "host-job-1",
		JobName:   "Host Execution Job",
		Container: nil,
		Steps: []protocol.JobStep{
			{ID: "step-1", Name: "Host Step", Script: "echo 'hello from host'"},
		},
	}

	res, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("unexpected error executing host job: %v", err)
	}
	if res.Conclusion != "success" {
		t.Fatalf("expected conclusion success, got %s", res.Conclusion)
	}

	if hostMock.prepareCalls != 1 {
		t.Errorf("expected host driver PrepareJob to be called once, got %d", hostMock.prepareCalls)
	}
	if hostMock.cleanupCalls != 1 {
		t.Errorf("expected host driver CleanupJob to be called once, got %d", hostMock.cleanupCalls)
	}
	if len(hostMock.stepsRun) != 1 {
		t.Errorf("expected host driver to run 1 step, got %d", len(hostMock.stepsRun))
	}
	if hostMock.preparedJob == nil {
		t.Fatal("expected host driver preparedJob to be non-nil")
	}
	if hostMock.preparedJob.Container != nil {
		t.Errorf("expected preparedJob.Container to be nil, got %+v", hostMock.preparedJob.Container)
	}
	if hostMock.preparedJob.Limits != limits {
		t.Errorf("expected preparedJob.Limits to match configured limits, got %+v", hostMock.preparedJob.Limits)
	}

	if dockerMock.prepareCalls != 0 {
		t.Errorf("expected docker driver PrepareJob to NOT be called, got %d", dockerMock.prepareCalls)
	}
	if dockerMock.cleanupCalls != 0 {
		t.Errorf("expected docker driver CleanupJob to NOT be called, got %d", dockerMock.cleanupCalls)
	}
	if len(dockerMock.stepsRun) != 0 {
		t.Errorf("expected docker driver to run 0 steps, got %d", len(dockerMock.stepsRun))
	}
}

func TestCoordinator_DriverSelection_ContainerJob(t *testing.T) {
	workDir := t.TempDir()
	hostMock := &mockDriver{}
	dockerMock := &mockDriver{}
	limits := &config.LimitsConfig{
		CPU:    1.5,
		Memory: 2 * 1024 * 1024 * 1024,
		Pids:   256,
	}

	coord := NewCoordinator(hostMock, nil, workDir).
		WithDockerDriver(dockerMock).
		WithLimits(limits)

	containerSpec := &protocol.JobContainer{
		Image:   "node:20-bookworm",
		Options: "--cpus 1.5",
	}

	job := &protocol.JobSpecification{
		JobID:     "docker-job-1",
		JobName:   "Container Execution Job",
		Container: containerSpec,
		Steps: []protocol.JobStep{
			{ID: "step-1", Name: "Container Step", Script: "node --version"},
		},
	}

	res, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("unexpected error executing container job: %v", err)
	}
	if res.Conclusion != "success" {
		t.Fatalf("expected conclusion success, got %s", res.Conclusion)
	}

	if dockerMock.prepareCalls != 1 {
		t.Errorf("expected docker driver PrepareJob to be called once, got %d", dockerMock.prepareCalls)
	}
	if dockerMock.cleanupCalls != 1 {
		t.Errorf("expected docker driver CleanupJob to be called once, got %d", dockerMock.cleanupCalls)
	}
	if len(dockerMock.stepsRun) != 1 {
		t.Errorf("expected docker driver to run 1 step, got %d", len(dockerMock.stepsRun))
	}
	if dockerMock.preparedJob == nil {
		t.Fatal("expected docker driver preparedJob to be non-nil")
	}
	if dockerMock.preparedJob.Container != containerSpec {
		t.Errorf("expected preparedJob.Container to match containerSpec, got %+v", dockerMock.preparedJob.Container)
	}
	if dockerMock.preparedJob.Limits != limits {
		t.Errorf("expected preparedJob.Limits to match configured limits, got %+v", dockerMock.preparedJob.Limits)
	}

	if hostMock.prepareCalls != 0 {
		t.Errorf("expected host driver PrepareJob to NOT be called, got %d", hostMock.prepareCalls)
	}
	if hostMock.cleanupCalls != 0 {
		t.Errorf("expected host driver CleanupJob to NOT be called, got %d", hostMock.cleanupCalls)
	}
	if len(hostMock.stepsRun) != 0 {
		t.Errorf("expected host driver to run 0 steps, got %d", len(hostMock.stepsRun))
	}
}

func TestCoordinator_DriverSelection_MissingDockerDriver(t *testing.T) {
	workDir := t.TempDir()
	hostMock := &mockDriver{}

	// docker driver is NOT configured (nil)
	coord := NewCoordinator(hostMock, nil, workDir)

	job := &protocol.JobSpecification{
		JobID:   "docker-missing-driver-job",
		JobName: "Missing Docker Driver Job",
		Container: &protocol.JobContainer{
			Image: "python:3.12-slim",
		},
		Steps: []protocol.JobStep{
			{ID: "step-1", Name: "Python Step", Script: "python3 --version"},
		},
	}

	_, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err == nil {
		t.Fatal("expected error executing container job without docker driver, got nil")
	}

	expectedErr := `job requires container image "python:3.12-slim" but Docker driver is not enabled/available`
	if !strings.Contains(err.Error(), expectedErr) {
		t.Errorf("expected error %q, got %q", expectedErr, err.Error())
	}

	if hostMock.prepareCalls != 0 {
		t.Errorf("expected host driver PrepareJob to NOT be called, got %d", hostMock.prepareCalls)
	}
	if hostMock.cleanupCalls != 0 {
		t.Errorf("expected host driver CleanupJob to NOT be called, got %d", hostMock.cleanupCalls)
	}
	if len(hostMock.stepsRun) != 0 {
		t.Errorf("expected host driver to run 0 steps, got %d", len(hostMock.stepsRun))
	}
}

func TestCoordinator_ResolveDriver(t *testing.T) {
	hostMock := &mockDriver{}
	dockerMock := &mockDriver{}

	coord := NewCoordinator(hostMock, nil, "_work").
		WithDockerDriver(dockerMock)

	// Case 1: Job without container resolves to host driver
	d, err := coord.resolveDriver(&protocol.JobSpecification{JobID: "j1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != hostMock {
		t.Errorf("expected hostMock, got %v", d)
	}

	// Case 2: Job with container resolves to docker driver
	d, err = coord.resolveDriver(&protocol.JobSpecification{
		JobID: "j2",
		Container: &protocol.JobContainer{
			Image: "ubuntu:22.04",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != dockerMock {
		t.Errorf("expected dockerMock, got %v", d)
	}

	// Case 3: Missing docker driver returns error
	coordNoDocker := NewCoordinator(hostMock, nil, "_work")
	_, err = coordNoDocker.resolveDriver(&protocol.JobSpecification{
		JobID: "j3",
		Container: &protocol.JobContainer{
			Image: "golang:1.24",
		},
	})
	if err == nil {
		t.Fatal("expected error for missing docker driver, got nil")
	}
	expectedMsg := `job requires container image "golang:1.24" but Docker driver is not enabled/available`
	if err.Error() != expectedMsg {
		t.Errorf("expected %q, got %q", expectedMsg, err.Error())
	}

	// Case 4: Nil host driver returns error
	coordNoHost := NewCoordinator(nil, nil, "_work")
	_, err = coordNoHost.resolveDriver(&protocol.JobSpecification{JobID: "j4"})
	if err == nil {
		t.Fatal("expected error for nil host driver, got nil")
	}
	if err.Error() != "nil driver" {
		t.Errorf("expected 'nil driver', got %q", err.Error())
	}
}

func TestCoordinator_ExecuteJob_PrepareJobFailure_ReturnsCompleteJobRequestFailure(t *testing.T) {
	workDir := t.TempDir()
	mock := &mockDriver{
		prepareFunc: func(ctx context.Context, job *driver.JobContext) error {
			return errors.New("docker pull rate limit exceeded")
		},
	}
	coord := NewCoordinator(mock, nil, workDir)

	job := &protocol.JobSpecification{
		JobID:   "job-fail-prep",
		PlanID:  "plan-fail-prep",
		JobName: "Fail Prep Job",
		Steps: []protocol.JobStep{
			{ID: "step-1", Name: "Echo", Type: "script", Script: "echo should not run"},
		},
	}

	completeReq, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err == nil {
		t.Fatal("expected error from ExecuteJob when PrepareJob fails, got nil")
	}
	if completeReq == nil {
		t.Fatal("expected non-nil CompleteJobRequest when PrepareJob fails, got nil")
	}
	if completeReq.JobID != "job-fail-prep" {
		t.Errorf("expected JobID 'job-fail-prep', got %q", completeReq.JobID)
	}
	if completeReq.PlanID != "plan-fail-prep" {
		t.Errorf("expected PlanID 'plan-fail-prep', got %q", completeReq.PlanID)
	}
	if completeReq.Conclusion != "failure" {
		t.Errorf("expected Conclusion 'failure', got %q", completeReq.Conclusion)
	}
	if completeReq.Outputs == nil {
		t.Error("expected non-nil Outputs map")
	}
}

func TestCoordinator_ExecuteJob_TimeoutEnforced(t *testing.T) {
	workDir := t.TempDir()
	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			select {
			case <-time.After(1 * time.Second):
				return &driver.StepResult{ExitCode: 0}, nil
			case <-ctx.Done():
				return &driver.StepResult{ExitCode: 1, Error: ctx.Err()}, ctx.Err()
			}
		},
	}

	limits := &config.LimitsConfig{
		Timeout: 50 * time.Millisecond,
	}
	coord := NewCoordinator(mock, nil, workDir).WithLimits(limits)

	job := &protocol.JobSpecification{
		JobID:   "job-timeout",
		PlanID:  "plan-timeout",
		JobName: "Timeout Job",
		Steps: []protocol.JobStep{
			{ID: "step-long", Name: "Long step", Script: "sleep 10"},
		},
	}

	completeReq, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("unexpected error from ExecuteJob: %v", err)
	}
	if completeReq == nil {
		t.Fatal("expected non-nil CompleteJobRequest")
	}
	if completeReq.Conclusion != "cancelled" {
		t.Errorf("expected Conclusion 'cancelled' on timeout, got %q", completeReq.Conclusion)
	}
}

type mockActionResolver struct {
	resolveFn func(ctx context.Context, ref *protocol.StepReference) (string, error)
}

func (m *mockActionResolver) Resolve(ctx context.Context, ref *protocol.StepReference) (string, error) {
	if m.resolveFn != nil {
		return m.resolveFn(ctx, ref)
	}
	return "", errors.New("not implemented")
}

func TestCoordinator_ExecuteJob_NodeActionStep(t *testing.T) {
	detector := action.NewHostNodeDetector("")
	if _, err := detector.Detect(context.Background()); err != nil {
		t.Skipf("Node.js runtime not found on host, skipping: %v", err)
	}

	workDir := t.TempDir()
	actionDir := filepath.Join(workDir, "mock-action")
	if err := os.MkdirAll(actionDir, 0755); err != nil {
		t.Fatalf("failed to create action dir: %v", err)
	}

	actionYAML := `
name: "Mock Setup"
runs:
  using: "node20"
  main: "index.js"
inputs:
  target-greeting:
    default: "greetings"
`
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYAML), 0644); err != nil {
		t.Fatalf("failed to write action.yml: %v", err)
	}

	actionJS := `
const fs = require('fs');
fs.appendFileSync(process.env.GITHUB_OUTPUT, 'out_val=hello_' + process.env['INPUT_TARGET-GREETING'] + '\n');
fs.appendFileSync(process.env.GITHUB_ENV, 'NODE_ACTION_RAN=true\n');
console.log("Mock action executed with input: " + process.env['INPUT_TARGET_GREETING']);
`
	if err := os.WriteFile(filepath.Join(actionDir, "index.js"), []byte(actionJS), 0644); err != nil {
		t.Fatalf("failed to write index.js: %v", err)
	}

	mock := &mockDriver{}
	coord := NewCoordinator(mock, nil, workDir).WithActionResolver(&mockActionResolver{
		resolveFn: func(ctx context.Context, ref *protocol.StepReference) (string, error) {
			if ref != nil && ref.Name == "actions/mock-setup" {
				return actionDir, nil
			}
			return "", errors.New("action not found")
		},
	})

	job := &protocol.JobSpecification{
		JobID:   "job-node-action",
		PlanID:  "plan-node-action",
		JobName: "Node Action Job",
		Steps: []protocol.JobStep{
			{
				ID:   "step-node",
				Name: "Set up mock",
				Type: "uses",
				Reference: &protocol.StepReference{
					Name:    "actions/mock-setup",
					Version: "v1",
				},
				Inputs: map[string]string{
					"target-greeting": "universe",
				},
			},
			{
				ID:     "step-verify",
				Name:   "Verify step",
				Type:   "script",
				Script: "echo verified",
			},
		},
	}

	completeReq, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("unexpected error from ExecuteJob: %v", err)
	}
	if completeReq.Conclusion != "success" {
		t.Errorf("expected Conclusion 'success', got %q", completeReq.Conclusion)
	}
	if completeReq.Outputs["out_val"] != "hello_universe" {
		t.Errorf("expected output out_val 'hello_universe', got %q", completeReq.Outputs["out_val"])
	}
	if len(completeReq.StepResults) != 2 {
		t.Fatalf("expected 2 step results, got %d", len(completeReq.StepResults))
	}
	if completeReq.StepResults[0].Conclusion != "success" || completeReq.StepResults[0].ExitCode != 0 {
		t.Errorf("expected step 0 success, got %+v", completeReq.StepResults[0])
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.stepsRun) != 1 {
		t.Fatalf("expected 1 driver step run, got %d", len(mock.stepsRun))
	}
	if mock.stepsRun[0].Environment["NODE_ACTION_RAN"] != "true" {
		t.Errorf("expected environment variable NODE_ACTION_RAN propagated to subsequent step, got %q", mock.stepsRun[0].Environment["NODE_ACTION_RAN"])
	}
}

func TestCoordinator_ExecuteJob_NodeActionStep_ResolutionFailure(t *testing.T) {
	workDir := t.TempDir()
	mock := &mockDriver{}
	coord := NewCoordinator(mock, nil, workDir).WithActionResolver(&mockActionResolver{
		resolveFn: func(ctx context.Context, ref *protocol.StepReference) (string, error) {
			return "", errors.New("download failed 404")
		},
	})

	job := &protocol.JobSpecification{
		JobID:   "job-action-fail",
		PlanID:  "plan-action-fail",
		JobName: "Fail Job",
		Steps: []protocol.JobStep{
			{
				ID:   "step-1",
				Name: "Missing Action",
				Type: "uses",
				Reference: &protocol.StepReference{
					Name:    "actions/missing",
					Version: "v1",
				},
			},
		},
	}

	completeReq, err := coord.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("unexpected ExecuteJob error: %v", err)
	}
	if completeReq.Conclusion != "failure" {
		t.Errorf("expected Conclusion 'failure', got %q", completeReq.Conclusion)
	}
	if len(completeReq.StepResults) != 1 || completeReq.StepResults[0].Conclusion != "failure" {
		t.Errorf("expected step conclusion 'failure'")
	}
}

func TestDynamicToolchainIngestion(t *testing.T) {
	t.Run("IngestPathFile_CleanAndPrepend", func(t *testing.T) {
		tmpDir := t.TempDir()
		pathFile := filepath.Join(tmpDir, "path.txt")

		// 1. Initial path with dirty paths (extra slashes, dot-dot)
		rawContent := "/opt/tools//bin/\n/custom/../other/bin\n\n/single/path\n"
		if err := os.WriteFile(pathFile, []byte(rawContent), 0o600); err != nil {
			t.Fatalf("failed to write path file: %v", err)
		}

		initialPath := filepath.Clean("/usr/bin") + string(filepath.ListSeparator) + filepath.Clean("/bin")
		updatedPath, err := IngestPathFile(pathFile, initialPath)
		if err != nil {
			t.Fatalf("IngestPathFile failed: %v", err)
		}

		expectedCleaned1 := filepath.Clean("/opt/tools/bin")
		expectedCleaned2 := filepath.Clean("/other/bin")
		expectedCleaned3 := filepath.Clean("/single/path")
		expectedPrefix := expectedCleaned1 + string(filepath.ListSeparator) +
			expectedCleaned2 + string(filepath.ListSeparator) +
			expectedCleaned3

		if !strings.HasPrefix(updatedPath, expectedPrefix) {
			t.Errorf("expected updatedPath to start with %q, got %q", expectedPrefix, updatedPath)
		}
		if !strings.HasSuffix(updatedPath, initialPath) {
			t.Errorf("expected updatedPath to end with %q, got %q", initialPath, updatedPath)
		}

		// 2. Verify truncation: file must be empty after ingestion
		fi, err := os.Stat(pathFile)
		if err != nil {
			t.Fatalf("failed to stat path file: %v", err)
		}
		if fi.Size() != 0 {
			t.Errorf("expected path file to be truncated to 0 bytes, got %d bytes", fi.Size())
		}

		// 3. Second ingestion on truncated file should not alter the path
		secondPath, err := IngestPathFile(pathFile, updatedPath)
		if err != nil {
			t.Fatalf("second IngestPathFile failed: %v", err)
		}
		if secondPath != updatedPath {
			t.Errorf("expected unchanged path after re-ingesting truncated file, got %q vs %q", secondPath, updatedPath)
		}

		// 4. Ingestion into empty currentPath
		singleFile := filepath.Join(tmpDir, "single.txt")
		if err := os.WriteFile(singleFile, []byte("/only/path\n"), 0o600); err != nil {
			t.Fatalf("failed to write single path file: %v", err)
		}
		emptyBase, err := IngestPathFile(singleFile, "")
		if err != nil {
			t.Fatalf("IngestPathFile with empty base failed: %v", err)
		}
		expectedSingle := filepath.Clean("/only/path")
		if emptyBase != expectedSingle {
			t.Errorf("expected %q, got %q", expectedSingle, emptyBase)
		}

		// 5. Non-existent file should be a no-op
		nonExistentPath, err := IngestPathFile(filepath.Join(tmpDir, "does-not-exist.txt"), "/existing/path")
		if err != nil {
			t.Fatalf("IngestPathFile on non-existent file returned error: %v", err)
		}
		if nonExistentPath != "/existing/path" {
			t.Errorf("expected /existing/path, got %q", nonExistentPath)
		}
	})

	t.Run("IngestEnvFile_KeyValAndMultiline", func(t *testing.T) {
		tmpDir := t.TempDir()
		envFile := filepath.Join(tmpDir, "env.txt")

		content := strings.Join([]string{
			"SIMPLE_KEY=simple_val",
			"MULTILINE_VAR<<EOF",
			"line one",
			"line two with special chars: = < >",
			"EOF",
			"ANOTHER_KEY=another_val",
			"DELIM_WITH_SPACES<<MY_DELIMITER",
			"first line",
			"second line",
			"MY_DELIMITER",
			"",
		}, "\n")

		if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
			t.Fatalf("failed to write env file: %v", err)
		}

		envMap := map[string]string{
			"EXISTING": "original",
		}

		if err := IngestEnvFile(envFile, envMap); err != nil {
			t.Fatalf("IngestEnvFile failed: %v", err)
		}

		if envMap["EXISTING"] != "original" {
			t.Errorf("expected EXISTING to remain 'original', got %q", envMap["EXISTING"])
		}
		if envMap["SIMPLE_KEY"] != "simple_val" {
			t.Errorf("expected SIMPLE_KEY='simple_val', got %q", envMap["SIMPLE_KEY"])
		}
		expectedMulti := "line one\nline two with special chars: = < >"
		if envMap["MULTILINE_VAR"] != expectedMulti {
			t.Errorf("expected MULTILINE_VAR=%q, got %q", expectedMulti, envMap["MULTILINE_VAR"])
		}
		if envMap["ANOTHER_KEY"] != "another_val" {
			t.Errorf("expected ANOTHER_KEY='another_val', got %q", envMap["ANOTHER_KEY"])
		}
		expectedDelim := "first line\nsecond line"
		if envMap["DELIM_WITH_SPACES"] != expectedDelim {
			t.Errorf("expected DELIM_WITH_SPACES=%q, got %q", expectedDelim, envMap["DELIM_WITH_SPACES"])
		}

		// Verify truncation
		fi, err := os.Stat(envFile)
		if err != nil {
			t.Fatalf("failed to stat env file: %v", err)
		}
		if fi.Size() != 0 {
			t.Errorf("expected env file to be truncated to 0 bytes, got %d", fi.Size())
		}

		// Re-ingestion on truncated file should be a no-op
		if err := IngestEnvFile(envFile, envMap); err != nil {
			t.Fatalf("second IngestEnvFile failed: %v", err)
		}

		// Non-existent file should be a no-op
		if err := IngestEnvFile(filepath.Join(tmpDir, "does-not-exist.txt"), envMap); err != nil {
			t.Fatalf("IngestEnvFile on non-existent file failed: %v", err)
		}
	})

	t.Run("CoordinatorJob_SubsequentStepsReceiveUpdatedEnvAndPath", func(t *testing.T) {
		workDir := t.TempDir()

		step1PathToAdd := filepath.Clean("/custom/toolchain/go/bin")
		step2PathToAdd := filepath.Clean("/custom/toolchain/rust/bin")

		mock := &mockDriver{
			executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
				switch step.ID {
				case "setup-step":
					// Step 1 appends dirty path and multiline env
					pathFile := step.Environment["GITHUB_PATH"]
					if err := os.WriteFile(pathFile, []byte("/custom/toolchain/go//bin/\n"), 0o600); err != nil {
						return nil, err
					}

					envFile := step.Environment["GITHUB_ENV"]
					envContent := "GO_VERSION=1.23.0\nRELEASE_NOTES<<DELIM\nFirst line\nSecond line\nDELIM\n"
					if err := os.WriteFile(envFile, []byte(envContent), 0o600); err != nil {
						return nil, err
					}
					return &driver.StepResult{ExitCode: 0}, nil

				case "build-step":
					// Step 2 verifies it received step 1's path and env
					gotPath := step.Environment["PATH"]
					if !strings.HasPrefix(gotPath, step1PathToAdd) {
						return nil, fmt.Errorf("build-step expected PATH to start with %s, got %s", step1PathToAdd, gotPath)
					}
					if step.Environment["GO_VERSION"] != "1.23.0" {
						return nil, fmt.Errorf("build-step expected GO_VERSION=1.23.0, got %q", step.Environment["GO_VERSION"])
					}
					if step.Environment["RELEASE_NOTES"] != "First line\nSecond line" {
						return nil, fmt.Errorf("build-step expected RELEASE_NOTES='First line\\nSecond line', got %q", step.Environment["RELEASE_NOTES"])
					}

					// Step 2 appends another path and env var
					pathFile := step.Environment["GITHUB_PATH"]
					if err := os.WriteFile(pathFile, []byte(step2PathToAdd+"\n"), 0o600); err != nil {
						return nil, err
					}
					envFile := step.Environment["GITHUB_ENV"]
					if err := os.WriteFile(envFile, []byte("CARGO_HOME=/custom/cargo\n"), 0o600); err != nil {
						return nil, err
					}
					return &driver.StepResult{ExitCode: 0}, nil

				case "test-step":
					// Step 3 verifies step 2's additions are prepended before step 1's
					gotPath := step.Environment["PATH"]
					expectedPrefix := step2PathToAdd + string(filepath.ListSeparator) + step1PathToAdd
					if !strings.HasPrefix(gotPath, expectedPrefix) {
						return nil, fmt.Errorf("test-step expected PATH to start with %s, got %s", expectedPrefix, gotPath)
					}
					if step.Environment["GO_VERSION"] != "1.23.0" {
						return nil, fmt.Errorf("test-step expected GO_VERSION=1.23.0, got %q", step.Environment["GO_VERSION"])
					}
					if step.Environment["CARGO_HOME"] != "/custom/cargo" {
						return nil, fmt.Errorf("test-step expected CARGO_HOME=/custom/cargo, got %q", step.Environment["CARGO_HOME"])
					}
					return &driver.StepResult{ExitCode: 0}, nil

				default:
					return &driver.StepResult{ExitCode: 0}, nil
				}
			},
		}

		coord := NewCoordinator(mock, nil, workDir)

		job := &protocol.JobSpecification{
			JobID:   "job-toolchain-test",
			JobName: "toolchain-job",
			Steps: []protocol.JobStep{
				{
					ID:   "setup-step",
					Name: "Setup Toolchain",
				},
				{
					ID:   "build-step",
					Name: "Build Application",
				},
				{
					ID:   "test-step",
					Name: "Test Application",
				},
			},
		}

		req, err := coord.ExecuteJob(context.Background(), job, "", "")
		if err != nil {
			t.Fatalf("ExecuteJob failed: %v", err)
		}
		if req.Conclusion != "success" {
			t.Fatalf("expected job conclusion 'success', got %q", req.Conclusion)
		}
		if len(req.StepResults) != 3 {
			t.Fatalf("expected 3 step results, got %d", len(req.StepResults))
		}
		for i, sr := range req.StepResults {
			if sr.Conclusion != "success" {
				t.Errorf("step %d (%s) failed: %+v", i, sr.ID, sr)
			}
		}
	})
}

func TestIngestStateFile(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.txt")
	content := "KEY1=VAL1\nKEY2<<EOF\nline1\nline2\nEOF\nKEY3=VAL3\n"
	if err := os.WriteFile(stateFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	state, err := IngestStateFile(stateFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state["KEY1"] != "VAL1" {
		t.Errorf("expected KEY1=VAL1, got %q", state["KEY1"])
	}
	if state["KEY2"] != "line1\nline2" {
		t.Errorf("expected KEY2 to be 'line1\\nline2', got %q", state["KEY2"])
	}
	if state["KEY3"] != "VAL3" {
		t.Errorf("expected KEY3=VAL3, got %q", state["KEY3"])
	}

	info, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("failed to stat state file: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("expected state file to be truncated to 0 bytes, got %d", info.Size())
	}
}

func TestPostStepExecution(t *testing.T) {
	detector := action.NewHostNodeDetector("")
	if _, err := detector.Detect(context.Background()); err != nil {
		t.Skipf("Node.js runtime not found on host, skipping: %v", err)
	}

	t.Run("LIFO post-step hook execution order", func(t *testing.T) {
		workDir := t.TempDir()
		orderLog := filepath.Join(workDir, "execution_order.log")

		// Create Action 1 with post script
		action1Dir := filepath.Join(workDir, "action1")
		if err := os.MkdirAll(action1Dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(action1Dir, "action.yml"), []byte(`
name: "Action One"
runs:
  using: "node20"
  main: "main.js"
  post: "post.js"
`), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(action1Dir, "main.js"), []byte(`
const fs = require('fs');
fs.appendFileSync(process.env.ORDER_LOG, 'main-1\n');
`), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(action1Dir, "post.js"), []byte(`
const fs = require('fs');
fs.appendFileSync(process.env.ORDER_LOG, 'post-1\n');
`), 0644); err != nil {
			t.Fatal(err)
		}

		// Create Action 2 with post script
		action2Dir := filepath.Join(workDir, "action2")
		if err := os.MkdirAll(action2Dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(action2Dir, "action.yml"), []byte(`
name: "Action Two"
runs:
  using: "node20"
  main: "main.js"
  post: "post.js"
`), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(action2Dir, "main.js"), []byte(`
const fs = require('fs');
fs.appendFileSync(process.env.ORDER_LOG, 'main-2\n');
`), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(action2Dir, "post.js"), []byte(`
const fs = require('fs');
fs.appendFileSync(process.env.ORDER_LOG, 'post-2\n');
`), 0644); err != nil {
			t.Fatal(err)
		}

		mock := &mockDriver{}
		coord := NewCoordinator(mock, nil, workDir).WithActionResolver(&mockActionResolver{
			resolveFn: func(ctx context.Context, ref *protocol.StepReference) (string, error) {
				if ref.Name == "actions/action1" {
					return action1Dir, nil
				}
				if ref.Name == "actions/action2" {
					return action2Dir, nil
				}
				return "", errors.New("not found")
			},
		})

		job := &protocol.JobSpecification{
			JobID:   "job-lifo",
			JobName: "LIFO Job",
			Variables: map[string]string{
				"ORDER_LOG": orderLog,
			},
			Steps: []protocol.JobStep{
				{
					ID:   "step-1",
					Name: "First Action",
					Type: "uses",
					Reference: &protocol.StepReference{
						Name:    "actions/action1",
						Version: "v1",
					},
				},
				{
					ID:   "step-2",
					Name: "Second Action",
					Type: "uses",
					Reference: &protocol.StepReference{
						Name:    "actions/action2",
						Version: "v1",
					},
				},
			},
		}

		completeReq, err := coord.ExecuteJob(context.Background(), job, "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if completeReq.Conclusion != "success" {
			t.Fatalf("expected success, got %q", completeReq.Conclusion)
		}

		data, err := os.ReadFile(orderLog)
		if err != nil {
			t.Fatalf("failed to read order log: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		expected := []string{"main-1", "main-2", "post-2", "post-1"}
		if len(lines) != len(expected) {
			t.Fatalf("expected %d entries %v, got %d %v", len(expected), expected, len(lines), lines)
		}
		for i, exp := range expected {
			if lines[i] != exp {
				t.Errorf("at index %d: expected %q, got %q", i, exp, lines[i])
			}
		}
	})

	t.Run("Execution under cancelled job context", func(t *testing.T) {
		workDir := t.TempDir()
		postLog := filepath.Join(workDir, "cancelled_post.log")

		actionDir := filepath.Join(workDir, "action-cancel")
		if err := os.MkdirAll(actionDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(`
name: "Cancel Action"
runs:
  using: "node20"
  main: "main.js"
  post: "post.js"
`), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(actionDir, "main.js"), []byte(`
console.log("main ran successfully");
`), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(actionDir, "post.js"), []byte(`
const fs = require('fs');
fs.writeFileSync(process.env.POST_LOG, 'post-ran-despite-cancel\n');
`), 0644); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())

		mock := &mockDriver{
			executeFunc: func(stepCtx context.Context, spec *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
				// Cancel context when step 2 starts
				cancel()
				<-stepCtx.Done()
				return &driver.StepResult{ExitCode: 1, Error: stepCtx.Err()}, stepCtx.Err()
			},
		}

		coord := NewCoordinator(mock, nil, workDir).WithActionResolver(&mockActionResolver{
			resolveFn: func(ctx context.Context, ref *protocol.StepReference) (string, error) {
				if ref.Name == "actions/cancel" {
					return actionDir, nil
				}
				return "", errors.New("not found")
			},
		})

		job := &protocol.JobSpecification{
			JobID:   "job-cancel",
			JobName: "Cancel Job",
			Variables: map[string]string{
				"POST_LOG": postLog,
			},
			Steps: []protocol.JobStep{
				{
					ID:   "step-1",
					Name: "Action step",
					Type: "uses",
					Reference: &protocol.StepReference{
						Name:    "actions/cancel",
						Version: "v1",
					},
				},
				{
					ID:     "step-2",
					Name:   "Long step to be cancelled",
					Type:   "script",
					Script: "sleep 10",
				},
			},
		}

		completeReq, err := coord.ExecuteJob(ctx, job, "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if completeReq.Conclusion != "cancelled" {
			t.Fatalf("expected conclusion 'cancelled', got %q", completeReq.Conclusion)
		}

		// Post hook MUST have run despite cancellation
		data, err := os.ReadFile(postLog)
		if err != nil {
			t.Fatalf("post hook did not run, failed to read log: %v", err)
		}
		if strings.TrimSpace(string(data)) != "post-ran-despite-cancel" {
			t.Fatalf("expected 'post-ran-despite-cancel', got %q", string(data))
		}
	})

	t.Run("STATE injection into post step environment", func(t *testing.T) {
		workDir := t.TempDir()
		stateOutputLog := filepath.Join(workDir, "state_output.log")

		actionDir := filepath.Join(workDir, "action-state")
		if err := os.MkdirAll(actionDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(`
name: "State Action"
runs:
  using: "node20"
  main: "main.js"
  post: "post.js"
`), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(actionDir, "main.js"), []byte(`
const fs = require('fs');
// Single line state
fs.appendFileSync(process.env.GITHUB_STATE, 'auth_token=secret_token_123\n');
fs.appendFileSync(process.env.GITHUB_STATE, 'my key=spaced value\n');
// Multiline delimiter state
fs.appendFileSync(process.env.GITHUB_STATE, 'cert<<EOF\nline1\nline2\nEOF\n');
`), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(actionDir, "post.js"), []byte(`
const fs = require('fs');
const token = process.env['STATE_AUTH_TOKEN'] || '';
const spaced = process.env['STATE_MY_KEY'] || '';
const cert = process.env['STATE_CERT'] || '';
const out = JSON.stringify({
  token: token,
  spaced: spaced,
  cert: cert
});
fs.writeFileSync(process.env.STATE_OUT_LOG, out);
`), 0644); err != nil {
			t.Fatal(err)
		}

		mock := &mockDriver{}
		coord := NewCoordinator(mock, nil, workDir).WithActionResolver(&mockActionResolver{
			resolveFn: func(ctx context.Context, ref *protocol.StepReference) (string, error) {
				if ref.Name == "actions/state" {
					return actionDir, nil
				}
				return "", errors.New("not found")
			},
		})

		job := &protocol.JobSpecification{
			JobID:   "job-state",
			JobName: "State Job",
			Variables: map[string]string{
				"STATE_OUT_LOG": stateOutputLog,
			},
			Steps: []protocol.JobStep{
				{
					ID:   "step-state",
					Name: "State step",
					Type: "uses",
					Reference: &protocol.StepReference{
						Name:    "actions/state",
						Version: "v1",
					},
				},
			},
		}

		completeReq, err := coord.ExecuteJob(context.Background(), job, "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if completeReq.Conclusion != "success" {
			t.Fatalf("expected success, got %q", completeReq.Conclusion)
		}

		data, err := os.ReadFile(stateOutputLog)
		if err != nil {
			t.Fatalf("failed to read state output log: %v", err)
		}
		var parsed map[string]string
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to parse json output: %v, raw: %s", err, string(data))
		}
		if parsed["token"] != "secret_token_123" {
			t.Errorf("expected token 'secret_token_123', got %q", parsed["token"])
		}
		if parsed["spaced"] != "spaced value" {
			t.Errorf("expected spaced 'spaced value', got %q", parsed["spaced"])
		}
		if parsed["cert"] != "line1\nline2" {
			t.Errorf("expected cert 'line1\\nline2', got %q", parsed["cert"])
		}
	})
}

func TestIngestEnvFile_BlocksForbiddenKeys(t *testing.T) {
	tempDir := t.TempDir()
	envFile := filepath.Join(tempDir, "github_env.txt")

	envContent := `SAFE_VAR=hello_safe
NODE_OPTIONS=--require /tmp/evil.js
LD_PRELOAD=/tmp/evil.so
DYLD_INSERT_LIBRARIES=/tmp/evil.dylib
PATH=/tmp/evil_bin:/usr/bin
GITHUB_ENV=/tmp/evil_env
RUNNER_TEMP=/tmp/evil_temp
ANOTHER_SAFE=world_123
`
	if err := os.WriteFile(envFile, []byte(envContent), 0644); err != nil {
		t.Fatalf("failed to write test env file: %v", err)
	}

	cumulative := make(map[string]string)
	if err := IngestEnvFile(envFile, cumulative); err != nil {
		t.Fatalf("IngestEnvFile failed: %v", err)
	}

	// Safe variables must be preserved
	if cumulative["SAFE_VAR"] != "hello_safe" {
		t.Errorf("expected SAFE_VAR 'hello_safe', got %q", cumulative["SAFE_VAR"])
	}
	if cumulative["ANOTHER_SAFE"] != "world_123" {
		t.Errorf("expected ANOTHER_SAFE 'world_123', got %q", cumulative["ANOTHER_SAFE"])
	}

	// Forbidden dangerous injection keys must be blocked
	forbidden := []string{
		"NODE_OPTIONS",
		"LD_PRELOAD",
		"DYLD_INSERT_LIBRARIES",
		"PATH",
		"GITHUB_ENV",
		"RUNNER_TEMP",
	}
	for _, key := range forbidden {
		if val, exists := cumulative[key]; exists {
			t.Errorf("forbidden key %q was injected with value %q", key, val)
		}
	}
}

func TestResolveActionScriptPath_PathTraversalRejection(t *testing.T) {
	tempDir := t.TempDir()
	actionDir := filepath.Join(tempDir, "actions", "checkout")
	if err := os.MkdirAll(actionDir, 0755); err != nil {
		t.Fatalf("failed to create action dir: %v", err)
	}

	// Valid in-directory script
	validRel := "dist/index.js"
	cleanPath, err := resolveActionScriptPath(actionDir, validRel)
	if err != nil {
		t.Fatalf("unexpected error for valid path: %v", err)
	}
	expected := filepath.Join(actionDir, "dist", "index.js")
	if cleanPath != expected {
		t.Errorf("expected %q, got %q", expected, cleanPath)
	}

	// Traversal attacks escaping actionDir
	escapes := []string{
		"../../evil.js",
		"../../../tmp/evil.js",
		"dist/../../../../etc/passwd",
	}
	for _, esc := range escapes {
		_, err := resolveActionScriptPath(actionDir, esc)
		if err == nil {
			t.Errorf("expected error for escaping path %q, got nil", esc)
		}
	}

	// Empty path
	if _, err := resolveActionScriptPath(actionDir, ""); err == nil {
		t.Error("expected error for empty script path, got nil")
	}
}

func TestShouldExecuteStep(t *testing.T) {
	ctx := map[string]any{
		"runner": map[string]any{
			"os": "macOS",
		},
		"matrix": map[string]any{
			"platform": "darwin-arm64",
		},
	}

	// 1. Success condition when not failed
	if !shouldExecuteStep("success()", ctx, false, false) {
		t.Error("expected success() to be true when not failed")
	}
	if shouldExecuteStep("success()", ctx, true, false) {
		t.Error("expected success() to be false when failed")
	}

	// 2. Always condition
	if !shouldExecuteStep("always()", ctx, false, false) {
		t.Error("expected always() to be true when not failed")
	}
	if !shouldExecuteStep("always()", ctx, true, false) {
		t.Error("expected always() to be true when failed")
	}

	// 3. Failure condition
	if shouldExecuteStep("failure()", ctx, false, false) {
		t.Error("expected failure() to be false when not failed")
	}
	if !shouldExecuteStep("failure()", ctx, true, false) {
		t.Error("expected failure() to be true when failed")
	}

	// 4. Expression evaluating to false: runner.os == 'macOS' && startsWith(matrix.platform, 'linux-')
	dockerCond := "runner.os == 'macOS' && startsWith(matrix.platform, 'linux-')"
	if shouldExecuteStep(dockerCond, ctx, false, false) {
		t.Error("expected dockerCond to be false for darwin platform")
	}

	// 5. Expression evaluating to true for native macOS
	nativeCond := "${{ runner.os != 'macOS' || !startsWith(matrix.platform, 'linux-') }}"
	if !shouldExecuteStep(nativeCond, ctx, false, false) {
		t.Error("expected nativeCond to be true for darwin platform")
	}

	// 6. Test with linux matrix platform
	linuxCtx := map[string]any{
		"runner": map[string]any{
			"os": "macOS",
		},
		"matrix": map[string]any{
			"platform": "linux-arm64",
		},
	}
	if !shouldExecuteStep(dockerCond, linuxCtx, false, false) {
		t.Error("expected dockerCond to be true for linux-arm64")
	}
	if shouldExecuteStep(nativeCond, linuxCtx, false, false) {
		t.Error("expected nativeCond to be false for linux-arm64")
	}
}

func TestEvaluateExpression_ContextPathVsLiteral(t *testing.T) {
	ctx := map[string]any{
		"github": map[string]any{
			"event_name": "push",
		},
		"env": map[string]any{
			"FOO": "bar",
		},
	}

	// String literals containing dot should NOT be treated as context paths and stripped
	for _, lit := range []string{"14.0", "1.4.0", "v1.2.3"} {
		if got := evaluateExpression(lit, ctx); got != lit {
			t.Errorf("evaluateExpression(%q) = %q, want %q", lit, got, lit)
		}
	}

	// Valid context paths should resolve
	if got := evaluateExpression("github.event_name", ctx); got != "push" {
		t.Errorf("evaluateExpression(github.event_name) = %q, want 'push'", got)
	}
	if got := evaluateExpression("env.FOO", ctx); got != "bar" {
		t.Errorf("evaluateExpression(env.FOO) = %q, want 'bar'", got)
	}

	// Unknown context paths should resolve to empty string
	if got := evaluateExpression("github.unknown_var", ctx); got != "" {
		t.Errorf("evaluateExpression(github.unknown_var) = %q, want empty", got)
	}
	if got := evaluateExpression("needs.detect.outputs.nonexistent", ctx); got != "" {
		t.Errorf("evaluateExpression(needs.detect.outputs.nonexistent) = %q, want empty", got)
	}
}

func TestCoordinator_GitHubContextAndEnvSync(t *testing.T) {
	d := &mockDriver{}
	tmpWorkDir := t.TempDir()
	c := NewCoordinator(d, nil, tmpWorkDir)

	job := &protocol.JobSpecification{
		JobID:   "test-env-sync",
		JobName: "test-env-sync",
		Variables: map[string]string{
			"system.github.runid":      "12345678",
			"system.github.runnumber":  "42",
			"system.github.runattempt": "1",
			"system.github.actor":      "test-actor",
			"system.github.repository": "org/repo",
		},
		Steps: []protocol.JobStep{
			{ID: "s1", Name: "step 1", Script: "echo test"},
		},
	}

	_, err := c.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(d.stepsRun) != 1 {
		t.Fatalf("expected 1 step run, got %d", len(d.stepsRun))
	}

	env := d.stepsRun[0].Environment
	expected := map[string]string{
		"GITHUB_RUN_ID":      "12345678",
		"GITHUB_RUN_NUMBER":  "42",
		"GITHUB_RUN_ATTEMPT": "1",
		"GITHUB_ACTOR":       "test-actor",
		"GITHUB_REPOSITORY":  "org/repo",
	}

	for k, expectedVal := range expected {
		if got := env[k]; got != expectedVal {
			t.Errorf("expected env %q = %q, got %q", k, expectedVal, got)
		}
	}
}

func TestEvaluateExpression_CaseInsensitiveFunctions(t *testing.T) {
	ctx := map[string]any{
		"github": map[string]any{
			"ref": "refs/HEADS/main",
		},
		"matrix": map[string]any{
			"file": "REPORT.PDF",
			"msg":  "Error: Failure in Job",
		},
	}

	if got := evaluateExpression("startsWith(github.ref, 'refs/heads/')", ctx); got != "true" {
		t.Errorf("expected startsWith to be 'true', got %q", got)
	}
	if got := evaluateExpression("endsWith(matrix.file, '.pdf')", ctx); got != "true" {
		t.Errorf("expected endsWith to be 'true', got %q", got)
	}
	if got := evaluateExpression("contains(matrix.msg, 'failure')", ctx); got != "true" {
		t.Errorf("expected contains to be 'true', got %q", got)
	}
}

func TestShouldExecuteStep_BooleanLiterals(t *testing.T) {
	ctx := map[string]any{}
	if shouldExecuteStep("false", ctx, false, false) {
		t.Error("expected shouldExecuteStep('false') to be false")
	}
	if shouldExecuteStep("False", ctx, false, false) {
		t.Error("expected shouldExecuteStep('False') to be false")
	}
	if !shouldExecuteStep("true", ctx, false, false) {
		t.Error("expected shouldExecuteStep('true') to be true")
	}
	if shouldExecuteStep("true", ctx, true, false) {
		t.Error("expected shouldExecuteStep('true') with jobFailed to be false")
	}
}

func TestCoordinator_ExecuteStep_InjectsTraceparent(t *testing.T) {
	d := &mockDriver{}
	tmpWorkDir := t.TempDir()
	c := NewCoordinator(d, nil, tmpWorkDir)

	job := &protocol.JobSpecification{
		JobID:   "trace-job",
		JobName: "trace-job",
		Steps: []protocol.JobStep{
			{ID: "step-1", Name: "step 1", Script: "echo test"},
		},
	}

	res, err := c.ExecuteJob(context.Background(), job, "", "")
	if err != nil {
		t.Fatalf("unexpected error executing job: %v", err)
	}
	if res.Conclusion != "success" {
		t.Fatalf("expected conclusion 'success', got %q", res.Conclusion)
	}

	if len(d.stepsRun) == 0 {
		t.Fatal("expected stepsRun to not be empty")
	}
	firstStep := d.stepsRun[0]
	if firstStep.Environment == nil {
		t.Fatal("expected firstStep.Environment to not be nil")
	}
	traceparent, ok := firstStep.Environment["TRACEPARENT"]
	if !ok || !strings.HasPrefix(traceparent, "00-") {
		t.Fatalf("expected TRACEPARENT starting with '00-', got %q", traceparent)
	}
}

func TestCoordinator_VaultFulfillment_TrustedContext(t *testing.T) {
	mockRunner := &mockDriver{}
	mockV := vault.NewMockProvider(map[string]string{
		"SECRET_TOKEN": "runner-vault-secret-value",
		"OTHER_SECRET": "another-secret-val",
	})

	coord := NewCoordinator(mockRunner, nil, "").WithVault(mockV)

	// Direct push -> trusted
	job := &protocol.JobSpecification{
		EventName: "push",
		Steps: []protocol.JobStep{
			{
				ID: "step-1",
				Environment: map[string]string{
					"SECRET_TOKEN": "", // empty in GitHub CI -> should fulfill
					"OTHER_SECRET": "",
				},
			},
		},
	}

	jobMasker := NewSecretMasker()
	fulfilledEnv := coord.prepareStepEnvironment(context.Background(), job, job.Steps[0], jobMasker)
	assert.Equal(t, "runner-vault-secret-value", fulfilledEnv["SECRET_TOKEN"])
	assert.Equal(t, "another-secret-val", fulfilledEnv["OTHER_SECRET"])

	// Verify secret is registered in jobMasker
	masked := jobMasker.Mask("Authorization: Bearer runner-vault-secret-value")
	assert.Equal(t, "Authorization: Bearer ***", masked)
}

func TestCoordinator_VaultFulfillment_ForkGuardBlocksUntrusted(t *testing.T) {
	mockRunner := &mockDriver{}
	mockV := vault.NewMockProvider(map[string]string{
		"SECRET_TOKEN": "runner-vault-secret-value",
	})

	var logCapture bytes.Buffer
	coord := NewCoordinator(mockRunner, nil, "").WithVault(mockV).WithOutput(&logCapture, &logCapture)

	// Untrusted fork pull request
	job := &protocol.JobSpecification{
		ID:        "job-untrusted",
		EventName: "pull_request",
		Variables: map[string]string{
			"GITHUB_HEAD_REF_REPO": "attacker/actr",
			"GITHUB_REPOSITORY":    "adrijshikhar/actr",
		},
		Steps: []protocol.JobStep{
			{
				ID: "step-1",
				Environment: map[string]string{
					"SECRET_TOKEN": "",
				},
			},
		},
	}

	fulfilledEnv := coord.prepareStepEnvironment(context.Background(), job, job.Steps[0])
	// Must stay empty!
	assert.Equal(t, "", fulfilledEnv["SECRET_TOKEN"])

	// Must log security notice
	logs := logCapture.String()
	assert.Contains(t, logs, "Vault secret resolution blocked for untrusted fork PR: SECRET_TOKEN")
}

func TestCoordinator_VaultFulfillment_EdgeCases(t *testing.T) {
	mockRunner := &mockDriver{}
	mockV := vault.NewMockProvider(map[string]string{
		"SECRET_TOKEN": "runner-vault-secret-value",
		"EMPTY_SECRET": "",
	})

	t.Run("nil vault leaves empty env unchanged", func(t *testing.T) {
		coord := NewCoordinator(mockRunner, nil, "")
		job := &protocol.JobSpecification{
			EventName: "push",
			Steps: []protocol.JobStep{
				{
					ID: "step-1",
					Environment: map[string]string{
						"SECRET_TOKEN": "",
					},
				},
			},
		}
		env := coord.prepareStepEnvironment(context.Background(), job, job.Steps[0])
		assert.Equal(t, "", env["SECRET_TOKEN"])
	})

	t.Run("non-empty env var is not overwritten", func(t *testing.T) {
		coord := NewCoordinator(mockRunner, nil, "").WithVault(mockV)
		job := &protocol.JobSpecification{
			EventName: "push",
			Steps: []protocol.JobStep{
				{
					ID: "step-1",
					Environment: map[string]string{
						"SECRET_TOKEN": "already-provided-by-user",
					},
				},
			},
		}
		env := coord.prepareStepEnvironment(context.Background(), job, job.Steps[0])
		assert.Equal(t, "already-provided-by-user", env["SECRET_TOKEN"])
	})

	t.Run("local execution is trusted even with fork variables", func(t *testing.T) {
		coord := NewCoordinator(mockRunner, nil, "").WithVault(mockV)
		job := &protocol.JobSpecification{
			IsLocalExecution: true,
			EventName:        "pull_request",
			Variables: map[string]string{
				"GITHUB_HEAD_REF_REPO": "attacker/actr",
				"GITHUB_REPOSITORY":    "adrijshikhar/actr",
			},
			Steps: []protocol.JobStep{
				{
					ID: "step-1",
					Environment: map[string]string{
						"SECRET_TOKEN": "",
					},
				},
			},
		}
		env := coord.prepareStepEnvironment(context.Background(), job, job.Steps[0])
		assert.Equal(t, "runner-vault-secret-value", env["SECRET_TOKEN"])
	})

	t.Run("same repo pull request is trusted", func(t *testing.T) {
		coord := NewCoordinator(mockRunner, nil, "").WithVault(mockV)
		job := &protocol.JobSpecification{
			EventName: "pull_request",
			Variables: map[string]string{
				"GITHUB_HEAD_REF_REPO": "adrijshikhar/actr",
				"GITHUB_REPOSITORY":    "adrijshikhar/actr",
			},
			Steps: []protocol.JobStep{
				{
					ID: "step-1",
					Environment: map[string]string{
						"SECRET_TOKEN": "",
					},
				},
			},
		}
		env := coord.prepareStepEnvironment(context.Background(), job, job.Steps[0])
		assert.Equal(t, "runner-vault-secret-value", env["SECRET_TOKEN"])
	})

	t.Run("workflow_dispatch is trusted", func(t *testing.T) {
		coord := NewCoordinator(mockRunner, nil, "").WithVault(mockV)
		job := &protocol.JobSpecification{
			EventName: "workflow_dispatch",
			Steps: []protocol.JobStep{
				{
					ID: "step-1",
					Environment: map[string]string{
						"SECRET_TOKEN": "",
					},
				},
			},
		}
		env := coord.prepareStepEnvironment(context.Background(), job, job.Steps[0])
		assert.Equal(t, "runner-vault-secret-value", env["SECRET_TOKEN"])
	})

	t.Run("empty secret in vault remains empty", func(t *testing.T) {
		coord := NewCoordinator(mockRunner, nil, "").WithVault(mockV)
		job := &protocol.JobSpecification{
			EventName: "push",
			Steps: []protocol.JobStep{
				{
					ID: "step-1",
					Environment: map[string]string{
						"EMPTY_SECRET": "",
					},
				},
			},
		}
		env := coord.prepareStepEnvironment(context.Background(), job, job.Steps[0])
		assert.Equal(t, "", env["EMPTY_SECRET"])
	})
}

func TestIsTrustedContext(t *testing.T) {
	tests := []struct {
		name     string
		job      *protocol.JobSpecification
		expected bool
	}{
		{
			name:     "nil job",
			job:      nil,
			expected: false,
		},
		{
			name: "local execution",
			job: &protocol.JobSpecification{
				IsLocalExecution: true,
			},
			expected: true,
		},
		{
			name: "local execution overrides untrusted fork",
			job: &protocol.JobSpecification{
				IsLocalExecution: true,
				EventName:        "pull_request",
				Variables: map[string]string{
					"GITHUB_HEAD_REF_REPO": "fork/repo",
					"GITHUB_REPOSITORY":    "origin/repo",
				},
			},
			expected: true,
		},
		{
			name: "direct push event",
			job: &protocol.JobSpecification{
				EventName: "push",
			},
			expected: true,
		},
		{
			name: "workflow dispatch event",
			job: &protocol.JobSpecification{
				EventName: "workflow_dispatch",
			},
			expected: true,
		},
		{
			name: "pull request same repo",
			job: &protocol.JobSpecification{
				EventName: "pull_request",
				Variables: map[string]string{
					"GITHUB_HEAD_REF_REPO": "myorg/myrepo",
					"GITHUB_REPOSITORY":    "myorg/myrepo",
				},
			},
			expected: true,
		},
		{
			name: "pull request same repo case insensitive",
			job: &protocol.JobSpecification{
				EventName: "pull_request",
				Variables: map[string]string{
					"GITHUB_HEAD_REF_REPO": "MyOrg/MyRepo",
					"GITHUB_REPOSITORY":    "myorg/myrepo",
				},
			},
			expected: true,
		},
		{
			name: "pull request fork repo untrusted",
			job: &protocol.JobSpecification{
				EventName: "pull_request",
				Variables: map[string]string{
					"GITHUB_HEAD_REF_REPO": "attacker/myrepo",
					"GITHUB_REPOSITORY":    "myorg/myrepo",
				},
			},
			expected: false,
		},
		{
			name: "pull request nil variables",
			job: &protocol.JobSpecification{
				EventName: "pull_request",
			},
			expected: false,
		},
		{
			name: "pull request empty head repo",
			job: &protocol.JobSpecification{
				EventName: "pull_request",
				Variables: map[string]string{
					"GITHUB_HEAD_REF_REPO": "",
					"GITHUB_REPOSITORY":    "myorg/myrepo",
				},
			},
			expected: false,
		},
		{
			name: "untrusted event type issues",
			job: &protocol.JobSpecification{
				EventName: "issues",
			},
			expected: false,
		},
		{
			name: "fallback to GITHUB_EVENT_NAME variable push",
			job: &protocol.JobSpecification{
				Variables: map[string]string{
					"GITHUB_EVENT_NAME": "push",
				},
			},
			expected: true,
		},
		{
			name: "fallback to GITHUB_EVENT_NAME variable fork PR",
			job: &protocol.JobSpecification{
				Variables: map[string]string{
					"GITHUB_EVENT_NAME":    "pull_request",
					"GITHUB_HEAD_REF_REPO": "fork/repo",
					"GITHUB_REPOSITORY":    "origin/repo",
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, isTrustedContext(tc.job))
		})
	}
}

func TestExecuteJob_SanitizesJobNameTraversalAndProtectsSiblingDirs(t *testing.T) {
	baseDir := t.TempDir()
	sentinel := filepath.Join(baseDir, "do-not-delete")
	require.NoError(t, os.MkdirAll(sentinel, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sentinel, "keep.txt"), []byte("ok"), 0o644))

	coord := NewCoordinator(host.NewDriver(), nil, baseDir).WithTracer(nil)
	job := &protocol.JobSpecification{
		JobID:   "job-123",
		JobName: "../do-not-delete",
		Steps:   []protocol.JobStep{{ID: "s1", Name: "ok", Script: "pwd"}},
	}
	res, err := coord.ExecuteJob(context.Background(), job, "", "")
	require.NoError(t, err)
	assert.Equal(t, "success", res.Conclusion)
	assert.FileExists(t, filepath.Join(sentinel, "keep.txt"))
}

func TestExecuteJob_MasksStdoutEvenWithoutStepLoggerAndMonitor(t *testing.T) {
	var buf bytes.Buffer
	coord := NewCoordinator(host.NewDriver(), nil, t.TempDir()).
		WithTracer(nil).
		WithOutput(&buf, &buf)
	coord.stepLoggerEnabled = false

	job := &protocol.JobSpecification{
		JobID:    "mask-job",
		JobToken: "super-secret-job-token",
		Steps:    []protocol.JobStep{{ID: "s1", Name: "echo", Script: `echo "tok=super-secret-job-token"`}},
	}
	_, err := coord.ExecuteJob(context.Background(), job, "", "super-secret-job-token")
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "super-secret-job-token")
	assert.Contains(t, buf.String(), "***")
}

func TestIsCheckoutStep_OnlyBuiltInCheckout(t *testing.T) {
	assert.False(t, isCheckoutStep(protocol.JobStep{Name: "checkout", Reference: &protocol.StepReference{Name: "org/custom-checkout-helper", Version: "v1"}}))
	assert.True(t, isCheckoutStep(protocol.JobStep{Name: "Checkout repo", Reference: &protocol.StepReference{Name: "actions/checkout", Version: "v4"}}))
}

func TestExecuteJob_ForbiddenEnvKeys_DroppedFromJobAndStep(t *testing.T) {
	d := &mockDriver{}
	coord := NewCoordinator(d, nil, t.TempDir()).WithTracer(nil)
	job := &protocol.JobSpecification{
		JobID: "forbidden-env-job",
		Environment: map[string]string{
			"NODE_OPTIONS":    "--inspect=0.0.0.0",
			"node_options":    "--inspect=evil",
			"CUSTOM_SAFE_VAR": "safe_val",
		},
		Steps: []protocol.JobStep{
			{
				ID:   "s1",
				Name: "check-env",
				Environment: map[string]string{
					"LD_PRELOAD":    "/malicious.so",
					"STEP_SAFE_VAR": "step_val",
				},
				Script: "echo ok",
			},
		},
	}
	res, err := coord.ExecuteJob(context.Background(), job, "", "")
	require.NoError(t, err)
	assert.Equal(t, "success", res.Conclusion)
	require.NotEmpty(t, d.stepsRun)
	stepSpec := d.stepsRun[0]
	assert.NotContains(t, stepSpec.Environment, "NODE_OPTIONS")
	assert.NotContains(t, stepSpec.Environment, "node_options")
	assert.NotContains(t, stepSpec.Environment, "LD_PRELOAD")
	assert.Equal(t, "safe_val", stepSpec.Environment["CUSTOM_SAFE_VAR"])
	assert.Equal(t, "step_val", stepSpec.Environment["STEP_SAFE_VAR"])
}

func TestExecuteJob_RejectsStepWorkingDirectoryTraversal(t *testing.T) {
	coord := NewCoordinator(&mockDriver{}, nil, t.TempDir()).WithTracer(nil)
	job := &protocol.JobSpecification{
		JobID:   "traversal-job",
		JobName: "traversal-job",
		Steps: []protocol.JobStep{
			{ID: "s1", Name: "escape", WorkingDirectory: "../../etc", Script: "pwd"},
		},
	}
	res, err := coord.ExecuteJob(context.Background(), job, "", "")
	require.Error(t, err)
	assert.Equal(t, "failure", res.Conclusion)
	assert.Contains(t, err.Error(), "outside job workspace")
}

func TestExecuteJob_ContainerRelativeWorkingDirectoryTraversal(t *testing.T) {
	dockerMock := &mockDriver{}
	coord := NewCoordinator(&mockDriver{}, nil, t.TempDir()).
		WithDockerDriver(dockerMock).
		WithTracer(nil)

	// Relative traversal must still be rejected for container jobs
	jobTraversal := &protocol.JobSpecification{
		JobID:     "container-traversal-job",
		JobName:   "container-traversal-job",
		Container: &protocol.JobContainer{Image: "alpine:latest"},
		Steps: []protocol.JobStep{
			{ID: "s1", Name: "escape", WorkingDirectory: "../../etc", Script: "pwd"},
		},
	}
	res, err := coord.ExecuteJob(context.Background(), jobTraversal, "", "")
	require.Error(t, err)
	assert.Equal(t, "failure", res.Conclusion)
	assert.Contains(t, err.Error(), "outside job workspace")

	// Absolute container path must still be allowed for container jobs
	jobAbs := &protocol.JobSpecification{
		JobID:     "container-abs-job",
		JobName:   "container-abs-job",
		Container: &protocol.JobContainer{Image: "alpine:latest"},
		Steps: []protocol.JobStep{
			{ID: "s1", Name: "inside", WorkingDirectory: "/app", Script: "pwd"},
		},
	}
	resAbs, errAbs := coord.ExecuteJob(context.Background(), jobAbs, "", "")
	require.NoError(t, errAbs)
	assert.Equal(t, "success", resAbs.Conclusion)
	require.Len(t, dockerMock.stepsRun, 1)
	assert.Equal(t, "/app", dockerMock.stepsRun[0].WorkingDirectory)
}

func TestExecuteStep_NilSpecGuard(t *testing.T) {
	coord := NewCoordinator(&mockDriver{}, nil, t.TempDir()).WithTracer(nil)
	_, _, err := coord.executeStep(context.Background(), &mockDriver{}, "job-1", nil, nil, "step", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil step spec")
}

func TestGenerateCheckoutScript_AskpassAndPathTraversal(t *testing.T) {
	env := map[string]string{
		"GITHUB_REPOSITORY": "owner/repo",
		"GITHUB_TOKEN":      "ghp_secret_token",
	}
	step := protocol.JobStep{
		ID:     "checkout",
		Inputs: map[string]string{"path": "../escape"},
	}
	script := generateCheckoutScript(step, env)
	assert.NotContains(t, script, "${GITHUB_TOKEN}@github.com")
	assert.Contains(t, script, "GIT_ASKPASS")
	assert.Contains(t, script, "ACTR_INTERNAL_TOKEN")
	assert.Contains(t, script, `mktemp "${RUNNER_TEMP:-/tmp}/git-askpass-XXXXXX"`)
	assert.NotContains(t, script, "git-askpass-XXXXXX.sh")
	assert.Contains(t, script, "invalid checkout path traversal")

	// Verify shell traversal guard rejects both ../escape and backslash sub\escape
	ws := t.TempDir()
	for _, badPath := range []string{"../escape", "/abs/path", `sub\escape`} {
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = append(os.Environ(),
			"GITHUB_WORKSPACE="+ws,
			"ACTR_CHECKOUT_PATH="+badPath,
			"ACTR_CHECKOUT_REPO=owner/repo",
		)
		out, err := cmd.CombinedOutput()
		require.Error(t, err, "expected traversal path %q to be rejected", badPath)
		assert.Contains(t, string(out), "invalid checkout path traversal")
	}
}

func TestExecuteJob_FallbackStreamConcurrentWriteRace(t *testing.T) {
	var sharedBuf bytes.Buffer
	d := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			var wg sync.WaitGroup
			for i := 0; i < 10; i++ {
				wg.Add(2)
				go func(n int) {
					defer wg.Done()
					_, _ = fmt.Fprintf(stdout, "out-%d token=super-secret-val\n", n)
				}(i)
				go func(n int) {
					defer wg.Done()
					_, _ = fmt.Fprintf(stderr, "err-%d token=super-secret-val\n", n)
				}(i)
			}
			wg.Wait()
			return &driver.StepResult{ExitCode: 0}, nil
		},
	}
	coord := NewCoordinator(d, nil, t.TempDir()).
		WithTracer(nil).
		WithOutput(&sharedBuf, &sharedBuf).
		WithStepLogger(false)

	job := &protocol.JobSpecification{
		JobID:    "fallback-race-job",
		JobToken: "super-secret-val",
		Steps:    []protocol.JobStep{{ID: "s1", Name: "concurrent-output", Script: "echo ok"}},
	}
	res, err := coord.ExecuteJob(context.Background(), job, "", "super-secret-val")
	require.NoError(t, err)
	assert.Equal(t, "success", res.Conclusion)
	assert.NotContains(t, sharedBuf.String(), "super-secret-val")
	assert.Contains(t, sharedBuf.String(), "***")
}

func TestHasPathPrefix(t *testing.T) {
	sep := string(filepath.Separator)
	assert.True(t, hasPathPrefix(sep+"tmp", sep))
	assert.True(t, hasPathPrefix(filepath.Join(sep+"work", "job"), filepath.Join(sep+"work", "job")))
	assert.True(t, hasPathPrefix(filepath.Join(sep+"work", "job", "sub"), filepath.Join(sep+"work", "job")))
	assert.False(t, hasPathPrefix(filepath.Join(sep+"work", "job2"), filepath.Join(sep+"work", "job")))
}

func TestExecuteJob_ConcurrentJobsIsolateDriverAndMonitor(t *testing.T) {
	sockPath := testSocketPath(t)
	server := monitor.NewServer(sockPath, "test-runner")
	require.NoError(t, server.Start())
	defer func() { _ = server.Stop() }()

	coord := NewCoordinator(host.NewDriver(), nil, t.TempDir()).WithTracer(nil)
	coord.SetMonitorServer(server)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			job := &protocol.JobSpecification{
				JobID:    fmt.Sprintf("conc-job-%d", idx),
				JobName:  "shared-name",
				JobToken: fmt.Sprintf("secret-token-%d", idx),
				Steps: []protocol.JobStep{
					{ID: "s1", Name: "run", Script: fmt.Sprintf(`echo "job=%d tok=secret-token-%d"`, idx, idx)},
				},
			}
			res, err := coord.ExecuteJob(context.Background(), job, "", job.JobToken)
			assert.NoError(t, err)
			if res != nil {
				assert.Equal(t, "success", res.Conclusion)
			}
		}(i)
	}
	wg.Wait()
}

func TestShouldExecuteStep_ExpressionParserEdgeCases(t *testing.T) {
	ctx := map[string]any{
		"inputs": map[string]any{"msg": "always()", "env": "prod"},
		"github": map[string]any{"ref": "refs/heads/main"},
	}

	// Literal 'always()' inside quotes must NOT bypass jobFailed=true!
	assert.False(t, shouldExecuteStep("inputs.msg == 'always()'", ctx, true, false))

	// Multiple format() calls in one comparison must evaluate == rather than outer format(...)
	assert.False(t, shouldExecuteStep("format('{0}', 'a') == format('{0}', 'b')", ctx, false, false))
	assert.True(t, shouldExecuteStep("format('{0}', 'a') == format('{0}', 'a')", ctx, false, false))

	// Tight operators without spaces + multiline condition
	assert.True(t, shouldExecuteStep("startsWith(github.ref,'refs/heads/')&&\ninputs.env=='prod'", ctx, false, false))
	assert.False(t, shouldExecuteStep("(inputs.env == ')') || false", ctx, false, false))

	// Short-circuited false && true must compare equal to false
	assert.True(t, shouldExecuteStep("(false && true) == false", ctx, false, false))
}

func TestExpressionEvaluator_BuiltinsAndOperators(t *testing.T) {
	ws := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module example.com/test\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "sub", "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "sub", "skip.tmp"), []byte("ignored\n"), 0o644))

	ctx := map[string]any{
		"github": map[string]any{
			"workspace": ws,
		},
		"a": map[string]any{
			"b": []any{
				map[string]any{"c": "hello", "hyphen-key": "world"},
				map[string]any{"c": 42},
			},
		},
		"arr": []any{"alpha", "beta", "gamma"},
		"num": 10,
	}

	// Property chains & indexing
	assert.Equal(t, "hello", evaluateExpression("a.b[0].c", ctx))
	assert.Equal(t, "world", evaluateExpression("a.b[0].hyphen-key", ctx))
	assert.Equal(t, "world", evaluateExpression("a.b[0]['hyphen-key']", ctx))
	assert.Equal(t, "42", evaluateExpression("a.b[1].c", ctx))
	assert.Equal(t, "", evaluateExpression("a.b[9].c", ctx))

	// Precedence: || < && < equality (==, !=) < relational (<, <=, >, >=) < !
	assert.Equal(t, "true", evaluateExpression("true || false && false", ctx))
	assert.Equal(t, "true", evaluateExpression("!(num < 5) && num >= 10", ctx))
	assert.Equal(t, "true", evaluateExpression("num > 5 && num <= 10", ctx))
	assert.Equal(t, "false", evaluateExpression("num > 10 || num < 10", ctx))
	assert.Equal(t, "true", evaluateExpression("'apple' < 'Banana'", ctx))
	assert.Equal(t, "false", evaluateExpression("true == (2 < 1)", ctx))
	assert.Equal(t, "false", evaluateExpression("true == 2 < 1", ctx))
	assert.Equal(t, "true", evaluateExpression("2 < 1 == false", ctx))

	// join, toJSON, fromJSON, contains
	assert.Equal(t, "alpha|beta|gamma", evaluateExpression("join(arr, '|')", ctx))
	assert.Equal(t, "alpha,beta,gamma", evaluateExpression("join(arr)", ctx))
	assert.Equal(t, "true", evaluateExpression("contains(arr, 'BETA')", ctx))
	assert.Equal(t, "false", evaluateExpression("contains(arr, 'delta')", ctx))
	assert.Equal(t, "42", evaluateExpression("fromJSON('{\"k\":{\"v\":42}}').k.v", ctx))
	assert.Equal(t, "b", evaluateExpression("fromJSON('[\"a\",\"b\"]')[1]", ctx))
	assert.Contains(t, evaluateExpression("toJSON(a.b[0])", ctx), `"c": "hello"`)

	// hashFiles with glob and exclusion
	h1 := evaluateExpression("hashFiles('**/*.go', '!sub/*.tmp')", ctx)
	h2 := evaluateExpression("hashFiles('go.mod')", ctx)
	assert.Len(t, h1, 64)
	assert.Len(t, h2, 64)
	assert.NotEqual(t, h1, h2)
	assert.Equal(t, "", evaluateExpression("hashFiles('nonexistent-*.xyz')", ctx))

	// containsStatusCheckFunction ignores literals and detects real calls
	assert.False(t, containsStatusCheckFunction("inputs.msg == 'always()'"))
	assert.False(t, containsStatusCheckFunction("'success()' != 'failure()'"))
	assert.True(t, containsStatusCheckFunction("always()"))
	assert.True(t, containsStatusCheckFunction("${{ failure() && true }}"))
	assert.True(t, containsStatusCheckFunction("cancelled() || success()"))

	// Syntax errors and deep nesting fail closed in shouldExecuteStep
	assert.False(t, shouldExecuteStep("inputs.a ==", ctx, false, false))
	assert.False(t, shouldExecuteStep("'unterminated", ctx, false, false))
	deep := strings.Repeat("(", 150) + "true" + strings.Repeat(")", 150)
	assert.False(t, shouldExecuteStep(deep, ctx, false, false))

	// hashFiles skips symlinks that resolve outside workspace
	outsideDir := t.TempDir()
	outsideSecret := filepath.Join(outsideDir, "secret.txt")
	require.NoError(t, os.WriteFile(outsideSecret, []byte("top-secret\n"), 0o644))
	require.NoError(t, os.Symlink(outsideSecret, filepath.Join(ws, "leaked.txt")))
	assert.Equal(t, "", evaluateExpression("hashFiles('leaked.txt')", ctx))
}
