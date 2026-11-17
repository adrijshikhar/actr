package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/adrijshikhar/actr/internal/driver/docker"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/docker/docker/client"
)

type (
	configCtxKey           struct{}
	driverCtxKey           struct{}
	dockerDriverCtxKey     struct{}
	dockerClientCtxKey     struct{}
	limitsCtxKey           struct{}
	brokerClientCtxKey     struct{}
	runServiceClientCtxKey struct{}
	coordinatorCtxKey      struct{}
	httpClientCtxKey       struct{}
)

// BrokerClient defines the interface required by the runner daemon for broker communication.
type BrokerClient interface {
	CreateSession(ctx context.Context, agentName string, agentID int64, poolID int64) error
	GetMessage(ctx context.Context, runnerVersion, os, arch string) (*protocol.BrokerMessage, error)
	AcknowledgeMessage(ctx context.Context, runnerRequestID string) error
	DeleteSession(ctx context.Context) error
}

// RunServiceClient defines the interface required by the runner daemon for run service communication.
type RunServiceClient interface {
	AcquireJob(ctx context.Context, runServiceURL, runnerRequestID, billingOwnerID, token string) (*protocol.JobSpecification, error)
	CompleteJob(ctx context.Context, runServiceURL, token string, req *protocol.CompleteJobRequest) error
}

// JobCoordinator defines the interface required by the runner daemon for executing jobs.
type JobCoordinator interface {
	ExecuteJob(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error)
}

func WithConfig(ctx context.Context, cfg *config.RunnerConfig) context.Context {
	return context.WithValue(ctx, configCtxKey{}, cfg)
}

func ConfigFromContext(ctx context.Context) *config.RunnerConfig {
	if ctx == nil {
		return nil
	}
	if cfg, ok := ctx.Value(configCtxKey{}).(*config.RunnerConfig); ok {
		return cfg
	}
	return nil
}

func WithDriver(ctx context.Context, d driver.Driver) context.Context {
	return context.WithValue(ctx, driverCtxKey{}, d)
}

func DriverFromContext(ctx context.Context) driver.Driver {
	if ctx == nil {
		return nil
	}
	if d, ok := ctx.Value(driverCtxKey{}).(driver.Driver); ok {
		return d
	}
	return nil
}

func WithBrokerClient(ctx context.Context, b BrokerClient) context.Context {
	return context.WithValue(ctx, brokerClientCtxKey{}, b)
}

func BrokerClientFromContext(ctx context.Context) BrokerClient {
	if ctx == nil {
		return nil
	}
	if b, ok := ctx.Value(brokerClientCtxKey{}).(BrokerClient); ok {
		return b
	}
	return nil
}

func WithRunServiceClient(ctx context.Context, r RunServiceClient) context.Context {
	return context.WithValue(ctx, runServiceClientCtxKey{}, r)
}

func RunServiceClientFromContext(ctx context.Context) RunServiceClient {
	if ctx == nil {
		return nil
	}
	if r, ok := ctx.Value(runServiceClientCtxKey{}).(RunServiceClient); ok {
		return r
	}
	return nil
}

func WithCoordinator(ctx context.Context, c JobCoordinator) context.Context {
	return context.WithValue(ctx, coordinatorCtxKey{}, c)
}

func CoordinatorFromContext(ctx context.Context) JobCoordinator {
	if ctx == nil {
		return nil
	}
	if c, ok := ctx.Value(coordinatorCtxKey{}).(JobCoordinator); ok {
		return c
	}
	return nil
}

func WithHTTPClient(ctx context.Context, client *http.Client) context.Context {
	return context.WithValue(ctx, httpClientCtxKey{}, client)
}

func HTTPClientFromContext(ctx context.Context) *http.Client {
	if ctx == nil {
		return nil
	}
	if c, ok := ctx.Value(httpClientCtxKey{}).(*http.Client); ok {
		return c
	}
	return nil
}

func WithDockerDriver(ctx context.Context, d driver.Driver) context.Context {
	return context.WithValue(ctx, dockerDriverCtxKey{}, d)
}

func DockerDriverFromContext(ctx context.Context) driver.Driver {
	if ctx == nil {
		return nil
	}
	if d, ok := ctx.Value(dockerDriverCtxKey{}).(driver.Driver); ok {
		return d
	}
	return nil
}

func WithDockerClient(ctx context.Context, cli docker.DockerClient) context.Context {
	return context.WithValue(ctx, dockerClientCtxKey{}, cli)
}

func DockerClientFromContext(ctx context.Context) docker.DockerClient {
	if ctx == nil {
		return nil
	}
	if cli, ok := ctx.Value(dockerClientCtxKey{}).(docker.DockerClient); ok {
		return cli
	}
	return nil
}

func WithLimits(ctx context.Context, limits *config.LimitsConfig) context.Context {
	return context.WithValue(ctx, limitsCtxKey{}, limits)
}

func LimitsFromContext(ctx context.Context) *config.LimitsConfig {
	if ctx == nil {
		return nil
	}
	if l, ok := ctx.Value(limitsCtxKey{}).(*config.LimitsConfig); ok {
		return l
	}
	return nil
}

// DetectDockerDriver returns a Docker driver from context or by connecting to the Docker daemon.
// If Docker is unreachable or socket is not present, it returns nil and an error describing the issue.
func DetectDockerDriver(ctx context.Context, socketOverride string, autoPull bool) (driver.Driver, error) {
	if ctx != nil {
		if d := DockerDriverFromContext(ctx); d != nil {
			return d, nil
		}
		if cli := DockerClientFromContext(ctx); cli != nil {
			return docker.NewDriver(cli, autoPull), nil
		}
	}

	if ctx == nil {
		ctx = context.Background()
	}

	var opts []client.Opt
	if socketOverride != "" {
		sock := socketOverride
		if !strings.HasPrefix(sock, "unix://") && !strings.HasPrefix(sock, "tcp://") {
			sock = "unix://" + sock
		}
		opts = append(opts, client.WithHost(sock))
	} else {
		// If socketOverride is not set and DOCKER_HOST is not set, check for standard socket existence
		if os.Getenv("DOCKER_HOST") == "" {
			if _, err := os.Stat("/var/run/docker.sock"); err != nil {
				return nil, fmt.Errorf("docker daemon socket /var/run/docker.sock not found: %w", err)
			}
		}
		opts = append(opts, client.FromEnv)
	}
	opts = append(opts, client.WithAPIVersionNegotiation())

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	if _, err := cli.Ping(pingCtx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("docker ping failed: %w", err)
	}

	return docker.NewDriver(cli, autoPull), nil
}
