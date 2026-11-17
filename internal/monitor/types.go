package monitor

import (
	"io"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
)

type StepStatus string

const (
	StatusQueued      StepStatus = "queued"
	StatusRunning     StepStatus = "running"
	StatusSuccess     StepStatus = "success"
	StatusFailed      StepStatus = "failed"
	StatusSkipped     StepStatus = "skipped"
	StatusInterrupted StepStatus = "interrupted"
)

type EventType string

const (
	EventStepStart  EventType = "step_start"
	EventStepLog    EventType = "step_log"
	EventStepFinish EventType = "step_finish"
	EventTelemetry  EventType = "telemetry"
	EventJobStart   EventType = "job_start"
	EventJobFinish  EventType = "job_finish"
)

type StepInfo struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Number    int        `json:"number"`
	Status    StepStatus `json:"status"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   time.Time  `json:"ended_at"`
}

type JobInfo struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Workflow  string      `json:"workflow"`
	Repo      string      `json:"repo"`
	Owner     string      `json:"owner"`
	Status    StepStatus  `json:"status"`
	Steps     []*StepInfo `json:"steps"`
	StartedAt time.Time   `json:"started_at"`
	EndedAt   time.Time   `json:"ended_at"`
}

// RunInfo is an alias for JobInfo for backwards compatibility with runner terminology.
type RunInfo = JobInfo

type RunnerMetrics struct {
	Timestamp      time.Time `json:"timestamp"`
	PID            int32     `json:"pid"`
	PGID           int32     `json:"pgid"`
	ProcessCount   int       `json:"process_count"`
	StepCPU        float64   `json:"step_cpu"`
	StepRSSBytes   uint64    `json:"step_rss_bytes"`
	StepElapsedSec int64     `json:"step_elapsed_sec"`
	ActiveStepName string    `json:"active_step_name,omitempty"`

	// Storage
	DiskUsedBytes    uint64  `json:"disk_used_bytes"`
	DiskFreeBytes    uint64  `json:"disk_free_bytes"`
	DiskTotalBytes   uint64  `json:"disk_total_bytes"`
	DiskUsedRatio    float64 `json:"disk_used_ratio"`
	DiskWriteRate    uint64  `json:"disk_write_rate"`
	DiskInodesFree   uint64  `json:"disk_inodes_free"`
	DiskWrittenBytes uint64  `json:"disk_written_bytes,omitempty"`

	// Network I/O
	NetRxRate  uint64 `json:"net_rx_rate"`
	NetTxRate  uint64 `json:"net_tx_rate"`
	NetRxBytes uint64 `json:"net_rx_bytes,omitempty"`
	NetTxBytes uint64 `json:"net_tx_bytes,omitempty"`

	// Host Node Vitals
	HostCPU      float64 `json:"host_cpu"`
	HostMemUsed  uint64  `json:"host_mem_used"`
	HostMemTotal uint64  `json:"host_mem_total"`
	HostLoad1m   float64 `json:"host_load_1m"`

	// Tracing
	TraceID string `json:"trace_id,omitempty"`
	SpanID  string `json:"span_id,omitempty"`
}

type TelemetrySample = RunnerMetrics

type Event struct {
	Type      EventType            `json:"type"`
	JobID     string               `json:"job_id,omitempty"`
	StepID    string               `json:"step_id,omitempty"`
	Job       *JobInfo             `json:"job,omitempty"`
	Step      *StepInfo            `json:"step,omitempty"`
	LogLine   string               `json:"log_line,omitempty"`
	Telemetry *TelemetrySample     `json:"telemetry,omitempty"`
	Limits    *config.LimitsConfig `json:"limits,omitempty"`
	Driver    string               `json:"driver,omitempty"`
}

type Snapshot struct {
	RunnerName string              `json:"runner_name"`
	ActiveJobs []*JobInfo          `json:"active_jobs"`
	RecentJobs []*JobInfo          `json:"recent_jobs"`
	Telemetry  *TelemetrySample    `json:"telemetry,omitempty"`
	StepLogs   map[string][]string `json:"step_logs,omitempty"`
}

// RunSummary represents a lightweight cached summary of a workflow run stored in index.json.
type RunSummary struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Workflow       string     `json:"workflow"`
	Repo           string     `json:"repo"`
	Owner          string     `json:"owner"`
	Status         StepStatus `json:"status"`
	StartedAt      time.Time  `json:"started_at"`
	EndedAt        time.Time  `json:"ended_at"`
	DurationSec    int64      `json:"duration_sec"`
	Driver         string     `json:"driver"`
	PeakCPUPercent float64    `json:"peak_cpu_percent"`
	PeakRSSBytes   uint64     `json:"peak_rss_bytes"`
	StepCount      int        `json:"step_count"`
}

// RunMetricsSummary captures aggregate peak resource vitals recorded across a run.
type RunMetricsSummary struct {
	PeakCPUPercent        float64 `json:"peak_cpu_percent"`
	PeakRSSBytes          uint64  `json:"peak_rss_bytes"`
	TotalNetRxBytes       uint64  `json:"total_net_rx_bytes"`
	TotalNetTxBytes       uint64  `json:"total_net_tx_bytes"`
	TotalDiskWrittenBytes uint64  `json:"total_disk_written_bytes"`
}

// StepMetadata represents detailed execution metadata for an individual step in meta.json.
type StepMetadata struct {
	ID           string     `json:"id"`
	Number       int        `json:"number"`
	Name         string     `json:"name"`
	Status       StepStatus `json:"status"`
	ExitCode     int        `json:"exit_code"`
	OOMKilled    bool       `json:"oom_killed"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      time.Time  `json:"ended_at"`
	DurationSec  int64      `json:"duration_sec"`
	PeakRSSBytes uint64     `json:"peak_rss_bytes"`
	LogBytes     int64      `json:"log_bytes"`
	LogLines     int        `json:"log_lines"`
}

