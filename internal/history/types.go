package history

import (
	"github.com/adrijshikhar/actr/internal/monitor"
)

// StatusInterrupted represents a run or step that was interrupted mid-flight (e.g. SIGKILL, daemon crash).
const StatusInterrupted = monitor.StatusInterrupted

// Type aliases for historical run models defined in monitor to prevent circular dependencies.
type RunSummary = monitor.RunSummary
type RunMetricsSummary = monitor.RunMetricsSummary
type StepMetadata = monitor.StepMetadata
type RunMetadata = monitor.RunMetadata
type RunRecord = monitor.RunMetadata
type StepRecord = monitor.StepMetadata
type IndexCatalog = monitor.IndexCatalog
type LogStore = monitor.LogStore
type Pruner = monitor.Pruner
type Store = monitor.HistoryStore
