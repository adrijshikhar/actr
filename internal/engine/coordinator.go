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
	"strings"
	"sync"
	"time"

	"github.com/adrijshikhar/actr/internal/action"
	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/protocol"
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
	actionResolver    action.Resolver
	nodeDetector      action.NodeDetector
	mu                sync.RWMutex
}

// PostStepHook represents a queued teardown hook for an action step.
type PostStepHook struct {
	StepID     string
	StepName   string
	ActionDir  string
	PostScript string
	Env        map[string]string
	State      map[string]string
	Inputs     map[string]string
}

// NewCoordinator creates a new Coordinator with the provided driver, RunServiceClient, and base working directory.
func NewCoordinator(d driver.Driver, runClient *protocol.RunServiceClient, workDir string) *Coordinator {
	if workDir == "" {
		workDir = "_work"
	}
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
	}
}

// WithDockerDriver configures the driver used for container-based jobs.
func (c *Coordinator) WithDockerDriver(d driver.Driver) *Coordinator {
	c.dockerDriver = d
	return c
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
			Type:   monitor.EventStepStart,
			JobID:  jobID,
			StepID: stepID,
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
			Type:   monitor.EventStepFinish,
			JobID:  jobID,
			StepID: stepID,
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
func (c *Coordinator) ExecuteJob(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error) {
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
		jobID = "job"
	}
	jobDisplayName := job.JobName
	if jobDisplayName == "" {
		jobDisplayName = jobID
	}

	c.mu.RLock()
	monSrv := c.monServer
	c.mu.RUnlock()

	if monSrv != nil {
		jobInfo := &monitor.JobInfo{
			ID:        jobID,
			Name:      jobDisplayName,
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
		if job.Variables != nil {
			if wf, ok := job.Variables["GITHUB_WORKFLOW"]; ok {
				jobInfo.Workflow = wf
			}
			if repo, ok := job.Variables["GITHUB_REPOSITORY"]; ok {
				jobInfo.Repo = repo
			}
			if owner, ok := job.Variables["GITHUB_REPOSITORY_OWNER"]; ok {
				jobInfo.Owner = owner
			}
		}
		monSrv.Publish(monitor.Event{
			Type:  monitor.EventJobStart,
			JobID: jobID,
			Job:   jobInfo,
		})
	}

	conclusion := "success"
	defer func() {
		c.mu.RLock()
		s := c.monServer
		c.mu.RUnlock()
		if s != nil {
			finalStatus := monitor.StatusSuccess
			if conclusion != "success" {
				finalStatus = monitor.StatusFailed
			}
			s.Publish(monitor.Event{
				Type:  monitor.EventJobFinish,
				JobID: jobID,
				Job: &monitor.JobInfo{
					ID:      jobID,
					Name:    jobDisplayName,
					Status:  finalStatus,
					EndedAt: time.Now(),
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
			if strings.HasPrefix(k, "system.github.") {
				sub := strings.TrimPrefix(k, "system.github.")
				upperKey := "GITHUB_" + strings.ToUpper(sub)
				if cumulativeEnv[upperKey] == "" {
					cumulativeEnv[upperKey] = v
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
	if cumulativeEnv["GITHUB_SERVER_URL"] == "" {
		cumulativeEnv["GITHUB_SERVER_URL"] = "https://github.com"
	}
	if cumulativeEnv["GITHUB_API_URL"] == "" {
		cumulativeEnv["GITHUB_API_URL"] = "https://api.github.com"
	}
	if cumulativeEnv["GITHUB_GRAPHQL_URL"] == "" {
		cumulativeEnv["GITHUB_GRAPHQL_URL"] = "https://api.github.com/graphql"
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
	if ghCtx["token"] == nil && cumulativeEnv["GITHUB_TOKEN"] != "" {
		ghCtx["token"] = cumulativeEnv["GITHUB_TOKEN"]
	}
	if ghCtx["repository"] == nil && cumulativeEnv["GITHUB_REPOSITORY"] != "" {
		ghCtx["repository"] = cumulativeEnv["GITHUB_REPOSITORY"]
	}
	if ghCtx["event_name"] == nil && cumulativeEnv["GITHUB_EVENT_NAME"] != "" {
		ghCtx["event_name"] = cumulativeEnv["GITHUB_EVENT_NAME"]
	}
	if ghCtx["server_url"] == nil && cumulativeEnv["GITHUB_SERVER_URL"] != "" {
		ghCtx["server_url"] = cumulativeEnv["GITHUB_SERVER_URL"]
	}
	if ghCtx["api_url"] == nil && cumulativeEnv["GITHUB_API_URL"] != "" {
		ghCtx["api_url"] = cumulativeEnv["GITHUB_API_URL"]
	}
	if ghCtx["graphql_url"] == nil && cumulativeEnv["GITHUB_GRAPHQL_URL"] != "" {
		ghCtx["graphql_url"] = cumulativeEnv["GITHUB_GRAPHQL_URL"]
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

	workflowName := ""
	if job.Variables != nil {
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

	var stepLogger *StepLogger
	if c.stepLoggerEnabled {
		stepLogger = NewStepLogger(jobDisplayName, stdout, c.colorEnabled).WithMasker(jobMasker)
	}

	for i, step := range job.Steps {
		if err := ctx.Err(); err != nil {
			conclusion = "cancelled"
			break
		}

		stepID := step.ID
		if stepID == "" {
			stepID = fmt.Sprintf("step_%d", i+1)
		}

		stepDisplayName := step.Name
		if stepDisplayName == "" {
			stepDisplayName = stepID
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

		cleanupStepFiles := func() {
			_ = os.Remove(envPath)
			_ = os.Remove(outputPath)
			_ = os.Remove(pathFilePath)
			_ = os.Remove(statePath)
		}

		// Build step environment: cumulative environment + step-specific overrides + command files
		stepEnv := make(map[string]string, len(cumulativeEnv)+len(step.Environment)+5)
		for k, v := range cumulativeEnv {
			stepEnv[k] = v
		}
		for k, v := range step.Environment {
			stepEnv[k] = evaluateExpression(v, parsedContext)
		}
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
				evaluatedInputs[k] = evaluateExpression(v, parsedContext)
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
			}
			res, _, stepErr = c.executeActionStep(ctx, jobID, stepID, stepDisplayName, ref, actCtx, stepLogger, jobMasker)

			// Ingest state file and queue post-step hook if manifest defines runs.post
			stepState, _ := IngestStateFile(statePath)
			if actCtx != nil && actCtx.Manifest != nil && strings.TrimSpace(actCtx.Manifest.Runs.Post) != "" {
				postScript := actCtx.Manifest.Runs.Post
				hookEnv := make(map[string]string, len(stepEnv))
				for k, v := range stepEnv {
					hookEnv[k] = v
				}
				postHooks = append(postHooks, PostStepHook{
					StepID:     stepID,
					StepName:   stepDisplayName,
					ActionDir:  actCtx.ActionDir,
					PostScript: postScript,
					Env:        hookEnv,
					State:      stepState,
					Inputs:     evaluatedInputs,
				})
			}
		} else {
			stepScript := step.Script
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

			res, _, stepErr = c.executeStep(ctx, d, jobID, spec, stepLogger, stepDisplayName, jobMasker)
		}

		if ctx.Err() != nil {
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
			conclusion = "cancelled"
			break
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
			conclusion = "failure"
			break
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
			conclusion = "failure"
			break
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
			conclusion = "failure"
			break
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
			conclusion = "failure"
			break
		}
		cumulativeEnv[pathKey] = updatedPath

		stepResults = append(stepResults, protocol.StepResult{
			ID:         stepID,
			Conclusion: "success",
			ExitCode:   0,
		})

		cleanupStepFiles()
	}

	// Teardown block: execute post-step hooks in reverse order (LIFO) with decoupled context
	if len(postHooks) > 0 {
		if postErr := c.executePostHooks(ctx, jobID, postHooks, stepLogger, jobMasker); postErr != nil {
			if conclusion == "success" {
				conclusion = "failure"
			}
		}
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

	return c.executeWithStreams(ctx, jobID, spec.ID, stepDisplayName, spec.Script, stepLogger, jobMasker, func(stdout, stderr io.Writer) (*driver.StepResult, error) {
		return d.ExecuteStep(ctx, spec, stdout, stderr)
	})
}

// executeActionStep resolves, detects node runtime, and executes a JavaScript action step.
func (c *Coordinator) executeActionStep(ctx context.Context, jobID, stepID, stepDisplayName string, ref *protocol.StepReference, actCtx *action.ActionContext, stepLogger *StepLogger, jobMasker *SecretMasker) (*driver.StepResult, time.Duration, error) {
	scriptDesc := ""
	if ref != nil {
		scriptDesc = fmt.Sprintf("uses: %s@%s", ref.Name, ref.Version)
	}
	return c.executeWithStreams(ctx, jobID, stepID, stepDisplayName, scriptDesc, stepLogger, jobMasker, func(stdout, stderr io.Writer) (*driver.StepResult, error) {
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

		scriptPath := filepath.Join(actionDir, manifest.Runs.Main)
		actCtx.ActionDir = actionDir
		actCtx.Manifest = manifest

		execErr := action.ExecuteNodeAction(ctx, nodePath, scriptPath, actCtx, stdout, stderr)
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
func (c *Coordinator) executeWithStreams(ctx context.Context, jobID, stepID, stepDisplayName, scriptDesc string, stepLogger *StepLogger, jobMasker *SecretMasker, runFn func(stdout, stderr io.Writer) (*driver.StepResult, error)) (*driver.StepResult, time.Duration, error) {
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
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()

			sample := sampler.Sample()
			monSrv.SetTelemetry(sample)

			for {
				select {
				case <-stopTelemetry:
					return
				case <-ticker.C:
					sample := sampler.Sample()
					monSrv.SetTelemetry(sample)
				}
			}
		}()
	}

	defer func() {
		c.mu.RLock()
		s := c.sampler
		c.mu.RUnlock()
		if s != nil {
			s.SetTargetPID(0, 0)
			if monSrv != nil {
				monSrv.SetTelemetry(s.Sample())
			}
		}
	}()

	startTime := time.Now()
	res, stepErr := runFn(stepOut, stepErrStream)
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

	if success {
		c.NotifyStepFinish(jobID, stepID, stepDisplayName, monitor.StatusSuccess, exitCode)
	} else {
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
    CLEAN_REF="${TARGET_REF#refs/heads/}"
    git checkout -f -- "$CLEAN_REF" 2>/dev/null || \
    git checkout -f -- "$TARGET_REF" 2>/dev/null || (
        git fetch --depth=1 "$CLONE_URL" "$TARGET_REF" 2>/dev/null || git fetch "$CLONE_URL"
        git checkout -f -- "$CLEAN_REF" 2>/dev/null || git checkout -f -- "$TARGET_REF"
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
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		val, exists := m[part]
		if !exists {
			return nil, false
		}
		current = val
	}
	return current, true
}

func evaluateExpression(expr string, ctx map[string]any) string {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return ""
	}

	// Handle OR expressions: left || right
	if strings.Contains(trimmed, " || ") {
		parts := strings.Split(trimmed, " || ")
		for _, part := range parts {
			res := evaluateExpression(part, ctx)
			if res != "" && res != "false" && res != "null" {
				return res
			}
		}
		return evaluateExpression(parts[len(parts)-1], ctx)
	}

	// Handle equality expressions: left == right
	if strings.Contains(trimmed, " == ") {
		parts := strings.SplitN(trimmed, " == ", 2)
		left := evaluateExpression(parts[0], ctx)
		right := evaluateExpression(parts[1], ctx)
		if strings.EqualFold(left, right) {
			return "true"
		}
		return "false"
	}

	// String literals: 'foo'
	if strings.HasPrefix(trimmed, "'") && strings.HasSuffix(trimmed, "'") && len(trimmed) >= 2 {
		return trimmed[1 : len(trimmed)-1]
	}

	// Boolean/null literals
	if trimmed == "true" || trimmed == "false" || trimmed == "null" {
		return trimmed
	}

	// Context lookup (e.g. inputs.platforms, needs.detect.outputs.platforms, github.event_name)
	if val, ok := lookupContext(trimmed, ctx); ok && val != nil {
		return fmt.Sprint(val)
	} else if strings.Contains(trimmed, ".") {
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

// IngestEnvFile reads key=value or multiline delimiter syntax from filePath,
// merges them into currentEnv, and truncates the file.
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
	return c.executePostHooks(ctx, jobID, postHooks, nil, nil)
}

// executePostHooks executes all queued post-step hooks in reverse order (LIFO).
// It decouples from the job context using context.WithoutCancel with a 5-minute timeout.
func (c *Coordinator) executePostHooks(ctx context.Context, jobID string, postHooks []PostStepHook, stepLogger *StepLogger, jobMasker *SecretMasker) error {
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
		if err := c.executeSinglePostHook(postCtx, jobID, hook, nodePath, stepLogger, jobMasker); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
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
	if !filepath.IsAbs(scriptPath) && hook.ActionDir != "" {
		scriptPath = filepath.Join(hook.ActionDir, scriptPath)
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
