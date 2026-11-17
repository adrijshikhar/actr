package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/adrijshikhar/actr/internal/driver/docker"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type mockDriver struct{}

func (m *mockDriver) Name() string                                                 { return "mock" }
func (m *mockDriver) PrepareJob(ctx context.Context, job *driver.JobContext) error { return nil }
func (m *mockDriver) ExecuteStep(ctx context.Context, step *driver.StepSpec, stdout, stderr io.Writer) (*driver.StepResult, error) {
	return &driver.StepResult{ExitCode: 0}, nil
}
func (m *mockDriver) CleanupJob(ctx context.Context) error { return nil }
func (m *mockDriver) Cleanup(ctx context.Context) error    { return nil }

type mockBrokerClient struct{}

func (m *mockBrokerClient) CreateSession(ctx context.Context, agentName string, agentID int64, poolID int64) error {
	return nil
}
func (m *mockBrokerClient) GetMessage(ctx context.Context, runnerVersion, os, arch string) (*protocol.BrokerMessage, error) {
	return nil, nil
}
func (m *mockBrokerClient) AcknowledgeMessage(ctx context.Context, runnerRequestID string) error {
	return nil
}
func (m *mockBrokerClient) DeleteSession(ctx context.Context) error {
	return nil
}

type mockRunServiceClient struct{}

func (m *mockRunServiceClient) AcquireJob(ctx context.Context, runServiceURL, runnerRequestID, billingOwnerID, token string) (*protocol.JobSpecification, error) {
	return &protocol.JobSpecification{JobID: "mock-job"}, nil
}
func (m *mockRunServiceClient) CompleteJob(ctx context.Context, runServiceURL, token string, req *protocol.CompleteJobRequest) error {
	return nil
}

type mockCoordinator struct{}

func (m *mockCoordinator) ExecuteJob(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error) {
	return &protocol.CompleteJobRequest{JobID: job.JobID, Conclusion: "success"}, nil
}

func TestDI_Config(t *testing.T) {
	ctx := context.Background()
	if got := ConfigFromContext(ctx); got != nil {
		t.Fatalf("expected nil config from empty context, got %+v", got)
	}

	cfg := &config.RunnerConfig{
		AgentID:   42,
		AgentName: "test-agent",
	}
	ctxWithCfg := WithConfig(ctx, cfg)
	got := ConfigFromContext(ctxWithCfg)
	if got == nil {
		t.Fatal("expected config from context, got nil")
	}
	if got.AgentID != 42 || got.AgentName != "test-agent" {
		t.Fatalf("expected agentID 42, got %+v", got)
	}
}

func TestDI_Driver(t *testing.T) {
	ctx := context.Background()
	if got := DriverFromContext(ctx); got != nil {
		t.Fatalf("expected nil driver from empty context, got %+v", got)
	}

	d := &mockDriver{}
	ctxWithDriver := WithDriver(ctx, d)
	got := DriverFromContext(ctxWithDriver)
	if got == nil {
		t.Fatal("expected driver from context, got nil")
	}
	if got.Name() != "mock" {
		t.Fatalf("expected driver name 'mock', got %s", got.Name())
	}
}

func TestDI_BrokerClient(t *testing.T) {
	ctx := context.Background()
	if got := BrokerClientFromContext(ctx); got != nil {
		t.Fatalf("expected nil broker client from empty context, got %+v", got)
	}

	b := &mockBrokerClient{}
	ctxWithBroker := WithBrokerClient(ctx, b)
	got := BrokerClientFromContext(ctxWithBroker)
	if got == nil {
		t.Fatal("expected broker client from context, got nil")
	}
}

func TestDI_RunServiceClient(t *testing.T) {
	ctx := context.Background()
	if got := RunServiceClientFromContext(ctx); got != nil {
		t.Fatalf("expected nil run service client from empty context, got %+v", got)
	}

	r := &mockRunServiceClient{}
	ctxWithRunClient := WithRunServiceClient(ctx, r)
	got := RunServiceClientFromContext(ctxWithRunClient)
	if got == nil {
		t.Fatal("expected run service client from context, got nil")
	}
}

func TestDI_Coordinator(t *testing.T) {
	ctx := context.Background()
	if got := CoordinatorFromContext(ctx); got != nil {
		t.Fatalf("expected nil coordinator from empty context, got %+v", got)
	}

	c := &mockCoordinator{}
	ctxWithCoord := WithCoordinator(ctx, c)
	got := CoordinatorFromContext(ctxWithCoord)
	if got == nil {
		t.Fatal("expected coordinator from context, got nil")
	}
}

func TestDI_HTTPClient(t *testing.T) {
	ctx := context.Background()
	if got := HTTPClientFromContext(ctx); got != nil {
		t.Fatalf("expected nil http client from empty context, got %+v", got)
	}

	client := &http.Client{}
	ctxWithClient := WithHTTPClient(ctx, client)
	got := HTTPClientFromContext(ctxWithClient)
	if got != client {
		t.Fatalf("expected client %p, got %p", client, got)
	}
}

