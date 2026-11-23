package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adrijshikhar/actr/internal/action"
	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/adrijshikhar/actr/internal/telemetry"
	"github.com/adrijshikhar/actr/internal/vault"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Coordinator orchestrates job execution across runner drivers,
// managing step lifecycles, environment variables, command files,
// and background lease renewals with the Run Service.
type Coordinator struct {
	hostDriver        driver.Driver
	dockerDriver      driver.Driver
	limits            *config.LimitsConfig
	runClient         *protocol.RunServiceClient
	workDir           string
	runnerName        string
	renewInterval     time.Duration
	stdout            io.Writer
	stderr            io.Writer
	stepLoggerEnabled bool
	colorEnabled      bool
	monServer         *monitor.Server
	sampler           *monitor.TelemetrySampler
	repoURL           string
	defaultRepo       string
	repoAlias         string
	tracer            *telemetry.Manager
	actionResolver    action.Resolver
	nodeDetector      action.NodeDetector
	vault             vault.Provider
	mu                sync.RWMutex
}

// PostStepHook represents a queued teardown hook for an action step.
type PostStepHook struct {
	StepID     string
	StepName   string
	ActionDir  string
	PostScript string
	PostIf     string
	StepFailed bool
	Env        map[string]string
	State      map[string]string
	Inputs     map[string]string
	Context    map[string]any
}

// NewCoordinator creates a new Coordinator with the provided driver, RunServiceClient, and base working directory.
func NewCoordinator(d driver.Driver, runClient *protocol.RunServiceClient, workDir string) *Coordinator {
	if workDir == "" {
		workDir = "_work"
	}
	tracer, _ := telemetry.NewManager(context.Background(), "actr")
	return &Coordinator{
		hostDriver:        d,
		runClient:         runClient,
		workDir:           workDir,
		runnerName:        "actr",
		renewInterval:     50 * time.Second,
		stdout:            os.Stdout,
		stderr:            os.Stderr,
		stepLoggerEnabled: true,
		colorEnabled:      os.Getenv("NO_COLOR") == "",
		tracer:            tracer,
	}
}

// WithRepoAlias configures the repository alias (e.g. from runner config) for this Coordinator.
func (c *Coordinator) WithRepoAlias(alias string) *Coordinator {
	c.repoAlias = alias
	return c
}

// WithTracer overrides the OpenTelemetry tracer manager on the Coordinator.
func (c *Coordinator) WithTracer(tm *telemetry.Manager) *Coordinator {
	c.tracer = tm
	return c
}

// Shutdown flushes and terminates the coordinator's telemetry tracer exporter.
func (c *Coordinator) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tracer != nil {
		return c.tracer.Shutdown(ctx)
	}
	return nil
}

// WithDockerDriver configures the driver used for container-based jobs.
func (c *Coordinator) WithDockerDriver(d driver.Driver) *Coordinator {
	c.dockerDriver = d
	return c
}

// WithVault configures the secret vault provider used for resolving secret environment variables.
func (c *Coordinator) WithVault(v vault.Provider) *Coordinator {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.vault = v
	return c
}

func (c *Coordinator) getVault() vault.Provider {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.vault
}

// WithActionResolver sets a custom action resolver on the Coordinator.
func (c *Coordinator) WithActionResolver(resolver action.Resolver) *Coordinator {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.actionResolver = resolver
	return c
}

// WithNodeDetector sets a custom node runtime detector on the Coordinator.
func (c *Coordinator) WithNodeDetector(detector action.NodeDetector) *Coordinator {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodeDetector = detector
	return c
}

func (c *Coordinator) getActionResolver() action.Resolver {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.actionResolver != nil {
		return c.actionResolver
	}
	return action.NewActionResolver(c.workDir, nil)
}

func (c *Coordinator) getNodeDetector() action.NodeDetector {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.nodeDetector != nil {
		return c.nodeDetector
	}
	return action.NewHostNodeDetector("")
}

// WithLimits configures resource governance limits applied to jobs.
func (c *Coordinator) WithLimits(limits *config.LimitsConfig) *Coordinator {
	c.limits = limits
	return c
}

// resolveDriver dynamically determines whether to use the host driver or docker driver for the given job.
func (c *Coordinator) resolveDriver(job *protocol.JobSpecification) (driver.Driver, error) {
	if job != nil && job.Container != nil {
		if c.dockerDriver == nil {
			return nil, fmt.Errorf("job requires container image %q but Docker driver is not enabled/available", job.Container.Image)
		}
		return c.dockerDriver, nil
	}
	if c.hostDriver == nil {
		return nil, errors.New("nil driver")
	}
	return c.hostDriver, nil
}

// WithRepositoryURL configures the runner's GitHub URL (e.g. from runner config).
func (c *Coordinator) WithRepositoryURL(repoURL string) *Coordinator {
	c.repoURL = repoURL
	c.defaultRepo = extractRepoFromURL(repoURL)
	return c
}

func extractRepoFromURL(rawURL string) string {
	cleaned := strings.TrimSpace(rawURL)
	cleaned = strings.TrimSuffix(cleaned, ".git")
	cleaned = strings.TrimSuffix(cleaned, "/")
	if idx := strings.Index(cleaned, "github.com/"); idx != -1 {
		return cleaned[idx+len("github.com/"):]
	}
	parts := strings.Split(cleaned, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return ""
}

// WithRunnerName overrides the default runner name reported in RUNNER_NAME.
func (c *Coordinator) WithRunnerName(name string) *Coordinator {
	c.runnerName = name
	return c
}

// WithRenewInterval configures the frequency of background job lease renewals.
func (c *Coordinator) WithRenewInterval(interval time.Duration) *Coordinator {
	c.renewInterval = interval
	return c
}

// WithOutput configures custom writers for step standard output and error streams.
func (c *Coordinator) WithOutput(stdout, stderr io.Writer) *Coordinator {
	c.stdout = stdout
	c.stderr = stderr
	return c
}

// WithStepLogger configures whether step output is formatted and prefixed.
func (c *Coordinator) WithStepLogger(enabled bool) *Coordinator {
	c.stepLoggerEnabled = enabled
	return c
}

// WithColor configures whether ANSI color codes are used in step formatting.
func (c *Coordinator) WithColor(enabled bool) *Coordinator {
	c.colorEnabled = enabled
	return c
}

// SetMonitorServer configures the IPC monitor server and initializes the telemetry sampler.
func (c *Coordinator) SetMonitorServer(srv *monitor.Server) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.monServer = srv
	if srv != nil {
		c.sampler = monitor.NewTelemetrySampler()
	} else {
		c.sampler = nil
	}
}

// Sampler returns the active TelemetrySampler if monitor server is set.
func (c *Coordinator) Sampler() *monitor.TelemetrySampler {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sampler
}

// NotifyStepStart publishes an EventStepStart to the monitor server if configured.
func (c *Coordinator) NotifyStepStart(jobID, stepID, stepName string) {
	c.mu.RLock()
	srv := c.monServer
	c.mu.RUnlock()

	if srv != nil {
		srv.Publish(monitor.Event{
			Type:      monitor.EventStepStart,
			JobID:     jobID,
			RepoAlias: c.repoAlias,
			StepID:    stepID,
			Step: &monitor.StepInfo{
				ID:        stepID,
				Name:      stepName,
				Status:    monitor.StatusRunning,
				StartedAt: time.Now(),
			},
		})
	}
}

// NotifyStepLog appends a log line to the monitor server for the given step.
func (c *Coordinator) NotifyStepLog(stepID, line string) {
	c.NotifyJobStepLog("", stepID, line)
}

// NotifyJobStepLog appends a log line to the monitor server for the given job and step.
func (c *Coordinator) NotifyJobStepLog(jobID, stepID, line string) {
	c.mu.RLock()
	srv := c.monServer
	c.mu.RUnlock()

	if srv != nil {
		srv.AppendJobStepLog(jobID, stepID, line)
	}
}

// NotifyStepFinish publishes an EventStepFinish to the monitor server if configured.
func (c *Coordinator) NotifyStepFinish(jobID, stepID, stepName string, status monitor.StepStatus, exitCode ...int) {
	c.mu.RLock()
	srv := c.monServer
	c.mu.RUnlock()

	code := 0
	if len(exitCode) > 0 {
		code = exitCode[0]
	} else if status == monitor.StatusFailed {
		code = 1
	}

	if srv != nil {
		srv.Publish(monitor.Event{
			Type:      monitor.EventStepFinish,
			JobID:     jobID,
			RepoAlias: c.repoAlias,
			StepID:    stepID,
			Step: &monitor.StepInfo{
				ID:       stepID,
				Name:     stepName,
				Status:   status,
				ExitCode: code,
				EndedAt:  time.Now(),
			},
		})
	}
}

func newFailedCompleteJobRequest(job *protocol.JobSpecification) *protocol.CompleteJobRequest {
	jobID := ""
	planID := ""
	if job != nil {
		jobID = job.JobID
		planID = job.PlanID
	}
	if jobID == "" {
		jobID = "job"
	}
	return &protocol.CompleteJobRequest{
		PlanID:      planID,
		JobID:       jobID,
		Conclusion:  "failure",
		Outputs:     make(map[string]string),
		StepResults: make([]protocol.StepResult, 0),
	}
}

