package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type persistTask struct {
	fn   func()
	done chan struct{}
}

// Server provides an IPC server over a Unix domain socket for real-time
// event streaming (NDJSON) and state snapshots (JSON).
type Server struct {
	sockPath   string
	runnerName string
	listener   net.Listener
	httpServer *http.Server

	mu                sync.RWMutex
	jobs              map[string]*JobInfo
	recentJobs        []*JobInfo
	activeJobs        []*JobInfo
	stepLogs          map[string]*RingBuffer
	telemetry         *TelemetrySample
	supervisorStatus  *SupervisorStatus
	reloadFn          func() error
	historyStore      HistoryStore
	maxRuns           int
	maxDiskBytes      int64
	peakMetrics       map[string]*RunMetricsSummary
	lastTelemetryTime map[string]time.Time

	persistMu     sync.Mutex
	persistCh     chan persistTask
	persistWg     sync.WaitGroup
	persistClosed bool

	subMu       sync.Mutex
	subscribers map[chan Event]struct{}
	closed      bool
}

// NewServer initializes a new Server.
func NewServer(sockPath string, runnerName string) *Server {
	return &Server{
		sockPath:          sockPath,
		runnerName:        runnerName,
		jobs:              make(map[string]*JobInfo),
		recentJobs:        make([]*JobInfo, 0),
		activeJobs:        make([]*JobInfo, 0),
		stepLogs:          make(map[string]*RingBuffer),
		subscribers:       make(map[chan Event]struct{}),
		maxRuns:           50,
		maxDiskBytes:      200 * 1024 * 1024,
		peakMetrics:       make(map[string]*RunMetricsSummary),
		lastTelemetryTime: make(map[string]time.Time),
	}
}

// WithHistoryStore sets the history persistence store for the server and hydrates recent runs.
func (s *Server) WithHistoryStore(store HistoryStore) *Server {
	s.mu.Lock()
	s.historyStore = store
	s.mu.Unlock()
	s.hydrateFromStore()
	return s
}

// HistoryStore returns the configured history persistence store.
func (s *Server) HistoryStore() HistoryStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.historyStore
}

// WithRetention configures maximum runs and disk storage limits for automated pruning.
func (s *Server) WithRetention(maxRuns int, maxDiskBytes int64) *Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxRuns = maxRuns
	s.maxDiskBytes = maxDiskBytes
	return s
}

// WithReloadFunc sets a callback to be invoked when POST /reload is received.
func (s *Server) WithReloadFunc(fn func() error) *Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadFn = fn
	return s
}

// SetSupervisorStatus updates the multi-repo supervisor status.
func (s *Server) SetSupervisorStatus(status *SupervisorStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status == nil {
		s.supervisorStatus = nil
		return
	}
	cp := *status
	if status.RepoSlots != nil {
		cp.RepoSlots = make([]*RepoSlotInfo, len(status.RepoSlots))
		for i, slot := range status.RepoSlots {
			if slot != nil {
				sc := *slot
				cp.RepoSlots[i] = &sc
			}
		}
	}
	s.supervisorStatus = &cp
}

// SupervisorStatus returns a deep copy of current supervisor status.
func (s *Server) SupervisorStatus() *SupervisorStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.supervisorStatus == nil {
		return nil
	}
	cp := *s.supervisorStatus
	if s.supervisorStatus.RepoSlots != nil {
		cp.RepoSlots = make([]*RepoSlotInfo, len(s.supervisorStatus.RepoSlots))
		for i, slot := range s.supervisorStatus.RepoSlots {
			if slot != nil {
				sc := *slot
				cp.RepoSlots[i] = &sc
			}
		}
	}
	return &cp
}

// PeakMetricsForJob returns the recorded peak metrics for the specified job.
func (s *Server) PeakMetricsForJob(jobID string) *RunMetricsSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	metrics, ok := s.peakMetrics[jobID]
	if !ok || metrics == nil {
		return nil
	}
	cp := *metrics
	return &cp
}

