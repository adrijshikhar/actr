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
