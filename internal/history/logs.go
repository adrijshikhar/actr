package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adrijshikhar/actr/internal/monitor"
)

type stepStats struct {
	bytes atomic.Int64
	lines atomic.Int64
}

// acquireStepLock returns and locks the active per-step RWMutex for key.
// It retries if the lock was deleted from s.stepLocks while waiting.
func (s *DiskStore) acquireStepLock(key string) *sync.RWMutex {
	for {
		val, _ := s.stepLocks.LoadOrStore(key, &sync.RWMutex{})
		lock := val.(*sync.RWMutex)
		lock.Lock()
		if cur, ok := s.stepLocks.Load(key); ok && cur == lock {
			return lock
		}
		lock.Unlock()
	}
}

// acquireStepRLock returns and read-locks the active per-step RWMutex for key.
// It retries if the lock was deleted from s.stepLocks while waiting.
func (s *DiskStore) acquireStepRLock(key string) *sync.RWMutex {
	for {
		val, _ := s.stepLocks.LoadOrStore(key, &sync.RWMutex{})
		lock := val.(*sync.RWMutex)
		lock.RLock()
		if cur, ok := s.stepLocks.Load(key); ok && cur == lock {
			return lock
		}
		lock.RUnlock()
	}
}

// AppendStepLog appends a log line to <baseDir>/<jobID>/steps/<stepID>.log, ensuring parent directory
// exists, line has newline termination, and step metadata statistics (LogBytes, LogLines) are updated
// in memory without blocking disk fsync.
func (s *DiskStore) AppendStepLog(jobID, stepID, line string) error {
	logPath, err := s.stepLogPath(jobID, stepID)
	if err != nil {
		return fmt.Errorf("invalid job or step ID: %w", err)
	}

	lineCount := strings.Count(line, "\n")
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
		lineCount++
	}
	if lineCount == 0 {
		lineCount = 1
	}
	byteCount := int64(len(line))

	key := jobID + "/" + stepID
	lock := s.acquireStepLock(key)
	defer lock.Unlock()

	stepsDir := filepath.Dir(logPath)
	if err := os.MkdirAll(stepsDir, 0700); err != nil {
		return fmt.Errorf("failed to create steps directory %s: %w", stepsDir, err)
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open step log file %s: %w", logPath, err)
	}

	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to write to step log file %s: %w", logPath, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close step log file %s: %w", logPath, err)
	}
	// In-memory tracking of step line counts and byte counts during streaming
	val, _ := s.stepStats.LoadOrStore(key, &stepStats{})
	stat := val.(*stepStats)
	stat.bytes.Add(byteCount)
	stat.lines.Add(int64(lineCount))

	return nil
}

// FinishStep finalizes an individual step in meta.json with the provided conclusion,
// exit code, and finish timestamp, flushes accumulated in-memory log statistics,
// and cleans up step locks and stats.
func (s *DiskStore) FinishStep(jobID, stepID, conclusion string, exitCode int, finishedAt time.Time, startedAt ...time.Time) error {
	if jobID == "" {
		return errors.New("empty job ID")
	}
	if stepID == "" {
		return errors.New("empty step ID")
	}

	runDir, err := s.runDir(jobID)
	if err != nil {
		return fmt.Errorf("invalid job ID %q: %w", jobID, err)
	}
	cleanStepID, err := sanitizeID(stepID)
	if err != nil {
		return fmt.Errorf("invalid step ID %q: %w", stepID, err)
	}

	key := jobID + "/" + cleanStepID

	s.mu.Lock()
	defer s.mu.Unlock()

	// Acquire stepLock after s.mu to maintain consistent lock order (s.mu -> stepLock)
	// and ensure any in-flight append completes before flushing stats and deleting the lock.
	lock := s.acquireStepLock(key)
	defer func() {
		s.stepLocks.Delete(key)
		lock.Unlock()
	}()

	metaPath := filepath.Join(runDir, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("failed to read run metadata for job %s: %w", jobID, err)
	}

	var meta RunMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return fmt.Errorf("failed to unmarshal run metadata for job %s: %w", jobID, err)
	}

	var target *StepMetadata
	for _, st := range meta.Steps {
		if st != nil && st.ID == cleanStepID {
			target = st
			break
		}
	}

	if target == nil {
		target = &StepMetadata{
			ID:     cleanStepID,
			Name:   cleanStepID,
			Number: len(meta.Steps) + 1,
		}
		meta.Steps = append(meta.Steps, target)
	}

	status := monitor.StepStatus(conclusion)
	if status == "" {
		if exitCode != 0 {
			status = monitor.StatusFailed
		} else {
			status = monitor.StatusSuccess
		}
	}
	target.Status = status
	target.ExitCode = exitCode

	if len(startedAt) > 0 && !startedAt[0].IsZero() && target.StartedAt.IsZero() {
		target.StartedAt = startedAt[0]
	}

	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	target.EndedAt = finishedAt
	if !target.StartedAt.IsZero() {
		dur := int64(target.EndedAt.Sub(target.StartedAt).Seconds())
		if dur < 0 {
			dur = 0
		}
		target.DurationSec = dur
	}

	// Flush accumulated in-memory stats
	if val, ok := s.stepStats.LoadAndDelete(key); ok {
		stat := val.(*stepStats)
		target.LogBytes += stat.bytes.Load()
		target.LogLines += int(stat.lines.Load())
	}

	if err := writeJSONAtomic(metaPath, &meta); err != nil {
		return fmt.Errorf("failed to update run metadata for job %s: %w", jobID, err)
	}

	return nil
}

// GetStepLogs returns all lines from <baseDir>/<jobID>/steps/<stepID>.log preserving ANSI escape sequences.
// If the step log file does not exist, an empty slice and nil error are returned.
func (s *DiskStore) GetStepLogs(jobID, stepID string) ([]string, error) {
	logPath, err := s.stepLogPath(jobID, stepID)
	if err != nil {
		return nil, fmt.Errorf("invalid job or step ID: %w", err)
	}

	lock := s.acquireStepRLock(jobID + "/" + stepID)
	defer lock.RUnlock()

	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read step log file %s: %w", logPath, err)
	}

	if len(data) == 0 {
		return []string{}, nil
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)
	lines := make([]string, 0)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan step log file %s: %w", logPath, err)
	}

	return lines, nil
}

// GetStepLogReader returns a readable stream for <baseDir>/<jobID>/steps/<stepID>.log.
// The caller is responsible for closing the returned reader.
func (s *DiskStore) GetStepLogReader(jobID, stepID string) (io.ReadCloser, error) {
	logPath, err := s.stepLogPath(jobID, stepID)
	if err != nil {
		return nil, fmt.Errorf("invalid job or step ID: %w", err)
	}

	f, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	return f, nil
}