// ExecuteJob runs all steps of the given JobSpecification sequentially, maintaining cumulative
// environment updates, aggregating step outputs, managing background lease renewal, and returning
// the CompleteJobRequest summary.
func (c *Coordinator) ExecuteJob(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (resp *protocol.CompleteJobRequest, err error) {
	if job == nil {
		return nil, errors.New("nil job specification")
	}

	d, err := c.resolveDriver(job)
	if err != nil {
		return newFailedCompleteJobRequest(job), err
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if c.limits != nil && c.limits.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.limits.Timeout)
		defer cancel()
	}

	jobID := job.JobID
	if jobID == "" {
		jobID = job.ID
	}
	if jobID == "" {
		jobID = "job"
	}
	jobDisplayName := job.JobName
	if jobDisplayName == "" {
		jobDisplayName = jobID
	}

	var workflowName, repo, owner string
	if job.Variables != nil {
		workflowName = job.Variables["GITHUB_WORKFLOW"]
		repo = job.Variables["GITHUB_REPOSITORY"]
		owner = job.Variables["GITHUB_REPOSITORY_OWNER"]
	}
	if job.ContextData != nil {
		if m, ok := parseContextData(job.ContextData).(map[string]any); ok {
			if gh, ok := m["github"].(map[string]any); ok {
				if workflowName == "" && gh["workflow"] != nil {
					workflowName = fmt.Sprint(gh["workflow"])
				}
				if repo == "" && gh["repository"] != nil {
					repo = fmt.Sprint(gh["repository"])
				}
				if owner == "" && gh["repository_owner"] != nil {
					owner = fmt.Sprint(gh["repository_owner"])
				}
			}
		}
	}
	if repo == "" {
		repo = c.defaultRepo
	}
	if repo == "" {
		repo = c.repoAlias
	}

	jobStartTime := time.Now()
	var jobSpan trace.Span
	if c.tracer != nil {
		ctx, jobSpan = c.tracer.StartJobSpan(ctx, jobID, workflowName, repo, owner)
		defer jobSpan.End()
	}

	c.mu.RLock()
	monSrv := c.monServer
	c.mu.RUnlock()

	if monSrv != nil {
		jobInfo := &monitor.JobInfo{
			ID:        jobID,
			Name:      jobDisplayName,
			Workflow:  workflowName,
			Repo:      repo,
			RepoAlias: c.repoAlias,
			RepoURL:   c.repoURL,
			Owner:     owner,
			Status:    monitor.StatusRunning,
			StartedAt: time.Now(),
			Steps:     make([]*monitor.StepInfo, 0, len(job.Steps)),
		}
		for i, st := range job.Steps {
			jobInfo.Steps = append(jobInfo.Steps, &monitor.StepInfo{
				ID:     st.ID,
				Name:   st.Name,
				Number: i + 1,
				Status: monitor.StatusQueued,
			})
		}
		driverName := "host"
		if job.Container != nil {
			driverName = "docker"
		}
		monSrv.Publish(monitor.Event{
			Type:      monitor.EventJobStart,
			JobID:     jobID,
			RepoAlias: c.repoAlias,
			Job:       jobInfo,
			Driver:    driverName,
		})
	}

	conclusion := "success"
	defer func() {
		jobDuration := time.Since(jobStartTime)
		if c.tracer != nil {
			if jobSpan != nil {
				if conclusion != "success" {
					c.tracer.RecordSpanError(jobSpan, fmt.Errorf("job failed with status %s", conclusion))
				} else {
					jobSpan.SetStatus(codes.Ok, "")
				}
			}
			c.tracer.RecordJobCompletion(ctx, jobID, workflowName, repo, owner, conclusion, jobDuration.Seconds())
		}

		c.mu.RLock()
		s := c.monServer
		c.mu.RUnlock()
		if s != nil {
			finalStatus := monitor.StatusSuccess
			if conclusion != "success" {
				finalStatus = monitor.StatusFailed
			}
			driverName := "host"
			if job.Container != nil {
				driverName = "docker"
			}
			s.Publish(monitor.Event{
				Type:      monitor.EventJobFinish,
				JobID:     jobID,
				RepoAlias: c.repoAlias,
				Driver:    driverName,
				Job: &monitor.JobInfo{
					ID:        jobID,
					Name:      jobDisplayName,
					Workflow:  workflowName,
					Repo:      repo,
					RepoAlias: c.repoAlias,
					RepoURL:   c.repoURL,
					Owner:     owner,
					Status:    finalStatus,
					EndedAt:   time.Now(),
				},
			})
		}
	}()

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = d.CleanupJob(cleanupCtx)
	}()

	baseWorkDir, err := filepath.Abs(c.workDir)
	if err != nil {
		conclusion = "failure"
		return newFailedCompleteJobRequest(job), fmt.Errorf("resolve base work directory: %w", err)
	}
	if err := os.MkdirAll(baseWorkDir, 0o755); err != nil {
		conclusion = "failure"
		return newFailedCompleteJobRequest(job), fmt.Errorf("create base work directory: %w", err)
	}

	runnerTemp := filepath.Join(baseWorkDir, "_temp")
	if err := os.MkdirAll(runnerTemp, 0o755); err != nil {
		conclusion = "failure"
		return newFailedCompleteJobRequest(job), fmt.Errorf("create runner temp directory: %w", err)
	}

	runnerToolCache := os.Getenv("RUNNER_TOOL_CACHE")
	if runnerToolCache == "" {
		runnerToolCache = filepath.Join(baseWorkDir, "_tool")
	}
	if absToolCache, err := filepath.Abs(runnerToolCache); err == nil {
		runnerToolCache = absToolCache
	}
	if err := os.MkdirAll(runnerToolCache, 0o755); err != nil {
		conclusion = "failure"
		return newFailedCompleteJobRequest(job), fmt.Errorf("create runner tool cache directory: %w", err)
	}

	jobDirName := job.JobName
	if jobDirName == "" {
		jobDirName = job.JobID
	}
	if jobDirName == "" {
		jobDirName = "workspace"
	}
	jobWorkspace := filepath.Join(baseWorkDir, jobDirName)
	_ = os.RemoveAll(jobWorkspace)
	if err := os.MkdirAll(jobWorkspace, 0o755); err != nil {
		conclusion = "failure"
		return newFailedCompleteJobRequest(job), fmt.Errorf("create job workspace: %w", err)
	}

	// Initialize cumulative environment
	cumulativeEnv := make(map[string]string)
	if hostPath := os.Getenv("PATH"); hostPath != "" {
		cumulativeEnv["PATH"] = hostPath
	}
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}
	standardToolDirs := []string{
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
	}
	if homeDir != "" {
		standardToolDirs = append(standardToolDirs,
			filepath.Join(homeDir, ".cargo", "bin"),
			filepath.Join(homeDir, ".bun", "bin"),
			filepath.Join(homeDir, ".local", "bin"),
			filepath.Join(homeDir, ".local", "share", "fnm", "aliases", "default", "bin"),
			filepath.Join(homeDir, ".fnm", "current", "bin"),
		)
	}
	currentPath := cumulativeEnv["PATH"]
	for _, p := range standardToolDirs {
		if _, err := os.Stat(p); err == nil {
			if !strings.Contains(":"+currentPath+":", ":"+p+":") {
				currentPath = p + ":" + currentPath
			}
		}
	}
	cumulativeEnv["PATH"] = currentPath

	if job.Variables != nil {
		for k, v := range job.Variables {
			cumulativeEnv[k] = v
			lowerK := strings.ToLower(k)
			if strings.HasPrefix(lowerK, "system.github.") {
				sub := lowerK[len("system.github."):]
				switch sub {
				case "runid":
					sub = "run_id"
				case "runnumber":
					sub = "run_number"
				case "runattempt":
					sub = "run_attempt"
				}
				upperKey := "GITHUB_" + strings.ToUpper(sub)
				if cumulativeEnv[upperKey] == "" {
					cumulativeEnv[upperKey] = v
				}
			} else if strings.HasPrefix(lowerK, "system.") {
				sub := lowerK[len("system."):]
				switch sub {
				case "runid", "run_id":
					if cumulativeEnv["GITHUB_RUN_ID"] == "" {
						cumulativeEnv["GITHUB_RUN_ID"] = v
					}
				case "runnumber", "run_number":
					if cumulativeEnv["GITHUB_RUN_NUMBER"] == "" {
						cumulativeEnv["GITHUB_RUN_NUMBER"] = v
					}
				case "runattempt", "run_attempt":
					if cumulativeEnv["GITHUB_RUN_ATTEMPT"] == "" {
						cumulativeEnv["GITHUB_RUN_ATTEMPT"] = v
					}
				}
			}
		}
	}

	if cumulativeEnv["GITHUB_REPOSITORY"] == "" && c.defaultRepo != "" {
		cumulativeEnv["GITHUB_REPOSITORY"] = c.defaultRepo
	}
	if cumulativeEnv["GITHUB_EVENT_NAME"] == "" {
		cumulativeEnv["GITHUB_EVENT_NAME"] = "workflow_dispatch"
	}
	if cumulativeEnv["GITHUB_TOKEN"] == "" && strings.TrimSpace(token) != "" {
		cumulativeEnv["GITHUB_TOKEN"] = token
	}

	for _, ep := range job.Endpoints {
		if strings.EqualFold(ep.Name, "SystemVssConnection") || strings.EqualFold(ep.Type, "SystemVssConnection") {
			if ep.URL != "" && cumulativeEnv["ACTIONS_RUNTIME_URL"] == "" {
				cumulativeEnv["ACTIONS_RUNTIME_URL"] = strings.TrimRight(ep.URL, "/") + "/"
			}
			if ep.Authorization != nil && ep.Authorization.Parameters != nil {
				for _, paramKey := range []string{"AccessToken", "accesstoken", "Token", "token"} {
					if tok, ok := ep.Authorization.Parameters[paramKey]; ok && tok != "" {
						if cumulativeEnv["ACTIONS_RUNTIME_TOKEN"] == "" {
							cumulativeEnv["ACTIONS_RUNTIME_TOKEN"] = tok
						}
						break
					}
				}
			}
		}
		for k, v := range ep.Data {
			if strings.EqualFold(k, "ResultsServiceUrl") && v != "" {
				cumulativeEnv["ACTIONS_RESULTS_URL"] = v
			} else if strings.EqualFold(k, "CacheServerUrl") && v != "" {
				cumulativeEnv["ACTIONS_CACHE_URL"] = v
			} else if strings.EqualFold(k, "GenerateIdTokenUrl") && v != "" {
				cumulativeEnv["ACTIONS_ID_TOKEN_REQUEST_URL"] = v
			}
		}
	}

	if cumulativeEnv["ACTIONS_RUNTIME_TOKEN"] == "" && strings.TrimSpace(token) != "" {
		cumulativeEnv["ACTIONS_RUNTIME_TOKEN"] = token
	}
	if cumulativeEnv["ACTIONS_RUNTIME_URL"] == "" && strings.TrimSpace(runServiceURL) != "" {
		cumulativeEnv["ACTIONS_RUNTIME_URL"] = strings.TrimRight(strings.TrimSpace(runServiceURL), "/") + "/"
	}
	if cumulativeEnv["ACTIONS_CACHE_URL"] == "" && cumulativeEnv["ACTIONS_RUNTIME_URL"] != "" {
		cumulativeEnv["ACTIONS_CACHE_URL"] = cumulativeEnv["ACTIONS_RUNTIME_URL"]
	}
	var parsedContext map[string]any
	if job.ContextData != nil {
		if m, ok := parseContextData(job.ContextData).(map[string]any); ok {
			parsedContext = m
		}
	}
	if parsedContext == nil {
		parsedContext = make(map[string]any)
	}
	ghCtx, _ := parsedContext["github"].(map[string]any)
	if ghCtx == nil {
		ghCtx = make(map[string]any)
		parsedContext["github"] = ghCtx
	}
	githubEnvPairs := []struct {
		ctxKey string
		envKey string
	}{
		{"token", "GITHUB_TOKEN"},
		{"sha", "GITHUB_SHA"},
		{"ref", "GITHUB_REF"},
		{"ref_name", "GITHUB_REF_NAME"},
		{"ref_type", "GITHUB_REF_TYPE"},
		{"repository", "GITHUB_REPOSITORY"},
		{"repository_owner", "GITHUB_REPOSITORY_OWNER"},
		{"event_name", "GITHUB_EVENT_NAME"},
		{"server_url", "GITHUB_SERVER_URL"},
		{"api_url", "GITHUB_API_URL"},
		{"graphql_url", "GITHUB_GRAPHQL_URL"},
		{"run_id", "GITHUB_RUN_ID"},
		{"run_number", "GITHUB_RUN_NUMBER"},
		{"run_attempt", "GITHUB_RUN_ATTEMPT"},
		{"actor", "GITHUB_ACTOR"},
		{"workflow", "GITHUB_WORKFLOW"},
		{"workflow_ref", "GITHUB_WORKFLOW_REF"},
		{"job", "GITHUB_JOB"},
	}
	for _, pair := range githubEnvPairs {
		if ghCtx[pair.ctxKey] == nil && cumulativeEnv[pair.envKey] != "" {
			ghCtx[pair.ctxKey] = cumulativeEnv[pair.envKey]
		}
		if cumulativeEnv[pair.envKey] == "" && ghCtx[pair.ctxKey] != nil {
			cumulativeEnv[pair.envKey] = fmt.Sprint(ghCtx[pair.ctxKey])
		}
	}

	if cumulativeEnv["GITHUB_SERVER_URL"] == "" {
		cumulativeEnv["GITHUB_SERVER_URL"] = "https://github.com"
	}
	if cumulativeEnv["GITHUB_API_URL"] == "" {
		cumulativeEnv["GITHUB_API_URL"] = "https://api.github.com"
	}
	if cumulativeEnv["GITHUB_GRAPHQL_URL"] == "" {
		cumulativeEnv["GITHUB_GRAPHQL_URL"] = "https://api.github.com/graphql"
	}
	if ghCtx["server_url"] == nil {
		ghCtx["server_url"] = cumulativeEnv["GITHUB_SERVER_URL"]
	}
	if ghCtx["api_url"] == nil {
		ghCtx["api_url"] = cumulativeEnv["GITHUB_API_URL"]
	}
	if ghCtx["graphql_url"] == nil {
		ghCtx["graphql_url"] = cumulativeEnv["GITHUB_GRAPHQL_URL"]
	}

	if cumulativeEnv["ACTIONS_RESULTS_URL"] == "" {
		serverURL := cumulativeEnv["GITHUB_SERVER_URL"]
		if serverURL == "" || serverURL == "https://github.com" {
			cumulativeEnv["ACTIONS_RESULTS_URL"] = "https://results-receiver.actions.githubusercontent.com/"
		} else if cumulativeEnv["ACTIONS_RUNTIME_URL"] != "" {
			cumulativeEnv["ACTIONS_RESULTS_URL"] = cumulativeEnv["ACTIONS_RUNTIME_URL"]
		}
	}
	runnerName := c.runnerName
	if runnerName == "" {
		runnerName = "actr"
	}
	if job.Variables != nil {
		if rn, ok := job.Variables["RUNNER_NAME"]; ok && rn != "" {
			runnerName = rn
		}
	}

	cumulativeEnv["RUNNER_NAME"] = runnerName
	cumulativeEnv["RUNNER_OS"] = runnerOS()
	cumulativeEnv["RUNNER_ARCH"] = runnerArch()
	cumulativeEnv["RUNNER_WORKSPACE"] = baseWorkDir
	cumulativeEnv["RUNNER_TEMP"] = runnerTemp
	cumulativeEnv["RUNNER_TOOL_CACHE"] = runnerToolCache
	cumulativeEnv["AGENT_TOOLSDIRECTORY"] = runnerToolCache
	cumulativeEnv["RUNNER_ENVIRONMENT"] = "self-hosted"
	if u := os.Getenv("USER"); u != "" {
		cumulativeEnv["RUNNER_USER"] = u
	}
	cumulativeEnv["GITHUB_WORKSPACE"] = jobWorkspace
	cumulativeEnv["CI"] = "true"
	cumulativeEnv["GITHUB_ACTIONS"] = "true"
	if job.JobName != "" {
		cumulativeEnv["GITHUB_JOB"] = job.JobName
	} else if job.JobID != "" {
		cumulativeEnv["GITHUB_JOB"] = job.JobID
	}

	runnerCtx, _ := parsedContext["runner"].(map[string]any)
	if runnerCtx == nil {
		runnerCtx = make(map[string]any)
		parsedContext["runner"] = runnerCtx
	}
	runnerCtx["name"] = runnerName
	runnerCtx["os"] = runnerOS()
	runnerCtx["arch"] = runnerArch()
	runnerCtx["temp"] = runnerTemp
	runnerCtx["workspace"] = baseWorkDir
	runnerCtx["tool_cache"] = runnerToolCache
	runnerCtx["environment"] = "self-hosted"

	if ghCtx["workspace"] == nil {
		ghCtx["workspace"] = jobWorkspace
	}
	if ghCtx["run_id"] == nil && cumulativeEnv["GITHUB_RUN_ID"] != "" {
		ghCtx["run_id"] = cumulativeEnv["GITHUB_RUN_ID"]
	}

	if job.Environment != nil {
		for k, v := range job.Environment {
			cumulativeEnv[k] = evaluateValue(v, parsedContext)
		}
	}
	parsedContext["env"] = cumulativeEnv

	if workflowName == "" && job.Variables != nil {
		workflowName = job.Variables["GITHUB_WORKFLOW"]
	}
	jobCtx := &driver.JobContext{
		JobID:          jobID,
		WorkflowName:   workflowName,
		JobDisplayName: jobDisplayName,
		WorkspaceDir:   jobWorkspace,
		TempDir:        runnerTemp,
		Container:      job.Container,
		Environment:    cumulativeEnv,
		Limits:         c.limits,
	}

	if err := d.PrepareJob(ctx, jobCtx); err != nil {
		conclusion = "failure"
		return newFailedCompleteJobRequest(job), fmt.Errorf("prepare job: %w", err)
	}

	// Start background lease renew ticker if RunService is configured
	renewInterval := c.renewInterval
	if renewInterval <= 0 {
		renewInterval = 50 * time.Second
	}

	renewCtx, cancelRenew := context.WithCancel(ctx)
	var renewWg sync.WaitGroup
	if c.runClient != nil && strings.TrimSpace(runServiceURL) != "" && strings.TrimSpace(job.PlanID) != "" && strings.TrimSpace(job.JobID) != "" {
		renewWg.Add(1)
		go func() {
			defer renewWg.Done()
			ticker := time.NewTicker(renewInterval)
			defer ticker.Stop()
			for {
				select {
				case <-renewCtx.Done():
					return
				case <-ticker.C:
					reqCtx, reqCancel := context.WithTimeout(renewCtx, 30*time.Second)
					_, _ = c.runClient.RenewJob(reqCtx, runServiceURL, job.PlanID, job.JobID, token)
					reqCancel()
				}
			}
		}()
	}
	defer func() {
		cancelRenew()
		renewWg.Wait()
	}()

	stepResults := make([]protocol.StepResult, 0, len(job.Steps))
	outputs := make(map[string]string)
	conclusion = "success"
	var postHooks []PostStepHook

	stdout := c.stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	jobMasker := NewSecretMasker()
	if job.JobToken != "" {
		jobMasker.AddMask(job.JobToken)
	}
	if token != "" {
		jobMasker.AddMask(token)
	}

	var stepLogger *StepLogger
	if c.stepLoggerEnabled {
		stepLogger = NewStepLogger(jobDisplayName, stdout, c.colorEnabled).WithMasker(jobMasker)
	}

	// Defer post-hooks teardown (LIFO) so that action cleanups run even on step setup failure or cancel
	defer func() {
		if len(postHooks) > 0 {
			jobFailed := conclusion != "success"
			if postErr := c.executePostHooks(ctx, jobID, postHooks, stepLogger, jobMasker, jobFailed); postErr != nil {
				if conclusion == "success" {
					conclusion = "failure"
				}
			}
		}
		if resp != nil {
			resp.Conclusion = conclusion
		}
	}()

	jobFailed := false
	jobCancelled := false

	for i, step := range job.Steps {
		if ctx.Err() != nil {
			jobCancelled = true
			conclusion = "cancelled"
		}

		stepID := step.ID
		if stepID == "" {
			stepID = fmt.Sprintf("step_%d", i+1)
		}

		stepDisplayName := step.Name
		if stepDisplayName == "" {
			stepDisplayName = stepID
		}

		if !shouldExecuteStep(step.Condition, parsedContext, jobFailed, jobCancelled) {
			c.NotifyJobStepLog(jobID, stepID, fmt.Sprintf("::debug::Skipping step %s (condition %q evaluated to false)", stepDisplayName, step.Condition))
			c.NotifyStepFinish(jobID, stepID, stepDisplayName, monitor.StatusSkipped, 0)
			continue
		}

		envFile, err := os.CreateTemp(runnerTemp, fmt.Sprintf("env_%s_*.txt", stepID))
		if err != nil {
			conclusion = "failure"
			return &protocol.CompleteJobRequest{
				PlanID:      job.PlanID,
				JobID:       job.JobID,
				Conclusion:  "failure",
				Outputs:     outputs,
				StepResults: stepResults,
			}, fmt.Errorf("create GITHUB_ENV file: %w", err)
		}
		envPath := envFile.Name()
		_ = envFile.Close()

		outFile, err := os.CreateTemp(runnerTemp, fmt.Sprintf("output_%s_*.txt", stepID))
		if err != nil {
			conclusion = "failure"
			_ = os.Remove(envPath)
			return &protocol.CompleteJobRequest{
				PlanID:      job.PlanID,
				JobID:       job.JobID,
				Conclusion:  "failure",
				Outputs:     outputs,
				StepResults: stepResults,
			}, fmt.Errorf("create GITHUB_OUTPUT file: %w", err)
		}
		outputPath := outFile.Name()
		_ = outFile.Close()

		pathFile, err := os.CreateTemp(runnerTemp, fmt.Sprintf("path_%s_*.txt", stepID))
		if err != nil {
			conclusion = "failure"
			_ = os.Remove(envPath)
			_ = os.Remove(outputPath)
			return &protocol.CompleteJobRequest{
				PlanID:      job.PlanID,
				JobID:       job.JobID,
				Conclusion:  "failure",
				Outputs:     outputs,
				StepResults: stepResults,
			}, fmt.Errorf("create GITHUB_PATH file: %w", err)
		}
		pathFilePath := pathFile.Name()
		_ = pathFile.Close()

		stateFile, err := os.CreateTemp(runnerTemp, fmt.Sprintf("state_%s_*.txt", stepID))
		if err != nil {
			conclusion = "failure"
			_ = os.Remove(envPath)
			_ = os.Remove(outputPath)
			_ = os.Remove(pathFilePath)
			return &protocol.CompleteJobRequest{
				PlanID:      job.PlanID,
				JobID:       job.JobID,
				Conclusion:  "failure",
				Outputs:     outputs,
				StepResults: stepResults,
			}, fmt.Errorf("create GITHUB_STATE file: %w", err)
		}
		statePath := stateFile.Name()
		_ = stateFile.Close()

		stepCtx := ctx
		var stepCancel context.CancelFunc
		if jobCancelled || ctx.Err() != nil {
			stepCtx, stepCancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		}

		cleanupStepFiles := func() {
			_ = os.Remove(envPath)
			_ = os.Remove(outputPath)
			_ = os.Remove(pathFilePath)
			_ = os.Remove(statePath)
			if stepCancel != nil {
				stepCancel()
			}
		}

		// Build step environment: cumulative environment + step-specific overrides + command files
		stepEnv := c.prepareStepEnvironment(stepCtx, job, step, cumulativeEnv, parsedContext, jobMasker)
		stepEnv["GITHUB_ENV"] = envPath
		stepEnv["GITHUB_OUTPUT"] = outputPath
		stepEnv["GITHUB_PATH"] = pathFilePath
		stepEnv["GITHUB_STATE"] = statePath
		stepEnv["GITHUB_WORKSPACE"] = jobWorkspace

		stepWorkDir := step.WorkingDirectory
		if stepWorkDir == "" {
			stepWorkDir = jobWorkspace
		} else if !filepath.IsAbs(stepWorkDir) {
			stepWorkDir = filepath.Join(jobWorkspace, stepWorkDir)
		}
		if err := os.MkdirAll(stepWorkDir, 0o755); err != nil {
			conclusion = "failure"
			cleanupStepFiles()
			return &protocol.CompleteJobRequest{
				PlanID:      job.PlanID,
				JobID:       job.JobID,
				Conclusion:  "failure",
				Outputs:     outputs,
				StepResults: stepResults,
			}, fmt.Errorf("create step working directory: %w", err)
		}

		var res *driver.StepResult
		var stepErr error
		var actCtx *action.ActionContext

		var evaluatedInputs map[string]string
		if step.Inputs != nil {
			evaluatedInputs = make(map[string]string, len(step.Inputs))
			for k, v := range step.Inputs {
				evaluatedInputs[k] = evaluateValue(v, parsedContext)
			}
		}

		if isActionStep(step) {
			ref := getStepReference(step)
			actCtx = &action.ActionContext{
				StepID:         stepID,
				Inputs:         evaluatedInputs,
				Env:            stepEnv,
				WorkspaceDir:   stepWorkDir,
				TempDir:        runnerTemp,
				ToolCacheDir:   runnerToolCache,
				StatePath:      statePath,
				PathFilePath:   pathFilePath,
				EnvFilePath:    envPath,
				OutputFilePath: outputPath,
				Context:        parsedContext,
			}
			res, _, stepErr = c.executeActionStep(stepCtx, jobID, stepID, stepDisplayName, ref, actCtx, stepLogger, jobMasker)

			// Ingest state file and queue post-step hook if manifest defines runs.post
			stepState, _ := IngestStateFile(statePath)
			if actCtx != nil && actCtx.Manifest != nil && strings.TrimSpace(actCtx.Manifest.Runs.Post) != "" {
				postScript := actCtx.Manifest.Runs.Post
				postIf := strings.TrimSpace(actCtx.Manifest.Runs.PostIf)
				stepFailed := stepErr != nil || (res != nil && res.ExitCode != 0)
				hookEnv := make(map[string]string, len(stepEnv))
				for k, v := range stepEnv {
					hookEnv[k] = v
				}
				postHooks = append(postHooks, PostStepHook{
					StepID:     stepID,
					StepName:   stepDisplayName,
					ActionDir:  actCtx.ActionDir,
					PostScript: postScript,
					PostIf:     postIf,
					StepFailed: stepFailed,
					Env:        hookEnv,
					State:      stepState,
					Inputs:     evaluatedInputs,
					Context:    parsedContext,
				})
			}
		} else {
			stepScript := step.Script
			if strings.HasPrefix(strings.TrimSpace(stepScript), "format(") || strings.Contains(stepScript, "${{") {
				stepScript = evaluateExpression(stepScript, parsedContext)
			}
			if stepScript == "" && isCheckoutStep(step) {
				stepScript = generateCheckoutScript(step, stepEnv)
			}

			spec := &driver.StepSpec{
				ID:               stepID,
				Name:             step.Name,
				WorkingDirectory: stepWorkDir,
				Environment:      stepEnv,
				Script:           stepScript,
			}

			res, _, stepErr = c.executeStep(stepCtx, d, jobID, spec, stepLogger, stepDisplayName, jobMasker)
		}

		if stepCtx.Err() != nil {
			exitCode := 1
			if res != nil && res.ExitCode != 0 {
				exitCode = res.ExitCode
			}
			stepResults = append(stepResults, protocol.StepResult{
				ID:         stepID,
				Conclusion: "failure",
				ExitCode:   exitCode,
			})
			cleanupStepFiles()
			jobCancelled = true
			conclusion = "cancelled"
			continue
		}

		if (res != nil && res.ExitCode != 0) || stepErr != nil {
			exitCode := 1
			if res != nil && res.ExitCode != 0 {
				exitCode = res.ExitCode
			}
			stepResults = append(stepResults, protocol.StepResult{
				ID:         stepID,
				Conclusion: "failure",
				ExitCode:   exitCode,
			})
			cleanupStepFiles()
			jobFailed = true
			if conclusion == "success" {
				conclusion = "failure"
			}
			continue
		}

		// Step succeeded: parse environment, output, and path files
		if err := IngestEnvFile(envPath, cumulativeEnv); err != nil {
			c.NotifyStepFinish(jobID, stepID, stepDisplayName, monitor.StatusFailed)
			stepResults = append(stepResults, protocol.StepResult{
				ID:         stepID,
				Conclusion: "failure",
				ExitCode:   1,
			})
			cleanupStepFiles()
			jobFailed = true
			if conclusion == "success" {
				conclusion = "failure"
			}
			continue
		}

		newOutputs, err := ParseEnvAndOutputFile(outputPath)
		if err != nil {
			c.NotifyStepFinish(jobID, stepID, stepDisplayName, monitor.StatusFailed)
			stepResults = append(stepResults, protocol.StepResult{
				ID:         stepID,
				Conclusion: "failure",
				ExitCode:   1,
			})
			cleanupStepFiles()
			jobFailed = true
			if conclusion == "success" {
				conclusion = "failure"
			}
			continue
		}
		stepOutputsMap := make(map[string]any)
		for k, v := range newOutputs {
			outputs[k] = v
			stepOutputsMap[k] = v
		}
		stepsMap, ok := parsedContext["steps"].(map[string]any)
		if !ok {
			stepsMap = make(map[string]any)
			parsedContext["steps"] = stepsMap
		}
		stepsMap[stepID] = map[string]any{"outputs": stepOutputsMap}
		if step.Name != "" {
			stepsMap[step.Name] = map[string]any{"outputs": stepOutputsMap}
		}

		pathKey := "PATH"
		for k := range cumulativeEnv {
			if strings.EqualFold(k, "PATH") {
				pathKey = k
				break
			}
		}
		updatedPath, err := IngestPathFile(pathFilePath, cumulativeEnv[pathKey])
		if err != nil {
			c.NotifyStepFinish(jobID, stepID, stepDisplayName, monitor.StatusFailed)
			stepResults = append(stepResults, protocol.StepResult{
				ID:         stepID,
				Conclusion: "failure",
				ExitCode:   1,
			})
			cleanupStepFiles()
			jobFailed = true
			if conclusion == "success" {
				conclusion = "failure"
			}
			continue
		}
		cumulativeEnv[pathKey] = updatedPath

		stepResults = append(stepResults, protocol.StepResult{
			ID:         stepID,
			Conclusion: "success",
			ExitCode:   0,
		})

		cleanupStepFiles()
	}

	return &protocol.CompleteJobRequest{
		PlanID:      job.PlanID,
		JobID:       job.JobID,
		Conclusion:  conclusion,
		Outputs:     outputs,
		StepResults: stepResults,
	}, nil
}