// SetJobTelemetry publishes a telemetry sample for a specific job and repo alias.
func (s *Server) SetJobTelemetry(jobID, repoAlias string, t *TelemetrySample) {
	s.mu.Lock()
	if jobID != "" && repoAlias != "" {
		if j, ok := s.jobs[jobID]; ok && j.RepoAlias == "" {
			j.RepoAlias = repoAlias
		}
	}
	s.mu.Unlock()
	s.Publish(Event{
		Type:      EventTelemetry,
		JobID:     jobID,
		RepoAlias: repoAlias,
		Telemetry: t,
	})
}

func (s *Server) hydrateFromStore() {
	s.mu.RLock()
	store := s.historyStore
	s.mu.RUnlock()
	if store == nil {
		return
	}
	runs, err := store.ListRuns(50)
	if err != nil || len(runs) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, r := range runs {
		if r == nil || r.ID == "" {
			continue
		}
		if _, exists := s.jobs[r.ID]; exists {
			continue
		}

		var steps []*StepInfo
		if meta, err := store.GetRun(r.ID); err == nil && meta != nil {
			steps = make([]*StepInfo, 0, len(meta.Steps))
			for _, st := range meta.Steps {
				if st == nil {
					continue
				}
				steps = append(steps, &StepInfo{
					ID:        st.ID,
					Name:      st.Name,
					Number:    st.Number,
					Status:    st.Status,
					StartedAt: st.StartedAt,
					EndedAt:   st.EndedAt,
				})
			}
		}
		if steps == nil {
			steps = make([]*StepInfo, 0)
		}

		job := &JobInfo{
			ID:        r.ID,
			Name:      r.Name,
			Workflow:  r.Workflow,
			Repo:      r.Repo,
			RepoAlias: r.RepoAlias,
			Owner:     r.Owner,
			Status:    r.Status,
			StartedAt: r.StartedAt,
			EndedAt:   r.EndedAt,
			Steps:     steps,
		}
		s.jobs[job.ID] = job
		s.recentJobs = append(s.recentJobs, job)
	}

	s.sortRecentJobsLocked()
}

func cloneStepInfo(st *StepInfo) *StepInfo {
	if st == nil {
		return nil
	}
	cp := *st
	return &cp
}

func cloneJobInfo(j *JobInfo) *JobInfo {
	if j == nil {
		return nil
	}
	cp := *j
	if j.MetricsSummary != nil {
		m := *j.MetricsSummary
		cp.MetricsSummary = &m
	}
	if j.Steps != nil {
		cp.Steps = make([]*StepInfo, len(j.Steps))
		for i, st := range j.Steps {
			cp.Steps[i] = cloneStepInfo(st)
		}
	}
	return &cp
}

