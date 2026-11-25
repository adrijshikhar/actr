package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockConn implements a no-op net.Conn for hijacked docker stream simulation.
type mockConn struct {
	net.Conn
	closeCalled atomic.Bool
	onClose     func()
}

func (m *mockConn) Close() error {
	m.closeCalled.Store(true)
	if m.onClose != nil {
		m.onClose()
	}
	return nil
}

// makeDockerStream encodes stdout and stderr into Docker's multiplexed stream format.
func makeDockerStream(stdoutStr, stderrStr string) io.Reader {
	var buf bytes.Buffer
	if stdoutStr != "" {
		payload := []byte(stdoutStr)
		hdr := make([]byte, 8)
		hdr[0] = 1 // stdout
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
		buf.Write(hdr)
		buf.Write(payload)
	}
	if stderrStr != "" {
		payload := []byte(stderrStr)
		hdr := make([]byte, 8)
		hdr[0] = 2 // stderr
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
		buf.Write(hdr)
		buf.Write(payload)
	}
	return &buf
}

type mockDockerClient struct {
	mu sync.Mutex

	existingImages map[string]types.ImageInspect
	inspectError   error
	pullError      error
	pullStream     io.Reader

	failNextStart bool
	createCount   int

	inspectedImages []string
	pulledImages    []string

	createdConfig           *container.Config
	createdHostConfig       *container.HostConfig
	createdNetworkingConfig *network.NetworkingConfig
	createdContainerName    string
	createResponseID        string
	createError             error

	startedContainerID string
	startError         error

	createdExecConfig   container.ExecOptions
	createdExecCommands [][]string
	execCreateID        string
	execCreateError     error

	attachedExecID   string
	execStreamStdout string
	execStreamStderr string
	execAttachError  error
	customHijacked   *types.HijackedResponse

	execInspectResp container.ExecInspect
	execInspectErr  error

	containerInspectResp types.ContainerJSON
	containerInspectErr  error

	removedContainerID string
	removedOptions     container.RemoveOptions
	removeError        error
}

func newMockDockerClient() *mockDockerClient {
	return &mockDockerClient{
		existingImages:   make(map[string]types.ImageInspect),
		createResponseID: "container-mock-12345",
		execCreateID:     "exec-mock-67890",
		execInspectResp: container.ExecInspect{
			ExitCode: 0,
			Running:  false,
		},
	}
}

func (m *mockDockerClient) ImageInspectWithRaw(ctx context.Context, img string) (types.ImageInspect, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inspectedImages = append(m.inspectedImages, img)
	if m.inspectError != nil {
		return types.ImageInspect{}, nil, m.inspectError
	}
	if resp, ok := m.existingImages[img]; ok {
		return resp, nil, nil
	}
	return types.ImageInspect{}, nil, errors.New("No such image")
}

func (m *mockDockerClient) ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pulledImages = append(m.pulledImages, ref)
	if m.pullError != nil {
		return nil, m.pullError
	}
	if m.pullStream != nil {
		return io.NopCloser(m.pullStream), nil
	}
	return io.NopCloser(strings.NewReader(`{"status":"Pull complete"}`)), nil
}

func (m *mockDockerClient) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createdConfig = config
	m.createdHostConfig = hostConfig
	m.createdNetworkingConfig = networkingConfig
	m.createdContainerName = containerName
	if m.createError != nil {
		return container.CreateResponse{}, m.createError
	}
	m.createCount++
	id := m.createResponseID
	if m.createCount > 1 {
		id = fmt.Sprintf("%s-%d", m.createResponseID, m.createCount)
	}
	return container.CreateResponse{ID: id}, nil
}

func (m *mockDockerClient) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startedContainerID = containerID
	if m.failNextStart {
		return errors.New("cannot start container: failed start")
	}
	return m.startError
}

func (m *mockDockerClient) ContainerExecCreate(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createdExecConfig = config
	m.createdExecCommands = append(m.createdExecCommands, config.Cmd)
	if m.execCreateError != nil {
		return types.IDResponse{}, m.execCreateError
	}
	return types.IDResponse{ID: m.execCreateID}, nil
}

