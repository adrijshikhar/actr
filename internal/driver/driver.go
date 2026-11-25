package driver

import (
	"context"
	"io"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/protocol"
)

type StepSpec struct {
	ID               string
	Name             string
	WorkingDirectory string
	Environment      map[string]string
	Script           string
	Shell            string
	OnProcessStart   func(pid int, pgid int)
}

type StepResult struct {
	ExitCode  int
	OOMKilled bool
	Error     error
}

type JobContext struct {
	JobID          string
	WorkflowName   string
	JobDisplayName string
	WorkspaceDir   string
	TempDir        string
	Container      *protocol.JobContainer
	Environment    map[string]string
	Limits         *config.LimitsConfig
}

type Driver interface {
	Name() string
	PrepareJob(ctx context.Context, job *JobContext) error
	ExecuteStep(ctx context.Context, step *StepSpec, stdout, stderr io.Writer) (*StepResult, error)
	CleanupJob(ctx context.Context) error
}

// Cloneable is an optional interface implemented by stateful drivers so each
// concurrent job receives an isolated instance.
type Cloneable interface {
	Clone() Driver
}
