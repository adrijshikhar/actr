package web

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/adrijshikhar/actr/internal/monitor"
)

// handleRunLogsStream streams real-time logs and step events for a run via Server-Sent Events (SSE).
func (s *Server) handleRunLogsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	if s.cfg.EventSubscriber == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "event subscriber not configured")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing run id")
		return
	}

	events, unsubscribe, err := s.cfg.EventSubscriber.SubscribeEvents(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if unsubscribe != nil {
		defer unsubscribe()
	}

	// Set SSE response headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.stopCh:
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.JobID != id {
				continue
			}

			switch ev.Type {
			case monitor.EventStepLog:
				line := ev.LogLine
				if s.cfg.Masker != nil {
					line = s.cfg.Masker.Mask(line)
				}
				data, err := json.Marshal(map[string]string{
					"step_id": ev.StepID,
					"line":    line,
				})
				if err == nil {
					if _, err := fmt.Fprintf(w, "event: log\ndata: %s\n\n", data); err != nil {
						return
					}
					flusher.Flush()
				}

			case monitor.EventStepStart:
				var data []byte
				var err error
				if ev.Step != nil {
					data, err = json.Marshal(ev.Step)
				} else {
					data, err = json.Marshal(map[string]any{"step_id": ev.StepID})
				}
				if err == nil {
					if _, err := fmt.Fprintf(w, "event: step_start\ndata: %s\n\n", data); err != nil {
						return
					}
					flusher.Flush()
				}

			case monitor.EventStepFinish:
				var data []byte
				var err error
				if ev.Step != nil {
					data, err = json.Marshal(ev.Step)
				} else {
					data, err = json.Marshal(map[string]any{"step_id": ev.StepID})
				}
				if err == nil {
					if _, err := fmt.Fprintf(w, "event: step_complete\ndata: %s\n\n", data); err != nil {
						return
					}
					flusher.Flush()
				}

			case monitor.EventJobStart:
				var data []byte
				var err error
				if ev.Job != nil {
					data, err = json.Marshal(ev.Job)
				} else {
					data, err = json.Marshal(map[string]any{"job_id": ev.JobID})
				}
				if err == nil {
					if _, err := fmt.Fprintf(w, "event: job_start\ndata: %s\n\n", data); err != nil {
						return
					}
					flusher.Flush()
				}

			case monitor.EventJobFinish:
				var data []byte
				var err error
				if ev.Job != nil {
					data, err = json.Marshal(ev.Job)
				} else {
					data, err = json.Marshal(map[string]any{"job_id": ev.JobID})
				}
				if err == nil {
					if _, err := fmt.Fprintf(w, "event: job_complete\ndata: %s\n\n", data); err != nil {
						return
					}
					flusher.Flush()
				}
				return
			}
		}
	}
}