func (m *mockDockerClient) ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attachedExecID = execID
	if m.execAttachError != nil {
		return types.HijackedResponse{}, m.execAttachError
	}
	if m.customHijacked != nil {
		return *m.customHijacked, nil
	}
	stream := makeDockerStream(m.execStreamStdout, m.execStreamStderr)
	return types.HijackedResponse{
		Reader: bufio.NewReader(stream),
		Conn:   &mockConn{},
	}, nil
}

func (m *mockDockerClient) ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.execInspectErr != nil {
		return container.ExecInspect{}, m.execInspectErr
	}
	return m.execInspectResp, nil
}

func (m *mockDockerClient) ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.containerInspectErr != nil {
		return types.ContainerJSON{}, m.containerInspectErr
	}
	return m.containerInspectResp, nil
}

func (m *mockDockerClient) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removedContainerID = containerID
	m.removedOptions = options
	return m.removeError
}

func TestDockerDriver_Name(t *testing.T) {
	mockCli := newMockDockerClient()
	drv := NewDriver(mockCli, true)
	assert.Equal(t, "docker", drv.Name())
}

func TestDockerDriver_PrepareJob_ContainerCreatedWithMountsAndLimits(t *testing.T) {
	mockCli := newMockDockerClient()
	drv := NewDriver(mockCli, true)

	tempDir := t.TempDir()
	workspaceDir := filepath.Join(tempDir, "workspace")
	runnerTempDir := filepath.Join(tempDir, "temp")

	jobCtx := &driver.JobContext{
		JobID:        "job-100",
		WorkspaceDir: workspaceDir,
		TempDir:      runnerTempDir,
		Container: &protocol.JobContainer{
			Image: "node:20-bookworm",
		},
		Limits: &config.LimitsConfig{
			CPU:    2.0,
			Memory: 4 * 1024 * 1024 * 1024,
			Pids:   512,
		},
	}

	err := drv.PrepareJob(context.Background(), jobCtx)
	require.NoError(t, err)

	// Verify pull occurred since image was not in mock cache
	assert.Contains(t, mockCli.pulledImages, "node:20-bookworm")

	// Verify container config
	require.NotNil(t, mockCli.createdConfig)
	assert.Equal(t, "node:20-bookworm", mockCli.createdConfig.Image)
	assert.Equal(t, []string{"/bin/sh", "-c"}, []string(mockCli.createdConfig.Entrypoint))
	assert.Equal(t, []string{"tail -f /dev/null"}, []string(mockCli.createdConfig.Cmd))

	// Verify host config mounts and limits
	require.NotNil(t, mockCli.createdHostConfig)
	assert.Equal(t, int64(2e9), mockCli.createdHostConfig.NanoCPUs)
	assert.Equal(t, int64(4*1024*1024*1024), mockCli.createdHostConfig.Memory)
	require.NotNil(t, mockCli.createdHostConfig.PidsLimit)
	assert.Equal(t, int64(512), *mockCli.createdHostConfig.PidsLimit)

	expectedWorkspaceBind := fmt.Sprintf("%s:/github/workspace:rw", workspaceDir)
	expectedTempBind := fmt.Sprintf("%s:/github/workflow:rw", runnerTempDir)
	assert.Contains(t, mockCli.createdHostConfig.Binds, expectedWorkspaceBind)
	assert.Contains(t, mockCli.createdHostConfig.Binds, expectedTempBind)

	// Verify container started
	assert.Equal(t, mockCli.createResponseID, mockCli.startedContainerID)
	assert.Equal(t, mockCli.createResponseID, drv.ContainerID())
}

func TestDockerDriver_PrepareJob_ImageExists_NoPull(t *testing.T) {
	mockCli := newMockDockerClient()
	mockCli.existingImages["golang:1.23-alpine"] = types.ImageInspect{
		ID: "sha256:existing-image-id",
	}

	drv := NewDriver(mockCli, true)

	tempDir := t.TempDir()
	jobCtx := &driver.JobContext{
		JobID:        "job-existing",
		WorkspaceDir: filepath.Join(tempDir, "workspace"),
		TempDir:      filepath.Join(tempDir, "temp"),
		Container: &protocol.JobContainer{
			Image: "golang:1.23-alpine",
		},
	}

	err := drv.PrepareJob(context.Background(), jobCtx)
	require.NoError(t, err)

	assert.Contains(t, mockCli.inspectedImages, "golang:1.23-alpine")
	assert.Empty(t, mockCli.pulledImages)
	assert.Equal(t, mockCli.createResponseID, mockCli.startedContainerID)
}

