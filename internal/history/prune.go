package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/adrijshikhar/actr/internal/monitor"
)

// Prune enforces LRU retention limits across stored workflow runs.
// If maxRuns > 0 and the number of runs exceeds maxRuns, oldest runs are pruned.
// If maxDiskBytes > 0 and the total disk usage of run folders exceeds maxDiskBytes,
// oldest runs are pruned one-by-one until total usage is <= 90% of maxDiskBytes.
// Updated index.json is written atomically.
func (s *DiskStore) Prune(maxRuns int, maxDiskBytes int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	catalog, err := s.loadIndexLocked()
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to load index catalog: %w", err)
	}

	if len(catalog.Runs) == 0 {
		return 0, nil
	}

	s.sortRuns(catalog.Runs)

	prunedCount := 0

	// 1. Count pruning: remove oldest runs beyond maxRuns limit
	if maxRuns > 0 && len(catalog.Runs) > maxRuns {
		toPrune := catalog.Runs[maxRuns:]
		catalog.Runs = catalog.Runs[:maxRuns]
		for _, run := range toPrune {
			runDir := filepath.Join(s.baseDir, run.ID)
			_ = os.RemoveAll(runDir)
			prunedCount++
		}
	}

	// 2. Disk size pruning: prune oldest runs until total usage is <= 90% of maxDiskBytes
	if maxDiskBytes > 0 && len(catalog.Runs) > 0 {
		runSizes := make(map[string]int64, len(catalog.Runs))
		var totalSize int64
		for _, r := range catalog.Runs {
			sz := dirSize(filepath.Join(s.baseDir, r.ID))
			runSizes[r.ID] = sz
			totalSize += sz
		}

		if totalSize > maxDiskBytes {
			threshold := int64(float64(maxDiskBytes) * 0.90)
			for len(catalog.Runs) > 0 && totalSize > threshold {
				lastIdx := len(catalog.Runs) - 1
				oldest := catalog.Runs[lastIdx]
				runDir := filepath.Join(s.baseDir, oldest.ID)
				_ = os.RemoveAll(runDir)
				totalSize -= runSizes[oldest.ID]
				catalog.Runs = catalog.Runs[:lastIdx]
				prunedCount++
			}
		}
	}

	if prunedCount > 0 {
		catalog.UpdatedAt = time.Now().UTC()
		indexPath := filepath.Join(s.baseDir, "index.json")
		if err := writeJSONAtomic(indexPath, catalog); err != nil {
			return prunedCount, fmt.Errorf("failed to save index catalog after pruning: %w", err)
		}
	}

	return prunedCount, nil
}

// RecoverInterruptedRuns scans index.json for any abandoned runs in "running" status
// (e.g. from SIGKILL or system crash) and transitions them to "interrupted", setting
// ended_at and updating their meta.json if present.
func (s *DiskStore) RecoverInterruptedRuns() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	catalog, err := s.loadIndexLocked()
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to load index catalog: %w", err)
	}

	if len(catalog.Runs) == 0 {
		return 0, nil
	}

	now := time.Now().UTC()
	recoveredCount := 0

	for _, run := range catalog.Runs {
		if run.Status == monitor.StatusRunning {
			run.Status = StatusInterrupted
			run.EndedAt = now
			if !run.StartedAt.IsZero() {
				dur := int64(now.Sub(run.StartedAt).Seconds())
				if dur < 0 {
					dur = 0
				}
				run.DurationSec = dur
			}
			recoveredCount++

			metaPath := filepath.Join(s.baseDir, run.ID, "meta.json")
			data, err := os.ReadFile(metaPath)
			if err == nil {
				var meta RunMetadata
				if err := json.Unmarshal(data, &meta); err == nil {
					meta.Status = StatusInterrupted
					meta.EndedAt = now
					meta.DurationSec = run.DurationSec
					for _, step := range meta.Steps {
						if step != nil && step.Status == monitor.StatusRunning {
							step.Status = StatusInterrupted
							step.EndedAt = now
							if !step.StartedAt.IsZero() {
								sDur := int64(now.Sub(step.StartedAt).Seconds())
								if sDur < 0 {
									sDur = 0
								}
								step.DurationSec = sDur
							}
						}
					}
					_ = writeJSONAtomic(metaPath, &meta)
				}
			}
		}
	}

	if recoveredCount > 0 {
		catalog.UpdatedAt = now
		indexPath := filepath.Join(s.baseDir, "index.json")
		if err := writeJSONAtomic(indexPath, catalog); err != nil {
			return recoveredCount, fmt.Errorf("failed to save index catalog after recovery: %w", err)
		}
	}

	return recoveredCount, nil
}

// dirSize computes the total byte size of regular files under path.
func dirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}