func cloneTelemetry(t *TelemetrySample) *TelemetrySample {
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

func (s *Server) isActiveJobLocked(jobID string) bool {
	for _, aj := range s.activeJobs {
		if aj != nil && aj.ID == jobID {
			return true
		}
	}
	return false
}

func (s *Server) evictJobStateLocked(jobID string) {
	delete(s.jobs, jobID)
	delete(s.peakMetrics, jobID)
	delete(s.lastTelemetryTime, jobID)
	prefix := jobID + "/"
	for k := range s.stepLogs {
		if strings.HasPrefix(k, prefix) {
			delete(s.stepLogs, k)
		}
	}
}

func (s *Server) enforceRetentionBoundsLocked() {
	limit := s.maxRuns
	if limit <= 0 {
		limit = 100
	}
	maxJobs := limit * 2
	if maxJobs < 100 {
		maxJobs = 100
	}

	if len(s.jobs) > maxJobs {
		retained := make(map[string]struct{}, len(s.activeJobs)+len(s.recentJobs))
		for _, aj := range s.activeJobs {
			if aj != nil {
				retained[aj.ID] = struct{}{}
			}
		}
		for _, rj := range s.recentJobs {
			if rj != nil {
				retained[rj.ID] = struct{}{}
			}
		}
		for id := range s.jobs {
			if _, keep := retained[id]; !keep {
				s.evictJobStateLocked(id)
			}
		}
	}

	if len(s.peakMetrics) > maxJobs {
		for id := range s.peakMetrics {
			if _, exists := s.jobs[id]; !exists {
				delete(s.peakMetrics, id)
			}
		}
	}

	if len(s.lastTelemetryTime) > maxJobs {
		for id := range s.lastTelemetryTime {
			if _, exists := s.jobs[id]; !exists {
				delete(s.lastTelemetryTime, id)
			}
		}
	}

	maxStepLogs := maxJobs * 10
	if len(s.stepLogs) > maxStepLogs {
		for k := range s.stepLogs {
			if len(s.stepLogs) <= maxStepLogs {
				break
			}
			jobID, _, hasSlash := strings.Cut(k, "/")
			if hasSlash && s.isActiveJobLocked(jobID) {
				continue
			}
			delete(s.stepLogs, k)
		}
	}
}

func (s *Server) sortRecentJobsLocked() {
	sort.Slice(s.recentJobs, func(i, j int) bool {
		if s.recentJobs[i].StartedAt.Equal(s.recentJobs[j].StartedAt) {
			return s.recentJobs[i].ID > s.recentJobs[j].ID
		}
		return s.recentJobs[i].StartedAt.After(s.recentJobs[j].StartedAt)
	})
	limit := s.maxRuns
	if limit <= 0 {
		limit = 100
	}
	if len(s.recentJobs) > limit {
		evicted := s.recentJobs[limit:]
		s.recentJobs = append([]*JobInfo(nil), s.recentJobs[:limit]...)
		for _, ej := range evicted {
			if ej == nil || ej.ID == "" {
				continue
			}
			if !s.isActiveJobLocked(ej.ID) {
				s.evictJobStateLocked(ej.ID)
			}
		}
	}
	s.enforceRetentionBoundsLocked()
}

func (s *Server) enqueuePersist(fn func(), wait bool) <-chan struct{} {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if s.persistClosed {
		return nil
	}
	if s.persistCh == nil {
		s.persistCh = make(chan persistTask, 4096)
		ch := s.persistCh
		s.persistWg.Add(1)
		go func() {
			defer s.persistWg.Done()
			for task := range ch {
				if task.fn != nil {
					task.fn()
				}
				if task.done != nil {
					close(task.done)
				}
			}
		}()
	}
	var done chan struct{}
	if wait {
		done = make(chan struct{})
	}
	s.persistCh <- persistTask{fn: fn, done: done}
	return done
}

// Start opens the Unix domain socket, applies permission 0600,
// and starts serving HTTP requests.
func (s *Server) Start() error {
	s.hydrateFromStore()

	dir := filepath.Dir(s.sockPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create socket dir %s: %w", dir, err)
	}

	// Guard against unlinking an active daemon's socket
	if conn, err := net.DialTimeout("unix", s.sockPath, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		return fmt.Errorf("socket %s is already in use by another running daemon", s.sockPath)
	}

	_ = os.Remove(s.sockPath)

	l, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return fmt.Errorf("listen unix socket %s: %w", s.sockPath, err)
	}
	if err := os.Chmod(s.sockPath, 0600); err != nil {
		_ = l.Close()
		return fmt.Errorf("chmod socket 0600: %w", err)
	}
	s.listener = l

	mux := http.NewServeMux()
	mux.HandleFunc("GET /snapshot", s.handleSnapshot)
	mux.HandleFunc("GET /stream", s.handleStream)
	mux.HandleFunc("GET /runs/{runID}/steps/{stepID}/logs", s.handleHistoricalStepLogs)
	mux.HandleFunc("POST /reload", s.handleReload)

	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = s.httpServer.Serve(l)
	}()

	return nil
}

