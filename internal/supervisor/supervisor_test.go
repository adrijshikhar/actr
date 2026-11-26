package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/adrijshikhar/actr/internal/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSocketPath returns a unix socket path suitable for testing on Darwin/Linux.
func testSocketPath(t *testing.T) string {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "daemon.sock")
	if len(sockPath) < 100 {
		return sockPath
	}

	dir, err := os.MkdirTemp("/tmp", "act-sup-*")
	if err != nil {
		t.Fatalf("failed to create short temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return filepath.Join(dir, "daemon.sock")
}

func TestSupervisor_LifecycleAndReload(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := testSocketPath(t)

	cfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 2,
		},
		Repos: map[string]config.RepoConfig{
			"repo-a": {
				URL:               "https://github.com/owner/repo-a",
				ConfigDir:         filepath.Join(tmpDir, "repos", "repo-a"),
				MaxConcurrentJobs: 1,
			},
		},
	}

	s, err := NewSupervisor(cfg, sockPath)
	if err != nil {
		t.Fatalf("NewSupervisor failed: %v", err)
	}

	if s.ResourceManager() == nil {
		t.Fatal("expected ResourceManager to be non-nil")
	}
	if s.MonitorServer() == nil {
		t.Fatal("expected MonitorServer to be non-nil")
	}
	if len(s.Workers()) != 1 {
		t.Fatalf("expected 1 worker initially, got %d", len(s.Workers()))
	}
	if _, ok := s.Workers()["repo-a"]; !ok {
		t.Fatal("expected repo-a worker to exist")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Start(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	// Test reload with second repo and increased global concurrency
	newCfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 3,
		},
		Repos: map[string]config.RepoConfig{
			"repo-a": cfg.Repos["repo-a"],
			"repo-b": {
				URL:               "https://github.com/owner/repo-b",
				ConfigDir:         filepath.Join(tmpDir, "repos", "repo-b"),
				MaxConcurrentJobs: 1,
			},
		},
	}

	if err := s.Reload(newCfg); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	if s.ResourceManager().MaxConcurrentJobs() != 3 {
		t.Errorf("expected max concurrent jobs 3 after reload, got %d", s.ResourceManager().MaxConcurrentJobs())
	}

	workers := s.Workers()
	if len(workers) != 2 {
		t.Fatalf("expected 2 workers after reload, got %d", len(workers))
	}
	if _, ok := workers["repo-a"]; !ok {
		t.Error("expected repo-a worker to remain after reload")
	}
	if _, ok := workers["repo-b"]; !ok {
		t.Error("expected repo-b worker to be present after reload")
	}

	// Test reload removing repo-a
	newCfg2 := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 3,
		},
		Repos: map[string]config.RepoConfig{
			"repo-b": newCfg.Repos["repo-b"],
		},
	}

	if err := s.Reload(newCfg2); err != nil {
		t.Fatalf("Reload removing repo-a failed: %v", err)
	}

	workers2 := s.Workers()
	if len(workers2) != 1 {
		t.Fatalf("expected 1 worker after removing repo-a, got %d", len(workers2))
	}
	if _, ok := workers2["repo-a"]; ok {
		t.Error("expected repo-a worker to be removed")
	}
	if _, ok := workers2["repo-b"]; !ok {
		t.Error("expected repo-b worker to remain")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("expected clean shutdown, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor shutdown timed out")
	}
}

func TestSupervisor_DrainOnContextCancel(t *testing.T) {
	sockPath := testSocketPath(t)

	cfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 2,
		},
		Repos: map[string]config.RepoConfig{
			"repo-drain": {
				URL:               "https://github.com/owner/repo-drain",
				MaxConcurrentJobs: 1,
			},
		},
	}

	s, err := NewSupervisor(cfg, sockPath)
	if err != nil {
		t.Fatalf("NewSupervisor failed: %v", err)
	}

	worker := s.Workers()["repo-drain"]
	if worker == nil {
		t.Fatal("expected repo-drain worker to exist")
	}

	mockBroker := &mockBrokerClient{
		msgQueue: make(chan *protocol.BrokerMessage, 1),
	}
	jobStarted := make(chan struct{})
	jobFinish := make(chan struct{})
	var jobFinished atomic.Bool

	mockCoord := &mockCoordinator{
		executeFn: func(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error) {
			close(jobStarted)
			<-jobFinish
			jobFinished.Store(true)
			return &protocol.CompleteJobRequest{Conclusion: "success"}, nil
		},
	}
	mockRunService := &mockRunServiceClient{
		acquireFn: func(ctx context.Context, runServiceURL, runnerRequestID, billingOwnerID, token string) (*protocol.JobSpecification, error) {
			return &protocol.JobSpecification{PlanID: "plan-1", JobID: "job-1"}, nil
		},
	}

	worker.brokerClient = mockBroker
	worker.coordinator = mockCoord
	worker.runServiceClient = mockRunService

	// Queue a job message
	mockBroker.msgQueue <- &protocol.BrokerMessage{
		MessageType: "RunnerJobRequest",
		MessageID:   100,
		Body:        `{"runner_request_id":"req-drain-1"}`,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Start(ctx)
	}()

	// Wait for job execution to start
	select {
	case <-jobStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for in-flight job to start")
	}

	// Cancel context while job is in-flight
	cancel()

	// Verify Start has not exited yet because the job is still running
	select {
	case err := <-errCh:
		t.Fatalf("Start returned before in-flight job finished draining: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if jobFinished.Load() {
		t.Fatal("job should still be running and not finished yet")
	}

	// Now allow the job to finish
	close(jobFinish)

	// Supervisor Start should now complete cleanly
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("expected clean shutdown, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Start to complete after job finished")
	}

	if !jobFinished.Load() {
		t.Fatal("expected in-flight job to have completed before Start returned")
	}
}