// executeStep executes a single step, publishing step start/finish events, streaming logs,
// and sampling telemetry via a 500ms ticker.
func (c *Coordinator) executeStep(ctx context.Context, d driver.Driver, jobID string, spec *driver.StepSpec, stepLogger *StepLogger, stepDisplayName string, jobMasker *SecretMasker) (*driver.StepResult, time.Duration, error) {
	if spec.OnProcessStart == nil {
		spec.OnProcessStart = func(pid, pgid int) {
			c.mu.RLock()
			s := c.sampler
			c.mu.RUnlock()
			if s != nil {
				s.SetTargetPID(int32(pid), int32(pgid))
			}
		}
	} else {
		orig := spec.OnProcessStart
		spec.OnProcessStart = func(pid, pgid int) {
			orig(pid, pgid)
			c.mu.RLock()
			s := c.sampler
			c.mu.RUnlock()
			if s != nil {
				s.SetTargetPID(int32(pid), int32(pgid))
			}
		}
	}

	return c.executeWithStreams(ctx, jobID, spec.ID, stepDisplayName, spec.Script, stepLogger, jobMasker, func(stepCtx context.Context, stdout, stderr io.Writer) (*driver.StepResult, error) {
		if c.tracer != nil && spec != nil {
			if spec.Environment == nil {
				spec.Environment = make(map[string]string)
			}
			c.tracer.InjectStepTraceContext(stepCtx, spec.Environment)
		}
		return d.ExecuteStep(stepCtx, spec, stdout, stderr)
	})
}

