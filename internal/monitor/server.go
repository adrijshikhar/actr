package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Server provides an IPC server over a Unix domain socket for real-time
// event streaming (NDJSON) and state snapshots (JSON).
type Server struct {
	sockPath   string
	runnerName string
	listener   net.Listener
	httpServer *http.Server

	mu           sync.RWMutex
	jobs         map[string]*JobInfo
	recentJobs   []*JobInfo
	activeJobs   []*JobInfo
	stepLogs     map[string]*RingBuffer
	telemetry    *TelemetrySample
	historyStore HistoryStore
	maxRuns      int
	maxDiskBytes int64
	peakMetrics  map[string]*RunMetricsSummary

	subMu       sync.Mutex
	subscribers map[chan Event]struct{}
	closed      bool
}

// NewServer initializes a new Server.
func NewServer(sockPath string, runnerName string) *Server {
	return &Server{
		sockPath:     sockPath,
		runnerName:   runnerName,
		jobs:         make(map[string]*JobInfo),
		recentJobs:   make([]*JobInfo, 0),
		activeJobs:   make([]*JobInfo, 0),
		stepLogs:     make(map[string]*RingBuffer),
		subscribers:  make(map[chan Event]struct{}),
		maxRuns:      50,
		maxDiskBytes: 200 * 1024 * 1024,
		peakMetrics:  make(map[string]*RunMetricsSummary),
	}
}

// WithHistoryStore sets the history persistence store for the server and hydrates recent runs.
func (s *Server) WithHistoryStore(store HistoryStore) *Server {
	s.historyStore = store
	s.hydrateFromStore()
	return s
}

// HistoryStore returns the configured history persistence store.
func (s *Server) HistoryStore() HistoryStore {
	return s.historyStore
}

// WithRetention configures maximum runs and disk storage limits for automated pruning.
func (s *Server) WithRetention(maxRuns int, maxDiskBytes int64) *Server {
	s.maxRuns = maxRuns
	s.maxDiskBytes = maxDiskBytes
	return s
}

