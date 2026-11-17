package tui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/monitor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestDashboardModel_ZoomToggle(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")
	m.width = 120
	m.height = 40

	if m.isZoomed {
		t.Fatal("expected dashboard initially not zoomed")
	}

	// Press 'z'
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = newModel.(*DashboardModel)

	if !m.isZoomed {
		t.Fatal("expected dashboard to be zoomed after 'z'")
	}

	// Press 'z' again
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = newModel.(*DashboardModel)

	if m.isZoomed {
		t.Fatal("expected dashboard to un-zoom after second 'z'")
	}
}

func TestDashboardModel_WindowSizeAndAutoFollow(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")

	if m.ready {
		t.Fatal("expected dashboard initially not ready")
	}
	if !m.autoFollow {
		t.Fatal("expected autoFollow initially true")
	}

	// Send WindowSizeMsg
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	m = newModel.(*DashboardModel)

	if !m.ready {
		t.Fatal("expected dashboard ready after WindowSizeMsg")
	}
	if m.width != 100 || m.height != 50 {
		t.Fatalf("expected width 100 height 50, got width %d height %d", m.width, m.height)
	}

	// Press 'f' to toggle autoFollow
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = newModel.(*DashboardModel)
	if m.autoFollow {
		t.Fatal("expected autoFollow to be false after 'f'")
	}

	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = newModel.(*DashboardModel)
	if !m.autoFollow {
		t.Fatal("expected autoFollow to be true after second 'f'")
	}
}

func TestDashboardModel_TreeNavigation(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newModel.(*DashboardModel)

	snap := &monitor.Snapshot{
		RunnerName: "test-runner",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:     "job-1",
				Name:   "Build and Test",
				Status: monitor.StatusRunning,
				Steps: []*monitor.StepInfo{
					{ID: "step-1", Name: "Setup Go", Status: monitor.StatusSuccess},
					{ID: "step-2", Name: "Run Tests", Status: monitor.StatusRunning},
				},
			},
		},
		StepLogs: map[string][]string{
			"step-1": {"Setup Go 1.23", "Installed tools"},
			"step-2": {"Running tests...", "ok github.com/test"},
		},
	}

	newModel, _ = m.Update(SnapshotMsg(snap))
	m = newModel.(*DashboardModel)

	// Initially at root
	if m.tree.SelectedNode().ID != "root" {
		t.Fatalf("expected root selected, got %s", m.tree.SelectedNode().ID)
	}

	// Down to live-runs branch
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = newModel.(*DashboardModel)
	if m.tree.SelectedNode().ID != "live-runs" {
		t.Fatalf("expected live-runs selected, got %s", m.tree.SelectedNode().ID)
	}

	// Down to job-1
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = newModel.(*DashboardModel)
	if m.tree.SelectedNode().ID != "job-1" {
		t.Fatalf("expected job-1 selected, got %s", m.tree.SelectedNode().ID)
	}

	// Down to step-1
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(*DashboardModel)
	if m.tree.SelectedNode().ID != "step-1" {
		t.Fatalf("expected step-1 selected, got %s", m.tree.SelectedNode().ID)
	}
	if m.activeStepID != "step-1" {
		t.Fatalf("expected activeStepID 'step-1', got %s", m.activeStepID)
	}
	if !strings.Contains(m.viewport.View(), "Setup Go 1.23") {
		t.Fatalf("expected viewport to show step-1 logs, got: %q", m.viewport.View())
	}

	// Down to step-2
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = newModel.(*DashboardModel)
	if m.tree.SelectedNode().ID != "step-2" {
		t.Fatalf("expected step-2 selected, got %s", m.tree.SelectedNode().ID)
	}
	if m.activeStepID != "step-2" {
		t.Fatalf("expected activeStepID 'step-2', got %s", m.activeStepID)
	}
	if !strings.Contains(m.viewport.View(), "Running tests...") {
		t.Fatalf("expected viewport to show step-2 logs, got: %q", m.viewport.View())
	}

	// Up to step-1
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = newModel.(*DashboardModel)
	if m.tree.SelectedNode().ID != "step-1" {
		t.Fatalf("expected step-1 selected after 'k', got %s", m.tree.SelectedNode().ID)
	}

	// Up to job-1
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = newModel.(*DashboardModel)
	if m.tree.SelectedNode().ID != "job-1" {
		t.Fatalf("expected job-1 selected after 'up', got %s", m.tree.SelectedNode().ID)
	}

	// Collapse job-1 with 'h'
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = newModel.(*DashboardModel)
	jobNode := m.tree.SelectedNode()
	if jobNode.Expanded {
		t.Fatal("expected job-1 collapsed after 'h'")
	}

	// Expand job-1 with 'l'
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = newModel.(*DashboardModel)
	if !jobNode.Expanded {
		t.Fatal("expected job-1 expanded after 'l'")
	}

	// Toggle job-1 with Enter
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newModel.(*DashboardModel)
	if jobNode.Expanded {
		t.Fatal("expected job-1 collapsed after Enter")
	}

	// Toggle job-1 with Space
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if !jobNode.Expanded {
		t.Fatal("expected job-1 expanded after Space")
	}
}

func TestDashboardModel_QuitDetachesCleanly(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")
	cancelled := false
	m.cancel = func() {
		cancelled = true
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if !cancelled {
		t.Fatal("expected cancel called on 'q'")
	}
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd returned on 'q'")
	}

	// Test ctrl+c
	cancelled = false
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !cancelled {
		t.Fatal("expected cancel called on ctrl+c")
	}
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd returned on ctrl+c")
	}
}

