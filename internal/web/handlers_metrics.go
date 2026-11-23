package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// fallbackHostMetrics samples host-level vitals using gopsutil with panic safety.
func fallbackHostMetrics() *monitor.RunnerMetrics {
	metrics := &monitor.RunnerMetrics{
		Timestamp: time.Now().UTC(),
	}
	defer func() {
		_ = recover()
	}()

	if cpuPercents, err := cpu.Percent(0, false); err == nil && len(cpuPercents) > 0 {
		metrics.HostCPU = cpuPercents[0]
	}
	if vMem, err := mem.VirtualMemory(); err == nil && vMem != nil {
		metrics.HostMemTotal = vMem.Total
		metrics.HostMemUsed = vMem.Used
	}
	if avg, err := load.Avg(); err == nil && avg != nil {
		metrics.HostLoad1m = avg.Load1
	}
	return metrics
}

// getCurrentMetrics retrieves metrics from the configured MetricsProvider or falls back to host vitals.
func (s *Server) getCurrentMetrics(ctx context.Context) *monitor.RunnerMetrics {
	if s.cfg.MetricsProvider != nil {
		if m, err := s.cfg.MetricsProvider.GetCurrentMetrics(ctx); err == nil && m != nil {
			return m
		}
	}
	return fallbackHostMetrics()
}

// handleMetricsCurrent returns an instant snapshot of runner / host metrics in JSON.
func (s *Server) handleMetricsCurrent(w http.ResponseWriter, r *http.Request) {
	metrics := s.getCurrentMetrics(r.Context())
	writeJSON(w, http.StatusOK, metrics)
}

// handleMetricsStream streams live metrics snapshots every 1s via Server-Sent Events (SSE).
func (s *Server) handleMetricsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	sendSample := func() bool {
		m := s.getCurrentMetrics(r.Context())
		data, err := json.Marshal(m)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: metrics\ndata: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Send initial snapshot immediately
	if !sendSample() {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !sendSample() {
				return
			}
		}
	}
}

// handleRunMetrics returns historical telemetry sample points for a completed or active run.
func (s *Server) handleRunMetrics(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RunStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "run store not configured")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing run id")
		return
	}

	samples, err := s.cfg.RunStore.GetRunMetrics(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if samples == nil {
		samples = []*monitor.TelemetrySample{}
	}

	writeJSON(w, http.StatusOK, samples)
}
