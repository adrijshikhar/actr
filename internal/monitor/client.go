package monitor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Client interacts with the IPC Server over a Unix domain socket.
type Client struct {
	httpClient *http.Client
}

// NewClient creates a new Client configured to communicate over the specified Unix socket path.
func NewClient(sockPath string) *Client {
	return &Client{
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					d := net.Dialer{
						Timeout: 5 * time.Second,
					}
					return d.DialContext(ctx, "unix", sockPath)
				},
				ResponseHeaderTimeout: 10 * time.Second,
			},
		},
	}
}

// GetSnapshot retrieves the current runner state snapshot from the daemon.
func (c *Client) GetSnapshot(ctx context.Context) (*Snapshot, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/snapshot", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch snapshot: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}
	return &snap, nil
}

// StreamEvents opens an NDJSON event stream with the daemon.
// Returns a read-only channel of events and a channel for streaming errors.
func (c *Client) StreamEvents(ctx context.Context) (<-chan Event, <-chan error) {
	events := make(chan Event, 100)
	errs := make(chan error, 1)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/stream", nil)
	if err != nil {
		errs <- err
		close(events)
		close(errs)
		return events, errs
	}

	resp, err := c.httpClient.Do(req) //nolint:bodyclose // Body is closed inside the streaming goroutine on stream completion
	if err != nil {
		errs <- fmt.Errorf("open stream: %w", err)
		close(events)
		close(errs)
		return events, errs
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		errs <- fmt.Errorf("unexpected status %d", resp.StatusCode)
		close(events)
		close(errs)
		return events, errs
	}

	go func() {
		defer close(events)
		defer close(errs)
		defer func() { _ = resp.Body.Close() }()

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, 10*1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var ev Event
			if err := json.Unmarshal(line, &ev); err == nil {
				select {
				case <-ctx.Done():
					return
				case events <- ev:
				}
			}
		}

		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			errs <- err
		}
	}()

	return events, errs
}

// GetHistoricalStepLogs fetches step logs for a completed or historical run from the daemon.
func (c *Client) GetHistoricalStepLogs(ctx context.Context, jobID, stepID string) ([]string, error) {
	if jobID == "" || stepID == "" {
		return nil, errors.New("jobID and stepID cannot be empty")
	}

	endpoint := fmt.Sprintf("http://unix/runs/%s/steps/%s/logs", jobID, stepID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch historical step logs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("step logs not found for job %s step %s: 404", jobID, stepID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d fetching step logs", resp.StatusCode)
	}

	var lines []string
	if err := json.NewDecoder(resp.Body).Decode(&lines); err != nil {
		return nil, fmt.Errorf("decode step logs: %w", err)
	}
	return lines, nil
}

// Reload triggers a dynamic configuration reload on the daemon via POST /reload.
func (c *Client) Reload(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/reload", nil)
	if err != nil {
		return fmt.Errorf("create reload request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send reload request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("reload failed with status %d", resp.StatusCode)
	}
	return nil
}