func TestDashboardModel_HandleEvents(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = newModel.(*DashboardModel)

	// Initial snapshot
	snap := &monitor.Snapshot{
		RunnerName: "test-runner",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:   "job-1",
				Name: "Build",
				Steps: []*monitor.StepInfo{
					{ID: "step-1", Name: "Compile", Status: monitor.StatusRunning},
				},
			},
		},
	}
	newModel, _ = m.Update(SnapshotMsg(snap))
	m = newModel.(*DashboardModel)

	// Select step-1
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}) // live-runs
	m = newModel.(*DashboardModel)
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}) // job-1
	m = newModel.(*DashboardModel)
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}) // step-1
	m = newModel.(*DashboardModel)

	// Send EventTelemetry
	sample := &monitor.TelemetrySample{
		PID:          1234,
		PGID:         1234,
		StepCPU:      42.5,
		StepRSSBytes: 256 * 1024 * 1024,
	}
	newModel, _ = m.Update(EventMsg(monitor.Event{
		Type:      monitor.EventTelemetry,
		Telemetry: sample,
	}))
	m = newModel.(*DashboardModel)

	if m.telemetry.Sample() == nil || m.telemetry.Sample().PID != 1234 {
		t.Fatalf("expected telemetry PID 1234, got %+v", m.telemetry.Sample())
	}

	// Send EventStepLog
	newModel, _ = m.Update(EventMsg(monitor.Event{
		Type:    monitor.EventStepLog,
		StepID:  "step-1",
		LogLine: "compiling package main",
	}))
	m = newModel.(*DashboardModel)

	if !strings.Contains(m.viewport.View(), "compiling package main") {
		t.Fatalf("expected viewport to contain log line, got: %q", m.viewport.View())
	}

	// Send EventStepFinish
	newModel, _ = m.Update(EventMsg(monitor.Event{
		Type:   monitor.EventStepFinish,
		JobID:  "job-1",
		StepID: "step-1",
		Step: &monitor.StepInfo{
			ID:     "step-1",
			Name:   "Compile",
			Status: monitor.StatusSuccess,
		},
	}))
	m = newModel.(*DashboardModel)

	treeView := m.tree.View(30)
	if !strings.Contains(treeView, "✓ Compile") {
		t.Fatalf("expected tree view to show '✓ Compile', got:\n%s", treeView)
	}
}

func TestDashboardModel_StepLabelPreservationAndEmptyJobID(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = newModel.(*DashboardModel)

	// Snapshot with single active job
	snap := &monitor.Snapshot{
		RunnerName: "test-runner",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:     "job-main",
				Name:   "CI Pipeline",
				Status: monitor.StatusRunning,
			},
		},
	}
	newModel, _ = m.Update(SnapshotMsg(snap))
	m = newModel.(*DashboardModel)

	// coordinator.NotifyStepStart emits StepStart without JobID
	newModel, _ = m.Update(EventMsg(monitor.Event{
		Type:   monitor.EventStepStart,
		StepID: "step-lint",
		Step: &monitor.StepInfo{
			ID:     "step-lint",
			Name:   "Run Linter",
			Status: monitor.StatusRunning,
		},
	}))
	m = newModel.(*DashboardModel)

	// Ensure no phantom node with empty ID or label exists
	m.tree.Root.Walk(func(n *TreeNode) bool {
		if n.ID == "" {
			t.Fatalf("detected phantom node with empty ID: %+v", n)
		}
		return true
	})

	treeView := m.tree.View(40)
	if !strings.Contains(treeView, "● Run Linter") {
		t.Fatalf("expected tree view to show '● Run Linter', got:\n%s", treeView)
	}

	// coordinator.NotifyStepFinish emits StepFinish with empty Name
	newModel, _ = m.Update(EventMsg(monitor.Event{
		Type:   monitor.EventStepFinish,
		StepID: "step-lint",
		Step: &monitor.StepInfo{
			ID:     "step-lint",
			Status: monitor.StatusSuccess,
			// Name is intentionally omitted ("") as in coordinator.NotifyStepFinish
		},
	}))
	m = newModel.(*DashboardModel)

	treeViewAfterFinish := m.tree.View(40)
	if !strings.Contains(treeViewAfterFinish, "✓ Run Linter") {
		t.Fatalf("expected step label 'Run Linter' to be preserved after finish, got:\n%s", treeViewAfterFinish)
	}
}

func TestDashboardModel_ViewportHeightAndLayoutAlignment(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = newModel.(*DashboardModel)

	// In unzoomed mode: targetOuterHeight = 39, telemOuterHeight = 6 -> logInnerHeight = 31
	expectedUnzoomedHeight := 31
	if m.viewport.Height != expectedUnzoomedHeight {
		t.Fatalf("expected unzoomed viewport height %d, got %d", expectedUnzoomedHeight, m.viewport.Height)
	}
	expectedUnzoomedWidth := 120 - 28 - 4
	if m.viewport.Width != expectedUnzoomedWidth {
		t.Fatalf("expected unzoomed viewport width %d, got %d", expectedUnzoomedWidth, m.viewport.Width)
	}

	// Verify unzoomed View fills the full terminal height
	viewLines := strings.Split(m.View(), "\n")
	if len(viewLines) != 40 {
		t.Fatalf("expected unzoomed view to fill exactly 40 lines, got %d", len(viewLines))
	}

	// In zoomed mode: targetOuterHeight = 39 -> zoomHeight = 37
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = newModel.(*DashboardModel)
	expectedZoomedHeight := 37
	if m.viewport.Height != expectedZoomedHeight {
		t.Fatalf("expected zoomed viewport height %d, got %d", expectedZoomedHeight, m.viewport.Height)
	}
	expectedZoomedWidth := 118
	if m.viewport.Width != expectedZoomedWidth {
		t.Fatalf("expected zoomed viewport width %d, got %d", expectedZoomedWidth, m.viewport.Width)
	}

	// Verify zoomed View fills the full terminal height
	zoomViewLines := strings.Split(m.View(), "\n")
	if len(zoomViewLines) != 40 {
		t.Fatalf("expected zoomed view to fill exactly 40 lines, got %d", len(zoomViewLines))
	}

	// Test responsive adaptation across multiple window sizes
	sizes := []struct {
		width  int
		height int
	}{
		{width: 50, height: 16},
		{width: 80, height: 24},
		{width: 100, height: 30},
		{width: 160, height: 50},
	}

	for _, sz := range sizes {
		testM := NewDashboardModel("/tmp/nonexistent.sock")
		resModel, _ := testM.Update(tea.WindowSizeMsg{Width: sz.width, Height: sz.height})
		testM = resModel.(*DashboardModel)

		treeWidth, rightWidth, _, logHeight := testM.calcDimensions()
		if treeWidth+rightWidth+4 > sz.width {
			t.Fatalf("sz %dx%d: horizontal overflow: treeWidth %d + rightWidth %d + 4 > width %d",
				sz.width, sz.height, treeWidth, rightWidth, sz.width)
		}
		if logHeight < 1 {
			t.Fatalf("sz %dx%d: logHeight %d < 1", sz.width, sz.height, logHeight)
		}

		view := testM.View()
		renderedLines := strings.Split(view, "\n")
		if len(renderedLines) != sz.height {
			t.Fatalf("sz %dx%d: expected view to fill %d lines, got %d",
				sz.width, sz.height, sz.height, len(renderedLines))
		}
	}
}

