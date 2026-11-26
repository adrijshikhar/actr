package web

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/adrijshikhar/actr/internal/history"
)

// RunsListResponse is the response payload for GET /api/runs.
type RunsListResponse struct {
	Runs   []*history.RunRecord `json:"runs"`
	Total  int                  `json:"total"`
	Limit  int                  `json:"limit"`
	Offset int                  `json:"offset"`
}

// LogsResponse is the response payload for GET /api/runs/{id}/logs.
type LogsResponse struct {
	Logs []string `json:"logs"`
}

// handleRuns returns a paginated and filtered list of runs.
func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RunStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "run store not configured")
		return
	}

	limit := 25
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if parsed, err := strconv.Atoi(lStr); err == nil {
			limit = parsed
		}
	}
	if limit < 1 {
		limit = 1
	} else if limit > 100 {
		limit = 100
	}

	offset := 0
	if oStr := r.URL.Query().Get("offset"); oStr != "" {
		if parsed, err := strconv.Atoi(oStr); err == nil {
			offset = parsed
		}
	}
	if offset < 0 {
		offset = 0
	}

	repo := r.URL.Query().Get("repo")
	status := r.URL.Query().Get("status")
	q := r.URL.Query().Get("q")

	runs, total, err := s.cfg.RunStore.GetRuns(r.Context(), limit, offset, repo, status, q)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if runs == nil {
		runs = []*history.RunRecord{}
	}

	writeJSON(w, http.StatusOK, RunsListResponse{
		Runs:   runs,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// handleRunDetail returns detailed metadata and steps for a single run.
func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RunStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "run store not configured")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing run id")
		return
	}

	run, err := s.cfg.RunStore.GetRun(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, history.ErrRunNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found") {
			writeJSONError(w, http.StatusNotFound, "run not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to get run")
		return
	}
	if run == nil {
		writeJSONError(w, http.StatusNotFound, "run not found")
		return
	}

	steps, err := s.cfg.RunStore.GetRunSteps(r.Context(), id)
	if err != nil || steps == nil {
		steps = run.Steps
	}
	if steps == nil {
		steps = []*history.StepRecord{}
	}

	runCopy := *run
	runCopy.Steps = steps

	writeJSON(w, http.StatusOK, &runCopy)
}

// handleRunLogs returns raw or step-filtered logs for a run, with secrets masked.
func (s *Server) handleRunLogs(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RunStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "run store not configured")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing run id")
		return
	}

	run, err := s.cfg.RunStore.GetRun(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, history.ErrRunNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found") {
			writeJSONError(w, http.StatusNotFound, "run not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to get run")
		return
	}
	if run == nil {
		writeJSONError(w, http.StatusNotFound, "run not found")
		return
	}

	stepID := r.URL.Query().Get("step_id")
	var rawLogs []string

	if stepID != "" {
		lines, err := s.cfg.RunStore.GetStepLogs(r.Context(), id, stepID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to get step logs")
			return
		}
		rawLogs = lines
	} else {
		// Aggregate logs across all steps
		steps, err := s.cfg.RunStore.GetRunSteps(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to get run steps")
			return
		}
		if len(steps) == 0 {
			steps = run.Steps
		}

		for _, st := range steps {
			if st == nil {
				continue
			}
			lines, err := s.cfg.RunStore.GetStepLogs(r.Context(), id, st.ID)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, "failed to get step logs")
				return
			}
			if len(lines) > 0 {
				rawLogs = append(rawLogs, lines...)
			}
		}
	}

	if rawLogs == nil {
		rawLogs = []string{}
	}

	logs := make([]string, len(rawLogs))
	if s.cfg.Masker != nil {
		for i, line := range rawLogs {
			logs[i] = s.cfg.Masker.Mask(line)
		}
	} else {
		copy(logs, rawLogs)
	}

	writeJSON(w, http.StatusOK, LogsResponse{
		Logs: logs,
	})
}
