package supervisor

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/protocol"
)

type mockBrokerClient struct {
	msgQueue chan *protocol.BrokerMessage
	ackFn    func(ctx context.Context, id string) error
	delFn    func(ctx context.Context) error
	createFn func(ctx context.Context, agentName string, agentID int64, poolID int64) error
	token    string
}

func (m *mockBrokerClient) CreateSession(ctx context.Context, agentName string, agentID int64, poolID int64) error {
	if m.createFn != nil {
		return m.createFn(ctx, agentName, agentID, poolID)
	}
	return nil
}

func (m *mockBrokerClient) Token() string {
	return m.token
}

func (m *mockBrokerClient) GetMessage(ctx context.Context, v, os, arch string) (*protocol.BrokerMessage, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg, ok := <-m.msgQueue:
		if !ok {
			return nil, context.Canceled
		}
		return msg, nil
	}
}

func (m *mockBrokerClient) AcknowledgeMessage(ctx context.Context, id string) error {
	if m.ackFn != nil {
		return m.ackFn(ctx, id)
	}
	return nil
}

func (m *mockBrokerClient) DeleteSession(ctx context.Context) error {
	if m.delFn != nil {
		return m.delFn(ctx)
	}
	return nil
}

type mockRunServiceClient struct {
	acquireFn  func(ctx context.Context, runServiceURL, runnerRequestID, billingOwnerID, token string) (*protocol.JobSpecification, error)
	completeFn func(ctx context.Context, runServiceURL, token string, req *protocol.CompleteJobRequest) error
}

func (m *mockRunServiceClient) AcquireJob(ctx context.Context, runServiceURL, runnerRequestID, billingOwnerID, token string) (*protocol.JobSpecification, error) {
	if m.acquireFn != nil {
		return m.acquireFn(ctx, runServiceURL, runnerRequestID, billingOwnerID, token)
	}
	return &protocol.JobSpecification{PlanID: "plan-1", JobID: "job-1"}, nil
}

func (m *mockRunServiceClient) CompleteJob(ctx context.Context, runServiceURL, token string, req *protocol.CompleteJobRequest) error {
	if m.completeFn != nil {
		return m.completeFn(ctx, runServiceURL, token, req)
	}
	return nil
}

type mockCoordinator struct {
	executeFn func(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error)
}

func (m *mockCoordinator) ExecuteJob(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, job, runServiceURL, token)
	}
	return &protocol.CompleteJobRequest{Conclusion: "success"}, nil
}