func TestDockerDriver_PrepareJob_AutoPullFalse_ImageMissing(t *testing.T) {
	mockCli := newMockDockerClient()
	drv := NewDriver(mockCli, false) // autoPull is false

	tempDir := t.TempDir()
	jobCtx := &driver.JobContext{
		JobID:        "job-no-pull",
		WorkspaceDir: filepath.Join(tempDir, "workspace"),
		TempDir:      filepath.Join(tempDir, "temp"),
		Container: &protocol.JobContainer{
			Image: "custom-image:nonexistent",
		},
	}

	err := drv.PrepareJob(context.Background(), jobCtx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "autoPull is false")
	assert.Empty(t, mockCli.pulledImages)
	assert.Nil(t, mockCli.createdConfig)
}

func TestDockerDriver_PrepareJob_NilJobContext(t *testing.T) {
	mockCli := newMockDockerClient()
	drv := NewDriver(mockCli, true)

	err := drv.PrepareJob(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil job context")
}

func TestDockerDriver_ExecuteStep_PathRewritingAndExecution(t *testing.T) {
	mockCli := newMockDockerClient()
	mockCli.execStreamStdout = "build completed successfully\n"
	mockCli.execStreamStderr = ""
	mockCli.execInspectResp = container.ExecInspect{
		ExitCode: 0,
	}

	drv := NewDriver(mockCli, true)

	tempDir := t.TempDir()
	workspaceDir := filepath.Join(tempDir, "workspace")
	runnerTempDir := filepath.Join(tempDir, "temp")
	fileCommandsDir := filepath.Join(runnerTempDir, "_runner_file_commands")
	require.NoError(t, os.MkdirAll(fileCommandsDir, 0755))

	envFile := filepath.Join(fileCommandsDir, "set_env_uuid-1234")
	outputFile := filepath.Join(fileCommandsDir, "set_output_uuid-1234")
	pathFile := filepath.Join(fileCommandsDir, "add_path_uuid-1234")

	jobCtx := &driver.JobContext{
		JobID:        "job-exec-test",
		WorkspaceDir: workspaceDir,
		TempDir:      runnerTempDir,
		Container: &protocol.JobContainer{
			Image: "node:20-bookworm",
		},
	}

	require.NoError(t, drv.PrepareJob(context.Background(), jobCtx))

	step := &driver.StepSpec{
		ID:               "step-install",
		Name:             "Install Dependencies",
		WorkingDirectory: filepath.Join(workspaceDir, "frontend"),
		Environment: map[string]string{
			"GITHUB_ENV":       envFile,
			"GITHUB_OUTPUT":    outputFile,
			"GITHUB_PATH":      pathFile,
			"GITHUB_WORKSPACE": workspaceDir,
			"NODE_ENV":         "test",
		},
		Script: "npm test",
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	res, err := drv.ExecuteStep(context.Background(), step, &stdout, &stderr)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 0, res.ExitCode)
	assert.False(t, res.OOMKilled)
	assert.Equal(t, "build completed successfully\n", stdout.String())

	// Verify ExecConfig received by Docker
	execConfig := mockCli.createdExecConfig
	require.Len(t, execConfig.Cmd, 3)
	assert.Equal(t, "sh", execConfig.Cmd[0])
	assert.Equal(t, "-e", execConfig.Cmd[1])
	assert.True(t, strings.HasPrefix(execConfig.Cmd[2], "/github/workflow/step_step-install_") && strings.HasSuffix(execConfig.Cmd[2], ".sh"))
	assert.Equal(t, "/github/workspace/frontend", execConfig.WorkingDir)

	envMap := make(map[string]string)
	for _, env := range execConfig.Env {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	assert.Equal(t, "/github/workflow/_runner_file_commands/set_env_uuid-1234", envMap["GITHUB_ENV"])
	assert.Equal(t, "/github/workflow/_runner_file_commands/set_output_uuid-1234", envMap["GITHUB_OUTPUT"])
	assert.Equal(t, "/github/workflow/_runner_file_commands/add_path_uuid-1234", envMap["GITHUB_PATH"])
	assert.Equal(t, "/github/workspace", envMap["GITHUB_WORKSPACE"])
	assert.Equal(t, "test", envMap["NODE_ENV"])

	// Verify temporary host script was cleaned up
	hostScript := filepath.Join(runnerTempDir, "step_step-install.sh")
	_, statErr := os.Stat(hostScript)
	assert.True(t, os.IsNotExist(statErr), "step script should be cleaned up from host temp dir")
}

func TestDockerDriver_ExecuteStep_OOMKilledDetection(t *testing.T) {
	mockCli := newMockDockerClient()
	mockCli.execInspectResp = container.ExecInspect{
		ExitCode: 137,
	}
	mockCli.containerInspectResp = types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{
			State: &types.ContainerState{
				OOMKilled: true,
				ExitCode:  137,
			},
		},
	}

	drv := NewDriver(mockCli, true)

	tempDir := t.TempDir()
	jobCtx := &driver.JobContext{
		JobID:        "job-oom-test",
		WorkspaceDir: filepath.Join(tempDir, "workspace"),
		TempDir:      filepath.Join(tempDir, "temp"),
		Container: &protocol.JobContainer{
			Image: "alpine:latest",
		},
	}
	require.NoError(t, drv.PrepareJob(context.Background(), jobCtx))

	step := &driver.StepSpec{
		ID:     "step-leak",
		Script: "tail /dev/zero",
	}

	var stdout, stderr bytes.Buffer
	res, err := drv.ExecuteStep(context.Background(), step, &stdout, &stderr)
	require.Error(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 137, res.ExitCode)
	assert.True(t, res.OOMKilled)
}

func TestDockerDriver_ExecuteStep_ExitCode137_NotOOM(t *testing.T) {
	mockCli := newMockDockerClient()
	mockCli.execInspectResp = container.ExecInspect{
		ExitCode: 137,
	}
	mockCli.containerInspectResp = types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{
			State: &types.ContainerState{
				OOMKilled: false,
				ExitCode:  137,
			},
		},
	}

	drv := NewDriver(mockCli, true)

	tempDir := t.TempDir()
	jobCtx := &driver.JobContext{
		JobID:        "job-sigkill-test",
		WorkspaceDir: filepath.Join(tempDir, "workspace"),
		TempDir:      filepath.Join(tempDir, "temp"),
		Container: &protocol.JobContainer{
			Image: "alpine:latest",
		},
	}
	require.NoError(t, drv.PrepareJob(context.Background(), jobCtx))

	step := &driver.StepSpec{
		ID:     "step-sigkill",
		Script: "kill -9 $$",
	}

	var stdout, stderr bytes.Buffer
	res, err := drv.ExecuteStep(context.Background(), step, &stdout, &stderr)
	require.Error(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 137, res.ExitCode)
	assert.False(t, res.OOMKilled)
}

