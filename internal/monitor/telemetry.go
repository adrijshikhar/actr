package monitor

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

type containerMetrics struct {
	cpu    float64
	rss    uint64
	procs  int
	rxRate uint64
	txRate uint64
	readAt time.Time
}

type TelemetrySampler struct {
	mu           sync.RWMutex
	sampleMu     sync.Mutex
	pid          int32
	pgid         int32
	workspaceDir string
	traceID      string
	spanID       string
	stepName     string
	stepStarted  time.Time

	// Docker container targeting
	containerID     string
	dockerCli       DockerStatsClient
	containerCancel context.CancelFunc
	cStatsMu        sync.RWMutex
	containerStats  *containerMetrics

	lastNetTime  time.Time
	lastNetRx    uint64
	lastNetTx    uint64
	lastDiskTime time.Time
	lastDiskWr   uint64
}

func NewTelemetrySampler() *TelemetrySampler {
	return &TelemetrySampler{}
}

func (s *TelemetrySampler) SetTargetPID(pid int32, pgid int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pid = pid
	s.pgid = pgid
}

func (s *TelemetrySampler) SetTargetContainer(containerID string, cli DockerStatsClient) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.containerCancel != nil {
		s.containerCancel()
		s.containerCancel = nil
	}

	s.containerID = containerID
	s.dockerCli = cli

	s.cStatsMu.Lock()
	s.containerStats = nil
	s.cStatsMu.Unlock()

	if containerID == "" || cli == nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.containerCancel = cancel

	go s.streamContainerStats(ctx, containerID, cli)
}

func (s *TelemetrySampler) streamContainerStats(ctx context.Context, containerID string, cli DockerStatsClient) {
	resp, err := cli.ContainerStats(ctx, containerID, true)
	if err != nil {
		return
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = resp.Body.Close()
		case <-done:
		}
	}()

	dec := json.NewDecoder(resp.Body)
	var prevCPU, prevSystem uint64
	var prevRx, prevTx uint64
	var lastNetTime time.Time

	for {
		var v container.StatsResponse
		if err := dec.Decode(&v); err != nil {
			return
		}

		sampleTime := v.Read
		if sampleTime.IsZero() {
			sampleTime = time.Now()
		}

		cpu := CalculateDockerCPUPercent(&v, prevCPU, prevSystem)
		if v.CPUStats.CPUUsage.TotalUsage > 0 {
			prevCPU = v.CPUStats.CPUUsage.TotalUsage
		}
		if v.CPUStats.SystemUsage > 0 {
			prevSystem = v.CPUStats.SystemUsage
		}

		rss := CalculateDockerMemoryRSS(v.MemoryStats)
		rx, tx := CalculateDockerNetworkIO(v.Networks)

		var rxRate, txRate uint64
		if !lastNetTime.IsZero() && sampleTime.After(lastNetTime) {
			dur := sampleTime.Sub(lastNetTime).Seconds()
			if dur > 0 {
				if rx >= prevRx {
					rxRate = uint64(float64(rx-prevRx) / dur)
				}
				if tx >= prevTx {
					txRate = uint64(float64(tx-prevTx) / dur)
				}
			}
		}
		lastNetTime = sampleTime
		prevRx = rx
		prevTx = tx

		procs := int(v.PidsStats.Current)
		if procs <= 0 && v.NumProcs > 0 {
			procs = int(v.NumProcs)
		}
		if procs <= 0 {
			procs = 1
		}

		s.cStatsMu.Lock()
		s.containerStats = &containerMetrics{
			cpu:    cpu,
			rss:    rss,
			procs:  procs,
			rxRate: rxRate,
			txRate: txRate,
			readAt: sampleTime,
		}
		s.cStatsMu.Unlock()
	}
}

func (s *TelemetrySampler) Close() {
	s.SetTargetContainer("", nil)
}

func (s *TelemetrySampler) SetWorkspaceDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaceDir = dir
}

func (s *TelemetrySampler) SetTraceContext(traceID, spanID, stepName string, startedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traceID = traceID
	s.spanID = spanID
	s.stepName = stepName
	s.stepStarted = startedAt
}