// TestRepoWorker_PermitReleasedOnPanic verifies that RAII executeWithGuards releases
// the global permit even if the job execution / coordinator panics.
func TestRepoWorker_PermitReleasedOnPanic(t *testing.T) {
	resMgr, err := NewResourceManager(1)
	if err != nil {
		t.Fatalf("failed to create resource manager: %v", err)
	}

	worker := &RepoWorker{
		repoAlias: "test-repo",
		resMgr:    resMgr,
		repoSem:   make(chan struct{}, 1),
	}

	ctx := context.Background()
	reqRef := JobRequestReference{RunnerRequestID: "123"}

	// 1. Verify panic is caught and permit is released
	err = worker.executeWithGuards(ctx, reqRef, func() error {
		if resMgr.ActiveCount() != 1 {
			t.Errorf("expected active count 1 inside guard, got %d", resMgr.ActiveCount())
		}
		panic("simulated fatal step panic")
	})

	if err == nil {
		t.Fatal("expected error from recovered panic, got nil")
	}
	if !strings.Contains(err.Error(), "simulated fatal step panic") {
		t.Fatalf("expected error message to contain panic string, got: %v", err)
	}

	if resMgr.ActiveCount() != 0 {
		t.Fatalf("expected resMgr.ActiveCount() == 0 after panic, got %d", resMgr.ActiveCount())
	}

	// 2. Verify normal execution also releases permit
	executed := false
	err = worker.executeWithGuards(ctx, reqRef, func() error {
		executed = true
		if resMgr.ActiveCount() != 1 {
			t.Errorf("expected active count 1 inside guard, got %d", resMgr.ActiveCount())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error during normal execution: %v", err)
	}
	if !executed {
		t.Fatal("expected function to have executed")
	}
	if resMgr.ActiveCount() != 0 {
		t.Fatalf("expected resMgr.ActiveCount() == 0 after normal execution, got %d", resMgr.ActiveCount())
	}
}

// TestRepoWorker_CapacityGating verifies that the worker does not poll the broker
// when global capacity is saturated, and resumes polling once a slot is freed.
func TestRepoWorker_CapacityGating(t *testing.T) {
	resMgr, err := NewResourceManager(1)
	if err != nil {
		t.Fatalf("failed to create resource manager: %v", err)
	}

	mockBroker := &mockBrokerClient{
		msgQueue: make(chan *protocol.BrokerMessage, 5),
	}

	worker := &RepoWorker{
		repoAlias:    "test-repo",
		resMgr:       resMgr,
		brokerClient: mockBroker,
		repoSem:      make(chan struct{}, 1),
	}

	// Saturate global capacity: active 1 of 1
	permit, err := resMgr.Acquire(context.Background())
	if err != nil {
		t.Fatalf("failed to acquire initial permit: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pollingCtx, cancelPolling := context.WithCancel(context.Background())
	defer cancelPolling()

	// Push a message to mock broker
	mockBroker.msgQueue <- &protocol.BrokerMessage{
		MessageType: "RunnerJobRequest",
		MessageID:   1,
		Body:        `{"runner_request_id":"req-1"}`,
	}

	go worker.runLoop(ctx, pollingCtx)

	// Since capacity is saturated (active == 1 == max), worker must wait on SlotAvailable
	time.Sleep(100 * time.Millisecond)

	if len(mockBroker.msgQueue) != 1 {
		t.Fatalf("expected message to remain in broker queue while saturated, remaining: %d", len(mockBroker.msgQueue))
	}

	// Release permit to free up capacity
	permit.Release()

	// Message should now be consumed by the worker
	deadline := time.Now().Add(1 * time.Second)
	consumed := false
	for time.Now().Before(deadline) {
		if len(mockBroker.msgQueue) == 0 {
			consumed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !consumed {
		t.Fatalf("expected message to be consumed after permit release, remaining: %d", len(mockBroker.msgQueue))
	}
}

// TestRepoWorker_LocalConcurrencyCap verifies that a repository worker never exceeds
// repoCfg.MaxConcurrentJobs even when global capacity has excess headroom.
func TestRepoWorker_LocalConcurrencyCap(t *testing.T) {
	// Global capacity is 5, but repo cap is 1
	resMgr, err := NewResourceManager(5)
	if err != nil {
		t.Fatalf("failed to create resource manager: %v", err)
	}

	mockBroker := &mockBrokerClient{
		msgQueue: make(chan *protocol.BrokerMessage, 5),
	}

	jobExecuting := make(chan struct{})
	jobBlocker := make(chan struct{})

	mockCoord := &mockCoordinator{
		executeFn: func(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error) {
			jobExecuting <- struct{}{}
			<-jobBlocker
			return &protocol.CompleteJobRequest{Conclusion: "success"}, nil
		},
	}
	mockRunService := &mockRunServiceClient{
		acquireFn: func(ctx context.Context, runServiceURL, runnerRequestID, billingOwnerID, token string) (*protocol.JobSpecification, error) {
			return &protocol.JobSpecification{PlanID: "plan-1", JobID: "job-1"}, nil
		},
	}

	worker := &RepoWorker{
		repoAlias:        "test-repo",
		resMgr:           resMgr,
		brokerClient:     mockBroker,
		runServiceClient: mockRunService,
		coordinator:      mockCoord,
		repoSem:          make(chan struct{}, 1), // Max 1 concurrent job for this repo
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pollingCtx, cancelPolling := context.WithCancel(context.Background())
	defer cancelPolling()

	// Push 2 jobs into the broker queue
	mockBroker.msgQueue <- &protocol.BrokerMessage{
		MessageType: "RunnerJobRequest",
		MessageID:   1,
		Body:        `{"runner_request_id":"req-1"}`,
	}
	mockBroker.msgQueue <- &protocol.BrokerMessage{
		MessageType: "RunnerJobRequest",
		MessageID:   2,
		Body:        `{"runner_request_id":"req-2"}`,
	}

	go worker.runLoop(ctx, pollingCtx)

	// Wait for the first job to start executing
	select {
	case <-jobExecuting:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for first job to start executing")
	}

	// Verify local repo semaphore is full (1/1)
	if len(worker.repoSem) != 1 {
		t.Fatalf("expected repoSem to be full (1), got %d", len(worker.repoSem))
	}

	// Verify the second message is NOT dequeued because repo semaphore is full
	time.Sleep(100 * time.Millisecond)
	if len(mockBroker.msgQueue) != 1 {
		t.Fatalf("expected second message to remain queued due to local cap, got %d", len(mockBroker.msgQueue))
	}

	// Unblock the first job
	close(jobBlocker)

	// Second job should now be dequeued and start executing
	select {
	case <-jobExecuting:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for second job to start executing")
	}
}

// TestRepoWorker_DispatchJob_CompleteOnCanceledContext verifies that when ctx is canceled,
// CompleteJob is still invoked using context.WithoutCancel with a 15-second timeout.
func TestRepoWorker_DispatchJob_CompleteOnCanceledContext(t *testing.T) {
	resMgr, _ := NewResourceManager(1)

	var completeCalled int32
	var completedCtxErr error
	var completeDeadline time.Duration

	mockRunService := &mockRunServiceClient{
		acquireFn: func(ctx context.Context, runServiceURL, runnerRequestID, billingOwnerID, token string) (*protocol.JobSpecification, error) {
			return &protocol.JobSpecification{PlanID: "plan-1", JobID: "job-1"}, nil
		},
		completeFn: func(ctx context.Context, runServiceURL, token string, req *protocol.CompleteJobRequest) error {
			atomic.AddInt32(&completeCalled, 1)
			completedCtxErr = ctx.Err()
			if deadline, ok := ctx.Deadline(); ok {
				completeDeadline = time.Until(deadline)
			}
			return nil
		},
	}

	worker := &RepoWorker{
		repoAlias:        "test-repo",
		resMgr:           resMgr,
		runServiceClient: mockRunService,
		coordinator: &mockCoordinator{
			executeFn: func(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error) {
				return &protocol.CompleteJobRequest{Conclusion: "cancelled"}, context.Canceled
			},
		},
		repoSem: make(chan struct{}, 1),
	}

	// Context that is already canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reqRef := JobRequestReference{RunnerRequestID: "req-1"}
	_ = worker.dispatchJob(ctx, reqRef)

	if atomic.LoadInt32(&completeCalled) != 1 {
		t.Fatalf("expected CompleteJob to be called once, got %d", completeCalled)
	}

	// context.WithoutCancel must ensure context is not canceled in CompleteJob
	if completedCtxErr != nil {
		t.Fatalf("expected CompleteJob context to be active (WithoutCancel), got err: %v", completedCtxErr)
	}

	// Timeout should be approximately 15 seconds
	if completeDeadline < 10*time.Second || completeDeadline > 16*time.Second {
		t.Fatalf("expected CompleteJob deadline ~15s, got %v", completeDeadline)
	}
}

// TestNewRepoWorker_GracefulCredentialLoading verifies initialization when credentials don't exist
// and when valid config is provided.
func TestNewRepoWorker_GracefulCredentialLoading(t *testing.T) {
	resMgr, _ := NewResourceManager(2)
	tmpDir := t.TempDir()

	repoCfg := config.RepoConfig{
		URL:               "https://github.com/owner/repo-a",
		ConfigDir:         tmpDir, // Empty dir without credentials
		MaxConcurrentJobs: 2,
	}

	worker, err := NewRepoWorker("repo-a", repoCfg, resMgr, nil)
	if err != nil {
		t.Fatalf("expected graceful NewRepoWorker with missing credentials, got error: %v", err)
	}

	if worker == nil {
		t.Fatal("expected non-nil worker")
	}

	if cap(worker.repoSem) != 2 {
		t.Fatalf("expected repoSem capacity 2, got %d", cap(worker.repoSem))
	}

	// Test invalid alias
	_, err = NewRepoWorker("", repoCfg, resMgr, nil)
	if err == nil {
		t.Fatal("expected error for empty alias")
	}

	// Test nil resMgr
	_, err = NewRepoWorker("repo-a", repoCfg, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil resMgr")
	}
}

// TestRepoWorker_StartAndStop verifies the worker start/stop lifecycle.
func TestRepoWorker_StartAndStop(t *testing.T) {
	resMgr, _ := NewResourceManager(1)
	mockBroker := &mockBrokerClient{
		msgQueue: make(chan *protocol.BrokerMessage, 1),
	}

	worker := &RepoWorker{
		repoAlias:    "test-repo",
		resMgr:       resMgr,
		brokerClient: mockBroker,
		repoSem:      make(chan struct{}, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pollingCtx, cancelPolling := context.WithCancel(context.Background())
	defer cancelPolling()

	if err := worker.Start(ctx, pollingCtx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Cannot start twice
	if err := worker.Start(ctx, pollingCtx); err == nil {
		t.Fatal("expected error starting already running worker")
	}

	// Test that worker.Stop() halts the worker cleanly without requiring external cancelPolling()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()

	if err := worker.Stop(stopCtx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

// TestRepoWorker_StopDrainsActiveJobs verifies that Stop awaits in-flight job completion
// before calling DeleteSession.
func TestRepoWorker_StopDrainsActiveJobs(t *testing.T) {
	resMgr, _ := NewResourceManager(1)
	msgQueue := make(chan *protocol.BrokerMessage, 1)

	var sessionDeleted bool
	var sessionDeleteMu sync.Mutex
	var jobFinished bool

	mockBroker := &mockBrokerClient{
		msgQueue: msgQueue,
		delFn: func(ctx context.Context) error {
			sessionDeleteMu.Lock()
			defer sessionDeleteMu.Unlock()
			sessionDeleted = true
			if !jobFinished {
				t.Error("DeleteSession was called before in-flight job finished")
			}
			return nil
		},
	}

	jobStarted := make(chan struct{})
	mockCoord := &mockCoordinator{
		executeFn: func(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error) {
			close(jobStarted)
			time.Sleep(100 * time.Millisecond)
			jobFinished = true
			return &protocol.CompleteJobRequest{PlanID: job.PlanID, JobID: job.JobID, Conclusion: "success"}, nil
		},
	}

	mockRun := &mockRunServiceClient{
		acquireFn: func(ctx context.Context, runServiceURL, runnerRequestID, billingOwnerID, token string) (*protocol.JobSpecification, error) {
			return &protocol.JobSpecification{PlanID: "p1", JobID: "j1"}, nil
		},
	}

	worker := &RepoWorker{
		repoAlias:        "drain-repo",
		resMgr:           resMgr,
		brokerClient:     mockBroker,
		runServiceClient: mockRun,
		coordinator:      mockCoord,
		repoSem:          make(chan struct{}, 1),
	}

	ctx := context.Background()
	pollingCtx := context.Background()

	if err := worker.Start(ctx, pollingCtx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	msgQueue <- &protocol.BrokerMessage{
		MessageID:   100,
		MessageType: "RunnerJobRequest",
		Body:        `{"runner_request_id":"req-drain"}`,
	}

	// Wait for job to begin executing
	select {
	case <-jobStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for job to start")
	}

	// Call Stop without canceling pollingCtx first
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelStop()

	if err := worker.Stop(stopCtx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	sessionDeleteMu.Lock()
	defer sessionDeleteMu.Unlock()
	if !sessionDeleted {
		t.Fatal("expected DeleteSession to be called")
	}
	if !jobFinished {
		t.Fatal("expected job to finish before Stop completed")
	}
}

// TestRepoWorker_DynamicTokenRefresh verifies that dispatchJob fetches updated tokens from brokerClient.
func TestRepoWorker_DynamicTokenRefresh(t *testing.T) {
	resMgr, _ := NewResourceManager(1)
	mockBroker := &mockBrokerClient{
		token: "refreshed-oauth-token-xyz",
	}

	var capturedToken string
	mockRun := &mockRunServiceClient{
		acquireFn: func(ctx context.Context, runServiceURL, runnerRequestID, billingOwnerID, token string) (*protocol.JobSpecification, error) {
			capturedToken = token
			return &protocol.JobSpecification{PlanID: "p1", JobID: "j1"}, nil
		},
	}

	worker := &RepoWorker{
		repoAlias:        "test-repo",
		resMgr:           resMgr,
		brokerClient:     mockBroker,
		runServiceClient: mockRun,
		token:            "initial-stale-token",
		repoSem:          make(chan struct{}, 1),
	}

	ref := JobRequestReference{
		RunnerRequestID: "req-1",
	}

	err := worker.dispatchJob(context.Background(), ref)
	if err != nil {
		t.Fatalf("dispatchJob failed: %v", err)
	}

	if capturedToken != "refreshed-oauth-token-xyz" {
		t.Fatalf("expected refreshed token, got %q", capturedToken)
	}
}

// TestRepoWorker_PanicAfterAcquireNotifiesCompleteJob verifies that if coordinator panics,
// CompleteJob is still invoked with conclusion="failure".
func TestRepoWorker_PanicAfterAcquireNotifiesCompleteJob(t *testing.T) {
	resMgr, _ := NewResourceManager(1)
	var completedWithFailure bool

	mockRun := &mockRunServiceClient{
		acquireFn: func(ctx context.Context, runServiceURL, runnerRequestID, billingOwnerID, token string) (*protocol.JobSpecification, error) {
			return &protocol.JobSpecification{PlanID: "plan-panic", JobID: "job-panic"}, nil
		},
		completeFn: func(ctx context.Context, runServiceURL, token string, req *protocol.CompleteJobRequest) error {
			if req.Conclusion == "failure" && req.JobID == "job-panic" {
				completedWithFailure = true
			}
			return nil
		},
	}

	mockCoord := &mockCoordinator{
		executeFn: func(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error) {
			panic("coordinator crashed during execution")
		},
	}

	worker := &RepoWorker{
		repoAlias:        "test-repo",
		resMgr:           resMgr,
		runServiceClient: mockRun,
		coordinator:      mockCoord,
		repoSem:          make(chan struct{}, 1),
	}

	ref := JobRequestReference{
		RunnerRequestID: "req-panic",
	}

	err := worker.executeWithGuards(context.Background(), ref, func() error {
		return worker.dispatchJob(context.Background(), ref)
	})

	if err == nil {
		t.Fatal("expected panic error")
	}

	if !completedWithFailure {
		t.Fatal("expected CompleteJob with conclusion='failure' to be delivered on coordinator panic")
	}
}