func TestSupervisor_NilConfigErrors(t *testing.T) {
	// Nil config on creation
	if _, err := NewSupervisor(nil, ""); err == nil {
		t.Error("expected error for nil config on NewSupervisor, got nil")
	}

	// Invalid global concurrency
	zeroConcurrCfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 0,
		},
		Repos: map[string]config.RepoConfig{
			"repo-1": {URL: "https://github.com/owner/repo-1"},
		},
	}
	if _, err := NewSupervisor(zeroConcurrCfg, ""); err == nil {
		t.Error("expected error for zero MaxConcurrentJobs on NewSupervisor, got nil")
	}

	validCfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 2,
		},
		Repos: map[string]config.RepoConfig{
			"repo-1": {URL: "https://github.com/owner/repo-1"},
		},
	}

	// Empty socket path: MonitorServer should be nil
	s, err := NewSupervisor(validCfg, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.MonitorServer() != nil {
		t.Errorf("expected nil MonitorServer with empty sockPath, got %v", s.MonitorServer())
	}

	// Nil config on Reload
	if err := s.Reload(nil); err == nil {
		t.Error("expected error for nil config on Reload, got nil")
	}

	// Invalid concurrency on Reload
	invalidReloadCfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: -1,
		},
		Repos: map[string]config.RepoConfig{
			"repo-1": {URL: "https://github.com/owner/repo-1"},
		},
	}
	if err := s.Reload(invalidReloadCfg); err == nil {
		t.Error("expected error for negative MaxConcurrentJobs on Reload, got nil")
	}

	// Starting without socket server should work without panic
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	if err := s.Start(ctx); err != nil {
		t.Errorf("expected clean exit with canceled context, got %v", err)
	}
}

func TestSupervisor_StopAndConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 2,
		},
		Repos: map[string]config.RepoConfig{
			"repo-stop": {
				URL:               "https://github.com/owner/repo-stop",
				ConfigDir:         filepath.Join(tmpDir, "repos", "repo-stop"),
				MaxConcurrentJobs: 1,
			},
		},
	}

	s, err := NewSupervisor(cfg, filepath.Join(tmpDir, "daemon.sock"))
	if err != nil {
		t.Fatalf("NewSupervisor failed: %v", err)
	}

	// Calling Stop when idle should be a no-op
	if err := s.Stop(); err != nil {
		t.Errorf("expected clean Stop when idle, got %v", err)
	}

	// Verify Config getter returns accurate copy
	gotCfg := s.Config()
	if gotCfg == nil || gotCfg.Global.MaxConcurrentJobs != 2 {
		t.Fatalf("expected config with MaxConcurrentJobs 2, got %v", gotCfg)
	}

	startDone := make(chan error, 1)
	go func() {
		startDone <- s.Start(context.Background())
	}()

	time.Sleep(50 * time.Millisecond)

	// Call s.Stop() explicitly while running
	if err := s.Stop(); err != nil {
		t.Errorf("Stop failed: %v", err)
	}

	select {
	case err := <-startDone:
		if err != nil {
			t.Errorf("Start returned error after Stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not stop within deadline after Stop()")
	}
}

func TestSupervisor_ReloadAtomicValidationFailure(t *testing.T) {
	cfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 2,
		},
		Repos: map[string]config.RepoConfig{
			"repo-ok": {
				URL: "https://github.com/owner/repo-ok",
			},
		},
	}

	s, err := NewSupervisor(cfg, "")
	if err != nil {
		t.Fatalf("NewSupervisor failed: %v", err)
	}

	// Reload with an invalid added repo (empty alias key)
	badCfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 10,
		},
		Repos: map[string]config.RepoConfig{
			"repo-ok": cfg.Repos["repo-ok"],
			"": {
				URL: "https://github.com/owner/repo-invalid",
			},
		},
	}

	err = s.Reload(badCfg)
	if err == nil {
		t.Fatal("expected error reloading with invalid repo alias")
	}

	// Invariant: resMgr must NOT have resized to 10 if validation failed
	if s.ResourceManager().MaxConcurrentJobs() != 2 {
		t.Errorf("expected MaxConcurrentJobs to remain 2 after failed reload, got %d", s.ResourceManager().MaxConcurrentJobs())
	}
}