func TestDockerDriver_CleanupJob(t *testing.T) {
	mockCli := newMockDockerClient()
	drv := NewDriver(mockCli, true)

	tempDir := t.TempDir()
	jobCtx := &driver.JobContext{
		JobID:        "job-cleanup-test",
		WorkspaceDir: filepath.Join(tempDir, "workspace"),
		TempDir:      filepath.Join(tempDir, "temp"),
		Container: &protocol.JobContainer{
			Image: "alpine:latest",
		},
	}
	require.NoError(t, drv.PrepareJob(context.Background(), jobCtx))
	assert.Equal(t, mockCli.createResponseID, drv.ContainerID())

	err := drv.CleanupJob(context.Background())
	require.NoError(t, err)

	assert.Equal(t, mockCli.createResponseID, mockCli.removedContainerID)
	assert.True(t, mockCli.removedOptions.Force)
	assert.True(t, mockCli.removedOptions.RemoveVolumes)
	assert.Empty(t, drv.ContainerID())

	// Calling CleanupJob again should be a clean no-op
	err = drv.CleanupJob(context.Background())
	require.NoError(t, err)
}

func TestLiveDockerIntegration(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("skipping live Docker integration test: /var/run/docker.sock not accessible")
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("skipping live Docker test: docker client init: %v", err)
	}
	defer func() {
		_ = cli.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = cli.Ping(ctx)
	if err != nil {
		t.Skipf("skipping live Docker test: docker ping: %v", err)
	}

	tempDir := t.TempDir()
	workspaceDir := filepath.Join(tempDir, "workspace")
	runnerTempDir := filepath.Join(tempDir, "temp")

	drv := NewDriver(cli, true)
	jobCtx := &driver.JobContext{
		JobID:        "job-live-docker",
		WorkspaceDir: workspaceDir,
		TempDir:      runnerTempDir,
		Container: &protocol.JobContainer{
			Image: "alpine:latest",
		},
		Limits: &config.LimitsConfig{
			CPU:    1.0,
			Memory: 256 * 1024 * 1024,
			Pids:   64,
		},
	}

	prepareCtx, prepCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer prepCancel()

	err = drv.PrepareJob(prepareCtx, jobCtx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = drv.CleanupJob(context.Background())
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	step := &driver.StepSpec{
		ID:     "live-step-1",
		Script: "echo 'hello from container' > /github/workspace/output.txt && cat /github/workspace/output.txt",
	}

	stepCtx, stepCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stepCancel()

	res, err := drv.ExecuteStep(stepCtx, step, &stdout, &stderr)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 0, res.ExitCode)
	assert.Contains(t, stdout.String(), "hello from container")

	// Verify file was written to host workspace
	hostFile := filepath.Join(workspaceDir, "output.txt")
	content, err := os.ReadFile(hostFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "hello from container")
}

