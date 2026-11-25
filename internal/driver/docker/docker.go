package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// Driver executes GitHub Actions steps inside isolated Docker containers with resource limits.
type Driver struct {
	cli      DockerClient
	autoPull bool

	mu          sync.Mutex
	containerID string
	jobCtx      *driver.JobContext

	execIDMu   sync.Mutex
	execCancel context.CancelFunc
}

// Ensure Driver satisfies driver.Driver and driver.Cloneable interfaces.
var _ driver.Driver = (*Driver)(nil)
var _ driver.Cloneable = (*Driver)(nil)

// NewDriver constructs a Docker driver with the given client and pull policy.
func NewDriver(cli DockerClient, autoPull bool) *Driver {
	return &Driver{
		cli:      cli,
		autoPull: autoPull,
	}
}

// Name returns the driver identifier.
func (d *Driver) Name() string {
	return "docker"
}

// Clone returns an isolated Driver instance sharing the underlying docker client and pull policy.
// This ensures concurrent jobs do not clobber container state or job context.
func (d *Driver) Clone() driver.Driver {
	d.mu.Lock()
	cli := d.cli
	autoPull := d.autoPull
	d.mu.Unlock()
	return NewDriver(cli, autoPull)
}

// ContainerID returns the currently running container ID for testing and telemetry tracking.
func (d *Driver) ContainerID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.containerID
}

type pullStreamMessage struct {
	Error       string `json:"error,omitempty"`
	ErrorDetail *struct {
		Message string `json:"message,omitempty"`
	} `json:"errorDetail,omitempty"`
}

func decodeImagePullStream(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var msg pullStreamMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Error != "" {
			return errors.New(msg.Error)
		}
		if msg.ErrorDetail != nil && msg.ErrorDetail.Message != "" {
			return errors.New(msg.ErrorDetail.Message)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read pull stream: %w", err)
	}
	return nil
}

// PrepareJob provisions the container image, mounts, and resource governance limits.
func (d *Driver) PrepareJob(ctx context.Context, job *driver.JobContext) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if job == nil {
		return errors.New("nil job context")
	}

	if job.WorkspaceDir != "" {
		if err := os.MkdirAll(job.WorkspaceDir, 0755); err != nil {
			return fmt.Errorf("prepare workspace dir: %w", err)
		}
	}
	if job.TempDir != "" {
		if err := os.MkdirAll(job.TempDir, 0755); err != nil {
			return fmt.Errorf("prepare temp dir: %w", err)
		}
	}

	imageName := ""
	if job.Container != nil && strings.TrimSpace(job.Container.Image) != "" {
		imageName = strings.TrimSpace(job.Container.Image)
	}
	if imageName == "" {
		return errors.New("no container image specified in job context")
	}

	// Check local image availability
	_, _, err := d.cli.ImageInspectWithRaw(ctx, imageName)
	if err != nil {
		if !d.autoPull {
			return fmt.Errorf("image %q not found locally and autoPull is false: %w", imageName, err)
		}
		reader, pullErr := d.cli.ImagePull(ctx, imageName, image.PullOptions{})
		if pullErr != nil {
			return fmt.Errorf("pull image %q: %w", imageName, pullErr)
		}
		if reader != nil {
			err := decodeImagePullStream(reader)
			_ = reader.Close()
			if err != nil {
				return fmt.Errorf("pull image %q: %w", imageName, err)
			}
		}
	}

	containerConfig := &container.Config{
		Image:      imageName,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"tail -f /dev/null"},
		WorkingDir: "/github/workspace",
		Labels: map[string]string{
			"actr.managed": "true",
			"actr.job_id":  job.JobID,
		},
	}

	if job.Container != nil && len(job.Container.Environment) > 0 {
		keys := make([]string, 0, len(job.Container.Environment))
		for k := range job.Container.Environment {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			containerConfig.Env = append(containerConfig.Env, fmt.Sprintf("%s=%s", k, job.Container.Environment[k]))
		}
	}

	// On Linux, map container execution to host user's UID:GID to avoid root file permission lockouts
	if runtime.GOOS == "linux" {
		containerConfig.User = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}

	binds := make([]string, 0, 4)
	if job.WorkspaceDir != "" {
		binds = append(binds, fmt.Sprintf("%s:/github/workspace:rw", job.WorkspaceDir))
	}
	if job.TempDir != "" {
		binds = append(binds, fmt.Sprintf("%s:/github/workflow:rw", job.TempDir))
	}
	if job.Container != nil && len(job.Container.Volumes) > 0 {
		binds = append(binds, job.Container.Volumes...)
	}

	hostConfig := &container.HostConfig{
		Binds: binds,
	}

	if job.Limits != nil {
		if job.Limits.CPU > 0 {
			hostConfig.NanoCPUs = int64(job.Limits.CPU * 1e9)
		}
		if job.Limits.Memory > 0 {
			hostConfig.Memory = job.Limits.Memory
			hostConfig.MemorySwap = job.Limits.Memory
		}
		if job.Limits.Pids > 0 {
			pids := job.Limits.Pids
			hostConfig.PidsLimit = &pids
		}
	}

	containerName := fmt.Sprintf("actr-%s-%d", sanitizeContainerName(job.JobID), time.Now().UnixNano())
	resp, err := d.cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}

	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = d.cli.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{
			Force:         true,
			RemoveVolumes: true,
		})
		cancel()
		d.mu.Lock()
		d.containerID = ""
		d.jobCtx = nil
		d.mu.Unlock()
		return fmt.Errorf("start container: %w", err)
	}

	d.mu.Lock()
	d.containerID = resp.ID
	d.jobCtx = job
	d.mu.Unlock()

	return nil
}

