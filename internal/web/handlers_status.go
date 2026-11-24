package web

import (
	"net/http"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

// StatusResponse represents the system status payload returned by GET /api/status.
type StatusResponse struct {
	Version         string  `json:"version"`
	DaemonConnected bool    `json:"daemon_connected"`
	ActiveJobs      int     `json:"active_jobs"`
	QueuedJobs      int     `json:"queued_jobs"`
	TotalRunners    int     `json:"total_runners"`
	HostCPU         float64 `json:"host_cpu"`
	HostMemUsed     uint64  `json:"host_mem_used"`
	HostMemTotal    uint64  `json:"host_mem_total"`
	UptimeSeconds   int64   `json:"uptime_seconds"`
}

// handleStatus returns the current application, daemon, and host status in JSON.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := StatusResponse{
		Version:       s.cfg.Version,
		UptimeSeconds: int64(time.Since(s.startTime).Seconds()),
	}

	if resp.Version == "" {
		resp.Version = "dev"
	}

	// Query daemon controller if available
	if s.cfg.DaemonController != nil {
		if status, err := s.cfg.DaemonController.Status(); err == nil {
			if status == "running" || status == "active" {
				resp.DaemonConnected = true
			}
		}
	}

	// Query RunStore for active and queued job counts
	if s.cfg.RunStore != nil {
		if _, queuedTotal, err := s.cfg.RunStore.GetRuns(r.Context(), 1, 0, "", "queued", ""); err == nil {
			resp.QueuedJobs = queuedTotal
		}
		if _, activeTotal, err := s.cfg.RunStore.GetRuns(r.Context(), 1, 0, "", "running", ""); err == nil {
			resp.ActiveJobs = activeTotal
		}
	}

	// Total runners & runner aggregation
	if s.cfg.RunnerProvider != nil {
		if rList, err := s.cfg.RunnerProvider.GetRunners(r.Context()); err == nil {
			resp.TotalRunners = len(rList)
			for _, runInfo := range rList {
				if resp.QueuedJobs == 0 && runInfo.QueuedJobs > 0 {
					resp.QueuedJobs += runInfo.QueuedJobs
				}
				if resp.ActiveJobs == 0 && runInfo.ActiveSlots > 0 {
					resp.ActiveJobs += runInfo.ActiveSlots
				}
			}
		}
	} else {
		resp.TotalRunners = len(s.fallbackRunners())
	}

	// Host telemetry with panic recovery for Darwin/unsupported environments
	func() {
		defer func() {
			_ = recover()
		}()

		if cpuPercents, err := cpu.Percent(0, false); err == nil && len(cpuPercents) > 0 {
			resp.HostCPU = cpuPercents[0]
		}
		if vMem, err := mem.VirtualMemory(); err == nil && vMem != nil {
			resp.HostMemTotal = vMem.Total
			resp.HostMemUsed = vMem.Used
		}
	}()

	writeJSON(w, http.StatusOK, resp)
}
