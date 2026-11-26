package demo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/adrijshikhar/actr/internal/monitor"
)

// StartServerWithStore initializes and starts a demonstration monitor server on sockPath
// with an attached history store.
func StartServerWithStore(ctx context.Context, sockPath string, store monitor.HistoryStore) (*monitor.Server, error) {
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}

	server := monitor.NewServer(sockPath, "m5-pro (macOS arm64)")
	if store != nil {
		server.WithHistoryStore(store)
	}
	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("start monitor server: %w", err)
	}

	seedInitialState(server)
	go runSimulation(ctx, server)

	return server, nil
}

// StartServer initializes and starts a demonstration monitor server on sockPath.
// It pre-seeds past completed jobs, sets up an active in-flight job, and continuously
// streams realistic logs, step status transitions, and fluctuating telemetry until ctx is cancelled.
func StartServer(ctx context.Context, sockPath string) (*monitor.Server, error) {
	return StartServerWithStore(ctx, sockPath, nil)
}

func seedInitialState(server *monitor.Server) {
	now := time.Now()

	// Multi-repo supervisor slot contracts
	server.SetSupervisorStatus(&monitor.SupervisorStatus{
		GlobalMaxJobs: 2,
		GlobalActive:  1,
		RepoSlots: []*monitor.RepoSlotInfo{
			{Alias: "aim", MaxJobs: 1, ActiveJobs: 0, Driver: "host", Status: "idle"},
			{Alias: "cxstatusline", MaxJobs: 1, ActiveJobs: 1, Driver: "host", Status: "busy"},
		},
	})

	// 1. Completed Linux Matrix Job
	linuxJob := &monitor.JobInfo{
		ID:        "job-linux-1",
		Name:      "test-linux-amd64",
		Workflow:  "CI (#43589138)",
		Repo:      "actr",
		RepoAlias: "actr",
		RepoURL:   "https://github.com/adrijshikhar/actr",
		Owner:     "adrijshikhar",
		Driver:    "docker",
		Status:    monitor.StatusSuccess,
		StartedAt: now.Add(-5 * time.Minute),
		EndedAt:   now.Add(-3 * time.Minute),
		MetricsSummary: &monitor.RunMetricsSummary{
			PeakCPUPercent:        68.4,
			PeakRSSBytes:          192 * 1024 * 1024,
			TotalNetRxBytes:       18 * 1024 * 1024,
			TotalNetTxBytes:       4 * 1024 * 1024,
			TotalDiskWrittenBytes: 45 * 1024 * 1024,
		},
		Steps: []*monitor.StepInfo{
			{ID: "l-1", Name: "Setup runner", Status: monitor.StatusSuccess, StartedAt: now.Add(-5 * time.Minute), EndedAt: now.Add(-4*time.Minute - 50*time.Second)},
			{ID: "l-2", Name: "Checkout code", Status: monitor.StatusSuccess, StartedAt: now.Add(-4*time.Minute - 50*time.Second), EndedAt: now.Add(-4*time.Minute - 35*time.Second)},
			{ID: "l-3", Name: "Run tests (-race)", Status: monitor.StatusSuccess, StartedAt: now.Add(-4*time.Minute - 35*time.Second), EndedAt: now.Add(-3 * time.Minute)},
		},
	}
	server.Publish(monitor.Event{Type: monitor.EventJobStart, JobID: linuxJob.ID, RepoAlias: "actr", Job: linuxJob})

	server.AppendJobStepLog(linuxJob.ID, "l-1", "\x1b[38;5;244m2026-11-16T14:30:00Z\x1b[0m \x1b[1;32m✓\x1b[0m Host runner environment validated (Linux x86_64)")
	server.AppendJobStepLog(linuxJob.ID, "l-2", "\x1b[38;5;244m2026-11-16T14:30:10Z\x1b[0m \x1b[1;34m##[group]Git checkout\x1b[0m")
	server.AppendJobStepLog(linuxJob.ID, "l-2", "\x1b[38;5;244m2026-11-16T14:30:12Z\x1b[0m Syncing repository https://github.com/adrijshikhar/actr.git")
	server.AppendJobStepLog(linuxJob.ID, "l-2", "\x1b[38;5;244m2026-11-16T14:30:25Z\x1b[0m \x1b[1;34m##[endgroup]\x1b[0m")
	server.AppendJobStepLog(linuxJob.ID, "l-3", "\x1b[38;5;244m2026-11-16T14:30:26Z\x1b[0m === RUN   TestProtocol_Handshake")
	server.AppendJobStepLog(linuxJob.ID, "l-3", "\x1b[38;5;244m2026-11-16T14:31:00Z\x1b[0m \x1b[1;32mPASS: all 11 packages passed in 4.1s\x1b[0m")

	server.Publish(monitor.Event{Type: monitor.EventJobFinish, JobID: linuxJob.ID, RepoAlias: "actr", Job: linuxJob})

	// 2. Completed macOS Matrix Job
	macJob := &monitor.JobInfo{
		ID:        "job-macos-1",
		Name:      "test-macos-arm64",
		Workflow:  "CI (#43589138)",
		Repo:      "aim",
		RepoAlias: "aim",
		RepoURL:   "https://github.com/adrijshikhar/aim",
		Owner:     "adrijshikhar",
		Driver:    "host",
		Status:    monitor.StatusSuccess,
		StartedAt: now.Add(-3 * time.Minute),
		EndedAt:   now.Add(-1 * time.Minute),
		MetricsSummary: &monitor.RunMetricsSummary{
			PeakCPUPercent:        54.0,
			PeakRSSBytes:          210 * 1024 * 1024,
			TotalNetRxBytes:       12 * 1024 * 1024,
			TotalNetTxBytes:       2 * 1024 * 1024,
			TotalDiskWrittenBytes: 30 * 1024 * 1024,
		},
		Steps: []*monitor.StepInfo{
			{ID: "m-1", Name: "Setup runner", Status: monitor.StatusSuccess, StartedAt: now.Add(-3 * time.Minute), EndedAt: now.Add(-2*time.Minute - 50*time.Second)},
			{ID: "m-2", Name: "Checkout code", Status: monitor.StatusSuccess, StartedAt: now.Add(-2*time.Minute - 50*time.Second), EndedAt: now.Add(-2*time.Minute - 40*time.Second)},
			{ID: "m-3", Name: "Run tests (-race)", Status: monitor.StatusSuccess, StartedAt: now.Add(-2*time.Minute - 40*time.Second), EndedAt: now.Add(-1 * time.Minute)},
		},
	}
	server.Publish(monitor.Event{Type: monitor.EventJobStart, JobID: macJob.ID, RepoAlias: "aim", Job: macJob})

	server.AppendJobStepLog(macJob.ID, "m-1", "\x1b[38;5;244m2026-11-16T14:32:00Z\x1b[0m \x1b[1;32m✓\x1b[0m macOS darwin/arm64 sandbox initialized")
	server.AppendJobStepLog(macJob.ID, "m-2", "\x1b[38;5;244m2026-11-16T14:32:15Z\x1b[0m Checked out commit 98f6bab (HEAD -> main)")
	server.AppendJobStepLog(macJob.ID, "m-3", "\x1b[38;5;244m2026-11-16T14:33:00Z\x1b[0m \x1b[1;32mPASS: all 11 packages passed in 3.8s\x1b[0m")

	server.Publish(monitor.Event{Type: monitor.EventJobFinish, JobID: macJob.ID, RepoAlias: "aim", Job: macJob})

	// 3. Active Live Job
	liveJob := &monitor.JobInfo{
		ID:        "job-live-1",
		Name:      "release-prebuilt",
		Workflow:  "Release (v0.1.2)",
		Repo:      "cxstatusline",
		RepoAlias: "cxstatusline",
		RepoURL:   "https://github.com/adrijshikhar/cxstatusline",
		Owner:     "adrijshikhar",
		Status:    monitor.StatusRunning,
		StartedAt: now.Add(-40 * time.Second),
		Steps: []*monitor.StepInfo{
			{ID: "s-1", Name: "Verify prerequisites", Status: monitor.StatusSuccess, StartedAt: now.Add(-40 * time.Second), EndedAt: now.Add(-30 * time.Second)},
			{ID: "s-2", Name: "Compile binaries", Status: monitor.StatusRunning, StartedAt: now.Add(-30 * time.Second)},
			{ID: "s-3", Name: "Package distribution tarballs", Status: monitor.StatusQueued},
			{ID: "s-4", Name: "Generate SHA256 checksums", Status: monitor.StatusQueued},
			{ID: "s-5", Name: "Legacy artifact sign", Status: monitor.StatusSkipped},
			{ID: "s-6", Name: "Publish GitHub Release", Status: monitor.StatusQueued},
		},
	}
	server.Publish(monitor.Event{Type: monitor.EventJobStart, JobID: liveJob.ID, RepoAlias: "cxstatusline", Job: liveJob})

	server.AppendJobStepLog(liveJob.ID, "s-1", "\x1b[38;5;244m2026-11-16T14:34:00Z\x1b[0m \x1b[1;34m##[group]Check environment\x1b[0m")
	server.AppendJobStepLog(liveJob.ID, "s-1", "\x1b[38;5;244m2026-11-16T14:34:01Z\x1b[0m Verified Go version: 1.27.4 darwin/arm64")
	server.AppendJobStepLog(liveJob.ID, "s-1", "\x1b[38;5;244m2026-11-16T14:34:05Z\x1b[0m \x1b[1;32m✓\x1b[0m Toolchain dependencies verified")
	server.AppendJobStepLog(liveJob.ID, "s-1", "\x1b[38;5;244m2026-11-16T14:34:10Z\x1b[0m \x1b[1;34m##[endgroup]\x1b[0m")

	initialCompileLogs := []string{
		"\x1b[38;5;244m2026-11-16T14:34:11Z\x1b[0m \x1b[1;34m##[group]Compiling static binaries (CGO_ENABLED=0)\x1b[0m",
		"\x1b[38;5;244m2026-11-16T14:34:12Z\x1b[0m \x1b[38;5;39m[1/8]\x1b[0m compiling internal/config...",
		"\x1b[38;5;244m2026-11-16T14:34:13Z\x1b[0m \x1b[38;5;39m[2/8]\x1b[0m compiling internal/driver/host...",
		"\x1b[38;5;244m2026-11-16T14:34:14Z\x1b[0m \x1b[38;5;39m[3/8]\x1b[0m compiling internal/engine...",
		"\x1b[38;5;244m2026-11-16T14:34:15Z\x1b[0m \x1b[38;5;39m[4/8]\x1b[0m compiling internal/monitor...",
		"\x1b[38;5;244m2026-11-16T14:34:16Z\x1b[0m \x1b[38;5;39m[5/8]\x1b[0m compiling internal/protocol...",
		"\x1b[38;5;244m2026-11-16T14:34:17Z\x1b[0m \x1b[38;5;39m[6/8]\x1b[0m compiling internal/tui...",
	}
	for _, l := range initialCompileLogs {
		server.AppendJobStepLog(liveJob.ID, "s-2", l)
	}

	stepStartTime := now.Add(-30 * time.Second)
	currPID := int32(os.Getpid()) // #nosec G115 -- PID fits in int32
	server.SetTelemetry(&monitor.TelemetrySample{
		Timestamp:           time.Now(),
		PID:                 currPID,
		PGID:                currPID,
		HostCPU:             15.4,
		StepCPU:             48.2,
		StepRSSBytes:        184 * 1024 * 1024,
		StepWorkingSetBytes: 210 * 1024 * 1024,
		ThreadCount:         18,
		HostMemUsed:         16 * 1024 * 1024 * 1024,
		HostMemTotal:        36 * 1024 * 1024 * 1024,
		TraceID:             "4bf92f3577b34da6a3ce929d0e0e4736",
		DiskTotalBytes:      250 * 1024 * 1024 * 1024,
		DiskUsedBytes:       18 * 1024 * 1024 * 1024,
		DiskFreeBytes:       142 * 1024 * 1024 * 1024,
		DiskUsedRatio:       0.14,
		DiskInodesFree:      42000000,
		NetRxRate:           14 * 1024 * 1024,
		NetTxRate:           2 * 1024 * 1024,
		DiskWriteRate:       18 * 1024 * 1024,
		HostLoad1m:          1.42,
		ActiveStepName:      "Compile binaries",
		StepElapsedSec:      int64(time.Since(stepStartTime).Seconds()),
	})
}