// executeActionStep resolves, detects node runtime, and executes a JavaScript action step.
func (c *Coordinator) executeActionStep(ctx context.Context, jobID, stepID, stepDisplayName string, ref *protocol.StepReference, actCtx *action.ActionContext, stepLogger *StepLogger, jobMasker *SecretMasker) (*driver.StepResult, time.Duration, error) {
	scriptDesc := ""
	if ref != nil {
		scriptDesc = fmt.Sprintf("uses: %s@%s", ref.Name, ref.Version)
	}
	return c.executeWithStreams(ctx, jobID, stepID, stepDisplayName, scriptDesc, stepLogger, jobMasker, func(stepCtx context.Context, stdout, stderr io.Writer) (*driver.StepResult, error) {
		if actCtx == nil {
			err := errors.New("action context is nil")
			_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
			return &driver.StepResult{ExitCode: 1, Error: err}, err
		}
		if ref == nil || strings.TrimSpace(ref.Name) == "" {
			err := errors.New("action step is missing reference")
			_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
			return &driver.StepResult{ExitCode: 1, Error: err}, err
		}

		resolver := c.getActionResolver()
		actionDir, err := resolver.Resolve(ctx, ref)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error resolving action %s: %v\n", ref.Name, err)
			return &driver.StepResult{ExitCode: 1, Error: err}, err
		}

		manifest, err := action.ParseManifestFile(actionDir)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error parsing action manifest in %s: %v\n", actionDir, err)
			return &driver.StepResult{ExitCode: 1, Error: err}, err
		}

		if !strings.HasPrefix(manifest.Runs.Using, "node") {
			err := fmt.Errorf("unsupported action runner %q for action %s", manifest.Runs.Using, ref.Name)
			_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
			return &driver.StepResult{ExitCode: 1, Error: err}, err
		}

		detector := c.getNodeDetector()
		nodePath, err := detector.Detect(ctx)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error detecting Node runtime: %v\n", err)
			return &driver.StepResult{ExitCode: 1, Error: err}, err
		}

		scriptPath, err := resolveActionScriptPath(actionDir, manifest.Runs.Main)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error resolving action script: %v\n", err)
			return &driver.StepResult{ExitCode: 1, Error: err}, err
		}
		actCtx.ActionDir = actionDir
		if manifest.Inputs != nil && actCtx.Context != nil {
			for name, inputDef := range manifest.Inputs {
				if strings.Contains(inputDef.Default, "${{") {
					inputDef.Default = evaluateExpression(inputDef.Default, actCtx.Context)
					manifest.Inputs[name] = inputDef
				}
			}
		}
		actCtx.Manifest = manifest

		execErr := action.ExecuteNodeAction(stepCtx, nodePath, scriptPath, actCtx, stdout, stderr)
		exitCode := 0
		if execErr != nil {
			exitCode = 1
			var exitErr *exec.ExitError
			if errors.As(execErr, &exitErr) {
				exitCode = exitErr.ExitCode()
			}
			return &driver.StepResult{ExitCode: exitCode, Error: execErr}, execErr
		}
		return &driver.StepResult{ExitCode: 0}, nil
	})
}