func TestDashboardModel_UnzoomedScrollingAndErrorFooter(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = newModel.(*DashboardModel)

	// Set logs on active step
	m.stepLogs["root"] = make([]string, 100)
	for i := 0; i < 100; i++ {
		m.stepLogs["root"][i] = fmt.Sprintf("Log line %d", i)
	}
	m.updateActiveStepLogs()

	// Verify unzoomed page scrolling keys pass through to viewport
	for _, key := range []string{"pgdown", "pagedown", "ctrl+d", "pgup", "pageup", "ctrl+u"} {
		var msg tea.KeyMsg
		switch key {
		case "ctrl+d":
			msg = tea.KeyMsg{Type: tea.KeyCtrlD}
		case "ctrl+u":
			msg = tea.KeyMsg{Type: tea.KeyCtrlU}
		case "pgdown":
			msg = tea.KeyMsg{Type: tea.KeyPgDown}
		case "pgup":
			msg = tea.KeyMsg{Type: tea.KeyPgUp}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		}
		newModel, _ = m.Update(msg)
		m = newModel.(*DashboardModel)
	}

	// Test ErrMsg display in footer
	newModel, _ = m.Update(ErrMsg(fmt.Errorf("connection refused")))
	m = newModel.(*DashboardModel)

	if m.lastErr == nil {
		t.Fatal("expected lastErr to be populated")
	}
	viewWithError := m.View()
	if !strings.Contains(viewWithError, "Error: connection refused") {
		t.Fatalf("expected footer to display error, got:\n%s", viewWithError)
	}

	// Test SnapshotMsg clears lastErr
	newModel, _ = m.Update(SnapshotMsg(&monitor.Snapshot{RunnerName: "reconnected-runner"}))
	m = newModel.(*DashboardModel)
	if m.lastErr != nil {
		t.Fatal("expected lastErr to be cleared after SnapshotMsg")
	}
	viewAfterReconnect := m.View()
	if strings.Contains(viewAfterReconnect, "Error: connection refused") {
		t.Fatalf("expected footer error to be removed after reconnection, got:\n%s", viewAfterReconnect)
	}
}

func TestDashboardModel_View(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")

	// Not ready
	if m.View() != "Initializing dashboard..." {
		t.Fatalf("expected 'Initializing dashboard...' when not ready, got %q", m.View())
	}

	// Set ready via WindowSizeMsg
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = newModel.(*DashboardModel)

	v := m.View()
	if !strings.Contains(v, "actr") {
		t.Fatalf("expected normal view to contain 'actr', got:\n%s", v)
	}
	if !strings.Contains(v, "z zoom") {
		t.Fatalf("expected normal view to contain footer instructions, got:\n%s", v)
	}

	// Zoom view
	m.isZoomed = true
	m.updateSizes()
	zoomView := m.View()
	if !strings.Contains(zoomView, "z zoom") {
		t.Fatalf("expected zoom view to contain footer, got:\n%s", zoomView)
	}
}

