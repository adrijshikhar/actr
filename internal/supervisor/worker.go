package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver/host"
	"github.com/adrijshikhar/actr/internal/engine"
	"github.com/adrijshikhar/actr/internal/logger"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/protocol"
)

// JobRequestReference holds metadata describing a dispatched job message.
type JobRequestReference struct {
	RunnerRequestID string `json:"runner_request_id"`
	RunServiceURL   string `json:"run_service_url"`
	BillingOwnerID  string `json:"billing_owner_id"`
}

// BrokerClientInterface defines the broker messaging contract.
type BrokerClientInterface interface {
	CreateSession(ctx context.Context, agentName string, agentID int64, poolID int64) error
	GetMessage(ctx context.Context, runnerVersion, osName, archName string) (*protocol.BrokerMessage, error)
	AcknowledgeMessage(ctx context.Context, runnerRequestID string) error
	DeleteSession(ctx context.Context) error
}

// RunServiceClientInterface defines the run service contract for acquiring and completing jobs.
type RunServiceClientInterface interface {
	AcquireJob(ctx context.Context, runServiceURL, runnerRequestID, billingOwnerID, token string) (*protocol.JobSpecification, error)
	CompleteJob(ctx context.Context, runServiceURL, token string, req *protocol.CompleteJobRequest) error
}

// JobExecutor defines the contract for executing workflow jobs.
type JobExecutor interface {
	ExecuteJob(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error)
}

// RepoWorker manages isolated job polling, permit acquisition, and execution lifecycle for a single repository.
type RepoWorker struct {
	mu               sync.Mutex
	repoAlias        string
	repoCfg          config.RepoConfig
	resMgr           *ResourceManager
	monServer        *monitor.Server
	brokerClient     BrokerClientInterface
	runServiceClient RunServiceClientInterface
	coordinator      JobExecutor
	repoSem          chan struct{}
	workFolder       string
	serverURL        string
	token            string
	agentName        string
	agentID          int64
	poolID           int64
	running          bool
	pollCancel       context.CancelFunc
	jobWg            sync.WaitGroup
	doneCh           chan struct{}
}

// NewRepoWorker constructs a new RepoWorker instance for the given repository.
func NewRepoWorker(alias string, repoCfg config.RepoConfig, resMgr *ResourceManager, monServer *monitor.Server) (*RepoWorker, error) {
	if alias == "" {
		return nil, fmt.Errorf("repo alias cannot be empty")
	}
	if resMgr == nil {
		return nil, fmt.Errorf("resource manager cannot be nil")
	}

	maxJobs := repoCfg.MaxConcurrentJobs
	if maxJobs <= 0 {
		maxJobs = 1
	}

	w := &RepoWorker{
		repoAlias: alias,
		repoCfg:   repoCfg,
		resMgr:    resMgr,
		monServer: monServer,
		repoSem:   make(chan struct{}, maxJobs),
	}

	if repoCfg.ConfigDir != "" {
		creds, err := config.LoadCredentials(repoCfg.ConfigDir)
		if err == nil && creds != nil {
			w.token = creds.IDToken
			runnerCfg, _ := config.LoadRunnerConfig(repoCfg.ConfigDir)
			if runnerCfg != nil {
				w.workFolder = runnerCfg.WorkFolder
				w.serverURL = runnerCfg.ServerURL
				w.agentName = runnerCfg.AgentName
				w.agentID = runnerCfg.AgentID
				w.poolID = runnerCfg.PoolID
				brokerURL := runnerCfg.ServerURL
				if runnerCfg.UseV2Flow && runnerCfg.ServerURLV2 != "" {
					brokerURL = runnerCfg.ServerURLV2
				}
				rsaPath := filepath.Join(repoCfg.ConfigDir, ".credentials_rsaparams")
				if rsaBytes, err := os.ReadFile(rsaPath); err == nil {
					var rsaParams protocol.RSAParameters
					if err := json.Unmarshal(rsaBytes, &rsaParams); err == nil {
						if privKey, err := protocol.ParseRSAPrivateKey(&rsaParams); err == nil {
							w.brokerClient = protocol.NewBrokerClient(nil, brokerURL, creds.AuthorizationURL, creds.ClientID, privKey)
						}
					}
				}
			}
		}
	}

	if w.runServiceClient == nil {
		w.runServiceClient = protocol.NewRunServiceClient(nil)
	}

	if w.workFolder == "" {
		w.workFolder = "_work"
	}

	if w.coordinator == nil {
		var protoRunClient *protocol.RunServiceClient
		if rc, ok := w.runServiceClient.(*protocol.RunServiceClient); ok {
			protoRunClient = rc
		}
		coord := engine.NewCoordinator(host.NewDriver(), protoRunClient, w.workFolder).
			WithRunnerName(alias).
			WithRepositoryURL(repoCfg.URL)
		if monServer != nil {
			coord.SetMonitorServer(monServer)
		}
		if limits, err := repoCfg.ParseLimits(); err == nil && limits != nil {
			coord.WithLimits(limits)
		}
		w.coordinator = coord
	}

	return w, nil
}