// RunMetadata represents the comprehensive execution record of a workflow run in meta.json.
type RunMetadata struct {
	ID             string               `json:"id"`
	Name           string               `json:"name"`
	Workflow       string               `json:"workflow"`
	Repo           string               `json:"repo"`
	Owner          string               `json:"owner"`
	Status         StepStatus           `json:"status"`
	StartedAt      time.Time            `json:"started_at"`
	EndedAt        time.Time            `json:"ended_at"`
	DurationSec    int64                `json:"duration_sec"`
	Driver         string               `json:"driver"`
	Limits         *config.LimitsConfig `json:"limits,omitempty"`
	MetricsSummary RunMetricsSummary    `json:"metrics_summary"`
	Steps          []*StepMetadata      `json:"steps"`
}

// IndexCatalog represents the top-level index.json file format.
type IndexCatalog struct {
	Version   int           `json:"version"`
	UpdatedAt time.Time     `json:"updated_at"`
	Runs      []*RunSummary `json:"runs"`
}

// LogStore defines the interface for per-step log operations.
type LogStore interface {
	AppendStepLog(jobID, stepID, line string) error
	GetStepLogs(jobID, stepID string) ([]string, error)
	GetStepLogReader(jobID, stepID string) (io.ReadCloser, error)
}

// Pruner defines the interface for retention pruning and crash recovery.
type Pruner interface {
	Prune(maxRuns int, maxDiskBytes int64) (prunedCount int, err error)
	RecoverInterruptedRuns() (recoveredCount int, err error)
}

// HistoryStore defines the interface for local run history persistence.
type HistoryStore interface {
	LogStore
	Pruner
	StartRun(job *JobInfo, limits *config.LimitsConfig, driver string) error
	FinishRun(jobID string, status StepStatus, metrics *RunMetricsSummary) error
	ListRuns(limit int) ([]*RunSummary, error)
	GetRun(jobID string) (*RunMetadata, error)
}