func TestDashboardModel_InitAndStreaming_Live(t *testing.T) {
	sockPath := testSocketPath(t)

	server := monitor.NewServer(sockPath, "test-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start monitor server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	m := NewDashboardModel(sockPath)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected Init() to return batch command")
	}

	if m.ctx == nil {
		t.Fatal("expected Init() to set m.ctx")
	}

	// Execute waitForEvents against real server
	streamCmd := m.waitForEvents(m.ctx)
	receivedMsgChan := make(chan tea.Msg, 1)

	go func() {
		msg := streamCmd()
		receivedMsgChan <- msg
	}()

	// Give the client stream connection a brief moment to connect to Unix socket
	time.Sleep(100 * time.Millisecond)

	server.Publish(monitor.Event{
		Type:    monitor.EventStepLog,
		StepID:  "step-live-1",
		LogLine: "live streamed message from test",
	})

	select {
	case msg := <-receivedMsgChan:
		evMsg, ok := msg.(EventMsg)
		if !ok {
			t.Fatalf("expected EventMsg from stream, got: %T (%+v)", msg, msg)
		}
		if evMsg.LogLine != "live streamed message from test" {
			t.Fatalf("expected log line 'live streamed message from test', got: %q", evMsg.LogLine)
		}

		// Update model with the real event
		newModel, nextCmd := m.Update(evMsg)
		m = newModel.(*DashboardModel)
		if len(m.stepLogs["step-live-1"]) != 1 || m.stepLogs["step-live-1"][0] != "live streamed message from test" {
			t.Fatalf("expected model to have log stored, got: %v", m.stepLogs["step-live-1"])
		}
		if nextCmd == nil {
			t.Fatal("expected Update(EventMsg) to return next waitForEvents command")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for live streamed event from monitor.Server")
	}

	// Test clean cancellation of stream
	m.cancel()
	cancelCmd := m.waitForEvents(m.ctx)
	cancelledMsgChan := make(chan tea.Msg, 1)
	go func() {
		cancelledMsgChan <- cancelCmd()
	}()

	select {
	case msg := <-cancelledMsgChan:
		if msg != nil {
			t.Fatalf("expected nil message after context cancellation, got: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waitForEvents to return nil after context cancellation")
	}
}

func TestDashboardModel_LogCapAt10000(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")

	// 1. Snapshot with > 10,000 lines
	excessLogs := make([]string, 10050)
	for i := range excessLogs {
		excessLogs[i] = fmt.Sprintf("line %d", i)
	}

	snap := &monitor.Snapshot{
		RunnerName: "test-runner",
		StepLogs: map[string][]string{
			"step-overflow": excessLogs,
		},
	}

	newModel, _ := m.Update(SnapshotMsg(snap))
	m = newModel.(*DashboardModel)

	if len(m.stepLogs["step-overflow"]) != 10000 {
		t.Fatalf("expected stepLogs capped at 10000, got %d", len(m.stepLogs["step-overflow"]))
	}
	// Verify it kept the most recent lines
	if m.stepLogs["step-overflow"][0] != "line 50" {
		t.Fatalf("expected first line to be 'line 50', got %q", m.stepLogs["step-overflow"][0])
	}
	if m.stepLogs["step-overflow"][9999] != "line 10049" {
		t.Fatalf("expected last line to be 'line 10049', got %q", m.stepLogs["step-overflow"][9999])
	}

	// 2. Incremental EventStepLog pushing beyond 10,000
	for i := 10050; i < 10060; i++ {
		ev := monitor.Event{
			Type:    monitor.EventStepLog,
			StepID:  "step-overflow",
			LogLine: fmt.Sprintf("line %d", i),
		}
		newModel, _ = m.Update(EventMsg(ev))
		m = newModel.(*DashboardModel)
	}

	if len(m.stepLogs["step-overflow"]) != 10000 {
		t.Fatalf("expected stepLogs still capped at 10000 after appending events, got %d", len(m.stepLogs["step-overflow"]))
	}
	if m.stepLogs["step-overflow"][9999] != "line 10059" {
		t.Fatalf("expected last line to be 'line 10059', got %q", m.stepLogs["step-overflow"][9999])
	}
}

func TestDashboardModel_ReconnectOnError(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected non-nil Init command")
	}

	// Send ErrMsg to model, verifying it sets lastErr and returns reconnect tick cmd
	newModel, tickCmd := m.Update(ErrMsg(fmt.Errorf("connection refused")))
	m = newModel.(*DashboardModel)

	if m.lastErr == nil || m.lastErr.Error() != "connection refused" {
		t.Fatalf("expected lastErr 'connection refused', got %v", m.lastErr)
	}
	if tickCmd == nil {
		t.Fatal("expected non-nil tickCmd scheduled on ErrMsg")
	}

	// Send reconnectMsg, verifying it attempts snapshot fetch
	_, reconnCmd := m.Update(reconnectMsg{})
	if reconnCmd == nil {
		t.Fatal("expected non-nil reconnect command to fetch snapshot")
	}
}