// executeWithStreams wraps common stream piping, logging, telemetry, and monitor notifications.
func (c *Coordinator) executeWithStreams(ctx context.Context, jobID, stepID, stepDisplayName, scriptDesc string, stepLogger *StepLogger, jobMasker *SecretMasker, runFn func(stepCtx context.Context, stdout, stderr io.Writer) (*driver.StepResult, error)) (*driver.StepResult, time.Duration, error) {
	c.NotifyStepStart(jobID, stepID, stepDisplayName)

	stdout := c.stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := c.stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	stepOut := stdout
	stepErrStream := stderr
	var stepOutWriter *StepWriter
	var stepErrWriter *StepWriter

	c.mu.RLock()
	monSrv := c.monServer
	sampler := c.sampler
	c.mu.RUnlock()

	if jobMasker == nil {
		if stepLogger != nil {
			jobMasker = stepLogger.Masker()
		} else {
			jobMasker = NewSecretMasker()
		}
	}

	if stepLogger != nil {
		stepLogger.LogStepStart(stepDisplayName, scriptDesc)
		stepOutWriter = stepLogger.NewWriter("stdout")
		stepErrWriter = stepLogger.NewWriter("stderr")
		if monSrv != nil {
			stepOutWriter.WithCallback(func(line string) {
				c.NotifyJobStepLog(jobID, stepID, line)
			})
			stepErrWriter.WithCallback(func(line string) {
				c.NotifyJobStepLog(jobID, stepID, line)
			})
		}
		stepOut = stepOutWriter
		stepErrStream = stepErrWriter
	} else if monSrv != nil {
		stepOutWriter = NewLineWriter(func(line string) {
			c.NotifyJobStepLog(jobID, stepID, line)
		}).WithMasker(jobMasker)
		stepErrWriter = NewLineWriter(func(line string) {
			c.NotifyJobStepLog(jobID, stepID, line)
		}).WithMasker(jobMasker)
		stepOut = io.MultiWriter(stdout, stepOutWriter)
		stepErrStream = io.MultiWriter(stderr, stepErrWriter)
	}

	stepStartTime := time.Now()
	var stepSpan trace.Span
	traceID := ""
	spanID := ""
	if c.tracer != nil {
		ctx, stepSpan = c.tracer.StartStepSpan(ctx, stepID, stepDisplayName)
		defer stepSpan.End()
		if sc := stepSpan.SpanContext(); sc.IsValid() {
			traceID = sc.TraceID().String()
			spanID = sc.SpanID().String()
		}
	}
	if sampler != nil {
		sampler.SetTraceContext(traceID, spanID, stepDisplayName, stepStartTime)
	}

	// Start 500ms ticker to sample telemetry when process starts
	if monSrv != nil && sampler != nil {
		stopTelemetry := make(chan struct{})
		telemetryDone := make(chan struct{})
		defer func() {
			close(stopTelemetry)
			<-telemetryDone
		}()
		go func() {
			defer close(telemetryDone)
			defer func() { _ = recover() }()
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()

			if sample := sampler.Sample(); sample != nil {
				monSrv.SetJobTelemetry(jobID, c.repoAlias, sample)
				if c.tracer != nil && c.tracer.Enabled() {
					c.tracer.RecordTelemetrySample(ctx, jobID, c.repoAlias, sample)
				}
			}

			for {
				select {
				case <-stopTelemetry:
					return
				case <-ticker.C:
					if sample := sampler.Sample(); sample != nil {
						monSrv.SetJobTelemetry(jobID, c.repoAlias, sample)
						if c.tracer != nil && c.tracer.Enabled() {
							c.tracer.RecordTelemetrySample(ctx, jobID, c.repoAlias, sample)
						}
					}
				}
			}
		}()
	}

	defer func() {
		defer func() { _ = recover() }()
		c.mu.RLock()
		s := c.sampler
		c.mu.RUnlock()
		if s != nil {
			s.SetTargetPID(0, 0)
			s.SetTraceContext("", "", "", time.Time{})
		}
	}()

	startTime := time.Now()
	res, stepErr := runFn(ctx, stepOut, stepErrStream)
	if stepOutWriter != nil {
		stepOutWriter.Flush()
	}
	if stepErrWriter != nil {
		stepErrWriter.Flush()
	}
	duration := time.Since(startTime)

	success := (res == nil || res.ExitCode == 0) && stepErr == nil && ctx.Err() == nil
	if stepLogger != nil {
		stepLogger.LogStepEnd(stepDisplayName, success, duration)
	}

	exitCode := 0
	if res != nil {
		exitCode = res.ExitCode
	} else if !success {
		exitCode = 1
	}

	durationSec := duration.Seconds()
	if success {
		if c.tracer != nil {
			if stepSpan != nil {
				c.tracer.FinishStepSpan(stepSpan, exitCode, "success")
			}
			c.tracer.RecordStepCompletion(ctx, stepID, stepDisplayName, "success", exitCode, durationSec)
		}
		c.NotifyStepFinish(jobID, stepID, stepDisplayName, monitor.StatusSuccess, exitCode)
	} else {
		if c.tracer != nil {
			if stepSpan != nil {
				if stepErr != nil {
					c.tracer.RecordSpanError(stepSpan, stepErr)
				}
				c.tracer.FinishStepSpan(stepSpan, exitCode, "failed")
			}
			c.tracer.RecordStepCompletion(ctx, stepID, stepDisplayName, "failed", exitCode, durationSec)
		}
		c.NotifyStepFinish(jobID, stepID, stepDisplayName, monitor.StatusFailed, exitCode)
	}

	return res, duration, stepErr
}

func isActionStep(step protocol.JobStep) bool {
	if isCheckoutStep(step) {
		return false
	}
	if strings.TrimSpace(step.Script) != "" {
		return false
	}
	if step.Reference != nil {
		if step.Reference.Type == "script" {
			return false
		}
		if step.Reference.Type == "repository" || step.Reference.Name != "" {
			return true
		}
	}
	if step.Type == "uses" {
		ref := getStepReference(step)
		return ref != nil && strings.Contains(ref.Name, "/")
	}
	return false
}

// isTrustedContext evaluates whether the given job is authorized to access secrets from the vault.
// Local runs, push events, and workflow_dispatch events are always trusted.
// Pull request events are trusted only if the pull request originates from an internal branch
// within the same repository (GITHUB_HEAD_REF_REPO == GITHUB_REPOSITORY).
// Fork pull requests are untrusted to prevent secret exfiltration.
func isTrustedContext(job *protocol.JobSpecification) bool {
	if job == nil {
		return false
	}
	if job.IsLocalExecution {
		return true
	}
	eventName := job.EventName
	if eventName == "" && job.Variables != nil {
		eventName = job.Variables["GITHUB_EVENT_NAME"]
	}
	if eventName == "push" || eventName == "workflow_dispatch" {
		return true
	}
	if eventName == "pull_request" {
		if job.Variables != nil {
			headRepo := job.Variables["GITHUB_HEAD_REF_REPO"]
			baseRepo := job.Variables["GITHUB_REPOSITORY"]
			if headRepo != "" && (headRepo == baseRepo || strings.EqualFold(headRepo, baseRepo)) {
				return true
			}
		}
	}
	return false
}

func (c *Coordinator) logSecurityNotice(jobID, stepID, msg string) {
	c.NotifyJobStepLog(jobID, stepID, msg)
	c.mu.RLock()
	out := c.stdout
	c.mu.RUnlock()
	if out != nil {
		_, _ = fmt.Fprintln(out, msg)
	}
}

// prepareStepEnvironment resolves environment variables for a step, inheriting cumulative variables
// and evaluating step-specific environment values. If a secret vault provider is configured, empty
// environment variables (v == "") are looked up in the vault. If found in a trusted context (push,
// workflow_dispatch, local run, same-repo PR), the secret is injected and registered with jobMasker.
// If the context is untrusted (fork PR), secret resolution is blocked and a security notice is logged.
func (c *Coordinator) prepareStepEnvironment(
	ctx context.Context,
	job *protocol.JobSpecification,
	step protocol.JobStep,
	extra ...any,
) map[string]string {
	var cumulativeEnv map[string]string
	var parsedContext map[string]any
	var jobMasker *SecretMasker

	for _, arg := range extra {
		switch v := arg.(type) {
		case map[string]string:
			cumulativeEnv = v
		case map[string]any:
			parsedContext = v
		case *SecretMasker:
			jobMasker = v
		}
	}

	stepEnv := make(map[string]string, len(cumulativeEnv)+len(step.Environment)+5)
	for k, v := range cumulativeEnv {
		stepEnv[k] = v
	}
	for k, v := range step.Environment {
		stepEnv[k] = evaluateValue(v, parsedContext)
	}

	v := c.getVault()
	if v != nil {
		trusted := isTrustedContext(job)
		jobID := ""
		if job != nil {
			jobID = job.JobID
			if jobID == "" {
				jobID = job.ID
			}
		}

		keys := make([]string, 0, len(stepEnv))
		for k := range stepEnv {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			if stepEnv[k] == "" {
				if secretVal, err := v.Get(ctx, k); err == nil && secretVal != "" {
					if trusted {
						stepEnv[k] = secretVal
						if jobMasker != nil {
							jobMasker.AddMask(secretVal)
						}
					} else {
						c.logSecurityNotice(jobID, step.ID, "Vault secret resolution blocked for untrusted fork PR: "+k)
					}
				}
			}
		}
	}

	return stepEnv
}