func runSimulation(ctx context.Context, server *monitor.Server) {
	ticker := time.NewTicker(650 * time.Millisecond)
	defer ticker.Stop()

	currPID := int32(os.Getpid()) // #nosec G115
	idx := 0

	cpuJitter := []float64{48.2, 53.7, 44.1, 62.8, 51.0, 39.5, 57.3, 49.8}
	memJitterMB := []uint64{184, 189, 195, 204, 212, 218, 225, 231, 238, 245}

	compileSequence := []string{
		"\x1b[38;5;244m2026-11-16T14:34:18Z\x1b[0m \x1b[38;5;39m[7/8]\x1b[0m compiling cmd/actr...",
		"\x1b[38;5;244m2026-11-16T14:34:19Z\x1b[0m \x1b[38;5;39m[8/8]\x1b[0m linking bin/actr (static executable)...",
		"\x1b[38;5;244m2026-11-16T14:34:20Z\x1b[0m \x1b[1;32m✓\x1b[0m Binary artifact created: 18.4 MB (arm64)",
		"\x1b[38;5;244m2026-11-16T14:34:21Z\x1b[0m \x1b[1;34m##[endgroup]\x1b[0m",
		"\x1b[38;5;244m2026-11-16T14:34:22Z\x1b[0m \x1b[1;36mℹ Notice:\x1b[0m Starting verification test suite (-race -count=1)",
		"\x1b[38;5;244m2026-11-16T14:34:23Z\x1b[0m === RUN   TestCoordinator_ExecuteJob_Success",
		"\x1b[38;5;244m2026-11-16T14:34:23Z\x1b[0m --- PASS: TestCoordinator_ExecuteJob_Success (0.08s)",
		"\x1b[38;5;244m2026-11-16T14:34:24Z\x1b[0m === RUN   TestServer_Publish_NDJSONStreaming",
		"\x1b[38;5;244m2026-11-16T14:34:24Z\x1b[0m --- PASS: TestServer_Publish_NDJSONStreaming (0.04s)",
		"\x1b[38;5;244m2026-11-16T14:34:25Z\x1b[0m === RUN   TestTelemetry_GradientProgressBars",
		"\x1b[38;5;244m2026-11-16T14:34:25Z\x1b[0m --- PASS: TestTelemetry_GradientProgressBars (0.01s)",
		"\x1b[38;5;244m2026-11-16T14:34:26Z\x1b[0m \x1b[1;32m✓ Test suite passed: 100% tests successful\x1b[0m",
	}

	packageSequence := []string{
		"\x1b[38;5;244m2026-11-16T14:34:27Z\x1b[0m \x1b[1;34m##[group]Compressing distribution packages\x1b[0m",
		"\x1b[38;5;244m2026-11-16T14:34:28Z\x1b[0m Creating actr_v0.1.2_darwin_arm64.tar.gz...",
		"\x1b[38;5;244m2026-11-16T14:34:29Z\x1b[0m Adding LICENSE, README.md, bin/actr...",
		"\x1b[38;5;244m2026-11-16T14:34:30Z\x1b[0m \x1b[1;32m✓\x1b[0m Package archived: 6.8 MB compressed",
		"\x1b[38;5;244m2026-11-16T14:34:31Z\x1b[0m Creating actr_v0.1.2_linux_x86_64.tar.gz...",
		"\x1b[38;5;244m2026-11-16T14:34:32Z\x1b[0m \x1b[1;32m✓\x1b[0m Package archived: 7.1 MB compressed",
		"\x1b[38;5;244m2026-11-16T14:34:33Z\x1b[0m \x1b[1;34m##[endgroup]\x1b[0m",
	}

	checksumSequence := []string{
		"\x1b[38;5;244m2026-11-16T14:34:34Z\x1b[0m Computing cryptographic digest sha256...",
		"\x1b[38;5;244m2026-11-16T14:34:35Z\x1b[0m 8f4a13c9e782b540... actr_v0.1.2_darwin_arm64.tar.gz",
		"\x1b[38;5;244m2026-11-16T14:34:36Z\x1b[0m d93e2b5c7104f681... actr_v0.1.2_linux_x86_64.tar.gz",
		"\x1b[38;5;244m2026-11-16T14:34:37Z\x1b[0m \x1b[1;32m✓\x1b[0m checksums.txt written and verified",
	}

	publishSequence := []string{
		"\x1b[38;5;244m2026-11-16T14:34:38Z\x1b[0m \x1b[1;34m##[group]Deploying to GitHub Releases\x1b[0m",
		"\x1b[38;5;244m2026-11-16T14:34:39Z\x1b[0m Uploading actr_v0.1.2_darwin_arm64.tar.gz (100%)...",
		"\x1b[38;5;244m2026-11-16T14:34:40Z\x1b[0m Uploading actr_v0.1.2_linux_x86_64.tar.gz (100%)...",
		"\x1b[38;5;244m2026-11-16T14:34:41Z\x1b[0m Uploading checksums.txt (100%)...",
		"\x1b[38;5;244m2026-11-16T14:34:42Z\x1b[0m \x1b[1;32m✓\x1b[0m Release published: https://github.com/adrijshikhar/actr/releases/tag/v0.1.2",
		"\x1b[38;5;244m2026-11-16T14:34:43Z\x1b[0m \x1b[1;34m##[endgroup]\x1b[0m",
	}

	activeStep := "s-2"
	activeStepName := "Compile binaries"
	stepStartTime := time.Now().Add(-30 * time.Second)
	currentSeq := compileSequence
	seqIdx := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 1. Update Telemetry with realistic jitter
			var netRx, netTx uint64
			switch activeStep {
			case "s-6": // Publish GitHub Release (uploading artifacts)
				netRx = 1*1024*1024 + uint64(idx%3)*256*1024
				netTx = 18*1024*1024 + uint64(idx%7)*1024*1024
			case "s-3":
				netRx = 800 * 1024
				netTx = 400 * 1024
			case "s-4":
				netRx = 256 * 1024
				netTx = 128 * 1024
			default: // s-2 Compile binaries / download dependencies
				netRx = 14*1024*1024 + uint64(idx%5)*512*1024
				netTx = 2*1024*1024 + uint64(idx%3)*256*1024
			}

			server.SetTelemetry(&monitor.TelemetrySample{
				Timestamp:           time.Now(),
				PID:                 currPID,
				PGID:                currPID,
				HostCPU:             14.0 + float64(idx%6)*0.7,
				StepCPU:             cpuJitter[idx%len(cpuJitter)],
				StepRSSBytes:        memJitterMB[idx%len(memJitterMB)] * 1024 * 1024,
				StepWorkingSetBytes: (memJitterMB[idx%len(memJitterMB)] + 25) * 1024 * 1024,
				ThreadCount:         16 + (idx % 4),
				HostMemUsed:         16 * 1024 * 1024 * 1024,
				HostMemTotal:        36 * 1024 * 1024 * 1024,
				TraceID:             "4bf92f3577b34da6a3ce929d0e0e4736",
				DiskTotalBytes:      250 * 1024 * 1024 * 1024,
				DiskUsedBytes:       18 * 1024 * 1024 * 1024,
				DiskFreeBytes:       142 * 1024 * 1024 * 1024,
				DiskUsedRatio:       0.14,
				DiskInodesFree:      42000000,
				NetRxRate:           netRx,
				NetTxRate:           netTx,
				DiskWriteRate:       18*1024*1024 + uint64(idx%7)*1024*1024,
				HostLoad1m:          1.42 + float64(idx%3)*0.02,
				ActiveStepName:      activeStepName,
				StepElapsedSec:      int64(time.Since(stepStartTime).Seconds()),
			})

			// 2. Stream log line into active step
			if seqIdx < len(currentSeq) {
				server.AppendJobStepLog("job-live-1", activeStep, currentSeq[seqIdx])
				seqIdx++
			} else {
				// Transition to next step
				switch activeStep {
				case "s-2":
					server.Publish(monitor.Event{
						Type:      monitor.EventStepFinish,
						JobID:     "job-live-1",
						RepoAlias: "cxstatusline",
						StepID:    "s-2",
						Step:      &monitor.StepInfo{ID: "s-2", Name: "Compile binaries", Status: monitor.StatusSuccess, EndedAt: time.Now()},
					})
					activeStep = "s-3"
					activeStepName = "Package distribution tarballs"
					stepStartTime = time.Now()
					currentSeq = packageSequence
					seqIdx = 0
					server.Publish(monitor.Event{
						Type:      monitor.EventStepStart,
						JobID:     "job-live-1",
						RepoAlias: "cxstatusline",
						StepID:    "s-3",
						Step:      &monitor.StepInfo{ID: "s-3", Name: "Package distribution tarballs", Status: monitor.StatusRunning, StartedAt: time.Now()},
					})

				case "s-3":
					server.Publish(monitor.Event{
						Type:      monitor.EventStepFinish,
						JobID:     "job-live-1",
						RepoAlias: "cxstatusline",
						StepID:    "s-3",
						Step:      &monitor.StepInfo{ID: "s-3", Name: "Package distribution tarballs", Status: monitor.StatusSuccess, EndedAt: time.Now()},
					})
					activeStep = "s-4"
					activeStepName = "Generate SHA256 checksums"
					stepStartTime = time.Now()
					currentSeq = checksumSequence
					seqIdx = 0
					server.Publish(monitor.Event{
						Type:      monitor.EventStepStart,
						JobID:     "job-live-1",
						RepoAlias: "cxstatusline",
						StepID:    "s-4",
						Step:      &monitor.StepInfo{ID: "s-4", Name: "Generate SHA256 checksums", Status: monitor.StatusRunning, StartedAt: time.Now()},
					})

				case "s-4":
					server.Publish(monitor.Event{
						Type:      monitor.EventStepFinish,
						JobID:     "job-live-1",
						RepoAlias: "cxstatusline",
						StepID:    "s-4",
						Step:      &monitor.StepInfo{ID: "s-4", Name: "Generate SHA256 checksums", Status: monitor.StatusSuccess, EndedAt: time.Now()},
					})
					// Note: s-5 was skipped, transition directly to s-6
					activeStep = "s-6"
					activeStepName = "Publish GitHub Release"
					stepStartTime = time.Now()
					currentSeq = publishSequence
					seqIdx = 0
					server.Publish(monitor.Event{
						Type:      monitor.EventStepStart,
						JobID:     "job-live-1",
						RepoAlias: "cxstatusline",
						StepID:    "s-6",
						Step:      &monitor.StepInfo{ID: "s-6", Name: "Publish GitHub Release", Status: monitor.StatusRunning, StartedAt: time.Now()},
					})

				case "s-6":
					server.Publish(monitor.Event{
						Type:      monitor.EventStepFinish,
						JobID:     "job-live-1",
						RepoAlias: "cxstatusline",
						StepID:    "s-6",
						Step:      &monitor.StepInfo{ID: "s-6", Name: "Publish GitHub Release", Status: monitor.StatusSuccess, EndedAt: time.Now()},
					})
					server.Publish(monitor.Event{
						Type:      monitor.EventJobFinish,
						JobID:     "job-live-1",
						RepoAlias: "cxstatusline",
						Job:       &monitor.JobInfo{ID: "job-live-1", Name: "release-prebuilt", Repo: "cxstatusline", RepoAlias: "cxstatusline", Status: monitor.StatusSuccess, EndedAt: time.Now()},
					})
					server.SetSupervisorStatus(&monitor.SupervisorStatus{
						GlobalMaxJobs: 2,
						GlobalActive:  0,
						RepoSlots: []*monitor.RepoSlotInfo{
							{Alias: "aim", MaxJobs: 1, ActiveJobs: 0, Driver: "host", Status: "idle"},
							{Alias: "cxstatusline", MaxJobs: 1, ActiveJobs: 0, Driver: "host", Status: "idle"},
						},
					})

					// Idle for a few cycles then cycle back or stream heartbeat
					select {
					case <-ctx.Done():
						return
					case <-time.After(3 * time.Second):
					}
					// Start next simulated live run
					activeStep = "s-2"
					activeStepName = "Compile binaries"
					stepStartTime = time.Now()
					currentSeq = compileSequence
					seqIdx = 0
					server.SetSupervisorStatus(&monitor.SupervisorStatus{
						GlobalMaxJobs: 2,
						GlobalActive:  1,
						RepoSlots: []*monitor.RepoSlotInfo{
							{Alias: "aim", MaxJobs: 1, ActiveJobs: 0, Driver: "host", Status: "idle"},
							{Alias: "cxstatusline", MaxJobs: 1, ActiveJobs: 1, Driver: "host", Status: "busy"},
						},
					})
					server.Publish(monitor.Event{
						Type:      monitor.EventJobStart,
						JobID:     "job-live-1",
						RepoAlias: "cxstatusline",
						Job: &monitor.JobInfo{
							ID:        "job-live-1",
							Name:      "release-prebuilt",
							Workflow:  "Release (v0.1.2)",
							Repo:      "cxstatusline",
							RepoAlias: "cxstatusline",
							Owner:     "adrijshikhar",
							Status:    monitor.StatusRunning,
							StartedAt: time.Now(),
							Steps: []*monitor.StepInfo{
								{ID: "s-1", Name: "Verify prerequisites", Status: monitor.StatusSuccess, StartedAt: time.Now().Add(-10 * time.Second), EndedAt: time.Now()},
								{ID: "s-2", Name: "Compile binaries", Status: monitor.StatusRunning, StartedAt: time.Now()},
								{ID: "s-3", Name: "Package distribution tarballs", Status: monitor.StatusQueued},
								{ID: "s-4", Name: "Generate SHA256 checksums", Status: monitor.StatusQueued},
								{ID: "s-5", Name: "Legacy artifact sign", Status: monitor.StatusSkipped},
								{ID: "s-6", Name: "Publish GitHub Release", Status: monitor.StatusQueued},
							},
						},
					})
					server.Publish(monitor.Event{
						Type:      monitor.EventStepStart,
						JobID:     "job-live-1",
						RepoAlias: "cxstatusline",
						StepID:    "s-2",
						Step:      &monitor.StepInfo{ID: "s-2", Name: "Compile binaries", Status: monitor.StatusRunning, StartedAt: time.Now()},
					})
					server.AppendJobStepLog("job-live-1", "s-1", "\x1b[38;5;244m2026-11-16T14:34:00Z\x1b[0m \x1b[1;34m##[group]Check environment\x1b[0m")
					server.AppendJobStepLog("job-live-1", "s-1", "\x1b[38;5;244m2026-11-16T14:34:01Z\x1b[0m Verified Go version: 1.27.4 darwin/arm64")
					server.AppendJobStepLog("job-live-1", "s-1", "\x1b[38;5;244m2026-11-16T14:34:05Z\x1b[0m \x1b[1;32m✓\x1b[0m Toolchain dependencies verified")
					server.AppendJobStepLog("job-live-1", "s-1", "\x1b[38;5;244m2026-11-16T14:34:10Z\x1b[0m \x1b[1;34m##[endgroup]\x1b[0m")
				}
			}

			idx++
		}
	}
}