func TestDashboardModel_DualSegmentFooter(t *testing.T) {
	t.Run("wide format with metrics", func(t *testing.T) {
		m := NewDashboardModel("/tmp/nonexistent.sock")
		resModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		m = resModel.(*DashboardModel)

		m.telemetry.Update(&RunnerMetrics{
			HostCPU:       16.8,
			HostMemUsed:   14 * 1024 * 1024 * 1024,
			HostMemTotal:  32 * 1024 * 1024 * 1024,
			DiskFreeBytes: 142 * 1024 * 1024 * 1024,
			HostLoad1m:    1.42,
		})

		view := m.View()
		lines := strings.Split(view, "\n")
		footer := lines[len(lines)-1]

		if !strings.Contains(footer, "HOST:") || !strings.Contains(footer, "16.8%") {
			t.Fatalf("expected host CPU in footer, got:\n%s", footer)
		}
		if !strings.Contains(footer, "142 GB") {
			t.Fatalf("expected disk free in footer, got:\n%s", footer)
		}
		if !strings.Contains(footer, "44% RAM") {
			t.Fatalf("expected RAM pct in footer, got:\n%s", footer)
		}
		if !strings.Contains(footer, "Load: 1.42") {
			t.Fatalf("expected Load in footer, got:\n%s", footer)
		}
		if !strings.Contains(footer, "↑/k up") {
			t.Fatalf("expected navigation shortcuts in footer, got:\n%s", footer)
		}
	})

	t.Run("medium format (70 <= width < 100)", func(t *testing.T) {
		m := NewDashboardModel("/tmp/nonexistent.sock")
		resModel, _ := m.Update(tea.WindowSizeMsg{Width: 85, Height: 30})
		m = resModel.(*DashboardModel)

		m.telemetry.Update(&RunnerMetrics{
			HostCPU:       16.8,
			HostMemUsed:   14 * 1024 * 1024 * 1024,
			HostMemTotal:  32 * 1024 * 1024 * 1024,
			DiskFreeBytes: 142 * 1024 * 1024 * 1024,
			HostLoad1m:    1.42,
		})

		view := m.View()
		lines := strings.Split(view, "\n")
		footer := lines[len(lines)-1]

		// Medium width should use: HOST: %.0f%%C • %.0f%%M • %s
		if !strings.Contains(footer, "HOST: 17%C • 44%M • 142 GB") {
			t.Fatalf("expected compact medium host vitals in footer, got:\n%s", footer)
		}
		if strings.Contains(footer, "Load:") {
			t.Fatalf("medium width should not contain Load:, got:\n%s", footer)
		}
	})

	t.Run("narrow format (< 70) collapses right segment", func(t *testing.T) {
		m := NewDashboardModel("/tmp/nonexistent.sock")
		resModel, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
		m = resModel.(*DashboardModel)

		m.telemetry.Update(&RunnerMetrics{
			HostCPU:       16.8,
			HostMemUsed:   14 * 1024 * 1024 * 1024,
			HostMemTotal:  32 * 1024 * 1024 * 1024,
			DiskFreeBytes: 142 * 1024 * 1024 * 1024,
			HostLoad1m:    1.42,
		})

		view := m.View()
		lines := strings.Split(view, "\n")
		footer := lines[len(lines)-1]

		if strings.Contains(footer, "HOST:") {
			t.Fatalf("expected footer to collapse right segment when width < 70, got:\n%s", footer)
		}
		if !strings.Contains(footer, "↑/k up") {
			t.Fatalf("expected navigation shortcuts in footer, got:\n%s", footer)
		}
	})

	t.Run("nil telemetry collapses right segment", func(t *testing.T) {
		m := NewDashboardModel("/tmp/nonexistent.sock")
		resModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		m = resModel.(*DashboardModel)

		view := m.View()
		lines := strings.Split(view, "\n")
		footer := lines[len(lines)-1]

		if strings.Contains(footer, "HOST:") {
			t.Fatalf("expected footer to collapse right segment when sample is nil, got:\n%s", footer)
		}
		if !strings.Contains(footer, "↑/k up") {
			t.Fatalf("expected navigation shortcuts in footer, got:\n%s", footer)
		}
	})

	t.Run("error displayed in left segment", func(t *testing.T) {
		m := NewDashboardModel("/tmp/nonexistent.sock")
		resModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		m = resModel.(*DashboardModel)
		m.lastErr = fmt.Errorf("connection lost")

		m.telemetry.Update(&RunnerMetrics{
			HostCPU:       16.8,
			HostMemUsed:   14 * 1024 * 1024 * 1024,
			HostMemTotal:  32 * 1024 * 1024 * 1024,
			DiskFreeBytes: 142 * 1024 * 1024 * 1024,
			HostLoad1m:    1.42,
		})

		view := m.View()
		lines := strings.Split(view, "\n")
		footer := lines[len(lines)-1]

		if !strings.Contains(footer, "Error: connection lost") {
			t.Fatalf("expected lastErr in footer, got:\n%s", footer)
		}
		if !strings.Contains(footer, "HOST:") {
			t.Fatalf("expected host vitals still rendered on right, got:\n%s", footer)
		}
	})

	t.Run("footer never exceeds terminal width across range of sizes", func(t *testing.T) {
		widths := []int{50, 60, 70, 75, 80, 85, 90, 95, 100, 105, 110, 115, 120, 130, 160}
		for _, w := range widths {
			m := NewDashboardModel("/tmp/nonexistent.sock")
			resModel, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
			m = resModel.(*DashboardModel)

			m.telemetry.Update(&RunnerMetrics{
				HostCPU:       16.8,
				HostMemUsed:   14 * 1024 * 1024 * 1024,
				HostMemTotal:  32 * 1024 * 1024 * 1024,
				DiskFreeBytes: 142 * 1024 * 1024 * 1024,
				HostLoad1m:    1.42,
			})

			view := m.View()
			lines := strings.Split(view, "\n")
			footer := lines[len(lines)-1]
			footerWidth := lipgloss.Width(footer)
			if footerWidth > w {
				t.Fatalf("at width %d: footer width %d exceeds window width:\n%q", w, footerWidth, footer)
			}
		}
	})
}

func TestDashboardModel_AutoFocusRunningStep(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")
	m.AutoFocusRunningStep()
	resModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = resModel.(*DashboardModel)

	snap := &monitor.Snapshot{
		RunnerName: "m5-pro",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:     "job-1",
				Name:   "release-prebuilt",
				Status: monitor.StatusRunning,
				Steps: []*monitor.StepInfo{
					{ID: "step-1", Name: "Verify prerequisites", Status: monitor.StatusSuccess},
					{ID: "step-2", Name: "Publish GitHub Release", Status: monitor.StatusRunning},
				},
			},
		},
		StepLogs: map[string][]string{
			"step-2": {"Uploading asset 100%...", "Release published!"},
		},
	}

	newModel, _ := m.Update(SnapshotMsg(snap))
	m = newModel.(*DashboardModel)

	sel := m.tree.SelectedNode()
	if sel == nil || sel.ID != "step-2" {
		t.Fatalf("expected running step-2 auto-selected, got: %+v", sel)
	}
	if !strings.Contains(m.viewport.View(), "Release published!") {
		t.Fatalf("expected step-2 logs in viewport, got: %q", m.viewport.View())
	}
}

