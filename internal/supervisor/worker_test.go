package supervisor

import (
	"context"
	"strings"
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

	cancelPolling()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()

	if err := worker.Stop(stopCtx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}