func (s *Server) hydrateFromStore() {
	if s.historyStore == nil {
		return
	}
	runs, err := s.historyStore.ListRuns(50)
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
		if meta, err := s.historyStore.GetRun(r.ID); err == nil && meta != nil {
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

func (s *Server) sortRecentJobsLocked() {
	sort.Slice(s.recentJobs, func(i, j int) bool {
		if s.recentJobs[i].StartedAt.Equal(s.recentJobs[j].StartedAt) {
			return s.recentJobs[i].ID > s.recentJobs[j].ID
		}
		return s.recentJobs[i].StartedAt.After(s.recentJobs[j].StartedAt)
	})
	if len(s.recentJobs) > 100 {
		s.recentJobs = s.recentJobs[:100]
	}
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
	return err
}

func (s *Server) handleHistoricalStepLogs(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	stepID := r.PathValue("stepID")
	if runID == "" || stepID == "" {
		http.Error(w, "missing runID or stepID", http.StatusBadRequest)
		return
	}

	if s.historyStore == nil {
		http.Error(w, "history store not configured", http.StatusNotFound)
		return
	}

	meta, err := s.historyStore.GetRun(runID)
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

	lines, err := s.historyStore.GetStepLogs(runID, stepID)
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

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	logsCopy := make(map[string][]string, len(s.stepLogs))
	for stepID, rb := range s.stepLogs {
		logsCopy[stepID] = rb.Lines()
	}

	activeJobsCopy := make([]*JobInfo, len(s.activeJobs))
	for i, j := range s.activeJobs {
		jCopy := *j
		jCopy.Steps = append([]*StepInfo(nil), j.Steps...)
		activeJobsCopy[i] = &jCopy
	}

	recentJobsCopy := make([]*JobInfo, len(s.recentJobs))
	for i, j := range s.recentJobs {
		jCopy := *j
		jCopy.Steps = append([]*StepInfo(nil), j.Steps...)
		recentJobsCopy[i] = &jCopy
	}

	snap := Snapshot{
		RunnerName: s.runnerName,
		ActiveJobs: activeJobsCopy,
		RecentJobs: recentJobsCopy,
		Telemetry:  s.telemetry,
		StepLogs:   logsCopy,
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

	ch := make(chan Event, 100)
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
		jCopy := *ev.Job
		jCopy.Steps = append([]*StepInfo(nil), ev.Job.Steps...)
		broadcastEv.Job = &jCopy
	}
	if ev.Step != nil {
		sCopy := *ev.Step
		broadcastEv.Step = &sCopy
	}

	s.mu.Lock()
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
				job.Owner = ev.Job.Owner
				job.Status = ev.Job.Status
				if !ev.Job.StartedAt.IsZero() {
					job.StartedAt = ev.Job.StartedAt
				}
				job.Steps = append([]*StepInfo(nil), ev.Job.Steps...)
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
			if s.historyStore != nil {
				driver := ev.Driver
				if driver == "" {
					driver = "docker"
				}
				_ = s.historyStore.StartRun(job, ev.Limits, driver)
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
				jobToSave = ev.Job
			} else if jobID != "" {
				if existing, ok := s.jobs[jobID]; ok {
					jobToSave = existing
					jobToSave.Status = StatusSuccess
					jobToSave.EndedAt = time.Now()
				}
			}
		}
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

		var peakMetrics *RunMetricsSummary
		if metrics, ok := s.peakMetrics[jobID]; ok {
			peakMetrics = metrics
			delete(s.peakMetrics, jobID)
		}
		if s.historyStore != nil && jobID != "" {
			finishStatus := StatusSuccess
			if jobToSave != nil && jobToSave.Status != "" {
				finishStatus = jobToSave.Status
			} else if ev.Job != nil && ev.Job.Status != "" {
				finishStatus = ev.Job.Status
			}
			_ = s.historyStore.FinishRun(jobID, finishStatus, peakMetrics)

			maxRuns := s.maxRuns
			if maxRuns <= 0 {
				maxRuns = 50
			}
			maxDiskBytes := s.maxDiskBytes
			if maxDiskBytes <= 0 {
				maxDiskBytes = 200 * 1024 * 1024
			}
			hStore := s.historyStore
			go func() {
				_, _ = hStore.Prune(maxRuns, maxDiskBytes)
			}()
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

			if s.historyStore != nil {
				targetJobID := ev.JobID
				if targetJobID == "" && len(s.activeJobs) == 1 {
					targetJobID = s.activeJobs[0].ID
				}
				if targetJobID != "" {
					_ = s.historyStore.AppendStepLog(targetJobID, ev.StepID, ev.LogLine)
				}
			}
		}
	case EventTelemetry:
		s.telemetry = ev.Telemetry
		if ev.Telemetry != nil {
			targetJobIDs := make([]string, 0, 1)
			if ev.JobID != "" {
				targetJobIDs = append(targetJobIDs, ev.JobID)
			} else {
				for _, aj := range s.activeJobs {
					targetJobIDs = append(targetJobIDs, aj.ID)
				}
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
				if ev.Telemetry.NetRxBytes > 0 {
					if ev.Telemetry.NetRxBytes > peak.TotalNetRxBytes {
						peak.TotalNetRxBytes = ev.Telemetry.NetRxBytes
					}
				} else if ev.Telemetry.NetRxRate > 0 {
					peak.TotalNetRxBytes += ev.Telemetry.NetRxRate
				}
				if ev.Telemetry.NetTxBytes > 0 {
					if ev.Telemetry.NetTxBytes > peak.TotalNetTxBytes {
						peak.TotalNetTxBytes = ev.Telemetry.NetTxBytes
					}
				} else if ev.Telemetry.NetTxRate > 0 {
					peak.TotalNetTxBytes += ev.Telemetry.NetTxRate
				}
				if ev.Telemetry.DiskWrittenBytes > 0 {
					if ev.Telemetry.DiskWrittenBytes > peak.TotalDiskWrittenBytes {
						peak.TotalDiskWrittenBytes = ev.Telemetry.DiskWrittenBytes
					}
				} else if ev.Telemetry.DiskWriteRate > 0 {
					peak.TotalDiskWrittenBytes += ev.Telemetry.DiskWriteRate
				}
			}
		}
	}
	s.mu.Unlock()

	s.subMu.Lock()
	defer s.subMu.Unlock()
	if s.closed {
		return
	}
	for ch := range s.subscribers {
		select {
		case ch <- broadcastEv:
		default:
			close(ch)
			delete(s.subscribers, ch)
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