func getStepReference(step protocol.JobStep) *protocol.StepReference {
	if step.Reference != nil && (step.Reference.Type == "repository" || step.Reference.Name != "") {
		return step.Reference
	}
	target := ""
	if step.Inputs != nil && step.Inputs["uses"] != "" {
		target = step.Inputs["uses"]
	} else if step.Name != "" {
		target = step.Name
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	parts := strings.SplitN(target, "@", 2)
	ref := &protocol.StepReference{
		Name: parts[0],
		Type: "repository",
	}
	if len(parts) > 1 {
		ref.Version = parts[1]
		ref.Ref = parts[1]
	}
	return ref
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

func isCheckoutStep(step protocol.JobStep) bool {
	lowerName := strings.ToLower(step.Name)
	lowerID := strings.ToLower(step.ID)
	return strings.Contains(lowerName, "checkout") || strings.Contains(lowerID, "checkout")
}

func generateCheckoutScript(step protocol.JobStep, env map[string]string) string {
	repo := ""
	if step.Inputs != nil {
		repo = step.Inputs["repository"]
	}
	if repo == "" && env != nil {
		repo = env["GITHUB_REPOSITORY"]
	}
	ref := ""
	if step.Inputs != nil {
		ref = step.Inputs["ref"]
	}
	subPath := ""
	if step.Inputs != nil {
		subPath = step.Inputs["path"]
	}

	if env != nil {
		env["ACTR_CHECKOUT_REPO"] = repo
		env["ACTR_CHECKOUT_REF"] = ref
		env["ACTR_CHECKOUT_PATH"] = subPath
	}

	return `set -e
TARGET_DIR="${GITHUB_WORKSPACE}"
if [ -n "$ACTR_CHECKOUT_PATH" ]; then
    TARGET_DIR="${GITHUB_WORKSPACE}/$ACTR_CHECKOUT_PATH"
fi
mkdir -p "$TARGET_DIR"
cd "$TARGET_DIR"

REPO_NAME="$ACTR_CHECKOUT_REPO"
if [ -z "$REPO_NAME" ]; then
    REPO_NAME="${GITHUB_REPOSITORY}"
fi
if [ -z "$REPO_NAME" ]; then
    echo "Error: repository not specified for checkout" >&2
    exit 1
fi

if ! echo "$REPO_NAME" | grep -Eq '^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$'; then
    echo "Error: invalid repository name format: $REPO_NAME" >&2
    exit 1
fi

CLONE_URL="https://github.com/${REPO_NAME}.git"
if [ -n "${GITHUB_TOKEN}" ]; then
    CLONE_URL="https://x-access-token:${GITHUB_TOKEN}@github.com/${REPO_NAME}.git"
fi

if [ ! -d ".git" ]; then
    git clone "$CLONE_URL" .
    git remote set-url origin "https://github.com/${REPO_NAME}.git" 2>/dev/null || true
fi

TARGET_REF="$ACTR_CHECKOUT_REF"
if [ -z "$TARGET_REF" ]; then
    TARGET_REF="${GITHUB_SHA}"
fi
if [ -z "$TARGET_REF" ]; then
    TARGET_REF="${GITHUB_REF}"
fi

if [ -n "$TARGET_REF" ]; then
    case "$TARGET_REF" in
        -*)
            echo "Error: invalid ref format: $TARGET_REF" >&2
            exit 1
            ;;
    esac
    CLEAN_REF="${TARGET_REF#refs/heads/}"
    git checkout -f -- "$CLEAN_REF" 2>/dev/null || \
    git checkout -f -- "$TARGET_REF" 2>/dev/null || (
        if git fetch --depth=1 "$CLONE_URL" "$TARGET_REF" 2>/dev/null; then
            git checkout -f FETCH_HEAD 2>/dev/null || \
            git checkout -f -- "$CLEAN_REF" 2>/dev/null || \
            git checkout -f -- "$TARGET_REF"
        else
            git fetch "$CLONE_URL"
            git checkout -f -- "$CLEAN_REF" 2>/dev/null || \
            git checkout -f -- "$TARGET_REF" 2>/dev/null || \
            git checkout -f -B "$CLEAN_REF" "origin/$CLEAN_REF" 2>/dev/null || \
            git checkout -f -- "$TARGET_REF"
        fi
    )
fi
`
}

func parseContextData(v any) any {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case map[string]any:
		if dSlice, ok := val["d"].([]any); ok {
			res := make(map[string]any, len(dSlice))
			for _, item := range dSlice {
				if itemMap, ok := item.(map[string]any); ok {
					if k, ok := itemMap["k"].(string); ok {
						res[k] = parseContextData(itemMap["v"])
					}
				}
			}
			return res
		}
		if aSlice, ok := val["a"].([]any); ok {
			res := make([]any, len(aSlice))
			for i, item := range aSlice {
				res[i] = parseContextData(item)
			}
			return res
		}
		res := make(map[string]any, len(val))
		for k, item := range val {
			res[k] = parseContextData(item)
		}
		return res
	default:
		return val
	}
}

func lookupContext(path string, ctx map[string]any) (any, bool) {
	parts := strings.Split(path, ".")
	var current any = ctx
	for _, part := range parts {
		if m, ok := current.(map[string]any); ok {
			val, exists := m[part]
			if !exists {
				found := false
				for k, v := range m {
					if strings.EqualFold(k, part) {
						val = v
						found = true
						break
					}
				}
				if !found {
					return nil, false
				}
			}
			current = val
			continue
		}
		if sm, ok := current.(map[string]string); ok {
			val, exists := sm[part]
			if !exists {
				found := false
				for k, v := range sm {
					if strings.EqualFold(k, part) {
						val = v
						found = true
						break
					}
				}
				if !found {
					return nil, false
				}
			}
			current = val
			continue
		}
		return nil, false
	}
	return current, true
}

// parseExpressionArgs splits a comma-separated argument list while respecting single-quoted strings and parentheses.
func parseExpressionArgs(s string) []string {
	var args []string
	var current strings.Builder
	inQuotes := false
	parenDepth := 0

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case '\'':
			if inQuotes && i+1 < len(s) && s[i+1] == '\'' {
				current.WriteByte('\'')
				current.WriteByte('\'')
				i++
				continue
			}
			inQuotes = !inQuotes
			current.WriteByte(ch)
		case '(':
			if !inQuotes {
				parenDepth++
			}
			current.WriteByte(ch)
		case ')':
			if !inQuotes {
				parenDepth--
			}
			current.WriteByte(ch)
		case ',':
			if !inQuotes && parenDepth == 0 {
				args = append(args, strings.TrimSpace(current.String()))
				current.Reset()
			} else {
				current.WriteByte(ch)
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		args = append(args, strings.TrimSpace(current.String()))
	}
	return args
}

// isTruthy reports whether an expression result string represents a truthy value.
func isTruthy(s string) bool {
	trimmed := strings.TrimSpace(s)
	return trimmed != "" && trimmed != "false" && trimmed != "null" && trimmed != "0"
}

// splitTopLevel splits s by sep only at parentheses depth 0 and outside single quotes.
func splitTopLevel(s, sep string) []string {
	var parts []string
	inQuotes := false
	parenDepth := 0
	lastIdx := 0
	sepLen := len(sep)

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case '\'':
			if inQuotes && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			inQuotes = !inQuotes
		case '(':
			if !inQuotes {
				parenDepth++
			}
		case ')':
			if !inQuotes && parenDepth > 0 {
				parenDepth--
			}
		}

		if !inQuotes && parenDepth == 0 && i+sepLen <= len(s) && s[i:i+sepLen] == sep {
			parts = append(parts, strings.TrimSpace(s[lastIdx:i]))
			lastIdx = i + sepLen
			i += sepLen - 1
		}
	}
	if lastIdx < len(s) {
		parts = append(parts, strings.TrimSpace(s[lastIdx:]))
	}
	return parts
}

func isContextPath(s string) bool {
	roots := []string{
		"github.", "env.", "job.", "steps.", "runner.",
		"needs.", "inputs.", "matrix.", "strategy.", "secrets.", "vars.",
	}
	for _, r := range roots {
		if strings.HasPrefix(s, r) {
			return true
		}
	}
	return false
}

func containsStatusCheckFunction(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "success()") ||
		strings.Contains(lower, "always()") ||
		strings.Contains(lower, "failure()") ||
		strings.Contains(lower, "cancelled()")
}

// shouldExecuteStep determines whether a job step should execute based on its condition.
func shouldExecuteStep(cond string, ctx map[string]any, jobFailed, jobCancelled bool) bool {
	trimmed := strings.TrimSpace(cond)
	if strings.EqualFold(trimmed, "false") {
		return false
	}
	if strings.EqualFold(trimmed, "true") {
		return !jobFailed && !jobCancelled
	}
	if trimmed == "" || strings.EqualFold(trimmed, "success()") {
		return !jobFailed && !jobCancelled
	}
	if strings.EqualFold(trimmed, "always()") {
		return true
	}
	if strings.EqualFold(trimmed, "failure()") {
		return jobFailed && !jobCancelled
	}
	if strings.EqualFold(trimmed, "cancelled()") {
		return jobCancelled
	}

	// When the condition names none of success(), always(), failure(), or cancelled(),
	// GitHub Actions implicitly requires !jobFailed && !jobCancelled.
	if !containsStatusCheckFunction(trimmed) {
		if jobFailed || jobCancelled {
			return false
		}
	}

	evalCtx := make(map[string]any, len(ctx)+4)
	for k, v := range ctx {
		evalCtx[k] = v
	}
	evalCtx["_job_failed"] = jobFailed
	evalCtx["_job_cancelled"] = jobCancelled

	res := evaluateExpression(trimmed, evalCtx)
	return isTruthy(res)
}

func evaluateValue(val string, ctx map[string]any) string {
	trimmed := strings.TrimSpace(val)
	if strings.Contains(trimmed, "${{") || strings.HasPrefix(trimmed, "format(") {
		return evaluateExpression(trimmed, ctx)
	}
	return val
}

func formatTemplate(templateStr string, argVals []string) string {
	var sb strings.Builder
	for i := 0; i < len(templateStr); {
		if templateStr[i] == '{' {
			if i+1 < len(templateStr) && templateStr[i+1] == '{' {
				sb.WriteByte('{')
				i += 2
				continue
			}
			closeIdx := strings.IndexByte(templateStr[i+1:], '}')
			if closeIdx != -1 {
				numStr := templateStr[i+1 : i+1+closeIdx]
				if idx, err := strconv.Atoi(numStr); err == nil && idx >= 0 && idx < len(argVals) {
					sb.WriteString(argVals[idx])
					i += 1 + closeIdx + 1
					continue
				}
			}
			sb.WriteByte('{')
			i++
			continue
		}
		if templateStr[i] == '}' {
			if i+1 < len(templateStr) && templateStr[i+1] == '}' {
				sb.WriteByte('}')
				i += 2
				continue
			}
			sb.WriteByte('}')
			i++
			continue
		}
		sb.WriteByte(templateStr[i])
		i++
	}
	return sb.String()
}