// SetBrokerClient overrides the BrokerClient (useful for testing and mocking).
func (w *RepoWorker) SetBrokerClient(b BrokerClientInterface) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.brokerClient = b
}

// SetRunServiceClient overrides the RunServiceClient (useful for testing and mocking).
func (w *RepoWorker) SetRunServiceClient(r RunServiceClientInterface) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.runServiceClient = r
}

// SetCoordinator overrides the JobExecutor (useful for testing and mocking).
func (w *RepoWorker) SetCoordinator(c JobExecutor) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coordinator = c
}

// ActiveJobs returns the number of currently active jobs executing for this repo worker.
func (w *RepoWorker) ActiveJobs() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.repoSem)
}

// Start begins the repository worker polling loop in a background goroutine.
func (w *RepoWorker) Start(ctx context.Context, pollingCtx context.Context) error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return fmt.Errorf("worker %q is already running", w.repoAlias)
	}
	w.running = true
	w.doneCh = make(chan struct{})
	pollCtx, cancelPoll := context.WithCancel(pollingCtx)
	w.pollCancel = cancelPoll
	w.mu.Unlock()

	go func() {
		defer close(w.doneCh)
		w.runLoop(ctx, pollCtx)
		w.jobWg.Wait()
	}()

	return nil
}

// Stop halts the worker and cleans up any open broker session.
func (w *RepoWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return nil
	}
	if w.pollCancel != nil {
		w.pollCancel()
	}
	done := w.doneCh
	w.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}

	w.mu.Lock()
	w.running = false
	w.mu.Unlock()

	if w.brokerClient != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := w.brokerClient.DeleteSession(cleanupCtx); err != nil {
			logger.Warn("failed to delete broker session", "repo", w.repoAlias, "error", err)
		}
	}
	return nil
}

// executeWithGuards acquires a global execution permit from ResourceManager and releases it
// via RAII defer, capturing any step or coordinator panics so the worker daemon never crashes.
func (w *RepoWorker) executeWithGuards(ctx context.Context, reqRef JobRequestReference, fn func() error) (err error) {
	permit, err := w.resMgr.Acquire(ctx)
	if err != nil {
		return err
	}
	defer permit.Release()

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered panic: %v", r)
			logger.Error("recovered from panic in job runner", "repo", w.repoAlias, "error", r)
		}
	}()

	return fn()
}