func TestDashboardModel_ActiveRunnerOverview(t *testing.T) {
	m := NewDashboardModel("/tmp/actr-test.sock")
	resModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = resModel.(*DashboardModel)

	snap := &monitor.Snapshot{
		RunnerName: "m5-pro",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:     "job-1",
				Name:   "release-prebuilt",
				Status: monitor.StatusRunning,
				Steps: []*monitor.StepInfo{
					{ID: "step-1", Name: "Publish GitHub Release", Status: monitor.StatusRunning},
				},
			},
		},
	}

	newModel, _ := m.Update(SnapshotMsg(snap))
	m = newModel.(*DashboardModel)

	// Explicitly select root
	m.tree.Cursor = 0
	m.updateActiveStepLogs()

	vp := m.viewport.View()
	if !strings.Contains(vp, "ACTR RUNNER ACTIVE") {
		t.Fatalf("expected active runner badge, got:\n%s", vp)
	}
	if !strings.Contains(vp, "release-prebuilt") {
		t.Fatalf("expected job name in active overview, got:\n%s", vp)
	}
	if !strings.Contains(vp, "Publish GitHub Release") {
		t.Fatalf("expected step name in active overview, got:\n%s", vp)
	}
	if !strings.Contains(vp, "/tmp/actr-test.sock") {
		t.Fatalf("expected dynamic sock path in active overview, got:\n%s", vp)
	}
}

func TestDashboard_UnifiedTree_LiveAndHistoricalBranches(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = newModel.(*DashboardModel)

	snap := &monitor.Snapshot{
		RunnerName: "runner-01",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:     "job-live-1",
				Name:   "test-linux-amd64",
				Status: monitor.StatusRunning,
				Steps: []*monitor.StepInfo{
					{ID: "step-l1", Name: "Set up runner", Status: monitor.StatusSuccess},
					{ID: "step-l2", Name: "Run tests (-race)", Status: monitor.StatusRunning},
				},
			},
		},
		RecentJobs: []*monitor.JobInfo{
			{
				ID:     "job-hist-1",
				Name:   "release-v0.1.2",
				Status: monitor.StatusSuccess,
				Steps: []*monitor.StepInfo{
					{ID: "step-h1", Name: "Build and publish", Status: monitor.StatusSuccess},
				},
			},
			{
				ID:     "job-hist-2",
				Name:   "flaky-test",
				Status: monitor.StatusFailed,
				Steps: []*monitor.StepInfo{
					{ID: "step-h2", Name: "Run suite", Status: monitor.StatusFailed},
				},
			},
		},
	}

	newModel, _ = m.Update(SnapshotMsg(snap))
	m = newModel.(*DashboardModel)

	if m.tree.Root == nil {
		t.Fatal("expected root node to be non-nil")
	}
	if m.tree.Root.Label != "actr • runner-01" {
		t.Fatalf("expected root label 'actr • runner-01', got %q", m.tree.Root.Label)
	}

	if len(m.tree.Root.Children) != 2 {
		t.Fatalf("expected root to have exactly 2 branch children, got %d", len(m.tree.Root.Children))
	}

	liveBranch := m.tree.Root.Children[0]
	if liveBranch.ID != "live-runs" {
		t.Fatalf("expected first branch ID 'live-runs', got %q", liveBranch.ID)
	}
	if liveBranch.Label != "LIVE RUNS (1 active)" {
		t.Fatalf("expected live branch label 'LIVE RUNS (1 active)', got %q", liveBranch.Label)
	}
	if len(liveBranch.Children) != 1 {
		t.Fatalf("expected 1 active job under live branch, got %d", len(liveBranch.Children))
	}
	if liveBranch.Children[0].ID != "job-live-1" {
		t.Fatalf("expected live job ID 'job-live-1', got %q", liveBranch.Children[0].ID)
	}
	if len(liveBranch.Children[0].Children) != 2 {
		t.Fatalf("expected 2 steps under job-live-1, got %d", len(liveBranch.Children[0].Children))
	}

	histBranch := m.tree.Root.Children[1]
	if histBranch.ID != "historical-runs" {
		t.Fatalf("expected second branch ID 'historical-runs', got %q", histBranch.ID)
	}
	if histBranch.Label != "HISTORICAL RUNS (2 past runs)" {
		t.Fatalf("expected historical branch label 'HISTORICAL RUNS (2 past runs)', got %q", histBranch.Label)
	}
	if len(histBranch.Children) != 2 {
		t.Fatalf("expected 2 historical jobs under historical branch, got %d", len(histBranch.Children))
	}
	if histBranch.Children[0].ID != "job-hist-1" {
		t.Fatalf("expected first historical job ID 'job-hist-1', got %q", histBranch.Children[0].ID)
	}
	if histBranch.Children[1].ID != "job-hist-2" {
		t.Fatalf("expected second historical job ID 'job-hist-2', got %q", histBranch.Children[1].ID)
	}

	view := m.tree.View(60)
	for _, expectedText := range []string{
		"LIVE RUNS (1 active)",
		"HISTORICAL RUNS (2 past runs)",
		"test-linux-amd64",
		"release-v0.1.2",
		"flaky-test",
	} {
		if !strings.Contains(view, expectedText) {
			t.Errorf("expected tree view to contain %q, got:\n%s", expectedText, view)
		}
	}
}