func findMatchingExpressionEnd(s string) int {
	inQuotes := false
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '\'' {
			if inQuotes && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			inQuotes = !inQuotes
			continue
		}
		if !inQuotes && s[i] == '}' && s[i+1] == '}' {
			return i
		}
	}
	return -1
}

func isSingleQuotedLiteral(s string) bool {
	if !strings.HasPrefix(s, "'") || !strings.HasSuffix(s, "'") || len(s) < 2 {
		return false
	}
	i := 1
	for i < len(s) {
		if s[i] == '\'' {
			if i+1 < len(s) && s[i+1] == '\'' {
				// Escaped quote: ''
				i += 2
				continue
			}
			return i == len(s)-1
		}
		i++
	}
	return false
}

func evaluateExpression(expr string, ctx map[string]any) string {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return ""
	}

	// String literals: 'foo'
	if isSingleQuotedLiteral(trimmed) {
		unquoted := trimmed[1 : len(trimmed)-1]
		return strings.ReplaceAll(unquoted, "''", "'")
	}

	// Handle format('template', arg0, arg1, ...) before embedded expression evaluation
	if strings.HasPrefix(trimmed, "format(") && strings.HasSuffix(trimmed, ")") {
		inner := strings.TrimSpace(trimmed[7 : len(trimmed)-1])
		args := parseExpressionArgs(inner)
		if len(args) > 0 {
			templateStr := evaluateExpression(args[0], ctx)
			var argVals []string
			for i := 1; i < len(args); i++ {
				argVals = append(argVals, evaluateExpression(args[i], ctx))
			}
			return formatTemplate(templateStr, argVals)
		}
	}

	// Strip outer ${{ ... }} wrappers if present across the entire expression
	if strings.HasPrefix(trimmed, "${{") && strings.HasSuffix(trimmed, "}}") {
		endIdx := findMatchingExpressionEnd(trimmed[3:])
		if endIdx != -1 && endIdx == len(trimmed[3:])-2 {
			trimmed = strings.TrimSpace(trimmed[3 : len(trimmed)-2])
		}
	}

	// Re-check format(...) in case it was wrapped in ${{ format(...) }}
	if strings.HasPrefix(trimmed, "format(") && strings.HasSuffix(trimmed, ")") {
		inner := strings.TrimSpace(trimmed[7 : len(trimmed)-1])
		args := parseExpressionArgs(inner)
		if len(args) > 0 {
			templateStr := evaluateExpression(args[0], ctx)
			var argVals []string
			for i := 1; i < len(args); i++ {
				argVals = append(argVals, evaluateExpression(args[i], ctx))
			}
			return formatTemplate(templateStr, argVals)
		}
	}

	// Check if this string contains embedded ${{ ... }} expressions with text before or after, or multiple expressions
	if strings.Contains(trimmed, "${{") && strings.Contains(trimmed, "}}") {
		var sb strings.Builder
		remaining := trimmed
		hasEmbedded := false
		for {
			s := strings.Index(remaining, "${{")
			if s == -1 {
				sb.WriteString(remaining)
				break
			}
			eRel := findMatchingExpressionEnd(remaining[s+len("${{"):])
			if eRel == -1 {
				sb.WriteString(remaining)
				break
			}
			hasEmbedded = true
			e := s + len("${{") + eRel
			sb.WriteString(remaining[:s])
			inner := strings.TrimSpace(remaining[s+len("${{") : e])
			sb.WriteString(evaluateExpression(inner, ctx))
			remaining = remaining[e+len("}}"):]
		}
		if hasEmbedded {
			return sb.String()
		}
	}

	// Trim outer matching parentheses: (expr)
	for strings.HasPrefix(trimmed, "(") && strings.HasSuffix(trimmed, ")") {
		depth := 0
		matched := true
		for i := 0; i < len(trimmed)-1; i++ {
			if trimmed[i] == '(' {
				depth++
			} else if trimmed[i] == ')' {
				depth--
				if depth == 0 {
					matched = false
					break
				}
			}
		}
		if matched && depth == 1 {
			trimmed = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
		} else {
			break
		}
	}

	// Handle status check functions
	if strings.EqualFold(trimmed, "success()") {
		failed, _ := ctx["_job_failed"].(bool)
		cancelled, _ := ctx["_job_cancelled"].(bool)
		if !failed && !cancelled {
			return "true"
		}
		return "false"
	}
	if strings.EqualFold(trimmed, "always()") {
		return "true"
	}
	if strings.EqualFold(trimmed, "failure()") {
		failed, _ := ctx["_job_failed"].(bool)
		cancelled, _ := ctx["_job_cancelled"].(bool)
		if failed && !cancelled {
			return "true"
		}
		return "false"
	}
	if strings.EqualFold(trimmed, "cancelled()") {
		cancelled, _ := ctx["_job_cancelled"].(bool)
		if cancelled {
			return "true"
		}
		return "false"
	}

	// Handle OR expressions: left || right at top level
	if orParts := splitTopLevel(trimmed, " || "); len(orParts) > 1 {
		for _, part := range orParts {
			res := evaluateExpression(part, ctx)
			if isTruthy(res) {
				return res
			}
		}
		return evaluateExpression(orParts[len(orParts)-1], ctx)
	}

	// Handle AND expressions: left && right at top level
	if andParts := splitTopLevel(trimmed, " && "); len(andParts) > 1 {
		for _, part := range andParts {
			res := evaluateExpression(part, ctx)
			if !isTruthy(res) {
				return ""
			}
		}
		return evaluateExpression(andParts[len(andParts)-1], ctx)
	}

	// Handle equality expressions: left == right at top level
	if eqParts := splitTopLevel(trimmed, " == "); len(eqParts) == 2 {
		left := evaluateExpression(eqParts[0], ctx)
		right := evaluateExpression(eqParts[1], ctx)
		if strings.EqualFold(left, right) {
			return "true"
		}
		return "false"
	}

	// Handle inequality expressions: left != right at top level
	if neParts := splitTopLevel(trimmed, " != "); len(neParts) == 2 {
		left := evaluateExpression(neParts[0], ctx)
		right := evaluateExpression(neParts[1], ctx)
		if !strings.EqualFold(left, right) {
			return "true"
		}
		return "false"
	}

	// Handle prefix NOT: !expr
	if strings.HasPrefix(trimmed, "!") {
		inner := strings.TrimSpace(trimmed[1:])
		res := evaluateExpression(inner, ctx)
		if isTruthy(res) {
			return "false"
		}
		return "true"
	}

	// Handle startsWith(searchString, prefix) - case-insensitive per GitHub Actions specification
	if strings.HasPrefix(trimmed, "startsWith(") && strings.HasSuffix(trimmed, ")") {
		inner := strings.TrimSpace(trimmed[11 : len(trimmed)-1])
		args := parseExpressionArgs(inner)
		if len(args) >= 2 {
			str := strings.ToLower(evaluateExpression(args[0], ctx))
			prefix := strings.ToLower(evaluateExpression(args[1], ctx))
			if strings.HasPrefix(str, prefix) {
				return "true"
			}
			return "false"
		}
	}

	// Handle endsWith(searchString, suffix) - case-insensitive per GitHub Actions specification
	if strings.HasPrefix(trimmed, "endsWith(") && strings.HasSuffix(trimmed, ")") {
		inner := strings.TrimSpace(trimmed[9 : len(trimmed)-1])
		args := parseExpressionArgs(inner)
		if len(args) >= 2 {
			str := strings.ToLower(evaluateExpression(args[0], ctx))
			suffix := strings.ToLower(evaluateExpression(args[1], ctx))
			if strings.HasSuffix(str, suffix) {
				return "true"
			}
			return "false"
		}
	}

	// Handle contains(searchString, item) - case-insensitive per GitHub Actions specification
	if strings.HasPrefix(trimmed, "contains(") && strings.HasSuffix(trimmed, ")") {
		inner := strings.TrimSpace(trimmed[9 : len(trimmed)-1])
		args := parseExpressionArgs(inner)
		if len(args) >= 2 {
			str := strings.ToLower(evaluateExpression(args[0], ctx))
			item := strings.ToLower(evaluateExpression(args[1], ctx))
			if strings.Contains(str, item) {
				return "true"
			}
			return "false"
		}
	}

	// Handle eq(a, b)
	if strings.HasPrefix(trimmed, "eq(") && strings.HasSuffix(trimmed, ")") {
		inner := strings.TrimSpace(trimmed[3 : len(trimmed)-1])
		args := parseExpressionArgs(inner)
		if len(args) >= 2 {
			a := evaluateExpression(args[0], ctx)
			b := evaluateExpression(args[1], ctx)
			if strings.EqualFold(a, b) {
				return "true"
			}
			return "false"
		}
	}

	// Handle ne(a, b)
	if strings.HasPrefix(trimmed, "ne(") && strings.HasSuffix(trimmed, ")") {
		inner := strings.TrimSpace(trimmed[3 : len(trimmed)-1])
		args := parseExpressionArgs(inner)
		if len(args) >= 2 {
			a := evaluateExpression(args[0], ctx)
			b := evaluateExpression(args[1], ctx)
			if !strings.EqualFold(a, b) {
				return "true"
			}
			return "false"
		}
	}

	// Handle and(arg0, arg1, ...)
	if strings.HasPrefix(trimmed, "and(") && strings.HasSuffix(trimmed, ")") {
		inner := strings.TrimSpace(trimmed[4 : len(trimmed)-1])
		args := parseExpressionArgs(inner)
		for _, arg := range args {
			res := evaluateExpression(arg, ctx)
			if !isTruthy(res) {
				return "false"
			}
		}
		return "true"
	}

	// Handle or(arg0, arg1, ...)
	if strings.HasPrefix(trimmed, "or(") && strings.HasSuffix(trimmed, ")") {
		inner := strings.TrimSpace(trimmed[3 : len(trimmed)-1])
		args := parseExpressionArgs(inner)
		for _, arg := range args {
			res := evaluateExpression(arg, ctx)
			if isTruthy(res) {
				return "true"
			}
		}
		return "false"
	}

	// Handle not(arg)
	if strings.HasPrefix(trimmed, "not(") && strings.HasSuffix(trimmed, ")") {
		inner := strings.TrimSpace(trimmed[4 : len(trimmed)-1])
		res := evaluateExpression(inner, ctx)
		if isTruthy(res) {
			return "false"
		}
		return "true"
	}

	// String literals: 'foo'
	if strings.HasPrefix(trimmed, "'") && strings.HasSuffix(trimmed, "'") && len(trimmed) >= 2 {
		unquoted := trimmed[1 : len(trimmed)-1]
		return strings.ReplaceAll(unquoted, "''", "'")
	}

	// Boolean/null literals
	if trimmed == "true" || trimmed == "false" || trimmed == "null" {
		return trimmed
	}

	// Context lookup (e.g. inputs.platforms, needs.detect.outputs.platforms, github.event_name)
	if val, ok := lookupContext(trimmed, ctx); ok && val != nil {
		return fmt.Sprint(val)
	} else if isContextPath(trimmed) {
		return ""
	}

	return trimmed
}

