package web

import (
	"net/http"

	"github.com/adrijshikhar/actr/internal/service"
)

type serviceStatusResponse struct {
	Status string `json:"status"`
}

type serviceActionResponse struct {
	Status string `json:"status"`
}

// handleServiceStatus returns the running status of the actr background daemon service.
// Uses DaemonController if configured, otherwise falls back to service.Status().
func (s *Server) handleServiceStatus(w http.ResponseWriter, r *http.Request) {
	var (
		status string
		err    error
	)

	if s.cfg.DaemonController != nil {
		status, err = s.cfg.DaemonController.Status()
	} else {
		status, err = service.Status()
	}

	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, serviceStatusResponse{
		Status: status,
	})
}

// handleServiceStart starts the actr background daemon service.
// Uses DaemonController if configured, otherwise falls back to service.Start().
func (s *Server) handleServiceStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalOrigin(w, r) {
		return
	}
	var err error
	if s.cfg.DaemonController != nil {
		err = s.cfg.DaemonController.Start()
	} else {
		err = service.Start()
	}

	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, serviceActionResponse{
		Status: "started",
	})
}

// handleServiceStop stops the actr background daemon service.
// Uses DaemonController if configured, otherwise falls back to service.Stop().
func (s *Server) handleServiceStop(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalOrigin(w, r) {
		return
	}
	var err error
	if s.cfg.DaemonController != nil {
		err = s.cfg.DaemonController.Stop()
	} else {
		err = service.Stop()
	}

	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, serviceActionResponse{
		Status: "stopped",
	})
}