func TestDockerDriver_PrepareJob_StartFailure_CleansUpContainer(t *testing.T) {
	mockCli := newMockDockerClient()
	mockCli.startError = errors.New("cannot start container: port already in use")

	drv := NewDriver(mockCli, true)

	tempDir := t.TempDir()
	jobCtx := &driver.JobContext{
		JobID:        "job-start-fail",
		WorkspaceDir: filepath.Join(tempDir, "workspace"),
		TempDir:      filepath.Join(tempDir, "temp"),
		Container: &protocol.JobContainer{
			Image: "alpine:latest",
		},
	}

	err := drv.PrepareJob(context.Background(), jobCtx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start container: cannot start container")

	// ContainerRemove must have been called during start failure cleanup
	assert.Equal(t, mockCli.createResponseID, mockCli.removedContainerID)
	assert.True(t, mockCli.removedOptions.Force)
	assert.True(t, mockCli.removedOptions.RemoveVolumes)

	// Subsequent CleanupJob also runs without issue
	cleanupErr := drv.CleanupJob(context.Background())
	require.NoError(t, cleanupErr)
}

func TestDockerDriver_ExecuteStep_ContextCancellation_BreaksDeadlock(t *testing.T) {
	mockCli := newMockDockerClient()

	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()

	conn := &mockConn{
		onClose: func() {
			_ = pw.Close()
		},
	}

	mockCli.customHijacked = &types.HijackedResponse{
		Reader: bufio.NewReader(pr),
		Conn:   conn,
	}

	drv := NewDriver(mockCli, true)

	tempDir := t.TempDir()
	jobCtx := &driver.JobContext{
		JobID:        "job-cancel-test",
		WorkspaceDir: filepath.Join(tempDir, "workspace"),
		TempDir:      filepath.Join(tempDir, "temp"),
		Container: &protocol.JobContainer{
			Image: "alpine:latest",
		},
	}
	require.NoError(t, drv.PrepareJob(context.Background(), jobCtx))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	step := &driver.StepSpec{
		ID:     "step-hang",
		Script: "sleep 100",
	}

	var stdout, stderr bytes.Buffer
	done := make(chan struct{})
	var res *driver.StepResult
	var stepErr error

	go func() {
		res, stepErr = drv.ExecuteStep(ctx, step, &stdout, &stderr)
		close(done)
	}()

	select {
	case <-done:
		// Succeeded in breaking out
	case <-time.After(3 * time.Second):
		t.Fatal("ExecuteStep deadlocked on cancelled context")
	}

	require.Error(t, stepErr)
	assert.True(t, conn.closeCalled.Load(), "expected attachResp.Conn.Close() to be called on context cancel")
	if res != nil {
		assert.NotEqual(t, 0, res.ExitCode)
	}
}

func TestDriver_CloneIsolatesConcurrentJobsAndClearsOnStartError(t *testing.T) {
	cli := newMockDockerClient()
	cli.existingImages["alpine:latest"] = types.ImageInspect{}
	base := NewDriver(cli, false)

	c1, ok := any(base).(driver.Cloneable)
	require.True(t, ok, "docker.Driver must implement driver.Cloneable")
	d1 := c1.Clone().(*Driver)
	d2 := c1.Clone().(*Driver)

	cli.failNextStart = true
	err := d1.PrepareJob(context.Background(), &driver.JobContext{
		JobID:     "job-fail",
		Container: &protocol.JobContainer{Image: "alpine:latest"},
	})
	require.Error(t, err)
	assert.Empty(t, d1.ContainerID(), "containerID must be cleared when ContainerStart fails")

	cli.failNextStart = false
	require.NoError(t, d1.PrepareJob(context.Background(), &driver.JobContext{
		JobID:     "job-1",
		Container: &protocol.JobContainer{Image: "alpine:latest"},
	}))
	require.NoError(t, d2.PrepareJob(context.Background(), &driver.JobContext{
		JobID:     "job-2",
		Container: &protocol.JobContainer{Image: "alpine:latest"},
	}))
	assert.NotEqual(t, d1.ContainerID(), d2.ContainerID())
}

func TestTranslatePaths_EnforcesSeparatorBoundary(t *testing.T) {
	assert.Equal(t, "/work/job2/file", translateWorkspacePath("/work/job2/file", "/work/job"))
	assert.Equal(t, "/github/workspace/sub/file", translateWorkspacePath("/work/job/sub/file", "/work/job"))
	assert.Equal(t, "/github/workflow", translateTempPath("/work/_temp", "/work/_temp"))
	assert.Equal(t, "/github/workflow/out", translateTempPath("/work/_temp2/out", "/work/_temp")) // does not treat /work/_temp2 as inside /work/_temp
}

func TestDriver_ImagePullStreamError(t *testing.T) {
	cli := newMockDockerClient()
	// Simulate image missing locally so it pulls
	cli.pullStream = strings.NewReader("{\"status\":\"Pulling fs layer\"}\n{\"error\":\"pull access denied, repository does not exist\",\"errorDetail\":{\"message\":\"pull access denied, repository does not exist\"}}\n")
	drv := NewDriver(cli, true)

	err := drv.PrepareJob(context.Background(), &driver.JobContext{
		JobID:     "job-pull-err",
		Container: &protocol.JobContainer{Image: "private/image:latest"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pull access denied")
}

func TestDriver_ExecuteStep_UniqueScriptsAndPathSanitization(t *testing.T) {
	cli := newMockDockerClient()
	drv := NewDriver(cli, true)
	tempDir := t.TempDir()

	jobCtx := &driver.JobContext{
		JobID:        "job-unique",
		WorkspaceDir: filepath.Join(tempDir, "workspace"),
		TempDir:      filepath.Join(tempDir, "temp"),
		Container:    &protocol.JobContainer{Image: "alpine:latest"},
	}
	require.NoError(t, drv.PrepareJob(context.Background(), jobCtx))

	// Step with malicious path traversal in ID
	step := &driver.StepSpec{
		ID:     "../../etc/passwd",
		Script: "echo safe",
	}
	_, err := drv.ExecuteStep(context.Background(), step, nil, nil)
	require.NoError(t, err)

	// Script command inside container should not contain path traversal
	cmd := cli.createdExecConfig.Cmd
	require.Len(t, cmd, 3)
	assert.Equal(t, "sh", cmd[0])
	assert.Equal(t, "-e", cmd[1])
	assert.True(t, strings.HasPrefix(cmd[2], "/github/workflow/step_"), "script should be inside /github/workflow and start with step_")
	assert.False(t, strings.Contains(cmd[2], ".."), "script path must not contain .. traversal")
}

func TestDriver_ExecuteStep_NilStep(t *testing.T) {
	cli := newMockDockerClient()
	drv := NewDriver(cli, false)

	res, err := drv.ExecuteStep(context.Background(), nil, nil, nil)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Equal(t, "nil step specification", err.Error())
}

func TestDriver_ExecuteStep_GithubStateMapped(t *testing.T) {
	cli := newMockDockerClient()
	drv := NewDriver(cli, true)
	tempDir := t.TempDir()
	runnerTemp := filepath.Join(tempDir, "temp")

	jobCtx := &driver.JobContext{
		JobID:        "job-state",
		WorkspaceDir: filepath.Join(tempDir, "workspace"),
		TempDir:      runnerTemp,
		Container:    &protocol.JobContainer{Image: "alpine:latest"},
	}
	require.NoError(t, drv.PrepareJob(context.Background(), jobCtx))

	step := &driver.StepSpec{
		ID:     "step-state",
		Script: "echo 'var=val' >> $GITHUB_STATE",
		Environment: map[string]string{
			"GITHUB_STATE": filepath.Join(runnerTemp, "_runner_file_commands", "state_file"),
		},
	}
	_, err := drv.ExecuteStep(context.Background(), step, nil, nil)
	require.NoError(t, err)

	found := false
	for _, env := range cli.createdExecConfig.Env {
		if strings.HasPrefix(env, "GITHUB_STATE=") {
			found = true
			assert.Equal(t, "GITHUB_STATE=/github/workflow/_runner_file_commands/state_file", env)
		}
	}
	assert.True(t, found, "GITHUB_STATE must be translated to /github/workflow path")
}

func TestDriver_ExecuteStep_CancelKillsExecProcess(t *testing.T) {
	cli := newMockDockerClient()
	cli.execInspectResp = container.ExecInspect{
		Running: true,
		Pid:     4242,
	}

	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	conn := &mockConn{
		onClose: func() {
			_ = pw.Close()
		},
	}
	cli.customHijacked = &types.HijackedResponse{
		Reader: bufio.NewReader(pr),
		Conn:   conn,
	}

	drv := NewDriver(cli, true)
	tempDir := t.TempDir()
	jobCtx := &driver.JobContext{
		JobID:        "job-cancel-kill",
		WorkspaceDir: filepath.Join(tempDir, "workspace"),
		TempDir:      filepath.Join(tempDir, "temp"),
		Container:    &protocol.JobContainer{Image: "alpine:latest"},
	}
	require.NoError(t, drv.PrepareJob(context.Background(), jobCtx))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	step := &driver.StepSpec{
		ID:     "step-hang",
		Script: "sleep 100",
	}

	var stdout, stderr bytes.Buffer
	_, err := drv.ExecuteStep(ctx, step, &stdout, &stderr)
	require.Error(t, err)

	// Verify kill command was sent to container
	cli.mu.Lock()
	defer cli.mu.Unlock()
	killFound := false
	for _, cmd := range cli.createdExecCommands {
		if len(cmd) == 3 && cmd[0] == "kill" && cmd[1] == "-KILL" && cmd[2] == "4242" {
			killFound = true
			break
		}
	}
	assert.True(t, killFound, "expected kill -KILL 4242 exec command to be created on cancellation")
}

func TestDriver_PrepareJob_ContainerEnvMerged(t *testing.T) {
	cli := newMockDockerClient()
	drv := NewDriver(cli, true)
	tempDir := t.TempDir()

	jobCtx := &driver.JobContext{
		JobID:        "job-env-merge",
		WorkspaceDir: filepath.Join(tempDir, "workspace"),
		TempDir:      filepath.Join(tempDir, "temp"),
		Container: &protocol.JobContainer{
			Image: "alpine:latest",
			Environment: map[string]string{
				"FOO": "BAR",
				"BAZ": "QUX",
			},
		},
	}
	require.NoError(t, drv.PrepareJob(context.Background(), jobCtx))
	require.NotNil(t, cli.createdConfig)
	assert.Contains(t, cli.createdConfig.Env, "FOO=BAR")
	assert.Contains(t, cli.createdConfig.Env, "BAZ=QUX")
}