// ExecuteStep writes the step script to the host temp directory and executes it inside the container.
func (d *Driver) ExecuteStep(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
	if step == nil {
		return nil, errors.New("nil step specification")
	}
	if ctx.Err() != nil {
		return &driver.StepResult{ExitCode: 1, Error: ctx.Err()}, ctx.Err()
	}

	d.mu.Lock()
	cid := d.containerID
	job := d.jobCtx
	d.mu.Unlock()

	if cid == "" {
		return nil, errors.New("container not running")
	}

	stepCtx, stepCancel := context.WithCancel(ctx)
	defer stepCancel()

	d.execIDMu.Lock()
	d.execCancel = stepCancel
	d.execIDMu.Unlock()

	defer func() {
		d.execIDMu.Lock()
		d.execCancel = nil
		d.execIDMu.Unlock()
	}()

	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	tempDir := ""
	if job != nil && job.TempDir != "" {
		tempDir = job.TempDir
	} else {
		tempDir = os.TempDir()
	}

	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return nil, fmt.Errorf("prepare temp dir: %w", err)
	}

	f, err := os.CreateTemp(tempDir, fmt.Sprintf("step_%s_*.sh", sanitizeStepID(step.ID)))
	if err != nil {
		return nil, fmt.Errorf("create step script: %w", err)
	}
	hostScript := f.Name()
	defer func() { _ = os.Remove(hostScript) }()

	if err := f.Chmod(0755); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("chmod step script: %w", err)
	}
	if _, err := f.WriteString(step.Script); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write step script: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close step script: %w", err)
	}

	// Environment variable translation for container consumption
	containerEnv := make(map[string]string)
	if job != nil {
		for k, v := range job.Environment {
			containerEnv[k] = v
		}
	}
	for k, v := range step.Environment {
		containerEnv[k] = v
	}

	hostWorkspace := ""
	hostTemp := ""
	if job != nil {
		hostWorkspace = job.WorkspaceDir
		hostTemp = job.TempDir
	}

	for k, v := range containerEnv {
		switch k {
		case "GITHUB_ENV", "GITHUB_OUTPUT", "GITHUB_PATH", "GITHUB_STATE":
			containerEnv[k] = translateTempPath(v, hostTemp)
		case "GITHUB_WORKSPACE":
			containerEnv[k] = "/github/workspace"
		case "RUNNER_TEMP":
			containerEnv[k] = "/github/workflow"
		case "RUNNER_WORKSPACE":
			containerEnv[k] = "/github"
		default:
			if hostWorkspace != "" && hasPathPrefix(v, hostWorkspace) {
				containerEnv[k] = translateWorkspacePath(v, hostWorkspace)
			} else if hostTemp != "" && hasPathPrefix(v, hostTemp) {
				containerEnv[k] = translateTempPath(v, hostTemp)
			}
		}
	}

	envSlice := make([]string, 0, len(containerEnv))
	for k, v := range containerEnv {
		envSlice = append(envSlice, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(envSlice)

	containerWorkDir := translateWorkspacePath(step.WorkingDirectory, hostWorkspace)

	shell := step.Shell
	if shell == "" {
		shell = "sh"
	}
	containerScriptPath := path.Join("/github/workflow", filepath.Base(hostScript))
	cmd := []string{shell, "-e", containerScriptPath}

	execConfig := container.ExecOptions{
		Cmd:          cmd,
		Env:          envSlice,
		WorkingDir:   containerWorkDir,
		AttachStdout: true,
		AttachStderr: true,
	}

	execResp, err := d.cli.ContainerExecCreate(stepCtx, cid, execConfig)
	if err != nil {
		return nil, fmt.Errorf("container exec create: %w", err)
	}

	attachResp, err := d.cli.ContainerExecAttach(stepCtx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("container exec attach: %w", err)
	}
	defer attachResp.Close()

	if step.OnProcessStart != nil {
		step.OnProcessStart(1, 1)
	}

	var wg sync.WaitGroup
	stopWatcher := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-stepCtx.Done():
			killCtx, killCancel := context.WithTimeout(context.WithoutCancel(stepCtx), 2*time.Second)
			defer killCancel()

			if inspect, err := d.cli.ContainerExecInspect(killCtx, execResp.ID); err == nil && inspect.Pid > 0 {
				if killResp, kErr := d.cli.ContainerExecCreate(killCtx, cid, container.ExecOptions{
					Cmd: []string{"kill", "-KILL", strconv.Itoa(inspect.Pid)},
				}); kErr == nil {
					if killAttach, aErr := d.cli.ContainerExecAttach(killCtx, killResp.ID, container.ExecAttachOptions{}); aErr == nil {
						killAttach.Close()
					}
				}
			}
			attachResp.Close()
		case <-stopWatcher:
		}
	}()

	// Demultiplex container stdout and stderr streams
	_, copyErr := stdcopy.StdCopy(stdout, stderr, attachResp.Reader)
	close(stopWatcher)
	wg.Wait()

	if stepCtx.Err() == nil && copyErr != nil {
		return nil, fmt.Errorf("read container streams: %w", copyErr)
	}

	inspectCtx, inspectCancel := context.WithTimeout(context.WithoutCancel(stepCtx), 3*time.Second)
	defer inspectCancel()

	inspectResp, err := d.cli.ContainerExecInspect(inspectCtx, execResp.ID)
	if err != nil {
		if stepCtx.Err() != nil {
			return &driver.StepResult{ExitCode: 1, Error: stepCtx.Err()}, stepCtx.Err()
		}
		return nil, fmt.Errorf("container exec inspect: %w", err)
	}

	exitCode := inspectResp.ExitCode
	oomKilled := false

	if exitCode == 137 {
		containerJSON, inspectErr := d.cli.ContainerInspect(inspectCtx, cid)
		if inspectErr == nil && containerJSON.State != nil && containerJSON.State.OOMKilled {
			oomKilled = true
		}
	}

	var stepErr error
	if exitCode != 0 {
		stepErr = fmt.Errorf("command failed with exit code %d", exitCode)
	}
	if stepCtx.Err() != nil {
		stepErr = stepCtx.Err()
		if exitCode == 0 {
			exitCode = 1
		}
	}

	return &driver.StepResult{
		ExitCode:  exitCode,
		OOMKilled: oomKilled,
		Error:     stepErr,
	}, stepErr
}