// runLoop executes the polling loop for this worker, enforcing capacity gating, local repo concurrency,
// and background job dispatching.
func (w *RepoWorker) runLoop(ctx context.Context, pollingCtx context.Context) {
	runnerVersion := "3.0.0"
	osName := runnerOS()
	archName := runnerArch()

	if w.brokerClient != nil && w.agentName != "" {
		if err := w.brokerClient.CreateSession(ctx, w.agentName, w.agentID, w.poolID); err != nil {
			logger.Error("failed to create broker session", "repo", w.repoAlias, "error", err)
		}
	}

	for {
		select {
		case <-pollingCtx.Done():
			return
		case <-ctx.Done():
			return
		default:
		}

		// Capacity-Gated Polling: Pause if global capacity is saturated
		if w.resMgr.ActiveCount() >= w.resMgr.MaxConcurrentJobs() {
			select {
			case <-w.resMgr.SlotAvailable():
			case <-time.After(500 * time.Millisecond):
			case <-pollingCtx.Done():
				return
			case <-ctx.Done():
				return
			}
			continue
		}

		// Acquire local repo semaphore slot
		select {
		case w.repoSem <- struct{}{}:
		case <-pollingCtx.Done():
			return
		case <-ctx.Done():
			return
		}

		// Double-check global capacity before calling broker (in case permits filled up while waiting for repoSem)
		if w.resMgr.ActiveCount() >= w.resMgr.MaxConcurrentJobs() {
			<-w.repoSem
			select {
			case <-w.resMgr.SlotAvailable():
			case <-time.After(500 * time.Millisecond):
			case <-pollingCtx.Done():
				return
			case <-ctx.Done():
				return
			}
			continue
		}

		if w.brokerClient == nil {
			<-w.repoSem
			select {
			case <-pollingCtx.Done():
				return
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}

		msg, err := w.brokerClient.GetMessage(pollingCtx, runnerVersion, osName, archName)
		if err != nil {
			<-w.repoSem
			if w.brokerClient != nil && w.agentName != "" && (strings.Contains(err.Error(), "RunnerSessionInvalid") ||
				strings.Contains(err.Error(), "Runner session is invalid") ||
				strings.Contains(err.Error(), "no active session")) {
				logger.Warn("broker session invalid; recreating broker session", "repo", w.repoAlias)
				_ = w.brokerClient.CreateSession(ctx, w.agentName, w.agentID, w.poolID)
			}
			select {
			case <-pollingCtx.Done():
				return
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
			}
			continue
		}

		if msg == nil || (!strings.EqualFold(msg.MessageType, "RunnerJobRequest") && !strings.EqualFold(msg.MessageType, "PipelineAgentJobRequest")) {
			<-w.repoSem
			continue
		}

		var reqRef JobRequestReference
		if msg.Body != "" {
			_ = json.Unmarshal([]byte(msg.Body), &reqRef)
		}
		if reqRef.RunnerRequestID == "" {
			reqRef.RunnerRequestID = strconv.FormatInt(msg.MessageID, 10)
		}

		// Execute job in background goroutine with local slot held and jobWg tracking
		w.jobWg.Add(1)
		go func(ref JobRequestReference) {
			defer w.jobWg.Done()
			defer func() { <-w.repoSem }()
			_ = w.executeWithGuards(ctx, ref, func() error {
				return w.dispatchJob(ctx, ref)
			})
		}(reqRef)
	}
}

// dispatchJob acknowledges the broker message, claims the job from Run Service, runs execution
// through coordinator, and delivers completion with context.WithoutCancel.
func (w *RepoWorker) dispatchJob(ctx context.Context, ref JobRequestReference) (execErr error) {
	if w.brokerClient != nil {
		if err := w.brokerClient.AcknowledgeMessage(ctx, ref.RunnerRequestID); err != nil {
			logger.Error("failed to ack message", "repo", w.repoAlias, "error", err)
		}
	}

	if w.runServiceClient == nil {
		return nil
	}

	runServiceURL := ref.RunServiceURL
	if runServiceURL == "" {
		runServiceURL = w.serverURL
	}

	token := w.token
	if bt, ok := w.brokerClient.(interface{ Token() string }); ok {
		if t := bt.Token(); t != "" {
			token = t
		}
	}

	jobSpec, err := w.runServiceClient.AcquireJob(ctx, runServiceURL, ref.RunnerRequestID, ref.BillingOwnerID, token)
	if err != nil {
		return fmt.Errorf("acquire job: %w", err)
	}

	jobToken := token
	if jobSpec.JobToken != "" {
		jobToken = jobSpec.JobToken
	}

	var completeReq *protocol.CompleteJobRequest
	defer func() {
		if completeReq == nil {
			completeReq = &protocol.CompleteJobRequest{
				PlanID:     jobSpec.PlanID,
				JobID:      jobSpec.JobID,
				Conclusion: "failure",
			}
		}
		completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_ = w.runServiceClient.CompleteJob(completeCtx, runServiceURL, jobToken, completeReq)
	}()

	if w.coordinator != nil {
		completeReq, execErr = w.coordinator.ExecuteJob(ctx, jobSpec, runServiceURL, jobToken)
	}
	return execErr
}

func runnerOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}

func runnerArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "ARM64"
	case "amd64":
		return "X64"
	case "386":
		return "X86"
	case "arm":
		return "ARM"
	default:
		return strings.ToUpper(runtime.GOARCH)
	}
}
