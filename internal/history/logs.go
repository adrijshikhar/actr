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
)

// getStepLock returns or initializes the per-step RWMutex for synchronizing concurrent access.
func (s *DiskStore) getStepLock(jobID, stepID string) *sync.RWMutex {
	key := jobID + "/" + stepID
	val, _ := s.stepLocks.LoadOrStore(key, &sync.RWMutex{})
	return val.(*sync.RWMutex)
}

// AppendStepLog appends a log line to <baseDir>/<jobID>/steps/<stepID>.log, ensuring parent directory
// exists, line has newline termination, and step metadata statistics (LogBytes, LogLines) are updated
// if the step is tracked in meta.json.
func (s *DiskStore) AppendStepLog(jobID, stepID, line string) error {
	if jobID == "" {
		return errors.New("empty job ID")
	}
	if stepID == "" {
		return errors.New("empty step ID")
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

	lock := s.getStepLock(jobID, stepID)
	lock.Lock()

	stepsDir := filepath.Join(s.baseDir, jobID, "steps")
	if err := os.MkdirAll(stepsDir, 0755); err != nil {
		lock.Unlock()
		return fmt.Errorf("failed to create steps directory %s: %w", stepsDir, err)
	}

	logPath := filepath.Join(stepsDir, stepID+".log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		lock.Unlock()
		return fmt.Errorf("failed to open step log file %s: %w", logPath, err)
	}

	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		lock.Unlock()
		return fmt.Errorf("failed to write to step log file %s: %w", logPath, err)
	}

	if err := f.Close(); err != nil {
		lock.Unlock()
		return fmt.Errorf("failed to close step log file %s: %w", logPath, err)
	}
	lock.Unlock()

	s.updateStepMeta(jobID, stepID, byteCount, lineCount)
	return nil
}

// GetStepLogs returns all lines from <baseDir>/<jobID>/steps/<stepID>.log preserving ANSI escape sequences.
// If the step log file does not exist, an empty slice and nil error are returned.
func (s *DiskStore) GetStepLogs(jobID, stepID string) ([]string, error) {
	if jobID == "" {
		return nil, errors.New("empty job ID")
	}
	if stepID == "" {
		return nil, errors.New("empty step ID")
	}

	lock := s.getStepLock(jobID, stepID)
	lock.RLock()
	defer lock.RUnlock()

	logPath := filepath.Join(s.baseDir, jobID, "steps", stepID+".log")
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
	if jobID == "" {
		return nil, errors.New("empty job ID")
	}
	if stepID == "" {
		return nil, errors.New("empty step ID")
	}

	logPath := filepath.Join(s.baseDir, jobID, "steps", stepID+".log")
	f, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// updateStepMeta atomically updates LogBytes and LogLines for stepID in meta.json if it exists.
func (s *DiskStore) updateStepMeta(jobID, stepID string, byteCount int64, lineCount int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	metaPath := filepath.Join(s.baseDir, jobID, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return
	}

	var meta RunMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return
	}

	updated := false
	for _, st := range meta.Steps {
		if st != nil && st.ID == stepID {
			st.LogBytes += byteCount
			st.LogLines += lineCount
			updated = true
			break
		}
	}

	if updated {
		_ = writeJSONAtomic(metaPath, &meta)
	}
}