// Stop gracefully shuts down the server, terminates all streaming subscribers,
// and cleans up the Unix domain socket file.
func (s *Server) Stop() error {
	s.subMu.Lock()
	if s.closed {
		s.subMu.Unlock()
		return nil
	}
	s.closed = true
	for ch := range s.subscribers {
		close(ch)
		delete(s.subscribers, ch)
	}
	s.subMu.Unlock()

	var err error
	if s.httpServer != nil {
		s.httpServer.SetKeepAlivesEnabled(false)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = s.httpServer.Shutdown(ctx)
		if err != nil {
			_ = s.httpServer.Close()
		}
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	_ = os.Remove(s.sockPath)

	s.persistMu.Lock()
	if !s.persistClosed {
		s.persistClosed = true
		if s.persistCh != nil {
			close(s.persistCh)
		}
	}
	s.persistMu.Unlock()
	s.persistWg.Wait()

	return err
}

func (s *Server) handleHistoricalStepLogs(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	stepID := r.PathValue("stepID")
	if runID == "" || stepID == "" {
		http.Error(w, "missing runID or stepID", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	store := s.historyStore
	s.mu.RUnlock()

	if store == nil {
		http.Error(w, "history store not configured", http.StatusNotFound)
		return
	}

	meta, err := store.GetRun(runID)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

	stepFound := false
	for _, st := range meta.Steps {
		if st != nil && st.ID == stepID {
			stepFound = true
			break
		}
	}
	if !stepFound {
		http.Error(w, "step not found", http.StatusNotFound)
		return
	}

	lines, err := store.GetStepLogs(runID, stepID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if lines == nil {
		lines = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(lines)
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	reloadFn := s.reloadFn
	s.mu.RUnlock()

	if reloadFn == nil {
		http.Error(w, "reload not supported", http.StatusNotImplemented)
		return
	}

	if err := reloadFn(); err != nil {
		http.Error(w, fmt.Sprintf("reload failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"reloaded"}`))
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	logsCopy := make(map[string][]string, len(s.stepLogs))
	for stepID, rb := range s.stepLogs {
		logsCopy[stepID] = rb.Lines()
	}

	activeJobsCopy := make([]*JobInfo, len(s.activeJobs))
	for i, j := range s.activeJobs {
		activeJobsCopy[i] = cloneJobInfo(j)
	}

	recentJobsCopy := make([]*JobInfo, len(s.recentJobs))
	for i, j := range s.recentJobs {
		recentJobsCopy[i] = cloneJobInfo(j)
	}

	var statusCopy *SupervisorStatus
	if s.supervisorStatus != nil {
		cp := *s.supervisorStatus
		if s.supervisorStatus.RepoSlots != nil {
			cp.RepoSlots = make([]*RepoSlotInfo, len(s.supervisorStatus.RepoSlots))
			for i, slot := range s.supervisorStatus.RepoSlots {
				if slot != nil {
					sc := *slot
					cp.RepoSlots[i] = &sc
				}
			}
		}
		statusCopy = &cp
	}

	snap := Snapshot{
		RunnerName:       s.runnerName,
		SupervisorStatus: statusCopy,
		ActiveJobs:       activeJobsCopy,
		RecentJobs:       recentJobsCopy,
		Telemetry:        cloneTelemetry(s.telemetry),
		StepLogs:         logsCopy,
	}
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan Event, 1000)
	s.subMu.Lock()
	if s.closed {
		s.subMu.Unlock()
		return
	}
	s.subscribers[ch] = struct{}{}
	s.subMu.Unlock()

	// Flush initial headers immediately so client knows connection is established
	flusher.Flush()

	defer func() {
		s.subMu.Lock()
		delete(s.subscribers, ch)
		s.subMu.Unlock()
	}()

	enc := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := enc.Encode(ev); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// Publish broadcasts an event to all active stream subscribers and updates internal state.
func (s *Server) Publish(ev Event) {
	broadcastEv := ev
	if ev.Job != nil {
		broadcastEv.Job = cloneJobInfo(ev.Job)
	}
	if ev.Step != nil {
		broadcastEv.Step = cloneStepInfo(ev.Step)
	}
	if ev.Telemetry != nil {
		broadcastEv.Telemetry = cloneTelemetry(ev.Telemetry)
	}

	var persistDone <-chan struct{}

	s.mu.Lock()
	store := s.historyStore

	switch ev.Type {
	case EventJobStart:
		jobID := ev.JobID
		if jobID == "" && ev.Job != nil {
			jobID = ev.Job.ID
		}
		if jobID != "" {
			job := &JobInfo{
				ID:        jobID,
				Name:      jobID,
				Status:    StatusRunning,
				StartedAt: time.Now(),
				Steps:     make([]*StepInfo, 0),
			}
			if ev.Job != nil {
				job.Name = ev.Job.Name
				job.Workflow = ev.Job.Workflow
				job.Repo = ev.Job.Repo
				job.RepoAlias = ev.Job.RepoAlias
				job.RepoURL = ev.Job.RepoURL
				job.Owner = ev.Job.Owner
				job.Driver = ev.Job.Driver
				job.Status = ev.Job.Status
				if !ev.Job.StartedAt.IsZero() {
					job.StartedAt = ev.Job.StartedAt
				}
				job.EndedAt = ev.Job.EndedAt
				job.Steps = make([]*StepInfo, len(ev.Job.Steps))
				for i, st := range ev.Job.Steps {
					job.Steps[i] = cloneStepInfo(st)
				}
			}
			if ev.RepoAlias != "" {
				job.RepoAlias = ev.RepoAlias
			}
			prefix := jobID + "/"
			for k := range s.stepLogs {
				if strings.HasPrefix(k, prefix) {
					delete(s.stepLogs, k)
				}
			}
			s.jobs[jobID] = job
			found := false
			for i, j := range s.activeJobs {
				if j.ID == jobID {
					s.activeJobs[i] = job
					found = true
					break
				}
			}
			if !found {
				s.activeJobs = append(s.activeJobs, job)
			}
			s.peakMetrics[jobID] = &RunMetricsSummary{}
			s.enforceRetentionBoundsLocked()
			if store != nil {
				driver := ev.Driver
				if driver == "" {
					driver = "docker"
				}
				startJob := cloneJobInfo(job)
				startLimits := ev.Limits
				if startLimits != nil {
					lc := *startLimits
					startLimits = &lc
				}
				s.enqueuePersist(func() {
					_ = store.StartRun(startJob, startLimits, driver)
				}, false)
			}
		}
	case EventJobFinish:
		jobID := ev.JobID
		if jobID == "" && ev.Job != nil {
			jobID = ev.Job.ID
		}
		updatedActive := make([]*JobInfo, 0)
		var jobToSave *JobInfo
		for _, j := range s.activeJobs {
			if j.ID == jobID {
				jobToSave = j
				if ev.Job != nil {
					j.Status = ev.Job.Status
					if !ev.Job.EndedAt.IsZero() {
						j.EndedAt = ev.Job.EndedAt
					} else {
						j.EndedAt = time.Now()
					}
					if ev.Job.Name != "" {
						j.Name = ev.Job.Name
					}
					if ev.Job.RepoAlias != "" {
						j.RepoAlias = ev.Job.RepoAlias
					}
				} else {
					j.Status = StatusSuccess
					j.EndedAt = time.Now()
				}
			} else {
				updatedActive = append(updatedActive, j)
			}
		}
		if jobToSave == nil {
			if ev.Job != nil {
				jobToSave = cloneJobInfo(ev.Job)
			} else if jobID != "" {
				if existing, ok := s.jobs[jobID]; ok {
					jobToSave = existing
					jobToSave.Status = StatusSuccess
					jobToSave.EndedAt = time.Now()
				}
			}
		}
		var peakMetrics *RunMetricsSummary
		if metrics, ok := s.peakMetrics[jobID]; ok {
			peakMetrics = metrics
			delete(s.peakMetrics, jobID)
		}
		if jobToSave != nil && peakMetrics != nil {
			mCopy := *peakMetrics
			jobToSave.MetricsSummary = &mCopy
		}
		delete(s.lastTelemetryTime, jobID)

		if jobToSave != nil {
			s.jobs[jobID] = jobToSave
			found := false
			for idx, rj := range s.recentJobs {
				if rj.ID == jobID {
					s.recentJobs[idx] = jobToSave
					found = true
					break
				}
			}
			if !found {
				s.recentJobs = append(s.recentJobs, jobToSave)
			}
			s.sortRecentJobsLocked()
		}
		s.activeJobs = updatedActive
		if len(s.activeJobs) == 0 {
			s.telemetry = nil
		}

		if store != nil && jobID != "" {
			fStatus := StatusSuccess
			if jobToSave != nil && jobToSave.Status != "" {
				fStatus = jobToSave.Status
			} else if ev.Job != nil && ev.Job.Status != "" {
				fStatus = ev.Job.Status
			}
			finishJobID := jobID
			finishStatus := fStatus
			var finishMetrics *RunMetricsSummary
			if peakMetrics != nil {
				mCopy := *peakMetrics
				finishMetrics = &mCopy
			}
			finishMaxRuns := s.maxRuns
			if finishMaxRuns <= 0 {
				finishMaxRuns = 50
			}
			finishMaxDisk := s.maxDiskBytes
			if finishMaxDisk <= 0 {
				finishMaxDisk = 200 * 1024 * 1024
			}
			persistDone = s.enqueuePersist(func() {
				_ = store.FinishRun(finishJobID, finishStatus, finishMetrics)
			}, true)
			s.enqueuePersist(func() {
				_, _ = store.Prune(finishMaxRuns, finishMaxDisk)
			}, false)
		}
	case EventStepStart, EventStepFinish:
		if ev.Step != nil && ev.JobID != "" {
			job, ok := s.jobs[ev.JobID]
			if !ok {
				job = &JobInfo{
					ID:        ev.JobID,
					Name:      ev.JobID,
					Status:    StatusRunning,
					StartedAt: time.Now(),
					Steps:     make([]*StepInfo, 0),
				}
				s.jobs[ev.JobID] = job
				s.activeJobs = append(s.activeJobs, job)
			}
			if ev.RepoAlias != "" && job.RepoAlias == "" {
				job.RepoAlias = ev.RepoAlias
			}
			stepClone := *ev.Step
			found := false
			for i, st := range job.Steps {
				if st.ID == ev.Step.ID {
					if stepClone.Name == "" {
						stepClone.Name = st.Name
					}
					if stepClone.StartedAt.IsZero() {
						stepClone.StartedAt = st.StartedAt
					}
					job.Steps[i] = &stepClone
					found = true
					break
				}
			}
			if !found {
				job.Steps = append(job.Steps, &stepClone)
			}

			if ev.Type == EventStepFinish && store != nil {
				stepJobID := ev.JobID
				stepFinishID := ev.Step.ID
				stepConclusion := string(ev.Step.Status)
				stepFinishedAt := ev.Step.EndedAt
				stepStartedAt := stepClone.StartedAt
				stepExitCode := ev.Step.ExitCode
				if stepExitCode == 0 && ev.Step.Status == StatusFailed {
					stepExitCode = 1
				}
				s.enqueuePersist(func() {
					_ = store.FinishStep(stepJobID, stepFinishID, stepConclusion, stepExitCode, stepFinishedAt, stepStartedAt)
				}, false)
			}
		}
	case EventStepLog:
		if ev.StepID != "" {
			key := stepLogKey(ev.JobID, ev.StepID)
			rb, ok := s.stepLogs[key]
			if !ok {
				rb = NewRingBuffer(5000)
				s.stepLogs[key] = rb
			}
			rb.Append(ev.LogLine)
			s.enforceRetentionBoundsLocked()

			if store != nil {
				targetJobID := ev.JobID
				if targetJobID == "" && len(s.activeJobs) == 1 {
					targetJobID = s.activeJobs[0].ID
				}
				if targetJobID != "" {
					logJobID := targetJobID
					logStepID := ev.StepID
					logLine := ev.LogLine
					s.enqueuePersist(func() {
						_ = store.AppendStepLog(logJobID, logStepID, logLine)
					}, false)
				}
			}
		}
	case EventTelemetry:
		s.telemetry = cloneTelemetry(ev.Telemetry)
		if ev.Telemetry != nil {
			if ev.RepoAlias != "" {
				if ev.JobID != "" {
					if j, ok := s.jobs[ev.JobID]; ok && j.RepoAlias == "" {
						j.RepoAlias = ev.RepoAlias
					}
				} else if len(s.activeJobs) == 1 && s.activeJobs[0].RepoAlias == "" {
					s.activeJobs[0].RepoAlias = ev.RepoAlias
				}
			}
			targetJobIDs := make([]string, 0, 1)
			if ev.JobID != "" {
				targetJobIDs = append(targetJobIDs, ev.JobID)
			} else if len(s.activeJobs) == 1 {
				targetJobIDs = append(targetJobIDs, s.activeJobs[0].ID)
			}
			for _, jID := range targetJobIDs {
				peak := s.peakMetrics[jID]
				if peak == nil {
					peak = &RunMetricsSummary{}
					s.peakMetrics[jID] = peak
				}
				if ev.Telemetry.StepCPU > peak.PeakCPUPercent {
					peak.PeakCPUPercent = ev.Telemetry.StepCPU
				}
				if ev.Telemetry.StepRSSBytes > peak.PeakRSSBytes {
					peak.PeakRSSBytes = ev.Telemetry.StepRSSBytes
				}

				// Calculate elapsed delta time in seconds
				deltaSeconds := 1.0
				if !ev.Telemetry.Timestamp.IsZero() {
					deltaSeconds = 0.5 // default 500ms sampling interval
					if lastTime, ok := s.lastTelemetryTime[jID]; ok && !lastTime.IsZero() {
						diff := ev.Telemetry.Timestamp.Sub(lastTime).Seconds()
						if diff > 0 && diff < 60 {
							deltaSeconds = diff
						} else if diff <= 0 {
							deltaSeconds = 0.0
						}
					}
					s.lastTelemetryTime[jID] = ev.Telemetry.Timestamp
				}

				if ev.Telemetry.NetRxBytes > 0 {
					if ev.Telemetry.NetRxBytes > peak.TotalNetRxBytes {
						peak.TotalNetRxBytes = ev.Telemetry.NetRxBytes
					}
				} else if ev.Telemetry.NetRxRate > 0 {
					peak.TotalNetRxBytes += uint64(math.Round(float64(ev.Telemetry.NetRxRate) * deltaSeconds))
				}
				if ev.Telemetry.NetTxBytes > 0 {
					if ev.Telemetry.NetTxBytes > peak.TotalNetTxBytes {
						peak.TotalNetTxBytes = ev.Telemetry.NetTxBytes
					}
				} else if ev.Telemetry.NetTxRate > 0 {
					peak.TotalNetTxBytes += uint64(math.Round(float64(ev.Telemetry.NetTxRate) * deltaSeconds))
				}
				if ev.Telemetry.DiskWrittenBytes > 0 {
					if ev.Telemetry.DiskWrittenBytes > peak.TotalDiskWrittenBytes {
						peak.TotalDiskWrittenBytes = ev.Telemetry.DiskWrittenBytes
					}
				} else if ev.Telemetry.DiskWriteRate > 0 {
					peak.TotalDiskWrittenBytes += uint64(math.Round(float64(ev.Telemetry.DiskWriteRate) * deltaSeconds))
				}
			}
			s.enforceRetentionBoundsLocked()
		}
	}
	s.mu.Unlock()

	if persistDone != nil {
		<-persistDone
	}

	s.subMu.Lock()
	defer s.subMu.Unlock()
	if s.closed {
		return
	}
	for ch := range s.subscribers {
		select {
		case ch <- broadcastEv:
		default:
			// Drop event if subscriber buffer is full to prevent log bursts from abruptly
			// disconnecting clients. Subscribers are cleaned up via context cancellation when closed.
		}
	}
}

func stepLogKey(jobID, stepID string) string {
	if jobID == "" {
		return stepID
	}
	return jobID + "/" + stepID
}

// AppendLog is a helper to record and broadcast a step log line.
func (s *Server) AppendLog(stepID string, line string) {
	s.Publish(Event{
		Type:    EventStepLog,
		StepID:  stepID,
		LogLine: line,
	})
}

// AppendJobStepLog is a helper to record and broadcast a step log line for a specific job.
func (s *Server) AppendJobStepLog(jobID, stepID string, line string) {
	s.Publish(Event{
		Type:    EventStepLog,
		JobID:   jobID,
		StepID:  stepID,
		LogLine: line,
	})
}

// SetTelemetry is a helper to update and broadcast a telemetry sample.
func (s *Server) SetTelemetry(t *TelemetrySample) {
	s.Publish(Event{
		Type:      EventTelemetry,
		Telemetry: t,
	})
}

// SubscriberCount returns the number of active stream subscribers.
func (s *Server) SubscriberCount() int {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	return len(s.subscribers)
}
