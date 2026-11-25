package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/monitor"
)

var validIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]+$`)

// ErrCorruptedIndex indicates that index.json exists but failed to unmarshal.
var ErrCorruptedIndex = errors.New("corrupted index catalog")

// sanitizeID verifies that id contains only alphanumeric characters, underscores,
// hyphens, and dots, is not empty, is not "index.json" (case-insensitive),
// does not contain "..", and is not ".".
func sanitizeID(id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", errors.New("empty ID")
	}
	normalized := strings.ToLower(id)
	if normalized == "index.json" {
		return "", fmt.Errorf("invalid ID %q: reserved name", id)
	}
	if strings.Contains(normalized, "..") {
		return "", fmt.Errorf("invalid ID %q: contains '..'", id)
	}
	if !validIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid ID %q: contains invalid characters", id)
	}
	if id == "." {
		return "", fmt.Errorf("invalid ID %q: cannot be '.'", id)
	}
	return id, nil
}

// DiskStore provides on-disk storage for run metadata, step logs, and the catalog index.
type DiskStore struct {
	baseDir   string
	mu        sync.RWMutex
	stepLocks sync.Map
	stepStats sync.Map
}

var _ Store = (*DiskStore)(nil)
var _ LogStore = (*DiskStore)(nil)
var _ Pruner = (*DiskStore)(nil)
var _ io.Closer = (*DiskStore)(nil)

// Close implements io.Closer for DiskStore. DiskStore has no persistent open descriptors to release.
func (s *DiskStore) Close() error {
	return nil
}

// runDir sanitizes jobID and ensures the resolved run path resides within baseDir.
func (s *DiskStore) runDir(jobID string) (string, error) {
	cleanID, err := sanitizeID(jobID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.baseDir, cleanID)
	rel, err := filepath.Rel(s.baseDir, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %s escapes base directory %s", path, s.baseDir)
	}
	return path, nil
}

// stepLogPath sanitizes jobID and stepID and ensures the step log path resides within baseDir.
func (s *DiskStore) stepLogPath(jobID, stepID string) (string, error) {
	runDir, err := s.runDir(jobID)
	if err != nil {
		return "", err
	}
	cleanStepID, err := sanitizeID(stepID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(runDir, "steps", cleanStepID+".log")
	rel, err := filepath.Rel(s.baseDir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %s escapes base directory %s", path, s.baseDir)
	}
	return path, nil
}

// backupCorruptIndex backs up a corrupted index.json to index.json.corrupt.<timestamp>.
func (s *DiskStore) backupCorruptIndex(indexPath string) error {
	if _, err := os.Stat(indexPath); err != nil {
		return err
	}
	timestamp := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	backupPath := fmt.Sprintf("%s.corrupt.%s", indexPath, timestamp)
	if err := os.Rename(indexPath, backupPath); err != nil {
		if data, rErr := os.ReadFile(indexPath); rErr == nil {
			_ = os.WriteFile(backupPath, data, 0600)
			_ = os.Remove(indexPath)
		}
		return err
	}
	return nil
}

// NewDiskStore initializes a DiskStore rooted at baseDir, creating the directory
// and an initial index.json if they do not exist, and recovering any interrupted runs.
func NewDiskStore(baseDir string) (*DiskStore, error) {
	if baseDir == "" {
		return nil, errors.New("baseDir cannot be empty")
	}

	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create base directory %s: %w", baseDir, err)
	}
	_ = os.Chmod(baseDir, 0700)

	indexPath := filepath.Join(baseDir, "index.json")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		catalog := &IndexCatalog{
			Version:   1,
			UpdatedAt: time.Now().UTC(),
			Runs:      []*RunSummary{},
		}
		if err := writeJSONAtomic(indexPath, catalog); err != nil {
			return nil, fmt.Errorf("failed to initialize index catalog: %w", err)
		}
	}

	store := &DiskStore{baseDir: baseDir}
	if _, err := store.RecoverInterruptedRuns(); err != nil {
		return nil, fmt.Errorf("failed to recover interrupted runs: %w", err)
	}

	return store, nil
}

// BaseDir returns the root storage directory for this DiskStore.
func (s *DiskStore) BaseDir() string {
	return s.baseDir
}

// StartRun initializes the run directory, writes meta.json with initial details,
// and records an entry in index.json.
func (s *DiskStore) StartRun(job *monitor.JobInfo, limits *config.LimitsConfig, driver string) error {
	if job == nil || job.ID == "" {
		return errors.New("invalid job or empty job ID")
	}

	runDir, err := s.runDir(job.ID)
	if err != nil {
		return fmt.Errorf("invalid job ID: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	indexPath := filepath.Join(s.baseDir, "index.json")
	catalog, err := s.loadIndexLocked()
	if err != nil {
		if os.IsNotExist(err) {
			catalog = &IndexCatalog{
				Version: 1,
				Runs:    []*RunSummary{},
			}
		} else if errors.Is(err, ErrCorruptedIndex) {
			_ = s.backupCorruptIndex(indexPath)
			return fmt.Errorf("failed to load index catalog: %w", err)
		} else {
			return fmt.Errorf("failed to read index catalog: %w", err)
		}
	}

	stepsDir := filepath.Join(runDir, "steps")
	if err := os.MkdirAll(stepsDir, 0700); err != nil {
		return fmt.Errorf("failed to create run directory %s: %w", stepsDir, err)
	}

	status := job.Status
	if status == "" {
		status = monitor.StatusRunning
	}

	startedAt := job.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}

	steps := make([]*StepMetadata, 0, len(job.Steps))
	for _, st := range job.Steps {
		if st == nil {
			continue
		}
		var dur int64
		if !st.EndedAt.IsZero() && !st.StartedAt.IsZero() {
			dur = int64(st.EndedAt.Sub(st.StartedAt).Seconds())
			if dur < 0 {
				dur = 0
			}
		}
		steps = append(steps, &StepMetadata{
			ID:          st.ID,
			Number:      st.Number,
			Name:        st.Name,
			Status:      st.Status,
			StartedAt:   st.StartedAt,
			EndedAt:     st.EndedAt,
			DurationSec: dur,
		})
	}

	var runDur int64
	if !job.EndedAt.IsZero() && !startedAt.IsZero() {
		runDur = int64(job.EndedAt.Sub(startedAt).Seconds())
		if runDur < 0 {
			runDur = 0
		}
	}

	meta := &RunMetadata{
		ID:          job.ID,
		Name:        job.Name,
		Workflow:    job.Workflow,
		Repo:        job.Repo,
		RepoAlias:   job.RepoAlias,
		Owner:       job.Owner,
		Status:      status,
		StartedAt:   startedAt,
		EndedAt:     job.EndedAt,
		DurationSec: runDur,
		Driver:      driver,
		Limits:      limits,
		Steps:       steps,
	}

	metaPath := filepath.Join(runDir, "meta.json")
	if err := writeJSONAtomic(metaPath, meta); err != nil {
		return fmt.Errorf("failed to write run metadata: %w", err)
	}

	summary := &RunSummary{
		ID:          meta.ID,
		Name:        meta.Name,
		Workflow:    meta.Workflow,
		Repo:        meta.Repo,
		RepoAlias:   meta.RepoAlias,
		Owner:       meta.Owner,
		Status:      meta.Status,
		StartedAt:   meta.StartedAt,
		EndedAt:     meta.EndedAt,
		DurationSec: meta.DurationSec,
		Driver:      meta.Driver,
		StepCount:   len(meta.Steps),
	}

	found := false
	for i, r := range catalog.Runs {
		if r.ID == summary.ID {
			catalog.Runs[i] = summary
			found = true
			break
		}
	}
	if !found {
		catalog.Runs = append(catalog.Runs, summary)
	}

	s.sortRuns(catalog.Runs)
	catalog.UpdatedAt = time.Now().UTC()

	if err := writeJSONAtomic(indexPath, catalog); err != nil {
		return fmt.Errorf("failed to update index catalog: %w", err)
	}

	return nil
}

// FinishRun finalizes a run record in meta.json with the terminal status, end time,
// duration, and metrics summary, and updates index.json accordingly.
func (s *DiskStore) FinishRun(jobID string, status monitor.StepStatus, metrics *RunMetricsSummary) error {
	if jobID == "" {
		return errors.New("empty job ID")
	}

	runDir, err := s.runDir(jobID)
	if err != nil {
		return fmt.Errorf("invalid job ID %q: %w", jobID, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	indexPath := filepath.Join(s.baseDir, "index.json")
	catalog, err := s.loadIndexLocked()
	if err != nil {
		if os.IsNotExist(err) {
			catalog = &IndexCatalog{
				Version: 1,
				Runs:    []*RunSummary{},
			}
		} else if errors.Is(err, ErrCorruptedIndex) {
			_ = s.backupCorruptIndex(indexPath)
			return fmt.Errorf("failed to load index catalog: %w", err)
		} else {
			return fmt.Errorf("failed to read index catalog: %w", err)
		}
	}

	metaPath := filepath.Join(runDir, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("failed to read run metadata for job %s: %w", jobID, err)
	}

	var meta RunMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return fmt.Errorf("failed to unmarshal run metadata for job %s: %w", jobID, err)
	}

	meta.Status = status
	now := time.Now().UTC()
	if meta.EndedAt.IsZero() {
		meta.EndedAt = now
	}
	if !meta.StartedAt.IsZero() {
		dur := int64(meta.EndedAt.Sub(meta.StartedAt).Seconds())
		if dur < 0 {
			dur = 0
		}
		meta.DurationSec = dur
	}
	if metrics != nil {
		meta.MetricsSummary = *metrics
	}

	if status == monitor.StatusSuccess {
		for _, st := range meta.Steps {
			if st != nil && (st.Status == monitor.StatusQueued || st.Status == monitor.StatusRunning || st.Status == "") {
				st.Status = monitor.StatusSuccess
			}
		}
	}

	// Flush any in-memory step stats that haven't been finalized via FinishStep,
	// synchronizing with stepLock before deleting stats and the lock.
	flushAndDeleteStep := func(key string) *stepStats {
		if val, ok := s.stepLocks.Load(key); ok {
			lock := val.(*sync.RWMutex)
			lock.Lock()
			var flushed *stepStats
			if sVal, ok := s.stepStats.LoadAndDelete(key); ok {
				flushed = sVal.(*stepStats)
			}
			if cur, exists := s.stepLocks.Load(key); exists && cur == lock {
				s.stepLocks.Delete(key)
			}
			lock.Unlock()
			return flushed
		}
		if sVal, ok := s.stepStats.LoadAndDelete(key); ok {
			return sVal.(*stepStats)
		}
		return nil
	}

	for _, st := range meta.Steps {
		if st == nil {
			continue
		}
		key := jobID + "/" + st.ID
		if stat := flushAndDeleteStep(key); stat != nil {
			st.LogBytes += stat.bytes.Load()
			st.LogLines += int(stat.lines.Load())
		}
	}

	// Clean up any remaining step locks or stats for this job
	prefix := jobID + "/"
	s.stepLocks.Range(func(k, _ any) bool {
		if ks, ok := k.(string); ok && strings.HasPrefix(ks, prefix) {
			_ = flushAndDeleteStep(ks)
		}
		return true
	})
	s.stepStats.Range(func(k, _ any) bool {
		if ks, ok := k.(string); ok && strings.HasPrefix(ks, prefix) {
			s.stepStats.Delete(k)
		}
		return true
	})

	if err := writeJSONAtomic(metaPath, &meta); err != nil {
		return fmt.Errorf("failed to update run metadata for job %s: %w", jobID, err)
	}

	var target *RunSummary
	for _, r := range catalog.Runs {
		if r.ID == jobID {
			target = r
			break
		}
	}

	if target == nil {
		target = &RunSummary{
			ID:        meta.ID,
			Name:      meta.Name,
			Workflow:  meta.Workflow,
			Repo:      meta.Repo,
			RepoAlias: meta.RepoAlias,
			Owner:     meta.Owner,
			Driver:    meta.Driver,
		}
		catalog.Runs = append(catalog.Runs, target)
	} else if target.RepoAlias == "" && meta.RepoAlias != "" {
		target.RepoAlias = meta.RepoAlias
	}

	target.Status = meta.Status
	target.StartedAt = meta.StartedAt
	target.EndedAt = meta.EndedAt
	target.DurationSec = meta.DurationSec
	target.StepCount = len(meta.Steps)
	if metrics != nil {
		target.PeakCPUPercent = metrics.PeakCPUPercent
		target.PeakRSSBytes = metrics.PeakRSSBytes
	}

	s.sortRuns(catalog.Runs)
	catalog.UpdatedAt = now

	if err := writeJSONAtomic(indexPath, catalog); err != nil {
		return fmt.Errorf("failed to update index catalog: %w", err)
	}

	return nil
}

// ListRuns returns run summaries sorted descending by started_at.
// If limit <= 0, all recorded runs are returned.
func (s *DiskStore) ListRuns(limit int) ([]*RunSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	indexPath := filepath.Join(s.baseDir, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []*RunSummary{}, nil
		}
		return nil, fmt.Errorf("failed to read index catalog: %w", err)
	}

	var catalog IndexCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("failed to parse index catalog: %w", err)
	}

	runs := catalog.Runs
	if runs == nil {
		return []*RunSummary{}, nil
	}

	if limit > 0 && limit < len(runs) {
		runs = runs[:limit]
	}

	result := make([]*RunSummary, len(runs))
	copy(result, runs)
	return result, nil
}

// GetRun reads and returns the full RunMetadata for the given jobID.
func (s *DiskStore) GetRun(jobID string) (*RunMetadata, error) {
	if jobID == "" {
		return nil, errors.New("empty job ID")
	}

	runDir, err := s.runDir(jobID)
	if err != nil {
		return nil, fmt.Errorf("invalid job ID %q: %w", jobID, err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	metaPath := filepath.Join(runDir, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, err
	}

	var meta RunMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal run metadata: %w", err)
	}

	for _, st := range meta.Steps {
		if st == nil {
			continue
		}
		key := jobID + "/" + st.ID
		if val, ok := s.stepStats.Load(key); ok {
			stat := val.(*stepStats)
			st.LogBytes += stat.bytes.Load()
			st.LogLines += int(stat.lines.Load())
		}
	}

	return &meta, nil
}

func (s *DiskStore) loadIndexLocked() (*IndexCatalog, error) {
	indexPath := filepath.Join(s.baseDir, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}

	var catalog IndexCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptedIndex, err)
	}
	if catalog.Runs == nil {
		catalog.Runs = []*RunSummary{}
	}

	return &catalog, nil
}

func (s *DiskStore) sortRuns(runs []*RunSummary) {
	sort.Slice(runs, func(i, j int) bool {
		if runs[i] == nil {
			return false
		}
		if runs[j] == nil {
			return true
		}
		if runs[i].StartedAt.Equal(runs[j].StartedAt) {
			return runs[i].ID > runs[j].ID
		}
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
}

// writeJSONAtomic marshals v as indented JSON and writes it to a temporary file
// in the target directory before atomically renaming it to targetPath.
func writeJSONAtomic(targetPath string, v any) error {
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary file in %s: %w", dir, err)
	}
	tmpPath := tmpFile.Name()

	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	data = append(data, '\n')

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to write data to temp file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	_ = os.Chmod(tmpPath, 0600)

	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("failed to rename temp file to %s: %w", targetPath, err)
	}

	cleanup = false
	return nil
}

// StepLockCount returns the current count of allocated step locks in memory (for testing and diagnostics).
func (s *DiskStore) StepLockCount() int {
	count := 0
	s.stepLocks.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// StepStatsCount returns the current count of active step stat trackers in memory (for testing and diagnostics).
func (s *DiskStore) StepStatsCount() int {
	count := 0
	s.stepStats.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}