func TestDashboard_LiveJobTransitionsToHistoricalOnFinish(t *testing.T) {
	m := NewDashboardModel("/tmp/nonexistent.sock")
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = newModel.(*DashboardModel)

	snap := &monitor.Snapshot{
		RunnerName: "runner-01",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:     "job-1",
				Name:   "build-and-test",
				Status: monitor.StatusRunning,
				Steps: []*monitor.StepInfo{
					{ID: "step-1", Name: "Run tests", Status: monitor.StatusRunning},
				},
			},
			{
				ID:     "job-2",
				Name:   "lint",
				Status: monitor.StatusRunning,
			},
		},
		RecentJobs: []*monitor.JobInfo{
			{
				ID:     "job-old",
				Name:   "old-run",
				Status: monitor.StatusSuccess,
			},
		},
	}
	newModel, _ = m.Update(SnapshotMsg(snap))
	m = newModel.(*DashboardModel)

	liveBranch := m.tree.Root.Children[0]
	histBranch := m.tree.Root.Children[1]
	if len(liveBranch.Children) != 2 || len(histBranch.Children) != 1 {
		t.Fatalf("expected 2 live and 1 historical job, got %d and %d", len(liveBranch.Children), len(histBranch.Children))
	}

	// 1. EventJobStart adds a new job under LIVE RUNS
	newModel, _ = m.Update(EventMsg(monitor.Event{
		Type:  monitor.EventJobStart,
		JobID: "job-3",
		Job: &monitor.JobInfo{
			ID:     "job-3",
			Name:   "deploy",
			Status: monitor.StatusRunning,
		},
	}))
	m = newModel.(*DashboardModel)

	if len(liveBranch.Children) != 3 {
		t.Fatalf("expected 3 live jobs after EventJobStart, got %d", len(liveBranch.Children))
	}
	if liveBranch.Label != "LIVE RUNS (3 active)" {
		t.Fatalf("expected live branch label 'LIVE RUNS (3 active)', got %q", liveBranch.Label)
	}

	// Select job-1
	_ = m.tree.SelectFirst(func(n *TreeNode) bool { return n.ID == "job-1" })
	if m.tree.SelectedNode() == nil || m.tree.SelectedNode().ID != "job-1" {
		t.Fatalf("expected job-1 to be selected before finish, got: %+v", m.tree.SelectedNode())
	}

	// 2. EventJobFinish moves job-1 from LIVE RUNS to prepended position in HISTORICAL RUNS
	newModel, _ = m.Update(EventMsg(monitor.Event{
		Type:  monitor.EventJobFinish,
		JobID: "job-1",
		Job: &monitor.JobInfo{
			ID:     "job-1",
			Name:   "build-and-test",
			Status: monitor.StatusSuccess,
		},
	}))
	m = newModel.(*DashboardModel)

	if len(liveBranch.Children) != 2 {
		t.Fatalf("expected 2 live jobs after job-1 finished, got %d", len(liveBranch.Children))
	}
	for _, ch := range liveBranch.Children {
		if ch.ID == "job-1" {
			t.Fatalf("expected job-1 removed from live branch")
		}
	}

	if len(histBranch.Children) != 2 {
		t.Fatalf("expected 2 historical jobs after job-1 finished, got %d", len(histBranch.Children))
	}
	if histBranch.Children[0].ID != "job-1" {
		t.Fatalf("expected job-1 prepended as first historical job, got %q", histBranch.Children[0].ID)
	}
	if histBranch.Children[0].Status != monitor.StatusSuccess {
		t.Fatalf("expected job-1 status success, got %v", histBranch.Children[0].Status)
	}

	if liveBranch.Label != "LIVE RUNS (2 active)" {
		t.Fatalf("expected live branch label 'LIVE RUNS (2 active)', got %q", liveBranch.Label)
	}
	if histBranch.Label != "HISTORICAL RUNS (2 past runs)" {
		t.Fatalf("expected historical branch label 'HISTORICAL RUNS (2 past runs)', got %q", histBranch.Label)
	}

	// Cursor selection must not be broken
	sel := m.tree.SelectedNode()
	if sel == nil || sel.ID != "job-1" {
		t.Fatalf("expected cursor selection preserved on job-1, got: %+v", sel)
	}
}

func TestDashboard_HistoricalStepSelection_LoadsLogs(t *testing.T) {
	sockPath := testSocketPath(t)

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to listen on test socket: %v", err)
	}
	defer func() { _ = l.Close() }()

	mux := http.NewServeMux()
	mux.HandleFunc("/runs/job-hist-1/steps/step-hist-1/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]string{
			"2026-11-17T00:10:15Z Step execution started",
			"PASS: all 42 tests passed cleanly",
			"2026-11-17T00:10:20Z Step completed with exit 0",
		})
	})
	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(l)
	}()
	defer func() { _ = server.Close() }()

	m := NewDashboardModel(sockPath)
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = newModel.(*DashboardModel)

	snap := &monitor.Snapshot{
		RunnerName: "runner-01",
		RecentJobs: []*monitor.JobInfo{
			{
				ID:     "job-hist-1",
				Name:   "build-and-test",
				Status: monitor.StatusSuccess,
				Steps: []*monitor.StepInfo{
					{ID: "step-hist-1", Name: "Run tests", Status: monitor.StatusSuccess},
				},
			},
		},
	}
	newModel, _ = m.Update(SnapshotMsg(snap))
	m = newModel.(*DashboardModel)

	// Step logs must initially be unhydrated in memory
	if len(m.stepLogs["job-hist-1/step-hist-1"]) != 0 || len(m.stepLogs["step-hist-1"]) != 0 {
		t.Fatalf("expected step logs initially unhydrated in memory")
	}

	// Expand historical job to expose its steps
	histBranch := m.tree.Root.Children[1]
	jobNode := histBranch.Children[0]
	jobNode.Expanded = true
	m.tree.Rebuild()

	// Select step-hist-1
	found := m.tree.SelectFirst(func(n *TreeNode) bool { return n.ID == "step-hist-1" })
	if !found {
		t.Fatalf("expected step-hist-1 visible in tree rows")
	}

	cmd := m.updateActiveStepLogs()
	if cmd == nil {
		t.Fatal("expected fetchHistoricalLogsCmd when selecting historical step with unhydrated logs")
	}
	vpPending := m.viewport.View()
	if !strings.Contains(vpPending, "loading logs") {
		t.Fatalf("expected viewport to show loading state while logs are pending, got:\n%s", vpPending)
	}

	msg := cmd()
	newModel, _ = m.Update(msg)
	m = newModel.(*DashboardModel)

	// Step logs should now be loaded into memory and rendered in the viewport
	cachedLogs := m.stepLogs["job-hist-1/step-hist-1"]
	if len(cachedLogs) != 3 {
		t.Fatalf("expected 3 log lines cached in stepLogs, got %d", len(cachedLogs))
	}
	if !strings.Contains(cachedLogs[1], "PASS: all 42 tests passed cleanly") {
		t.Fatalf("expected log content in cachedLogs, got %v", cachedLogs)
	}

	vp := m.viewport.View()
	if !strings.Contains(vp, "PASS: all 42 tests passed cleanly") {
		t.Fatalf("expected viewport to render loaded historical logs, got:\n%s", vp)
	}
}

