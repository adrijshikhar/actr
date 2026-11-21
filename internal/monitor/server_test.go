package monitor

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testSocketPath returns a unix socket path suitable for testing.
// On Darwin, AF_UNIX paths cannot exceed 104 bytes, whereas Darwin's t.TempDir()
// in /var/folders/... easily exceeds 90+ bytes. If t.TempDir() is too long,
// /tmp is used as the base directory.
func testSocketPath(t *testing.T) string {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test_daemon.sock")
	if len(sockPath) < 100 {
		return sockPath
	}

	dir, err := os.MkdirTemp("/tmp", "act-sock-*")
	if err != nil {
		t.Fatalf("failed to create short temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return filepath.Join(dir, "test_daemon.sock")
}

func TestIPCServerAndClient_SnapshotAndStream(t *testing.T) {
	sockPath := testSocketPath(t)

	server := NewServer(sockPath, "test-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	// Verify socket file exists with permissions 0600
	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("socket file missing: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected socket permissions 0600, got %v", info.Mode().Perm())
	}

	client := NewClient(sockPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	snap, err := client.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("failed to get snapshot: %v", err)
	}
	if snap.RunnerName != "test-runner" {
		t.Fatalf("expected runner name test-runner, got %s", snap.RunnerName)
	}

	events, errs := client.StreamEvents(ctx)

	// Publish test event
	expectedEvent := Event{
		Type:   EventStepStart,
		JobID:  "job-1",
		StepID: "step-1",
		Step:   &StepInfo{ID: "step-1", Name: "Clone", Status: StatusRunning},
	}
	server.Publish(expectedEvent)

	select {
	case ev := <-events:
		if ev.Type != EventStepStart || ev.StepID != "step-1" {
			t.Fatalf("unexpected received event: %+v", ev)
		}
	case err := <-errs:
		t.Fatalf("streaming error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestServer_Publish_StateTransitionsAndSnapshot(t *testing.T) {
	sockPath := testSocketPath(t)

	server := NewServer(sockPath, "state-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	client := NewClient(sockPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Initial snapshot: empty active and recent jobs
	snap, err := client.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("failed to get initial snapshot: %v", err)
	}
	if len(snap.ActiveJobs) != 0 || len(snap.RecentJobs) != 0 {
		t.Fatalf("expected 0 active/recent jobs, got %d active, %d recent", len(snap.ActiveJobs), len(snap.RecentJobs))
	}

	// 1. Job Start
	job := &JobInfo{
		ID:       "job-100",
		Name:     "Build and Test",
		Workflow: "ci.yml",
		Repo:     "actr",
		Owner:    "adrijshikhar",
		Status:   StatusRunning,
	}
	server.Publish(Event{
		Type:  EventJobStart,
		JobID: "job-100",
		Job:   job,
	})

	snap, err = client.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("failed to get snapshot after job start: %v", err)
	}
	if len(snap.ActiveJobs) != 1 || snap.ActiveJobs[0].ID != "job-100" {
		t.Fatalf("expected 1 active job 'job-100', got %+v", snap.ActiveJobs)
	}

	// 2. Step Start
	step := &StepInfo{
		ID:     "step-1",
		Name:   "Checkout",
		Status: StatusRunning,
	}
	server.Publish(Event{
		Type:   EventStepStart,
		JobID:  "job-100",
		StepID: "step-1",
		Step:   step,
	})

	// 3. Step Logs via AppendLog
	server.AppendLog("step-1", "Fetching repo...")
	server.AppendLog("step-1", "Checked out main branch.")

	// 4. Telemetry via SetTelemetry
	server.SetTelemetry(&TelemetrySample{
		PID:          1234,
		HostCPU:      45.5,
		HostMemTotal: 16000000000,
		HostMemUsed:  8000000000,
	})

	// 5. Step Finish
	step.Status = StatusSuccess
	server.Publish(Event{
		Type:   EventStepFinish,
		JobID:  "job-100",
		StepID: "step-1",
		Step:   step,
	})

	// Check snapshot mid-run
	snap, err = client.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot mid-run failed: %v", err)
	}
	if len(snap.ActiveJobs) != 1 || len(snap.ActiveJobs[0].Steps) != 1 {
		t.Fatalf("expected 1 active job with 1 step, got %+v", snap.ActiveJobs)
	}
	if snap.ActiveJobs[0].Steps[0].Status != StatusSuccess {
		t.Fatalf("expected step status success, got %v", snap.ActiveJobs[0].Steps[0].Status)
	}
	if logs, ok := snap.StepLogs["step-1"]; !ok || len(logs) != 2 {
		t.Fatalf("expected 2 log lines for step-1, got %+v", snap.StepLogs)
	}
	if snap.Telemetry == nil || snap.Telemetry.PID != 1234 {
		t.Fatalf("expected telemetry with PID 1234, got %+v", snap.Telemetry)
	}

	// 6. Job Finish
	finishedJob := *job
	finishedJob.Status = StatusSuccess
	server.Publish(Event{
		Type:  EventJobFinish,
		JobID: "job-100",
		Job:   &finishedJob,
	})

	snap, err = client.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot after job finish failed: %v", err)
	}
	if len(snap.ActiveJobs) != 0 {
		t.Fatalf("expected 0 active jobs after finish, got %d", len(snap.ActiveJobs))
	}
	if len(snap.RecentJobs) != 1 || snap.RecentJobs[0].Status != StatusSuccess {
		t.Fatalf("expected 1 recent job with StatusSuccess, got %+v", snap.RecentJobs)
	}
}

func TestServer_MultipleSubscribersAndSlowSubscriber(t *testing.T) {
	sockPath := testSocketPath(t)

	server := NewServer(sockPath, "multi-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	client1 := NewClient(sockPath)
	client2 := NewClient(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	events1, errs1 := client1.StreamEvents(ctx)
	events2, errs2 := client2.StreamEvents(ctx)

	if server.SubscriberCount() != 2 {
		t.Fatalf("expected 2 subscribers, got %d", server.SubscriberCount())
	}

	// Publish an event
	ev := Event{
		Type:    EventStepLog,
		StepID:  "step-multi",
		LogLine: "broadcast test",
	}
	server.Publish(ev)

	// Both clients should receive the event
	select {
	case e := <-events1:
		if e.LogLine != "broadcast test" {
			t.Fatalf("client1 unexpected event: %+v", e)
		}
	case err := <-errs1:
		t.Fatalf("client1 stream error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for client1 event")
	}

	select {
	case e := <-events2:
		if e.LogLine != "broadcast test" {
			t.Fatalf("client2 unexpected event: %+v", e)
		}
	case err := <-errs2:
		t.Fatalf("client2 stream error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for client2 event")
	}

	// Verify slow subscriber does not block publish
	// Saturate channel buffer (>100 events)
	for i := 0; i < 150; i++ {
		server.Publish(Event{
			Type:    EventStepLog,
			StepID:  "step-multi",
			LogLine: "spam",
		})
	}
	// Publishing 150 events completed without hanging!
}

func TestServer_StartRemovesExistingSocket(t *testing.T) {
	sockPath := testSocketPath(t)

	// Create a dummy file at the socket path first
	if err := os.WriteFile(sockPath, []byte("stale data"), 0600); err != nil {
		t.Fatalf("failed to create dummy file: %v", err)
	}

	server := NewServer(sockPath, "clean-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("server should start cleanly even if stale socket file existed: %v", err)
	}
	defer func() { _ = server.Stop() }()

	client := NewClient(sockPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	snap, err := client.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("failed to fetch snapshot: %v", err)
	}
	if snap.RunnerName != "clean-runner" {
		t.Fatalf("unexpected runner name %s", snap.RunnerName)
	}
}

func TestServer_StopIdempotentAndCleansUp(t *testing.T) {
	sockPath := testSocketPath(t)

	server := NewServer(sockPath, "stop-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}

	client := NewClient(sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, _ := client.StreamEvents(ctx)

	// Stop server
	if err := server.Stop(); err != nil {
		t.Fatalf("first Stop failed: %v", err)
	}

	// Calling Stop() a second time should be safe and idempotent
	if err := server.Stop(); err != nil {
		t.Fatalf("second Stop failed: %v", err)
	}

	// Stream should end
	select {
	case _, ok := <-events:
		if ok {
			// In case an event was in flight, ensure channel eventually closes
			for range events {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for events channel to close after server stop")
	}

	// Socket file should be removed
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Fatalf("expected socket file to be deleted, but os.Stat returned: %v", err)
	}
}

func TestServer_PublishConcurrency(t *testing.T) {
	sockPath := testSocketPath(t)

	server := NewServer(sockPath, "concurrent-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	var wg sync.WaitGroup
	const goroutines = 20
	const iterations = 50

	for g := int32(0); g < int32(goroutines); g++ {
		wg.Add(1)
		go func(gid int32) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				server.AppendLog("step-c", "log line")
				server.SetTelemetry(&TelemetrySample{PID: gid})
				server.Publish(Event{
					Type:   EventStepStart,
					StepID: "step-c",
				})
			}
		}(g)
	}

	wg.Wait()
}

func TestServer_EmptyActiveJobsSerializesAsArray(t *testing.T) {
	sockPath := testSocketPath(t)

	server := NewServer(sockPath, "test-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	// Add a job and then finish it, leaving activeJobs empty
	server.Publish(Event{
		Type:  EventJobStart,
		JobID: "job-1",
		Job:   &JobInfo{ID: "job-1", Status: StatusRunning},
	})
	server.Publish(Event{
		Type:  EventJobFinish,
		JobID: "job-1",
		Job:   &JobInfo{ID: "job-1", Status: StatusSuccess},
	})

	// Make raw HTTP request to /snapshot
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
	}
	httpClient := &http.Client{Transport: tr}
	resp, err := httpClient.Get("http://unix/snapshot")
	if err != nil {
		t.Fatalf("snapshot request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read snapshot body: %v", err)
	}

	bodyStr := string(body)
	if !strings.Contains(bodyStr, `"active_jobs":[]`) {
		t.Errorf("expected active_jobs to serialize as '[]', got raw JSON: %s", bodyStr)
	}
	if strings.Contains(bodyStr, `"active_jobs":null`) {
		t.Errorf("active_jobs serialized as null: %s", bodyStr)
	}
}

func TestServer_Start_SocketAlreadyInUse(t *testing.T) {
	sockPath := testSocketPath(t)

	server1 := NewServer(sockPath, "runner-1")
	if err := server1.Start(); err != nil {
		t.Fatalf("failed to start server1: %v", err)
	}
	defer func() { _ = server1.Stop() }()

	server2 := NewServer(sockPath, "runner-2")
	err := server2.Start()
	if err == nil {
		_ = server2.Stop()
		t.Fatalf("expected error starting server2 on active socket, got nil")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Errorf("expected 'already in use' error, got %v", err)
	}

	// Verify server1 socket is still intact and reachable
	client := NewClient(sockPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snap, err := client.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("server1 socket was corrupted by server2: %v", err)
	}
	if snap.RunnerName != "runner-1" {
		t.Errorf("expected runner name 'runner-1', got %s", snap.RunnerName)
	}
}

func TestServer_Publish_SlowSubscriberChannelClosed(t *testing.T) {
	sockPath := testSocketPath(t)

	server := NewServer(sockPath, "test-slow-sub")
	if err := server.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	client := NewClient(sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, errs := client.StreamEvents(ctx)

	// Consume the first event to establish connection
	server.Publish(Event{Type: EventStepStart, JobID: "j1", StepID: "s1"})
	select {
	case <-events:
	case err := <-errs:
		t.Fatalf("stream err: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial event")
	}

	// Flood publish without reading to fill buffer (channel capacity is 100)
	for i := 0; i < 500; i++ {
		server.Publish(Event{Type: EventStepLog, StepID: "s1", LogLine: fmt.Sprintf("line %d", i)})
	}

	// The subscriber channel should be closed when full
	closed := false
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()

	for !closed {
		select {
		case _, ok := <-events:
			if !ok {
				closed = true
			}
		case <-timer.C:
			t.Fatal("timed out waiting for slow subscriber stream to close")
		}
	}
}

func TestServer_JobTelemetryAttribution(t *testing.T) {
	sockPath := testSocketPath(t)
	s := NewServer(sockPath, "actr • test")

	j1 := &JobInfo{ID: "job-1", Repo: "adrijshikhar/aim", RepoAlias: "aim", Name: "CI"}
	j2 := &JobInfo{ID: "job-2", Repo: "adrijshikhar/cxstatusline", RepoAlias: "cxstatusline", Name: "Vitest"}

	s.Publish(Event{Type: EventJobStart, Job: j1})
	s.Publish(Event{Type: EventJobStart, Job: j2})

	sample1 := &TelemetrySample{PID: 100, StepCPU: 45.0, StepRSSBytes: 500 * 1024 * 1024}
	s.Publish(Event{Type: EventTelemetry, JobID: "job-1", Telemetry: sample1})

	peak1 := s.PeakMetricsForJob("job-1")
	if peak1 == nil || peak1.PeakCPUPercent != 45.0 {
		t.Errorf("expected peak CPU 45.0 for job-1, got %v", peak1)
	}

	peak2 := s.PeakMetricsForJob("job-2")
	if peak2 != nil && peak2.PeakCPUPercent == 45.0 {
		t.Errorf("job-2 peak CPU was cross-contaminated by job-1 telemetry: %v", peak2)
	}
}

func TestServer_SupervisorStatusAndReload(t *testing.T) {
	sockPath := testSocketPath(t)
	s := NewServer(sockPath, "actr • multi-repo")

	reloaded := false
	s.WithReloadFunc(func() error {
		reloaded = true
		return nil
	})

	status := &SupervisorStatus{
		GlobalMaxJobs: 2,
		GlobalActive:  1,
		RepoSlots: []*RepoSlotInfo{
			{Alias: "aim", MaxJobs: 1, ActiveJobs: 1, Driver: "host", Status: "busy"},
			{Alias: "cxstatusline", MaxJobs: 1, ActiveJobs: 0, Driver: "host", Status: "idle"},
		},
	}
	s.SetSupervisorStatus(status)

	if err := s.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() { _ = s.Stop() }()

	client := NewClient(sockPath)
	snap, err := client.GetSnapshot(context.Background())
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	if snap.SupervisorStatus == nil || snap.SupervisorStatus.GlobalMaxJobs != 2 {
		t.Fatalf("expected supervisor status with GlobalMaxJobs 2, got %v", snap.SupervisorStatus)
	}

	// Test POST /reload over unix socket HTTP
	httpc := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
	resp, err := httpc.Post("http://unix/reload", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /reload failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /reload, got %d", resp.StatusCode)
	}
	if !reloaded {
		t.Fatal("expected reloadFn to be called")
	}

	// Test POST /reload error returns 500
	s.WithReloadFunc(func() error {
		return fmt.Errorf("simulated reload failure")
	})
	respErr, err := httpc.Post("http://unix/reload", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /reload failed: %v", err)
	}
	defer func() { _ = respErr.Body.Close() }()
	if respErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 InternalServerError from /reload, got %d", respErr.StatusCode)
	}

	// Test POST /reload when reloadFn is nil returns 501
	s.WithReloadFunc(nil)
	respNil, err := httpc.Post("http://unix/reload", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /reload failed: %v", err)
	}
	defer func() { _ = respNil.Body.Close() }()
	if respNil.StatusCode != http.StatusNotImplemented {
		t.Fatalf("expected 501 NotImplemented from /reload, got %d", respNil.StatusCode)
	}
}

func TestServer_SetJobTelemetry(t *testing.T) {
	sockPath := testSocketPath(t)
	s := NewServer(sockPath, "actr • test")

	s.Publish(Event{
		Type: EventJobStart,
		Job: &JobInfo{
			ID:   "job-test-telemetry",
			Repo: "adrijshikhar/aim",
			Name: "CI",
		},
	})

	sample := &TelemetrySample{
		PID:          1234,
		StepCPU:      33.5,
		StepRSSBytes: 250 * 1024 * 1024,
	}
	s.SetJobTelemetry("job-test-telemetry", "aim", sample)

	peak := s.PeakMetricsForJob("job-test-telemetry")
	if peak == nil || peak.PeakCPUPercent != 33.5 || peak.PeakRSSBytes != 250*1024*1024 {
		t.Fatalf("unexpected peak metrics: %v", peak)
	}

	s.mu.RLock()
	job := s.jobs["job-test-telemetry"]
	s.mu.RUnlock()
	if job == nil || job.RepoAlias != "aim" {
		t.Fatalf("expected job RepoAlias 'aim', got %v", job)
	}
}

func TestServer_EventJobStart_ClearsPriorStepLogs(t *testing.T) {
	s := NewServer("", "test-runner")
	s.AppendJobStepLog("job-1", "step-1", "old log line 1")
	s.AppendJobStepLog("job-1", "step-2", "old log line 2")
	s.AppendJobStepLog("job-2", "step-1", "unrelated job log")

	s.mu.RLock()
	rb1 := s.stepLogs["job-1/step-1"]
	s.mu.RUnlock()
	if rb1 == nil || len(rb1.Lines()) == 0 {
		t.Fatal("expected logs for job-1/step-1 before restart")
	}

	// Re-start job-1
	s.Publish(Event{
		Type:  EventJobStart,
		JobID: "job-1",
		Job: &JobInfo{
			ID:   "job-1",
			Name: "job-1",
		},
	})

	s.mu.RLock()
	rb1After := s.stepLogs["job-1/step-1"]
	rb2 := s.stepLogs["job-2/step-1"]
	s.mu.RUnlock()

	if rb1After != nil && len(rb1After.Lines()) != 0 {
		t.Fatalf("expected logs for job-1/step-1 to be cleared, got %v", rb1After.Lines())
	}
	if rb2 == nil || len(rb2.Lines()) == 0 {
		t.Fatal("expected logs for job-2/step-1 to remain intact")
	}
}