func (s *TelemetrySampler) Sample() (sample *RunnerMetrics) {
	defer func() {
		_ = recover() // Protect against panics in third-party metric collectors (e.g. gopsutil net/disk parsing on Darwin)
	}()

	s.sampleMu.Lock()
	defer s.sampleMu.Unlock()

	s.mu.RLock()
	targetPID := s.pid
	targetPGID := s.pgid
	workspaceDir := s.workspaceDir
	traceID := s.traceID
	spanID := s.spanID
	stepName := s.stepName
	stepStarted := s.stepStarted
	targetContainerID := s.containerID
	s.mu.RUnlock()

	now := time.Now()
	sample = &RunnerMetrics{
		Timestamp:      now,
		PID:            targetPID,
		PGID:           targetPGID,
		TraceID:        traceID,
		SpanID:         spanID,
		ActiveStepName: stepName,
	}

	if !stepStarted.IsZero() {
		elapsed := int64(now.Sub(stepStarted).Seconds())
		if elapsed < 0 {
			elapsed = 0
		}
		sample.StepElapsedSec = elapsed
	}

	// Host CPU & Memory
	if cpuPercents, err := cpu.Percent(0, false); err == nil && len(cpuPercents) > 0 {
		sample.HostCPU = cpuPercents[0]
	}
	if vMem, err := mem.VirtualMemory(); err == nil && vMem != nil {
		sample.HostMemUsed = vMem.Used
		sample.HostMemTotal = vMem.Total
	}

	// Host Load Average
	if avg, err := load.Avg(); err == nil && avg != nil {
		sample.HostLoad1m = avg.Load1
	}

	// Step Process CPU & Memory
	if targetContainerID != "" {
		s.cStatsMu.RLock()
		cStats := s.containerStats
		s.cStatsMu.RUnlock()

		if cStats != nil {
			sample.StepCPU = cStats.cpu
			sample.StepRSSBytes = cStats.rss
			sample.ProcessCount = cStats.procs
			sample.NetRxRate = cStats.rxRate
			sample.NetTxRate = cStats.txRate
		} else {
			sample.ProcessCount = 1
		}
	} else if targetPID > 0 {
		if p, err := process.NewProcess(targetPID); err == nil {
			sample.ProcessCount = 1
			if cpuPercent, err := p.CPUPercent(); err == nil {
				sample.StepCPU = cpuPercent
			}
			if memInfo, err := p.MemoryInfo(); err == nil && memInfo != nil {
				sample.StepRSSBytes = memInfo.RSS
			}

			// Aggregate child processes in the process group
			if children, err := p.Children(); err == nil {
				sample.ProcessCount += len(children)
				for _, ch := range children {
					if cCPU, err := ch.CPUPercent(); err == nil {
						sample.StepCPU += cCPU
					}
					if cMem, err := ch.MemoryInfo(); err == nil && cMem != nil {
						sample.StepRSSBytes += cMem.RSS
					}
				}
			}
		}
	}

	// Workspace Ephemeral Storage
	targetDir := workspaceDir
	if targetDir == "" {
		targetDir = "."
	}
	if usage, err := disk.Usage(targetDir); err == nil && usage != nil {
		sample.DiskUsedBytes = usage.Used
		sample.DiskFreeBytes = usage.Free
		sample.DiskTotalBytes = usage.Total
		if usage.Total > 0 {
			sample.DiskUsedRatio = float64(usage.Used) / float64(usage.Total)
		}
		if usage.InodesFree > 100_000_000_000 {
			sample.DiskInodesFree = 0
		} else {
			sample.DiskInodesFree = usage.InodesFree
		}
	}

	// Disk Write Rate
	func() {
		defer func() { _ = recover() }()
		if diskCounters, err := disk.IOCounters(); err == nil {
			var totalWriteBytes uint64
			for _, c := range diskCounters {
				totalWriteBytes += c.WriteBytes
			}
			if !s.lastDiskTime.IsZero() && now.After(s.lastDiskTime) {
				durationSec := now.Sub(s.lastDiskTime).Seconds()
				if durationSec > 0 && totalWriteBytes >= s.lastDiskWr {
					sample.DiskWriteRate = uint64(float64(totalWriteBytes-s.lastDiskWr) / durationSec)
				}
			}
			s.lastDiskTime = now
			s.lastDiskWr = totalWriteBytes
		}
	}()

	// Network I/O Rate (host fallback when no container targeted)
	if targetContainerID == "" {
		func() {
			defer func() { _ = recover() }()
			if netCounters, err := net.IOCounters(false); err == nil && len(netCounters) > 0 {
				rx := netCounters[0].BytesRecv
				tx := netCounters[0].BytesSent
				if !s.lastNetTime.IsZero() && now.After(s.lastNetTime) {
					durationSec := now.Sub(s.lastNetTime).Seconds()
					if durationSec > 0 {
						if rx >= s.lastNetRx {
							sample.NetRxRate = uint64(float64(rx-s.lastNetRx) / durationSec)
						}
						if tx >= s.lastNetTx {
							sample.NetTxRate = uint64(float64(tx-s.lastNetTx) / durationSec)
						}
					}
				}
				s.lastNetTime = now
				s.lastNetRx = rx
				s.lastNetTx = tx
			}
		}()
	}

	return sample
}