func TestSupervisor_WithTracer(t *testing.T) {
	cfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 2,
		},
		Repos: map[string]config.RepoConfig{
			"repo-a": {URL: "https://github.com/owner/repo-a"},
		},
	}
	s, err := NewSupervisor(cfg, "")
	if err != nil {
		t.Fatalf("NewSupervisor failed: %v", err)
	}

	tm, err := telemetry.NewManager(context.Background(), "supervisor-test")
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer func() { _ = tm.Shutdown(context.Background()) }()

	s.WithTracer(tm)
	if s.tracer != tm {
		t.Errorf("expected supervisor tracer to be set")
	}
	worker := s.workers["repo-a"]
	if worker == nil || worker.tracer != tm {
		t.Errorf("expected worker tracer to be set via WithTracer")
	}
}

type drainCheckBroker struct{}

func (b *drainCheckBroker) CreateSession(context.Context, string, int64, int64) error { return nil }
func (b *drainCheckBroker) AcknowledgeMessage(context.Context, string) error          { return nil }
func (b *drainCheckBroker) DeleteSession(context.Context) error                       { return nil }
func (b *drainCheckBroker) GetMessage(ctx context.Context, _, _, _ string) (*protocol.BrokerMessage, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return &protocol.BrokerMessage{MessageID: 42, MessageType: "RunnerJobRequest"}, nil
	}
}

type drainCheckRunClient struct{}

func (r *drainCheckRunClient) AcquireJob(ctx context.Context, _, _, _, _ string) (*protocol.JobSpecification, error) {
	return &protocol.JobSpecification{JobID: "drain-job", PlanID: "plan-1"}, nil
}
func (r *drainCheckRunClient) CompleteJob(context.Context, string, string, *protocol.CompleteJobRequest) error {
	return nil
}

type drainCheckExecutor struct {
	started    chan struct{}
	once       sync.Once
	ctxWasLive atomic.Bool
}

func (e *drainCheckExecutor) ExecuteJob(ctx context.Context, job *protocol.JobSpecification, _, _ string) (*protocol.CompleteJobRequest, error) {
	e.once.Do(func() { close(e.started) })
	time.Sleep(80 * time.Millisecond)
	e.ctxWasLive.Store(ctx.Err() == nil)
	return &protocol.CompleteJobRequest{JobID: job.JobID, PlanID: job.PlanID, Conclusion: "success"}, nil
}

func TestSupervisor_GracefulDrainDoesNotCancelInFlightJobsBeforeTimeout(t *testing.T) {
	cfg := &config.SupervisorConfig{
		Version: "1",
		Global:  config.GlobalConfig{MaxConcurrentJobs: 1},
		Repos:   map[string]config.RepoConfig{"r1": {URL: "https://github.com/o/r1", MaxConcurrentJobs: 1}},
	}
	s, err := NewSupervisor(cfg, "")
	require.NoError(t, err)
	w := s.Workers()["r1"]
	exec := &drainCheckExecutor{started: make(chan struct{})}
	w.SetBrokerClient(&drainCheckBroker{})
	w.SetRunServiceClient(&drainCheckRunClient{})
	w.SetCoordinator(exec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	<-exec.started
	cancel() // must cancel pollingCtx first while keeping jobCtx alive during 30s drain window
	require.NoError(t, <-done)
	assert.True(t, exec.ctxWasLive.Load(), "in-flight job context must survive polling cancellation during drain")
}

func TestSupervisor_ReloadRecreatesModifiedRepoWorker(t *testing.T) {
	cfg := &config.SupervisorConfig{
		Version: "1",
		Global:  config.GlobalConfig{MaxConcurrentJobs: 4},
		Repos: map[string]config.RepoConfig{
			"r1": {URL: "https://github.com/o/r1", MaxConcurrentJobs: 1},
			"r2": {URL: "https://github.com/o/r2", MaxConcurrentJobs: 1},
		},
	}
	s, err := NewSupervisor(cfg, "")
	require.NoError(t, err)

	oldR1 := s.Workers()["r1"]
	oldR2 := s.Workers()["r2"]
	require.NotNil(t, oldR1)
	require.NotNil(t, oldR2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	time.Sleep(30 * time.Millisecond)

	// Modify r1, keep r2 identical
	newCfg := &config.SupervisorConfig{
		Version: "1",
		Global:  config.GlobalConfig{MaxConcurrentJobs: 4},
		Repos: map[string]config.RepoConfig{
			"r1": {URL: "https://github.com/o/r1", MaxConcurrentJobs: 2},
			"r2": {URL: "https://github.com/o/r2", MaxConcurrentJobs: 1},
		},
	}
	require.NoError(t, s.Reload(newCfg))

	workers := s.Workers()
	newR1 := workers["r1"]
	newR2 := workers["r2"]
	require.NotNil(t, newR1)
	require.NotNil(t, newR2)
	assert.NotSame(t, oldR1, newR1, "modified repo worker must be recreated")
	assert.Same(t, oldR2, newR2, "unchanged repo worker must be preserved")
	assert.Equal(t, 2, cap(newR1.repoSem))

	cancel()
	require.NoError(t, <-done)
}