func TestDashboard_HistoricalSelection_PeakVitalsHUD(t *testing.T) {
	sockPath := testSocketPath(t)
	m := NewDashboardModel(sockPath)
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = newModel.(*DashboardModel)

	snap := &monitor.Snapshot{
		RunnerName: "runner-01",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:     "job-live-1",
				Name:   "test-linux-amd64",
				Status: monitor.StatusRunning,
				Steps: []*monitor.StepInfo{
					{ID: "step-l1", Name: "Run tests", Status: monitor.StatusRunning},
				},
			},
		},
		RecentJobs: []*monitor.JobInfo{
			{
				ID:     "job-hist-1",
				Name:   "build-and-test",
				Status: monitor.StatusSuccess,
				Steps: []*monitor.StepInfo{
					{ID: "step-h1", Name: "Run tests (-race)", Status: monitor.StatusSuccess},
				},
			},
		},
		Telemetry: &monitor.TelemetrySample{
			PID:     12345,
			StepCPU: 45.0,
		},
	}
	newModel, _ = m.Update(SnapshotMsg(snap))
	m = newModel.(*DashboardModel)

	// Attach detailed historical run summary with peak metrics
	histSummary := &HistoricalRunSummary{
		JobName:        "build-and-test",
		Status:         monitor.StatusSuccess,
		DurationSec:    225, // 03:45
		Driver:         "docker",
		TraceID:        "4bf92f35e89a",
		PeakCPUPercent: 182.4,
		PeakRSSBytes:   uint64(1.5 * 1024 * 1024 * 1024),
		TotalNetRx:     140 * 1024 * 1024,
		TotalNetTx:     28 * 1024 * 1024,
		TotalDiskWrite: 84 * 1024 * 1024,
		Limits: &config.LimitsConfig{
			CPU:    2.0,
			Memory: 4 * 1024 * 1024 * 1024,
		},
	}
	m.SetHistoricalRunSummary("job-hist-1", histSummary)

	// 1. Live step selected: telemetry is in live mode
	m.tree.SelectFirst(func(n *TreeNode) bool { return n.ID == "step-l1" })
	m.updateActiveStepLogs()
	assert.False(t, m.telemetry.IsHistorical(), "expected live mode when live step selected")
	dashView := m.View()
	assert.Contains(t, dashView, "∿ SYSTEM VITALS & RADAR")
	assert.NotContains(t, dashView, "∿ RUN VITALS (PEAK SUMMARY)")

	// 2. Select historical job: telemetry switches to Peak Vitals mode
	found := m.tree.SelectFirst(func(n *TreeNode) bool { return n.ID == "job-hist-1" })
	require.True(t, found, "job-hist-1 must be found in tree")
	m.updateActiveStepLogs()
	assert.True(t, m.telemetry.IsHistorical(), "expected historical mode when historical job selected")
	dashView = m.View()
	assert.Contains(t, dashView, "∿ RUN VITALS (PEAK SUMMARY)")
	assert.Contains(t, dashView, "build-and-test")
	assert.Contains(t, dashView, "03:45")
	assert.Contains(t, dashView, "182.4%")
	assert.Contains(t, dashView, "1.5 GB")
	assert.Contains(t, dashView, "NET: 140 MB ↓  28 MB ↑")
	assert.Contains(t, dashView, "DISK WR: 84 MB")

	// 3. Expand historical job and select step-h1: telemetry remains in Peak Vitals mode
	histBranch := m.tree.Root.Children[1]
	jobNode := histBranch.Children[0]
	jobNode.Expanded = true
	m.tree.Rebuild()
	foundStep := m.tree.SelectFirst(func(n *TreeNode) bool { return n.ID == "step-h1" })
	require.True(t, foundStep, "step-h1 must be selectable in tree")
	m.updateActiveStepLogs()
	assert.True(t, m.telemetry.IsHistorical(), "expected historical mode when historical step selected")
	dashView = m.View()
	assert.Contains(t, dashView, "∿ RUN VITALS (PEAK SUMMARY)")

	// 4. Move back to live-runs node: telemetry clears historical mode and restores live vitals
	m.tree.SelectFirst(func(n *TreeNode) bool { return n.ID == "job-live-1" })
	m.updateActiveStepLogs()
	assert.False(t, m.telemetry.IsHistorical(), "expected live mode when moved back to live job")
	dashView = m.View()
	assert.Contains(t, dashView, "∿ SYSTEM VITALS & RADAR")
	assert.NotContains(t, dashView, "∿ RUN VITALS (PEAK SUMMARY)")

	// 5. Select historical-runs branch header: telemetry clears historical mode
	m.tree.SelectFirst(func(n *TreeNode) bool { return n.ID == "historical-runs" })
	m.updateActiveStepLogs()
	assert.False(t, m.telemetry.IsHistorical(), "expected live/cleared mode on historical branch root")
	dashView = m.View()
	assert.Contains(t, dashView, "∿ SYSTEM VITALS & RADAR")
}
