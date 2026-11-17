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
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/protocol"
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

	// WorkingDirectory should resolve relative to GITHUB_WORKSPACE
	expectedWorkDir := filepath.Join(workDir, "my-build-job", "custom-subdir")
	if recordedStep.WorkingDirectory != expectedWorkDir {
		t.Errorf("expected WorkingDirectory %q, got %q", expectedWorkDir, recordedStep.WorkingDirectory)
	}

	// Verify working directory was created
	if fi, err := os.Stat(expectedWorkDir); err != nil || !fi.IsDir() {
		t.Errorf("expected step working directory %q to exist as dir", expectedWorkDir)
	}

	// Standard variables
	env := recordedStep.Environment
	expectedWorkspace := filepath.Join(workDir, "my-build-job")
	if env["GITHUB_WORKSPACE"] != expectedWorkspace {
		t.Errorf("expected GITHUB_WORKSPACE %q, got %q", expectedWorkspace, env["GITHUB_WORKSPACE"])
	}
	if env["RUNNER_WORKSPACE"] != workDir {
		t.Errorf("expected RUNNER_WORKSPACE %q, got %q", workDir, env["RUNNER_WORKSPACE"])
	}
	expectedTemp := filepath.Join(workDir, "_temp")
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

	coord := NewCoordinator(mock, nil, filepath.Join(tmpDir, "work")).WithStepLogger(true)
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
	for _, ev := range evs {
		switch ev.Type {
		case monitor.EventJobStart:
			if ev.JobID == "job-mon-test" {
				hasJobStart = true
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
		case monitor.EventStepFinish:
			if ev.JobID == "job-mon-test" {
				hasStepFinish = true
			}
		}
	}

	if !hasJobStart {
		t.Error("missing EventJobStart")
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

	var capturedPIDDuringStep int32
	mock := &mockDriver{
		executeFunc: func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
			if step.OnProcessStart != nil {
				step.OnProcessStart(12345, 12345)
			}
			// wait briefly for telemetry sampler (ticker runs every 500ms)
			time.Sleep(550 * time.Millisecond)
			return &driver.StepResult{ExitCode: 0}, nil
		},
	}

	coord := NewCoordinator(mock, nil, filepath.Join(tmpDir, "work")).WithStepLogger(true)
	coord.SetMonitorServer(server)

	// Wrap executeFunc to capture PID while process is running
	origExec := mock.executeFunc
	mock.executeFunc = func(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
		res, err := origExec(ctx, step, stdout, stderr)
		if coord.Sampler() != nil {
			sample := coord.Sampler().Sample()
			capturedPIDDuringStep = sample.PID
		}
		return res, err
	}

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

	var gotJobStart, gotJobFinish, gotStepStart, gotStepFinish, gotTelemetryWithPID bool
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

	if capturedPIDDuringStep != 12345 {
		t.Errorf("expected sampler target PID during step to be 12345, got %d", capturedPIDDuringStep)
	}

	// Verify sampler target PID was reset to 0 after step defer
	if coord.Sampler() != nil {
		sample := coord.Sampler().Sample()
		if sample.PID != 0 {
			t.Errorf("expected sampler target PID to be reset to 0 after step completion, got %d", sample.PID)
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
				{"k": "token", "v": "ghs_secret_123"}
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
		{"github.repository", "adrijshikhar/cxstatusline"},
		{"'plain literal'", "plain literal"},
	}

	for _, tt := range tests {
		got := evaluateExpression(tt.expr, parsed)
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