// IngestPathFile reads lines from the file at filePath, cleans each non-empty entry,
// prepends them to currentPath (joined with filepath.ListSeparator), and truncates the file.
func IngestPathFile(filePath string, currentPath string) (string, error) {
	if filePath == "" {
		return currentPath, nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return currentPath, nil
		}
		return currentPath, err
	}

	if err := os.Truncate(filePath, 0); err != nil {
		return currentPath, err
	}

	if len(data) == 0 {
		return currentPath, nil
	}

	var newPaths []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		cleaned := filepath.Clean(line)
		if cleaned != "" {
			newPaths = append(newPaths, cleaned)
		}
	}
	if err := scanner.Err(); err != nil {
		return currentPath, err
	}

	if len(newPaths) == 0 {
		return currentPath, nil
	}

	prefix := strings.Join(newPaths, string(filepath.ListSeparator))
	if currentPath == "" {
		return prefix, nil
	}
	return prefix + string(filepath.ListSeparator) + currentPath, nil
}

var forbiddenEnvKeys = map[string]bool{
	"NODE_OPTIONS":          true,
	"LD_PRELOAD":            true,
	"LD_LIBRARY_PATH":       true,
	"DYLD_INSERT_LIBRARIES": true,
	"DYLD_LIBRARY_PATH":     true,
	"PATH":                  true,
	"RUNNER_TEMP":           true,
	"RUNNER_TOOL_CACHE":     true,
	"RUNNER_WORKSPACE":      true,
	"GITHUB_ENV":            true,
	"GITHUB_OUTPUT":         true,
	"GITHUB_STATE":          true,
	"GITHUB_PATH":           true,
	"GITHUB_WORKSPACE":      true,
	"GITHUB_ACTION_PATH":    true,
}

// IngestEnvFile reads key=value or multiline delimiter syntax from filePath,
// merges them into currentEnv, and truncates the file. It drops dangerous environment
// variables to prevent arbitrary code injection or hijacking subsequent actions.
func IngestEnvFile(filePath string, currentEnv map[string]string) error {
	if filePath == "" {
		return nil
	}

	newVars, err := ParseCommandFile(filePath)
	if err != nil {
		return err
	}

	if err := os.Truncate(filePath, 0); err != nil && !os.IsNotExist(err) {
		return err
	}

	if currentEnv != nil {
		for k, v := range newVars {
			if forbiddenEnvKeys[strings.ToUpper(strings.TrimSpace(k))] {
				continue
			}
			currentEnv[k] = v
		}
	}

	return nil
}

// IngestStateFile parses KEY=VAL and delimiter syntax from state file.
func IngestStateFile(filePath string) (map[string]string, error) {
	if filePath == "" {
		return make(map[string]string), nil
	}

	state, err := ParseCommandFile(filePath)
	if err != nil {
		return nil, err
	}

	if err := os.Truncate(filePath, 0); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	if state == nil {
		state = make(map[string]string)
	}
	return state, nil
}

// ExecutePostHooks executes post-step hooks in reverse order (LIFO) with a decoupled context.
func (c *Coordinator) ExecutePostHooks(ctx context.Context, jobID string, postHooks []PostStepHook) error {
	return c.executePostHooks(ctx, jobID, postHooks, nil, nil, false)
}

// executePostHooks executes all queued post-step hooks in reverse order (LIFO).
// It decouples from the job context using context.WithoutCancel with a 5-minute timeout.
func (c *Coordinator) executePostHooks(ctx context.Context, jobID string, postHooks []PostStepHook, stepLogger *StepLogger, jobMasker *SecretMasker, jobFailed bool) error {
	postCtx, postCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer postCancel()

	detector := c.getNodeDetector()
	nodePath, err := detector.Detect(postCtx)
	if err != nil {
		c.NotifyJobStepLog(jobID, "", fmt.Sprintf("Error detecting Node runtime for post hooks: %v", err))
		return err
	}

	var firstErr error
	for i := len(postHooks) - 1; i >= 0; i-- {
		hook := postHooks[i]
		hookCtx := make(map[string]any, len(hook.Context)+4)
		for k, v := range hook.Context {
			hookCtx[k] = v
		}
		if hook.Inputs != nil {
			hookCtx["inputs"] = hook.Inputs
		}
		if hook.Env != nil {
			hookCtx["env"] = hook.Env
		}
		if hook.State != nil {
			hookCtx["state"] = hook.State
		}

		cond := strings.TrimSpace(hook.PostIf)
		if cond == "" {
			cond = "always()"
		}
		jobCancelled := ctx.Err() != nil
		if !shouldExecuteStep(cond, hookCtx, jobFailed, jobCancelled) {
			continue
		}
		if err := c.executeSinglePostHook(postCtx, jobID, hook, nodePath, stepLogger, jobMasker); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// resolveActionScriptPath safely resolves a relative script path within an action directory,
// verifying that path traversal cannot escape the action directory boundary.
func resolveActionScriptPath(actionDir, relPath string) (string, error) {
	if strings.TrimSpace(relPath) == "" {
		return "", errors.New("action script path cannot be empty")
	}
	cleanDir := filepath.Clean(actionDir)
	cleanPath := filepath.Clean(filepath.Join(cleanDir, relPath))
	if !strings.HasPrefix(cleanPath, cleanDir+string(filepath.Separator)) && cleanPath != cleanDir {
		return "", fmt.Errorf("action script %q escapes action directory %s", relPath, actionDir)
	}
	return cleanPath, nil
}

func (c *Coordinator) executeSinglePostHook(ctx context.Context, jobID string, hook PostStepHook, nodePath string, stepLogger *StepLogger, jobMasker *SecretMasker) error {
	postEnv := make(map[string]string, len(hook.Env)+len(hook.State)+10)
	for k, v := range hook.Env {
		postEnv[k] = v
	}

	for k, v := range hook.State {
		upperKey := "STATE_" + strings.ToUpper(strings.ReplaceAll(k, " ", "_"))
		postEnv[upperKey] = v
		if _, exists := postEnv["STATE_"+k]; !exists {
			postEnv["STATE_"+k] = v
		}
	}

	scriptPath := hook.PostScript
	if hook.ActionDir != "" {
		resolved, err := resolveActionScriptPath(hook.ActionDir, scriptPath)
		if err != nil {
			return fmt.Errorf("invalid post hook script path: %w", err)
		}
		scriptPath = resolved
	}

	workspaceDir := postEnv["GITHUB_WORKSPACE"]
	if workspaceDir == "" {
		workspaceDir = hook.ActionDir
	}
	tempDir := postEnv["RUNNER_TEMP"]
	toolCacheDir := postEnv["RUNNER_TOOL_CACHE"]

	actCtx := &action.ActionContext{
		StepID:       hook.StepID,
		ActionDir:    hook.ActionDir,
		Inputs:       hook.Inputs,
		Env:          postEnv,
		WorkspaceDir: workspaceDir,
		TempDir:      tempDir,
		ToolCacheDir: toolCacheDir,
	}

	if hook.ActionDir != "" {
		if manifest, err := action.ParseManifestFile(hook.ActionDir); err == nil {
			if manifest.Inputs != nil && hook.Context != nil {
				for name, inputDef := range manifest.Inputs {
					if strings.Contains(inputDef.Default, "${{") {
						inputDef.Default = evaluateExpression(inputDef.Default, hook.Context)
						manifest.Inputs[name] = inputDef
					}
				}
			}
			actCtx.Manifest = manifest
		}
	}

	if tempDir != "" {
		if sf, err := os.CreateTemp(tempDir, fmt.Sprintf("post_state_%s_*.txt", hook.StepID)); err == nil {
			actCtx.StatePath = sf.Name()
			_ = sf.Close()
			defer func() { _ = os.Remove(actCtx.StatePath) }()
		}
		if of, err := os.CreateTemp(tempDir, fmt.Sprintf("post_out_%s_*.txt", hook.StepID)); err == nil {
			actCtx.OutputFilePath = of.Name()
			_ = of.Close()
			defer func() { _ = os.Remove(actCtx.OutputFilePath) }()
		}
		if ef, err := os.CreateTemp(tempDir, fmt.Sprintf("post_env_%s_*.txt", hook.StepID)); err == nil {
			actCtx.EnvFilePath = ef.Name()
			_ = ef.Close()
			defer func() { _ = os.Remove(actCtx.EnvFilePath) }()
		}
		if pf, err := os.CreateTemp(tempDir, fmt.Sprintf("post_path_%s_*.txt", hook.StepID)); err == nil {
			actCtx.PathFilePath = pf.Name()
			_ = pf.Close()
			defer func() { _ = os.Remove(actCtx.PathFilePath) }()
		}
	}

	stdout := c.stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := c.stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	stepDisplayName := hook.StepName
	if stepDisplayName == "" {
		stepDisplayName = hook.StepID
	}
	stepDisplayName = "Post " + stepDisplayName

	if jobMasker == nil {
		if stepLogger != nil {
			jobMasker = stepLogger.Masker()
		} else {
			jobMasker = NewSecretMasker()
		}
	}

	var stepOutWriter *StepWriter
	var stepErrWriter *StepWriter

	if stepLogger != nil {
		stepLogger.LogStepStart(stepDisplayName, fmt.Sprintf("Post action %s", hook.PostScript))
		stepOutWriter = stepLogger.NewWriter("stdout")
		stepErrWriter = stepLogger.NewWriter("stderr")
		stepOutWriter.WithCallback(func(line string) {
			c.NotifyJobStepLog(jobID, hook.StepID, line)
		})
		stepErrWriter.WithCallback(func(line string) {
			c.NotifyJobStepLog(jobID, hook.StepID, line)
		})
		stdout = stepOutWriter
		stderr = stepErrWriter
	} else {
		lineOutWriter := NewLineWriter(func(line string) {
			c.NotifyJobStepLog(jobID, hook.StepID, line)
		}).WithMasker(jobMasker)
		lineErrWriter := NewLineWriter(func(line string) {
			c.NotifyJobStepLog(jobID, hook.StepID, line)
		}).WithMasker(jobMasker)
		stdout = io.MultiWriter(stdout, lineOutWriter)
		stderr = io.MultiWriter(stderr, lineErrWriter)
		stepOutWriter = lineOutWriter
		stepErrWriter = lineErrWriter
	}

	startTime := time.Now()
	execErr := action.ExecuteNodeAction(ctx, nodePath, scriptPath, actCtx, stdout, stderr)
	if stepOutWriter != nil {
		stepOutWriter.Flush()
	}
	if stepErrWriter != nil {
		stepErrWriter.Flush()
	}
	duration := time.Since(startTime)
	success := execErr == nil
	if stepLogger != nil {
		stepLogger.LogStepEnd(stepDisplayName, success, duration)
	}

	if execErr != nil {
		c.NotifyJobStepLog(jobID, hook.StepID, fmt.Sprintf("Error executing post step %s: %v", hook.StepID, execErr))
		return execErr
	}
	return nil
}
