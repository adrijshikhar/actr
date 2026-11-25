package supervisor

import (
	"context"
	"fmt"
	"reflect"
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
	reloadMu      sync.Mutex
	cfg           *config.SupervisorConfig
	sockPath      string
	resMgr        *ResourceManager
	monServer     *monitor.Server
	tracer        *telemetry.Manager
	workers       map[string]*RepoWorker
	pollingCtx    context.Context
	pollingCancel context.CancelFunc
	jobCtx        context.Context
	jobCancel     context.CancelFunc
	drainWg       sync.WaitGroup
	statusWg      sync.WaitGroup
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

	jobCtx, jobCancel := context.WithCancel(context.WithoutCancel(ctx))
	defer jobCancel()
	s.jobCtx = jobCtx
	s.jobCancel = jobCancel

	pollingCtx, pollingCancel := context.WithCancel(ctx)
	defer pollingCancel()
	s.pollingCtx = pollingCtx
	s.pollingCancel = pollingCancel

	if s.monServer != nil {
		if err := s.monServer.Start(); err != nil {
			logger.Error("failed to start monitor server", "error", err)
			s.monServer = nil
		}
	}

	for alias, w := range s.workers {
		if err := w.Start(jobCtx, pollingCtx); err != nil {
			logger.Error("failed to start worker", "repo", alias, "error", err)
		}
	}
	s.mu.Unlock()

	s.updateMonitorStatus()

	s.statusWg.Add(1)
	go func() {
		defer s.statusWg.Done()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-pollingCtx.Done():
				return
			case <-ticker.C:
				s.updateMonitorStatus()
			}
		}
	}()

	<-pollingCtx.Done()

	// 1. Immediately halt broker message polling
	pollingCancel()

	// 2. Concurrently drain all workers with a 30-second deadline while jobCtx remains alive
	drainCtx, drainCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer drainCancel()

	s.mu.RLock()
	workersToDrain := make([]*RepoWorker, 0, len(s.workers))
	for _, w := range s.workers {
		workersToDrain = append(workersToDrain, w)
	}
	s.mu.RUnlock()

	for _, w := range workersToDrain {
		s.drainWg.Add(1)
		go func(worker *RepoWorker) {
			defer s.drainWg.Done()
			if err := worker.Stop(drainCtx); err != nil {
				logger.Warn("worker stop encountered error or timed out", "repo", worker.repoAlias, "error", err)
			}
		}(w)
	}
	s.drainWg.Wait()

	// 3. Cancel job context after drain completes or times out
	jobCancel()

	// Bounded wait for all worker done channels to complete wind-down after job cancellation
	windDownCtx, windDownCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	for _, w := range workersToDrain {
		select {
		case <-w.doneCh:
		case <-windDownCtx.Done():
		}
	}
	windDownCancel()

	s.statusWg.Wait()

	// 4. Stop monitor server
	s.mu.RLock()
	monServer := s.monServer
	s.mu.RUnlock()
	if monServer != nil {
		_ = monServer.Stop()
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
	if !s.running || s.pollingCancel == nil {
		return nil
	}
	s.pollingCancel()
	return nil
}

// Reload dynamically updates the supervisor configuration: adjusting global concurrency,
// starting workers for added/modified repositories, and gracefully draining removed/replaced workers.
func (s *Supervisor) Reload(newCfg *config.SupervisorConfig) error {
	if newCfg == nil {
		return fmt.Errorf("nil config on reload")
	}

	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	s.mu.Lock()
	if s.running && s.pollingCtx != nil && s.pollingCtx.Err() != nil {
		s.mu.Unlock()
		return fmt.Errorf("cannot reload supervisor while shutting down")
	}

	// Pre-instantiate workers for added or modified repositories to fail early before mutations
	replacementWorkers := make(map[string]*RepoWorker)
	for alias, repoCfg := range newCfg.Repos {
		existing, exists := s.workers[alias]
		if !exists || !reflect.DeepEqual(existing.repoCfg, repoCfg) {
			w, err := NewRepoWorker(alias, repoCfg, s.resMgr, s.monServer)
			if err != nil {
				s.mu.Unlock()
				return fmt.Errorf("create worker for repo %q: %w", alias, err)
			}
			if s.tracer != nil {
				w.WithTracer(s.tracer)
			}
			replacementWorkers[alias] = w
		}
	}

	if err := s.resMgr.Resize(newCfg.Global.MaxConcurrentJobs); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("resize resource manager: %w", err)
	}

	// Collect removed and replaced workers for drain
	var workersToRemove []*RepoWorker
	var replacedOldWorkers []*RepoWorker
	for alias, w := range s.workers {
		if _, exists := newCfg.Repos[alias]; !exists {
			workersToRemove = append(workersToRemove, w)
			delete(s.workers, alias)
		} else if _, replaced := replacementWorkers[alias]; replaced {
			replacedOldWorkers = append(replacedOldWorkers, w)
			delete(s.workers, alias)
		}
	}

	isRunning := s.running
	baseJobCtx := s.jobCtx

	// For replaced workers, stop the old worker before starting the replacement worker on the same repo
	if isRunning && len(replacedOldWorkers) > 0 {
		for _, oldWorker := range replacedOldWorkers {
			drainCtx, drainCancel := context.WithTimeout(context.WithoutCancel(baseJobCtx), 30*time.Second)
			_ = oldWorker.Stop(drainCtx)
			drainCancel()
		}
	}

	// Apply added and replacement workers
	for alias, w := range replacementWorkers {
		s.workers[alias] = w
		if isRunning {
			if err := w.Start(s.jobCtx, s.pollingCtx); err != nil {
				logger.Error("failed to start worker", "repo", alias, "error", err)
			}
		}
	}

	s.cfg = newCfg
	if isRunning && len(workersToRemove) > 0 {
		s.drainWg.Add(len(workersToRemove))
	}
	s.mu.Unlock()

	// Drain removed workers concurrently, tracked by drainWg so Start cannot exit prematurely
	if isRunning && len(workersToRemove) > 0 {
		parentCtx := context.Background()
		if baseJobCtx != nil {
			parentCtx = context.WithoutCancel(baseJobCtx)
		}
		for _, w := range workersToRemove {
			go func(worker *RepoWorker) {
				defer s.drainWg.Done()
				drainCtx, drainCancel := context.WithTimeout(parentCtx, 30*time.Second)
				defer drainCancel()
				if err := worker.Stop(drainCtx); err != nil {
					logger.Warn("failed to cleanly stop removed worker", "repo", worker.repoAlias, "error", err)
				}
			}(w)
		}
	}

	s.updateMonitorStatus()
	logger.Info("supervisor configuration reloaded", "maxConcurrentJobs", newCfg.Global.MaxConcurrentJobs)
	return nil
}

// updateMonitorStatus syncs the current global and per-repo capacity into the MonitorServer.
func (s *Supervisor) updateMonitorStatus() {
	s.mu.RLock()
	monServer := s.monServer
	if monServer == nil {
		s.mu.RUnlock()
		return
	}
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

	monServer.SetSupervisorStatus(&monitor.SupervisorStatus{
		GlobalMaxJobs: globalMax,
		GlobalActive:  globalActive,
		RepoSlots:     slots,
	})
}
