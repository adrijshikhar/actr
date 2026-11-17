package host

import (
	"context"
	"sync"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/shirou/gopsutil/v4/process"
)

// HostLimiter applies resource governance limits to host processes and tracks OOM events.
type HostLimiter interface {
	Apply(ctx context.Context, pid int, limits *config.LimitsConfig) (cleanup func(), err error)
	IsOOMKilled(pid int) bool
}

// NewHostLimiter returns a platform-appropriate HostLimiter implementation.
func NewHostLimiter() HostLimiter {
	return newPlatformLimiter()
}

// BaseLimiter provides thread-safe OOM status tracking across platforms.
type BaseLimiter struct {
	oomPids sync.Map
}

// IsOOMKilled reports whether the given pid was terminated due to an OOM condition.
func (b *BaseLimiter) IsOOMKilled(pid int) bool {
	v, ok := b.oomPids.Load(pid)
	return ok && v.(bool)
}

// MarkOOMKilled records that the given pid was terminated due to an OOM condition.
func (b *BaseLimiter) MarkOOMKilled(pid int) {
	b.oomPids.Store(pid, true)
}

// ClearPID removes tracking state for the given pid.
func (b *BaseLimiter) ClearPID(pid int) {
	b.oomPids.Delete(pid)
}

// processWatchdog periodically inspects process memory and terminates it with SIGKILL if limits are breached.
type processWatchdog struct {
	pid          int
	memoryLimit  int64
	pollInterval time.Duration
	stopCh       chan struct{}
	doneCh       chan struct{}
	once         sync.Once
	onOOM        func(pid int)
	rssChecker   func(pid int) (uint64, error)
	killFn       func(pid int)
}

func newProcessWatchdog(pid int, memoryLimit int64, pollInterval time.Duration, onOOM func(pid int)) *processWatchdog {
	if pollInterval <= 0 {
		pollInterval = 100 * time.Millisecond
	}
	return &processWatchdog{
		pid:          pid,
		memoryLimit:  memoryLimit,
		pollInterval: pollInterval,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		onOOM:        onOOM,
		rssChecker:   getProcessTreeRSS,
		killFn:       killProcessGroupOrPID,
	}
}

func (w *processWatchdog) Start() {
	go w.run()
}

func (w *processWatchdog) Stop() {
	w.once.Do(func() {
		close(w.stopCh)
		<-w.doneCh
	})
}

func (w *processWatchdog) run() {
	defer close(w.doneCh)
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	checker := w.rssChecker
	if checker == nil {
		checker = getProcessTreeRSS
	}
	killer := w.killFn
	if killer == nil {
		killer = killProcessGroupOrPID
	}

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			rss, err := checker(w.pid)
			if err != nil {
				// Process might have exited or is not inspectable
				return
			}
			if w.memoryLimit > 0 && rss > uint64(w.memoryLimit) {
				if w.onOOM != nil {
					w.onOOM(w.pid)
				}
				killer(w.pid)
				return
			}
		}
	}
}

func getProcessTreeRSS(pid int) (uint64, error) {
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return 0, err
	}
	memInfo, err := p.MemoryInfo()
	if err != nil {
		return 0, err
	}
	totalRSS := memInfo.RSS

	children, err := p.Children()
	if err == nil {
		for _, ch := range children {
			if cMem, err := ch.MemoryInfo(); err == nil && cMem != nil {
				totalRSS += cMem.RSS
			}
		}
	}
	return totalRSS, nil
}
