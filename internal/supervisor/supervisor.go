package supervisor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/logger"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/telemetry"
)

// Supervisor manages multi-tenant repository workers, global resource governance,
// IPC monitor server lifecycle, and dynamic configuration hot-reloading.
type Supervisor struct {
	mu            sync.RWMutex
	cfg           *config.SupervisorConfig
	sockPath      string
	resMgr        *ResourceManager
	monServer     *monitor.Server
	tracer        *telemetry.Manager
	workers       map[string]*RepoWorker
	pollingCtx    context.Context
	pollingCancel context.CancelFunc
	runCtx        context.Context
	runCancel     context.CancelFunc
	running       bool
}

// NewSupervisor constructs a new multi-tenant Supervisor.
func NewSupervisor(cfg *config.SupervisorConfig, sockPath string) (*Supervisor, error) {
	if cfg == nil {
		return nil, fmt.Errorf("supervisor config cannot be nil")
	}

	resMgr, err := NewResourceManager(cfg.Global.MaxConcurrentJobs)
	if err != nil {
		return nil, fmt.Errorf("create resource manager: %w", err)
	}

	var monServer *monitor.Server
	if sockPath != "" {
		monServer = monitor.NewServer(sockPath, "actr • multi-repo")
	}

	workers := make(map[string]*RepoWorker, len(cfg.Repos))
	for alias, repoCfg := range cfg.Repos {
		w, err := NewRepoWorker(alias, repoCfg, resMgr, monServer)
		if err != nil {
			return nil, fmt.Errorf("create worker for %q: %w", alias, err)
		}
		workers[alias] = w
	}

	sup := &Supervisor{
		cfg:       cfg,
		sockPath:  sockPath,
		resMgr:    resMgr,
		monServer: monServer,
		workers:   workers,
	}
	sup.updateMonitorStatus()
	return sup, nil
}

// WithTracer attaches a shared OpenTelemetry Manager to the Supervisor and propagates it to all workers.
func (s *Supervisor) WithTracer(tm *telemetry.Manager) *Supervisor {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracer = tm
	for _, w := range s.workers {
		w.WithTracer(tm)
	}
	return s
}

// ResourceManager returns the global resource governance manager.
func (s *Supervisor) ResourceManager() *ResourceManager {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resMgr
}

// MonitorServer returns the IPC monitor server, if configured.
func (s *Supervisor) MonitorServer() *monitor.Server {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.monServer
}

// Workers returns a shallow copy of the active repository workers.
func (s *Supervisor) Workers() map[string]*RepoWorker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make(map[string]*RepoWorker, len(s.workers))
	for k, v := range s.workers {
		res[k] = v
	}
	return res
}

// Config returns the current supervisor configuration.
func (s *Supervisor) Config() *config.SupervisorConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Start boots the IPC monitor server and repository workers, awaiting context cancellation
// and performing an orderly, graceful drain before returning.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("supervisor is already running")
	}
	s.running = true

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	s.runCtx = runCtx
	s.runCancel = runCancel

	pollingCtx, pollingCancel := context.WithCancel(runCtx)
	s.pollingCtx = pollingCtx
	s.pollingCancel = pollingCancel

	if s.monServer != nil {
		if err := s.monServer.Start(); err != nil {
			logger.Warn("failed to start monitor server", "error", err)
		}
	}

	for alias, w := range s.workers {
		if err := w.Start(runCtx, pollingCtx); err != nil {
			logger.Error("failed to start worker", "repo", alias, "error", err)
		}
	}
	s.mu.Unlock()

	s.updateMonitorStatus()

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				s.updateMonitorStatus()
			}
		}
	}()

	<-runCtx.Done()

	// 1. Immediately halt broker message polling
	pollingCancel()

	// 2. Concurrently drain all workers with a 30-second deadline
	drainCtx, drainCancel := context.WithTimeout(context.WithoutCancel(runCtx), 30*time.Second)
	defer drainCancel()

	s.mu.RLock()
	workersToDrain := make([]*RepoWorker, 0, len(s.workers))
	for _, w := range s.workers {
		workersToDrain = append(workersToDrain, w)
	}
	s.mu.RUnlock()

	var wg sync.WaitGroup
	for _, w := range workersToDrain {
		wg.Add(1)
		go func(worker *RepoWorker) {
			defer wg.Done()
			if err := worker.Stop(drainCtx); err != nil {
				logger.Warn("worker stop encountered error or timed out", "repo", worker.repoAlias, "error", err)
			}
		}(w)
	}
	wg.Wait()

	// 3. Stop monitor server
	if s.monServer != nil {
		_ = s.monServer.Stop()
	}

	s.mu.Lock()
	s.running = false
	s.mu.Unlock()

	return nil
}

