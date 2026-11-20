package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
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
	} else if targetPID > 0 || targetPGID > 0 {
		sampled := false
		if (runtime.GOOS == "darwin" || runtime.GOOS == "linux") && targetPGID > 0 {
			cpuVal, rssVal, count, err := sampleProcessGroup(targetPGID)
			if err == nil && count > 0 {
				sample.StepCPU = cpuVal
				sample.StepRSSBytes = rssVal
				sample.ProcessCount = count
				sampled = true
			}
		}

		if !sampled && targetPID > 0 {
			if p, err := process.NewProcess(targetPID); err == nil {
				sample.ProcessCount = 1
				if cpuPercent, err := p.CPUPercent(); err == nil {
					sample.StepCPU = cpuPercent
				}
				if memInfo, err := p.MemoryInfo(); err == nil && memInfo != nil {
					sample.StepRSSBytes = memInfo.RSS
				}

				// Aggregate all descendant processes recursively
				descendants := getAllDescendants(p)
				sample.ProcessCount += len(descendants)
				for _, ch := range descendants {
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

	return sample
}

// sampleProcessGroup queries ps to aggregate CPU%, RSS bytes, and process count
// for all processes in the specified process group (PGID).
func sampleProcessGroup(pgid int32) (cpu float64, rss uint64, count int, err error) {
	if pgid <= 0 {
		return 0, 0, 0, errors.New("invalid pgid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ps", "-eo", "pid,pgid,%cpu,rss") // #nosec G204 -- fixed args
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) <= 1 {
		return 0, 0, 0, nil
	}
	pgidStr := strconv.Itoa(int(pgid))
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[1] != pgidStr {
			continue
		}
		cVal, errC := strconv.ParseFloat(fields[2], 64)
		rssKB, errM := strconv.ParseUint(fields[3], 10, 64)
		if errC == nil && errM == nil {
			cpu += cVal
			rss += rssKB * 1024
			count++
		}
	}
	return cpu, rss, count, nil
}

// getAllDescendants recursively gathers all descendant processes of p.
func getAllDescendants(p *process.Process) []*process.Process {
	if p == nil {
		return nil
	}
	var all []*process.Process
	children, err := p.Children()
	if err != nil || len(children) == 0 {
		return all
	}
	for _, ch := range children {
		all = append(all, ch)
		all = append(all, getAllDescendants(ch)...)
	}
	return all
}
