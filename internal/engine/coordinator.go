package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

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
	mu                sync.RWMutex
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

		// Build step environment: cumulative environment + step-specific overrides + command files
		stepEnv := make(map[string]string, len(cumulativeEnv)+len(step.Environment)+4)
		for k, v := range cumulativeEnv {
			stepEnv[k] = v
		}
		for k, v := range step.Environment {
			stepEnv[k] = evaluateExpression(v, parsedContext)
		}
		stepEnv["GITHUB_ENV"] = envPath
		stepEnv["GITHUB_OUTPUT"] = outputPath
		stepEnv["GITHUB_PATH"] = pathFilePath
		stepEnv["GITHUB_WORKSPACE"] = jobWorkspace

		stepWorkDir := step.WorkingDirectory
		if stepWorkDir == "" {
			stepWorkDir = jobWorkspace
		} else if !filepath.IsAbs(stepWorkDir) {
			stepWorkDir = filepath.Join(jobWorkspace, stepWorkDir)
		}
		if err := os.MkdirAll(stepWorkDir, 0o755); err != nil {
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
			}, fmt.Errorf("create step working directory: %w", err)
		}

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

		res, _, stepErr := c.executeStep(ctx, d, jobID, spec, stepLogger, stepDisplayName, jobMasker)

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
			_ = os.Remove(envPath)
			_ = os.Remove(outputPath)
			_ = os.Remove(pathFilePath)
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
			_ = os.Remove(envPath)
			_ = os.Remove(outputPath)
			_ = os.Remove(pathFilePath)
			conclusion = "failure"
			break
		}

		// Step succeeded: parse environment, output, and path files
		newEnv, err := ParseEnvAndOutputFile(envPath)
		if err != nil {
			c.NotifyStepFinish(jobID, stepID, stepDisplayName, monitor.StatusFailed)
			stepResults = append(stepResults, protocol.StepResult{
				ID:         stepID,
				Conclusion: "failure",
				ExitCode:   1,
			})
			_ = os.Remove(envPath)
			_ = os.Remove(outputPath)
			_ = os.Remove(pathFilePath)
			conclusion = "failure"
			break
		}
		for k, v := range newEnv {
			cumulativeEnv[k] = v
		}

		newOutputs, err := ParseEnvAndOutputFile(outputPath)
		if err != nil {
			c.NotifyStepFinish(jobID, stepID, stepDisplayName, monitor.StatusFailed)
			stepResults = append(stepResults, protocol.StepResult{
				ID:         stepID,
				Conclusion: "failure",
				ExitCode:   1,
			})
			_ = os.Remove(envPath)
			_ = os.Remove(outputPath)
			_ = os.Remove(pathFilePath)
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

		newPaths, err := ParsePathFile(pathFilePath)
		if err != nil {
			c.NotifyStepFinish(jobID, stepID, stepDisplayName, monitor.StatusFailed)
			stepResults = append(stepResults, protocol.StepResult{
				ID:         stepID,
				Conclusion: "failure",
				ExitCode:   1,
			})
			_ = os.Remove(envPath)
			_ = os.Remove(outputPath)
			_ = os.Remove(pathFilePath)
			conclusion = "failure"
			break
		}
		if len(newPaths) > 0 {
			pathPrefix := strings.Join(newPaths, string(filepath.ListSeparator))
			pathKey := "PATH"
			for k := range cumulativeEnv {
				if strings.EqualFold(k, "PATH") {
					pathKey = k
					break
				}
			}
			if existing, ok := cumulativeEnv[pathKey]; ok && existing != "" {
				cumulativeEnv[pathKey] = pathPrefix + string(filepath.ListSeparator) + existing
			} else {
				cumulativeEnv[pathKey] = pathPrefix
			}
		}

		stepResults = append(stepResults, protocol.StepResult{
			ID:         stepID,
			Conclusion: "success",
			ExitCode:   0,
		})

		_ = os.Remove(envPath)
		_ = os.Remove(outputPath)
		_ = os.Remove(pathFilePath)
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
	c.NotifyStepStart(jobID, spec.ID, stepDisplayName)

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
		stepLogger.LogStepStart(stepDisplayName, spec.Script)
		stepOutWriter = stepLogger.NewWriter("stdout")
		stepErrWriter = stepLogger.NewWriter("stderr")
		if monSrv != nil {
			stepOutWriter.WithCallback(func(line string) {
				c.NotifyJobStepLog(jobID, spec.ID, line)
			})
			stepErrWriter.WithCallback(func(line string) {
				c.NotifyJobStepLog(jobID, spec.ID, line)
			})
		}
		stepOut = stepOutWriter
		stepErrStream = stepErrWriter
	} else if monSrv != nil {
		stepOutWriter = NewLineWriter(func(line string) {
			c.NotifyJobStepLog(jobID, spec.ID, line)
		}).WithMasker(jobMasker)
		stepErrWriter = NewLineWriter(func(line string) {
			c.NotifyJobStepLog(jobID, spec.ID, line)
		}).WithMasker(jobMasker)
		stepOut = io.MultiWriter(stdout, stepOutWriter)
		stepErrStream = io.MultiWriter(stderr, stepErrWriter)
	}

	// Wire process start hook to telemetry sampler
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
	res, stepErr := d.ExecuteStep(ctx, spec, stepOut, stepErrStream)
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
		c.NotifyStepFinish(jobID, spec.ID, stepDisplayName, monitor.StatusSuccess, exitCode)
	} else {
		c.NotifyStepFinish(jobID, spec.ID, stepDisplayName, monitor.StatusFailed, exitCode)
	}

	return res, duration, stepErr
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