func TestDI_NilContext(t *testing.T) {
	var nilCtx context.Context
	if got := ConfigFromContext(nilCtx); got != nil {
		t.Fatalf("expected nil from nil context, got %+v", got)
	}
	if got := DriverFromContext(nilCtx); got != nil {
		t.Fatalf("expected nil from nil context, got %+v", got)
	}
	if got := BrokerClientFromContext(nilCtx); got != nil {
		t.Fatalf("expected nil from nil context, got %+v", got)
	}
	if got := RunServiceClientFromContext(nilCtx); got != nil {
		t.Fatalf("expected nil from nil context, got %+v", got)
	}
	if got := CoordinatorFromContext(nilCtx); got != nil {
		t.Fatalf("expected nil from nil context, got %+v", got)
	}
	if got := HTTPClientFromContext(nilCtx); got != nil {
		t.Fatalf("expected nil from nil context, got %+v", got)
	}
	if got := DockerDriverFromContext(nilCtx); got != nil {
		t.Fatalf("expected nil from nil context, got %+v", got)
	}
	if got := DockerClientFromContext(nilCtx); got != nil {
		t.Fatalf("expected nil from nil context, got %+v", got)
	}
	if got := LimitsFromContext(nilCtx); got != nil {
		t.Fatalf("expected nil from nil context, got %+v", got)
	}
}

type stubDockerClient struct{}

var _ docker.DockerClient = (*stubDockerClient)(nil)

func (s *stubDockerClient) ImageInspectWithRaw(ctx context.Context, image string) (types.ImageInspect, []byte, error) {
	return types.ImageInspect{}, nil, nil
}
func (s *stubDockerClient) ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (s *stubDockerClient) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	return container.CreateResponse{ID: "stub-container"}, nil
}
func (s *stubDockerClient) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	return nil
}
func (s *stubDockerClient) ContainerExecCreate(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error) {
	return types.IDResponse{ID: "stub-exec"}, nil
}
func (s *stubDockerClient) ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
	return types.HijackedResponse{}, nil
}
func (s *stubDockerClient) ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error) {
	return container.ExecInspect{ExitCode: 0}, nil
}
func (s *stubDockerClient) ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	return types.ContainerJSON{}, nil
}
func (s *stubDockerClient) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	return nil
}

func TestDI_DockerDriver(t *testing.T) {
	ctx := context.Background()
	if got := DockerDriverFromContext(ctx); got != nil {
		t.Fatalf("expected nil docker driver from empty context, got %+v", got)
	}

	d := &mockDriver{}
	ctxWithDriver := WithDockerDriver(ctx, d)
	got := DockerDriverFromContext(ctxWithDriver)
	if got == nil {
		t.Fatal("expected docker driver from context, got nil")
	}
	if got.Name() != "mock" {
		t.Fatalf("expected driver name 'mock', got %s", got.Name())
	}
}

func TestDI_DockerClient(t *testing.T) {
	ctx := context.Background()
	if got := DockerClientFromContext(ctx); got != nil {
		t.Fatalf("expected nil docker client from empty context, got %+v", got)
	}

	cli := &stubDockerClient{}
	ctxWithCli := WithDockerClient(ctx, cli)
	got := DockerClientFromContext(ctxWithCli)
	if got != cli {
		t.Fatalf("expected client %p, got %p", cli, got)
	}
}

func TestDI_Limits(t *testing.T) {
	ctx := context.Background()
	if got := LimitsFromContext(ctx); got != nil {
		t.Fatalf("expected nil limits from empty context, got %+v", got)
	}

	limits := &config.LimitsConfig{
		CPU:    2.5,
		Memory: 4 * 1024 * 1024 * 1024,
		Pids:   512,
	}
	ctxWithLimits := WithLimits(ctx, limits)
	got := LimitsFromContext(ctxWithLimits)
	if got == nil {
		t.Fatal("expected limits from context, got nil")
	}
	if got.CPU != 2.5 || got.Memory != 4*1024*1024*1024 || got.Pids != 512 {
		t.Fatalf("unexpected limits: %+v", got)
	}
}

func TestDI_DetectDockerDriver_FromContextDriver(t *testing.T) {
	ctx := context.Background()
	mockDrv := &mockDriver{}
	ctx = WithDockerDriver(ctx, mockDrv)

	drv, err := DetectDockerDriver(ctx, "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if drv != mockDrv {
		t.Fatalf("expected injected driver %p, got %p", mockDrv, drv)
	}
}

func TestDI_DetectDockerDriver_FromContextClient(t *testing.T) {
	ctx := context.Background()
	cli := &stubDockerClient{}
	ctx = WithDockerClient(ctx, cli)

	drv, err := DetectDockerDriver(ctx, "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if drv == nil {
		t.Fatal("expected non-nil driver from context client")
	}
	if drv.Name() != "docker" {
		t.Fatalf("expected driver name 'docker', got %s", drv.Name())
	}
}

func TestDI_DetectDockerDriver_UnreachableSocket(t *testing.T) {
	ctx := context.Background()
	// An unreachable non-existent socket should return an error / nil driver
	drv, err := DetectDockerDriver(ctx, "/tmp/non-existent-actr-docker.sock", true)
	if drv != nil {
		t.Fatalf("expected nil driver for unreachable socket, got %+v", drv)
	}
	if err == nil {
		t.Fatal("expected error for unreachable socket, got nil")
	}
}