// Stop initiates graceful shutdown of the supervisor.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.runCancel == nil {
		return nil
	}
	s.runCancel()
	return nil
}

// Reload dynamically updates the supervisor configuration: adjusting global concurrency,
// starting workers for added repositories, and gracefully draining workers for removed repositories.
func (s *Supervisor) Reload(newCfg *config.SupervisorConfig) error {
	if newCfg == nil {
		return fmt.Errorf("nil config on reload")
	}

	s.mu.Lock()
	if s.running && s.runCtx != nil && s.runCtx.Err() != nil {
		s.mu.Unlock()
		return fmt.Errorf("cannot reload supervisor while shutting down")
	}

	// Pre-instantiate workers for added repositories to fail early before mutations
	addedWorkers := make(map[string]*RepoWorker)
	for alias, repoCfg := range newCfg.Repos {
		if _, exists := s.workers[alias]; !exists {
			w, err := NewRepoWorker(alias, repoCfg, s.resMgr, s.monServer)
			if err != nil {
				s.mu.Unlock()
				return fmt.Errorf("create worker for added repo %q: %w", alias, err)
			}
			if s.tracer != nil {
				w.WithTracer(s.tracer)
			}
			addedWorkers[alias] = w
		}
	}

	if err := s.resMgr.Resize(newCfg.Global.MaxConcurrentJobs); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("resize resource manager: %w", err)
	}

	// Detect removed repositories
	var removedWorkers []*RepoWorker
	for alias, w := range s.workers {
		if _, exists := newCfg.Repos[alias]; !exists {
			removedWorkers = append(removedWorkers, w)
			delete(s.workers, alias)
		}
	}

	// Apply added workers
	for alias, w := range addedWorkers {
		s.workers[alias] = w
		if s.running {
			if err := w.Start(s.runCtx, s.pollingCtx); err != nil {
				logger.Error("failed to start added worker", "repo", alias, "error", err)
			}
		}
	}

	s.cfg = newCfg
	isRunning := s.running
	s.mu.Unlock()

	// Drain removed workers concurrently
	if isRunning && len(removedWorkers) > 0 {
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer drainCancel()

		var wg sync.WaitGroup
		for _, w := range removedWorkers {
			wg.Add(1)
			go func(worker *RepoWorker) {
				defer wg.Done()
				if err := worker.Stop(drainCtx); err != nil {
					logger.Warn("failed to cleanly stop removed worker", "repo", worker.repoAlias, "error", err)
				}
			}(w)
		}
		wg.Wait()
	}

	s.updateMonitorStatus()
	logger.Info("supervisor configuration reloaded", "maxConcurrentJobs", newCfg.Global.MaxConcurrentJobs)
	return nil
}

// updateMonitorStatus syncs the current global and per-repo capacity into the MonitorServer.
func (s *Supervisor) updateMonitorStatus() {
	if s.monServer == nil {
		return
	}
	s.mu.RLock()
	globalMax := s.resMgr.MaxConcurrentJobs()
	globalActive := s.resMgr.ActiveCount()
	slots := make([]*monitor.RepoSlotInfo, 0, len(s.workers))
	for alias, w := range s.workers {
		maxJ := w.repoCfg.MaxConcurrentJobs
		if maxJ <= 0 {
			maxJ = 1
		}
		activeJ := w.ActiveJobs()
		status := "idle"
		if activeJ > 0 {
			status = "busy"
		}
		slots = append(slots, &monitor.RepoSlotInfo{
			Alias:      alias,
			MaxJobs:    maxJ,
			ActiveJobs: activeJ,
			Status:     status,
			Driver:     w.repoCfg.Driver,
		})
	}
	s.mu.RUnlock()

	s.monServer.SetSupervisorStatus(&monitor.SupervisorStatus{
		GlobalMaxJobs: globalMax,
		GlobalActive:  globalActive,
		RepoSlots:     slots,
	})
}
