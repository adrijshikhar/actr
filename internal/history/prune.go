package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrijshikhar/actr/internal/monitor"
)

// Prune enforces LRU retention limits across stored workflow runs.
// Active runs ("running" or "queued") are never pruned.
// If maxRuns > 0 and the number of completed runs exceeds maxRuns, oldest completed runs are pruned.
// If maxDiskBytes > 0 and the total disk usage of run folders exceeds maxDiskBytes,
// oldest completed runs are pruned one-by-one until total usage is <= 90% of maxDiskBytes.
// Updated index.json is written atomically.
func (s *DiskStore) Prune(maxRuns int, maxDiskBytes ...int64) (int, error) {
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

	// Partition runs by status: active runs vs completed runs.
	// Active runs (running, queued) must never be pruned.
	var activeRuns []*RunSummary
	var completedRuns []*RunSummary
	for _, r := range catalog.Runs {
		if r == nil {
			continue
		}
		if r.Status == monitor.StatusRunning || r.Status == monitor.StatusQueued {
			activeRuns = append(activeRuns, r)
		} else {
			completedRuns = append(completedRuns, r)
		}
	}

	prunedCount := 0

	// 1. Count pruning: remove oldest completed runs beyond maxRuns limit
	if maxRuns > 0 && len(completedRuns) > maxRuns {
		toPrune := completedRuns[maxRuns:]
		completedRuns = completedRuns[:maxRuns]
		for _, run := range toPrune {
			if run == nil || strings.TrimSpace(run.ID) == "" {
				continue
			}
			runDir, err := s.runDir(run.ID)
			if err != nil {
				continue
			}
			_ = os.RemoveAll(runDir)
			prunedCount++
		}
	}

	// 2. Disk size pruning: prune oldest completed runs until total usage is <= 90% of maxDiskBytes
	var diskLimit int64
	if len(maxDiskBytes) > 0 {
		diskLimit = maxDiskBytes[0]
	}
	if diskLimit > 0 {
		runSizes := make(map[string]int64)
		var totalSize int64

		// Calculate size of all valid runs (both active and completed)
		for _, r := range append(activeRuns, completedRuns...) {
			if r == nil || strings.TrimSpace(r.ID) == "" {
				continue
			}
			runDir, err := s.runDir(r.ID)
			if err != nil {
				continue
			}
			sz := dirSize(runDir)
			runSizes[r.ID] = sz
			totalSize += sz
		}

		if totalSize > diskLimit {
			threshold := int64(float64(diskLimit) * 0.90)
			for len(completedRuns) > 0 && totalSize > threshold {
				lastIdx := len(completedRuns) - 1
				oldest := completedRuns[lastIdx]
				completedRuns = completedRuns[:lastIdx]

				if oldest != nil && strings.TrimSpace(oldest.ID) != "" {
					if runDir, err := s.runDir(oldest.ID); err == nil {
						_ = os.RemoveAll(runDir)
						totalSize -= runSizes[oldest.ID]
						prunedCount++
					}
				}
			}
		}
	}

	if prunedCount > 0 || len(catalog.Runs) != len(activeRuns)+len(completedRuns) {
		catalog.Runs = append(activeRuns, completedRuns...)
		s.sortRuns(catalog.Runs)
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
		if errors.Is(err, ErrCorruptedIndex) {
			indexPath := filepath.Join(s.baseDir, "index.json")
			_ = s.backupCorruptIndex(indexPath)
			catalog = &IndexCatalog{Version: 1, Runs: []*RunSummary{}}
			_ = writeJSONAtomic(indexPath, catalog)
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
		if run.Status == monitor.StatusRunning || run.Status == monitor.StatusQueued {
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

			if strings.TrimSpace(run.ID) == "" {
				continue
			}
			runDir, err := s.runDir(run.ID)
			if err != nil {
				continue
			}

			metaPath := filepath.Join(runDir, "meta.json")
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
