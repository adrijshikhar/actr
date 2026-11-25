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
	"sync/atomic"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/adrijshikhar/actr/internal/driver/docker"
	"github.com/adrijshikhar/actr/internal/driver/host"
	"github.com/adrijshikhar/actr/internal/engine"
	"github.com/adrijshikhar/actr/internal/logger"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/adrijshikhar/actr/internal/telemetry"
	"github.com/adrijshikhar/actr/internal/vault"
	dockerclient "github.com/docker/docker/client"
)

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
	tracer           *telemetry.Manager
	repoSem          chan struct{}
	executingJobs    atomic.Int32
	workFolder       string
	serverURL        string
	token            string
	agentName        string
	agentID          int64
	poolID           int64
	running          bool
	stopping         bool
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
		if err != nil {
			logger.Warn("failed to load credentials", "repo", alias, "configDir", repoCfg.ConfigDir, "error", err)
		} else if creds != nil {
			w.token = creds.IDToken
			runnerCfg, err := config.LoadRunnerConfig(repoCfg.ConfigDir)
			if err != nil {
				logger.Warn("failed to load runner config", "repo", alias, "configDir", repoCfg.ConfigDir, "error", err)
			} else if runnerCfg != nil {
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
				rsaBytes, err := os.ReadFile(rsaPath)
				if err != nil {
					logger.Warn("failed to read RSA parameters", "repo", alias, "path", rsaPath, "error", err)
				} else {
					var rsaParams protocol.RSAParameters
					if err := json.Unmarshal(rsaBytes, &rsaParams); err != nil {
						logger.Warn("failed to unmarshal RSA parameters", "repo", alias, "error", err)
					} else if privKey, err := protocol.ParseRSAPrivateKey(&rsaParams); err != nil {
						logger.Warn("failed to parse RSA private key", "repo", alias, "error", err)
					} else {
						w.brokerClient = protocol.NewBrokerClient(nil, brokerURL, creds.AuthorizationURL, creds.ClientID, privKey)
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
	if !filepath.IsAbs(w.workFolder) && repoCfg.ConfigDir != "" {
		w.workFolder = filepath.Join(repoCfg.ConfigDir, w.workFolder)
	}

	if w.coordinator == nil {
		var protoRunClient *protocol.RunServiceClient
		if rc, ok := w.runServiceClient.(*protocol.RunServiceClient); ok {
			protoRunClient = rc
		}

		var primaryDriver driver.Driver = host.NewDriver()
		var dockerDrv driver.Driver
		if strings.EqualFold(repoCfg.Driver, "docker") {
			cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
			if err != nil {
				return nil, fmt.Errorf("initialize docker client for repo %q: %w", alias, err)
			}
			dockerDrv = docker.NewDriver(cli, true)
			primaryDriver = dockerDrv
		}

		coord := engine.NewCoordinator(primaryDriver, protoRunClient, w.workFolder).
			WithRunnerName(alias).
			WithRepoAlias(alias).
			WithRepositoryURL(repoCfg.URL)
		if dockerDrv != nil {
			coord.WithDockerDriver(dockerDrv)
		}
		if monServer != nil {
			coord.SetMonitorServer(monServer)
		}
		if limits, err := repoCfg.ParseLimits(); err != nil {
			logger.Warn("failed to parse repo limits", "repo", alias, "error", err)
		} else if limits != nil {
			coord.WithLimits(limits)
		}
		if w.tracer != nil {
			coord.WithTracer(w.tracer)
		}
		if vaultProv, err := vault.NewProviderFromConfig(&repoCfg.Vault, repoCfg.ConfigDir); err != nil {
			logger.Warn("failed to initialize vault provider", "repo", alias, "error", err)
		} else if vaultProv != nil {
			coord.WithVault(vaultProv)
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

func (w *RepoWorker) getBrokerClient() BrokerClientInterface {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.brokerClient
}

// WithTracer sets the OpenTelemetry Manager on the worker and its coordinator.
func (w *RepoWorker) WithTracer(tm *telemetry.Manager) *RepoWorker {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tracer = tm
	if c, ok := w.coordinator.(*engine.Coordinator); ok && tm != nil {
		c.WithTracer(tm)
	}
	return w
}

// SetRunServiceClient overrides the RunServiceClient (useful for testing and mocking).
func (w *RepoWorker) SetRunServiceClient(r RunServiceClientInterface) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.runServiceClient = r
}

func (w *RepoWorker) getRunServiceClient() RunServiceClientInterface {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.runServiceClient
}

// SetCoordinator overrides the JobExecutor (useful for testing and mocking).
func (w *RepoWorker) SetCoordinator(c JobExecutor) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coordinator = c
}

func (w *RepoWorker) getCoordinator() JobExecutor {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.coordinator
}

func (w *RepoWorker) getConnectionInfo() (serverURL, token, agentName string, agentID, poolID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.serverURL, w.token, w.agentName, w.agentID, w.poolID
}

// ActiveJobs returns the number of currently active jobs executing for this repo worker.
func (w *RepoWorker) ActiveJobs() int {
	if w == nil {
		return 0
	}
	return int(w.executingJobs.Load())
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
		defer func() {
			w.mu.Lock()
			w.running = false
			w.stopping = false
			w.mu.Unlock()
		}()
		w.runLoop(ctx, pollCtx)
		w.jobWg.Wait()
	}()

	return nil
}

// Stop halts the worker and cleans up any open broker session.
func (w *RepoWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	if !w.running || w.stopping {
		done := w.doneCh
		w.mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	w.stopping = true
	if w.pollCancel != nil {
		w.pollCancel()
	}
	done := w.doneCh
	broker := w.brokerClient
	w.mu.Unlock()

	var drainErr error
	select {
	case <-done:
	case <-ctx.Done():
		drainErr = ctx.Err()
	}

	if drainErr != nil {
		return drainErr
	}

	if broker != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := broker.DeleteSession(cleanupCtx); err != nil {
			logger.Warn("failed to delete broker session", "repo", w.repoAlias, "error", err)
		}
	}

	w.mu.Lock()
	w.running = false
	w.stopping = false
	w.mu.Unlock()

	return nil
}

// executeWithGuards acquires a global execution permit from ResourceManager and releases it
// via RAII defer, capturing any step or coordinator panics so the worker daemon never crashes.
func (w *RepoWorker) executeWithGuards(ctx context.Context, reqRef protocol.JobRequestReference, fn func() error) (err error) {
	permit, err := w.resMgr.Acquire(ctx)
	if err != nil {
		return err
	}
	return w.executeWithPermit(permit, fn)
}

func (w *RepoWorker) executeWithPermit(permit Permit, fn func() error) (err error) {
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

	_, _, agentName, agentID, poolID := w.getConnectionInfo()
	if broker := w.getBrokerClient(); broker != nil && agentName != "" {
		if err := broker.CreateSession(pollingCtx, agentName, agentID, poolID); err != nil {
			logger.Error("failed to create broker session", "repo", w.repoAlias, "error", err)
		}
	}

	const (
		minBackoff = 500 * time.Millisecond
		maxBackoff = 10 * time.Second
	)
	backoff := minBackoff

	for {
		select {
		case <-pollingCtx.Done():
			return
		case <-ctx.Done():
			return
		default:
		}

		broker := w.getBrokerClient()
		if broker == nil {
			select {
			case <-pollingCtx.Done():
				return
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
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

		// Wait until global execution capacity is available before polling
		if err := w.resMgr.WaitAvailable(pollingCtx); err != nil {
			<-w.repoSem
			return
		}

		msg, err := broker.GetMessage(pollingCtx, runnerVersion, osName, archName)
		if err != nil {
			<-w.repoSem
			if pollingCtx.Err() != nil || ctx.Err() != nil {
				return
			}
			_, _, agentName, agentID, poolID = w.getConnectionInfo()
			if agentName != "" && (strings.Contains(err.Error(), "RunnerSessionInvalid") ||
				strings.Contains(err.Error(), "Runner session is invalid") ||
				strings.Contains(err.Error(), "no active session")) {
				logger.Warn("broker session invalid; recreating broker session", "repo", w.repoAlias)
				if sessErr := broker.CreateSession(pollingCtx, agentName, agentID, poolID); sessErr != nil {
					logger.Warn("failed to recreate broker session", "repo", w.repoAlias, "error", sessErr)
				}
			}
			select {
			case <-pollingCtx.Done():
				return
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		backoff = minBackoff

		if msg == nil || (!strings.EqualFold(msg.MessageType, "RunnerJobRequest") && !strings.EqualFold(msg.MessageType, "PipelineAgentJobRequest")) {
			<-w.repoSem
			if msg == nil {
				select {
				case <-pollingCtx.Done():
					return
				case <-ctx.Done():
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
			continue
		}

		var reqRef protocol.JobRequestReference
		if msg.Body != "" {
			if err := json.Unmarshal([]byte(msg.Body), &reqRef); err != nil {
				logger.Warn("failed to unmarshal job request reference", "repo", w.repoAlias, "messageID", msg.MessageID, "error", err)
			}
		}
		if reqRef.RunnerRequestID == "" {
			reqRef.RunnerRequestID = strconv.FormatInt(msg.MessageID, 10)
		}

		// Acquire global execution permit after receiving job and before acknowledging
		permit, err := w.resMgr.Acquire(ctx)
		if err != nil {
			<-w.repoSem
			return
		}

		w.executingJobs.Add(1)
		w.jobWg.Add(1)
		go func(ref protocol.JobRequestReference, p Permit) {
			defer w.jobWg.Done()
			defer w.executingJobs.Add(-1)
			defer func() { <-w.repoSem }()
			_ = w.executeWithPermit(p, func() error {
				return w.dispatchJob(ctx, ref)
			})
		}(reqRef, permit)
	}
}

// dispatchJob acknowledges the broker message, claims the job from Run Service, runs execution
// through coordinator, and delivers completion with context.WithoutCancel.
func (w *RepoWorker) dispatchJob(ctx context.Context, ref protocol.JobRequestReference) (execErr error) {
	broker := w.getBrokerClient()
	if broker != nil {
		if err := broker.AcknowledgeMessage(ctx, ref.RunnerRequestID); err != nil {
			logger.Error("failed to ack message", "repo", w.repoAlias, "error", err)
		}
	}

	runClient := w.getRunServiceClient()
	if runClient == nil {
		return nil
	}

	serverURL, token, _, _, _ := w.getConnectionInfo()
	runServiceURL := ref.RunServiceURL
	if runServiceURL == "" {
		runServiceURL = serverURL
	}

	if bt, ok := broker.(interface{ Token() string }); ok {
		if t := bt.Token(); t != "" {
			token = t
		}
	}

	jobSpec, err := runClient.AcquireJob(ctx, runServiceURL, ref.RunnerRequestID, ref.BillingOwnerID, token)
	if err != nil {
		return fmt.Errorf("acquire job: %w", err)
	}
	if jobSpec == nil {
		return fmt.Errorf("acquire job returned nil specification")
	}

	jobToken := token
	if jobSpec.JobToken != "" {
		jobToken = jobSpec.JobToken
	}

	var completeReq *protocol.CompleteJobRequest
	defer func() {
		if jobSpec == nil {
			return
		}
		if completeReq == nil {
			completeReq = &protocol.CompleteJobRequest{
				PlanID:     jobSpec.PlanID,
				JobID:      jobSpec.JobID,
				Conclusion: "failure",
			}
		}
		completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_ = runClient.CompleteJob(completeCtx, runServiceURL, jobToken, completeReq)
	}()

	if coord := w.getCoordinator(); coord != nil {
		completeReq, execErr = coord.ExecuteJob(ctx, jobSpec, runServiceURL, jobToken)
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
