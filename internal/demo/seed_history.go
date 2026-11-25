package demo

import (
	"time"

	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
)

type seedLogEntry struct {
	stepID string
	line   string
}

func seedRun(store history.Store, job *monitor.JobInfo, driver string, logs []seedLogEntry, status monitor.StepStatus, metrics *monitor.RunMetricsSummary) error {
	if err := store.StartRun(job, nil, driver); err != nil {
		return err
	}
	for _, entry := range logs {
		if err := store.AppendStepLog(job.ID, entry.stepID, entry.line); err != nil {
			return err
		}
	}
	for _, st := range job.Steps {
		if st == nil || st.Status == monitor.StatusQueued {
			continue
		}
		exitCode := 0
		if st.Status == monitor.StatusFailed {
			exitCode = 1
		}
		if err := store.FinishStep(job.ID, st.ID, string(st.Status), exitCode, st.EndedAt, st.StartedAt); err != nil {
			return err
		}
	}
	if status != "" && status != monitor.StatusQueued && status != monitor.StatusRunning {
		if err := store.FinishRun(job.ID, status, metrics); err != nil {
			return err
		}
	}
	return nil
}

// SeedHistoryStore populates the provided history store with representative past demo runs:
// - release-v0.1.0 (success)
// - nightly-security-scan (success)
// - flaky-integration-test (failed)
// This ensures that running actr monitor immediately demonstrates both LIVE RUNS and HISTORICAL RUNS.
func SeedHistoryStore(store history.Store) error {
	if store == nil {
		return nil
	}

	now := time.Now().UTC()

	// 1. release-v0.1.0 (success)
	runRelease := &monitor.JobInfo{
		ID:        "release-v0.1.0",
		Name:      "release-v0.1.0",
		Workflow:  "Release (v0.1.0)",
		Repo:      "actr",
		RepoAlias: "actr",
		RepoURL:   "https://github.com/adrijshikhar/actr",
		Owner:     "adrijshikhar",
		Status:    monitor.StatusRunning,
		StartedAt: now.Add(-3 * time.Hour),
		EndedAt:   now.Add(-2*time.Hour - 48*time.Minute),
		Steps: []*monitor.StepInfo{
			{ID: "rel-1", Number: 1, Name: "Checkout repository", Status: monitor.StatusSuccess, StartedAt: now.Add(-3 * time.Hour), EndedAt: now.Add(-2*time.Hour - 58*time.Minute)},
			{ID: "rel-2", Number: 2, Name: "Cross-compile multi-arch static binaries", Status: monitor.StatusSuccess, StartedAt: now.Add(-2*time.Hour - 58*time.Minute), EndedAt: now.Add(-2*time.Hour - 50*time.Minute)},
			{ID: "rel-3", Number: 3, Name: "Publish artifacts to GitHub Releases", Status: monitor.StatusSuccess, StartedAt: now.Add(-2*time.Hour - 50*time.Minute), EndedAt: now.Add(-2*time.Hour - 48*time.Minute)},
		},
	}
	releaseLogs := []seedLogEntry{
		{"rel-1", "\x1b[38;5;244m2026-11-16T11:00:00Z\x1b[0m \x1b[1;34m##[group]Git checkout\x1b[0m"},
		{"rel-1", "\x1b[38;5;244m2026-11-16T11:00:02Z\x1b[0m Syncing repository https://github.com/adrijshikhar/actr.git at tag v0.1.0"},
		{"rel-1", "\x1b[38;5;244m2026-11-16T11:00:15Z\x1b[0m \x1b[1;32m✓\x1b[0m Repository checked out at commit 420cb8d"},
		{"rel-1", "\x1b[38;5;244m2026-11-16T11:00:16Z\x1b[0m \x1b[1;34m##[endgroup]\x1b[0m"},
		{"rel-2", "\x1b[38;5;244m2026-11-16T11:02:00Z\x1b[0m \x1b[1;34m##[group]Compiling static binaries\x1b[0m"},
		{"rel-2", "\x1b[38;5;244m2026-11-16T11:02:05Z\x1b[0m \x1b[38;5;39m[darwin/arm64]\x1b[0m CGO_ENABLED=0 go build -trimpath -ldflags=\"-s -w\" -o bin/actr_darwin_arm64 ./cmd/actr"},
		{"rel-2", "\x1b[38;5;244m2026-11-16T11:04:10Z\x1b[0m \x1b[38;5;39m[linux/amd64]\x1b[0m CGO_ENABLED=0 go build -trimpath -ldflags=\"-s -w\" -o bin/actr_linux_amd64 ./cmd/actr"},
		{"rel-2", "\x1b[38;5;244m2026-11-16T11:06:20Z\x1b[0m \x1b[1;32m✓\x1b[0m All distribution artifacts built successfully (checksums generated)"},
		{"rel-2", "\x1b[38;5;244m2026-11-16T11:06:21Z\x1b[0m \x1b[1;34m##[endgroup]\x1b[0m"},
		{"rel-3", "\x1b[38;5;244m2026-11-16T11:10:00Z\x1b[0m Uploading release assets to GitHub (tag: v0.1.0)"},
		{"rel-3", "\x1b[38;5;244m2026-11-16T11:12:00Z\x1b[0m \x1b[1;32m✓ Release v0.1.0 published successfully\x1b[0m"},
	}
	if err := seedRun(store, runRelease, "docker", releaseLogs, monitor.StatusSuccess, &monitor.RunMetricsSummary{
		PeakCPUPercent:        74.2,
		PeakRSSBytes:          256 * 1024 * 1024,
		TotalNetRxBytes:       45 * 1024 * 1024,
		TotalNetTxBytes:       120 * 1024 * 1024,
		TotalDiskWrittenBytes: 180 * 1024 * 1024,
	}); err != nil {
		return err
	}

	// 2. nightly-security-scan (success)
	runSecurity := &monitor.JobInfo{
		ID:        "nightly-security-scan",
		Name:      "nightly-security-scan",
		Workflow:  "Nightly Security Scan (#142)",
		Repo:      "aim",
		RepoAlias: "aim",
		RepoURL:   "https://github.com/adrijshikhar/aim",
		Owner:     "adrijshikhar",
		Status:    monitor.StatusRunning,
		StartedAt: now.Add(-14 * time.Hour),
		EndedAt:   now.Add(-13*time.Hour - 45*time.Minute),
		Steps: []*monitor.StepInfo{
			{ID: "sec-1", Number: 1, Name: "Sync vulnerability database", Status: monitor.StatusSuccess, StartedAt: now.Add(-14 * time.Hour), EndedAt: now.Add(-13*time.Hour - 55*time.Minute)},
			{ID: "sec-2", Number: 2, Name: "Run govulncheck & staticcheck", Status: monitor.StatusSuccess, StartedAt: now.Add(-13*time.Hour - 55*time.Minute), EndedAt: now.Add(-13*time.Hour - 47*time.Minute)},
			{ID: "sec-3", Number: 3, Name: "Audit dependency license compliance", Status: monitor.StatusSuccess, StartedAt: now.Add(-13*time.Hour - 47*time.Minute), EndedAt: now.Add(-13*time.Hour - 45*time.Minute)},
		},
	}
	securityLogs := []seedLogEntry{
		{"sec-1", "\x1b[38;5;244m2026-11-16T00:00:00Z\x1b[0m \x1b[1;32m✓\x1b[0m Go vulnerability database synced (govulncheck v1.1.4)"},
		{"sec-2", "\x1b[38;5;244m2026-11-16T00:05:00Z\x1b[0m Scanning 38 module dependencies across 8 packages..."},
		{"sec-2", "\x1b[38;5;244m2026-11-16T00:12:30Z\x1b[0m \x1b[1;32mPASS: No known vulnerabilities found\x1b[0m"},
		{"sec-3", "\x1b[38;5;244m2026-11-16T00:14:00Z\x1b[0m \x1b[1;32m✓\x1b[0m License audit passed: 100% MIT / Apache-2.0 / BSD compatible"},
	}
	if err := seedRun(store, runSecurity, "docker", securityLogs, monitor.StatusSuccess, &monitor.RunMetricsSummary{
		PeakCPUPercent:        88.5,
		PeakRSSBytes:          384 * 1024 * 1024,
		TotalNetRxBytes:       210 * 1024 * 1024,
		TotalNetTxBytes:       12 * 1024 * 1024,
		TotalDiskWrittenBytes: 95 * 1024 * 1024,
	}); err != nil {
		return err
	}

	// 3. flaky-integration-test (failed)
	runFlaky := &monitor.JobInfo{
		ID:        "flaky-integration-test",
		Name:      "flaky-integration-test",
		Workflow:  "Integration Test Matrix (#891)",
		Repo:      "cxstatusline",
		RepoAlias: "cxstatusline",
		RepoURL:   "https://github.com/adrijshikhar/cxstatusline",
		Owner:     "adrijshikhar",
		Status:    monitor.StatusRunning,
		StartedAt: now.Add(-26 * time.Hour),
		EndedAt:   now.Add(-25*time.Hour - 52*time.Minute),
		Steps: []*monitor.StepInfo{
			{ID: "flk-1", Number: 1, Name: "Spin up PostgreSQL test container", Status: monitor.StatusSuccess, StartedAt: now.Add(-26 * time.Hour), EndedAt: now.Add(-25*time.Hour - 58*time.Minute)},
			{ID: "flk-2", Number: 2, Name: "Seed database fixtures", Status: monitor.StatusSuccess, StartedAt: now.Add(-25*time.Hour - 58*time.Minute), EndedAt: now.Add(-25*time.Hour - 55*time.Minute)},
			{ID: "flk-3", Number: 3, Name: "Run concurrent transaction tests (-race)", Status: monitor.StatusFailed, StartedAt: now.Add(-25*time.Hour - 55*time.Minute), EndedAt: now.Add(-25*time.Hour - 52*time.Minute)},
		},
	}
	flakyLogs := []seedLogEntry{
		{"flk-1", "\x1b[38;5;244m2026-11-15T12:00:00Z\x1b[0m \x1b[1;32m✓\x1b[0m Container postgres:16-alpine healthy on 127.0.0.1:5432"},
		{"flk-2", "\x1b[38;5;244m2026-11-15T12:02:00Z\x1b[0m Applied 14 migrations, seeded 10,000 test records"},
		{"flk-3", "\x1b[38;5;244m2026-11-15T12:05:00Z\x1b[0m === RUN   TestConcurrentTransactions_DeadlockRetry"},
		{"flk-3", "\x1b[38;5;244m2026-11-15T12:07:45Z\x1b[0m \x1b[1;31m--- FAIL: TestConcurrentTransactions_DeadlockRetry (165.20s)\x1b[0m"},
		{"flk-3", "\x1b[38;5;244m2026-11-15T12:07:46Z\x1b[0m \x1b[1;31m    integration_test.go:189: context deadline exceeded waiting for isolation lock\x1b[0m"},
		{"flk-3", "\x1b[38;5;244m2026-11-15T12:07:47Z\x1b[0m \x1b[1;31mFAIL: exit status 1\x1b[0m"},
	}
	if err := seedRun(store, runFlaky, "docker", flakyLogs, monitor.StatusFailed, &monitor.RunMetricsSummary{
		PeakCPUPercent:        96.8,
		PeakRSSBytes:          512 * 1024 * 1024,
		TotalNetRxBytes:       140 * 1024 * 1024,
		TotalNetTxBytes:       85 * 1024 * 1024,
		TotalDiskWrittenBytes: 310 * 1024 * 1024,
	}); err != nil {
		return err
	}

	// 4. job-queued-1 (queued task waiting for available runner slot)
	runQueued := &monitor.JobInfo{
		ID:        "job-queued-1",
		Name:      "test-matrix-concurrent",
		Workflow:  "E2E Integration Matrix (#43589210)",
		Repo:      "actr",
		RepoAlias: "actr",
		RepoURL:   "https://github.com/adrijshikhar/actr",
		Owner:     "adrijshikhar",
		Driver:    "docker",
		Status:    monitor.StatusQueued,
		StartedAt: now.Add(-30 * time.Second),
		Steps: []*monitor.StepInfo{
			{ID: "q-1", Number: 1, Name: "Provision runner container sandbox", Status: monitor.StatusQueued},
			{ID: "q-2", Number: 2, Name: "Run multi-platform integration tests", Status: monitor.StatusQueued},
		},
	}
	queuedLogs := []seedLogEntry{
		{"q-1", "\x1b[38;5;244m2026-11-24T22:40:00Z\x1b[0m \x1b[1;33m⏳ Queued:\x1b[0m Job is waiting in queue for runner 'actr-runner-darwin-01' (max concurrency: 4, occupied: 2)"},
		{"q-1", "\x1b[38;5;244m2026-11-24T22:40:01Z\x1b[0m Waiting for workflow slot allocation..."},
	}
	return seedRun(store, runQueued, "docker", queuedLogs, monitor.StatusQueued, nil)
}