// CleanupJob terminates and removes the job container and associated temporary volumes.
func (d *Driver) CleanupJob(ctx context.Context) error {
	d.execIDMu.Lock()
	if d.execCancel != nil {
		d.execCancel()
	}
	d.execIDMu.Unlock()

	d.mu.Lock()
	cid := d.containerID
	d.containerID = ""
	d.jobCtx = nil
	d.mu.Unlock()

	if cid == "" {
		return nil
	}

	// Use detached uncancelable context with timeout to guarantee cleanup even on SIGINT
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	err := d.cli.ContainerRemove(cleanupCtx, cid, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
	if err != nil {
		if client.IsErrNotFound(err) || strings.Contains(strings.ToLower(err.Error()), "no such container") {
			return nil
		}
		return err
	}
	return nil
}

// Cleanup is an alias for CleanupJob.
func (d *Driver) Cleanup(ctx context.Context) error {
	return d.CleanupJob(ctx)
}

func sanitizeContainerName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	res := b.String()
	if res == "" {
		return "job"
	}
	return res
}

func sanitizeStepID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	res := b.String()
	if res == "" {
		return "step"
	}
	return res
}

func hasPathPrefix(p, prefix string) bool {
	cp := filepath.Clean(p)
	cpref := filepath.Clean(prefix)
	if cp == cpref {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(cp, strings.TrimSuffix(cpref, sep)+sep)
}

func translateTempPath(hostPath, hostTemp string) string {
	if hostPath == "" {
		return ""
	}
	if hostTemp != "" && hasPathPrefix(hostPath, hostTemp) {
		rel, err := filepath.Rel(hostTemp, hostPath)
		if err == nil {
			return path.Join("/github/workflow", filepath.ToSlash(rel))
		}
	}
	if idx := strings.Index(hostPath, "_runner_file_commands"); idx != -1 {
		return path.Join("/github/workflow", filepath.ToSlash(hostPath[idx:]))
	}
	return path.Join("/github/workflow", filepath.Base(hostPath))
}

func translateWorkspacePath(hostPath, hostWorkspace string) string {
	if hostPath == "" || hostPath == "." {
		return "/github/workspace"
	}
	if hostWorkspace != "" {
		if hostPath == hostWorkspace {
			return "/github/workspace"
		}
		if hasPathPrefix(hostPath, hostWorkspace) {
			rel, err := filepath.Rel(hostWorkspace, hostPath)
			if err == nil {
				return path.Join("/github/workspace", filepath.ToSlash(rel))
			}
		}
	}
	if filepath.IsAbs(hostPath) {
		return hostPath
	}
	return path.Join("/github/workspace", filepath.ToSlash(hostPath))
}
