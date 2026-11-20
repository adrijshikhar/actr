package tui

import (
	"bytes"
	"encoding/base64"
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
	"github.com/muesli/termenv"
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
	if len(histBranch.Children) != 1 {
		t.Fatalf("expected 1 repo group under historical branch, got %d", len(histBranch.Children))
	}
	defaultRepo := histBranch.Children[0]
	if defaultRepo.NodeType != NodeRepo || defaultRepo.Label != "default" {
		t.Fatalf("expected default repo node, got %+v", defaultRepo)
	}
	if len(defaultRepo.Children) != 2 {
		t.Fatalf("expected 2 historical jobs under default repo node, got %d", len(defaultRepo.Children))
	}
	if defaultRepo.Children[0].ID != "job-hist-1" {
		t.Fatalf("expected first historical job ID 'job-hist-1', got %q", defaultRepo.Children[0].ID)
	}
	if defaultRepo.Children[1].ID != "job-hist-2" {
		t.Fatalf("expected second historical job ID 'job-hist-2', got %q", defaultRepo.Children[1].ID)
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
		t.Fatalf("expected 2 live and 1 historical repo, got %d and %d", len(liveBranch.Children), len(histBranch.Children))
	}
	if len(histBranch.Children[0].Children) != 1 {
		t.Fatalf("expected 1 historical job in default repo, got %d", len(histBranch.Children[0].Children))
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

	if len(histBranch.Children) != 1 {
		t.Fatalf("expected 1 historical repo after job-1 finished, got %d", len(histBranch.Children))
	}
	defaultRepo := histBranch.Children[0]
	if len(defaultRepo.Children) != 2 {
		t.Fatalf("expected 2 historical jobs after job-1 finished, got %d", len(defaultRepo.Children))
	}
	if defaultRepo.Children[0].ID != "job-1" {
		t.Fatalf("expected job-1 prepended as first historical job, got %q", defaultRepo.Children[0].ID)
	}
	if defaultRepo.Children[0].Status != monitor.StatusSuccess {
		t.Fatalf("expected job-1 status success, got %v", defaultRepo.Children[0].Status)
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
	jobNode := m.tree.FindNode("job-hist-1")
	require.NotNil(t, jobNode, "job-hist-1 must exist in tree")
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
	jobNode := m.tree.FindNode("job-hist-1")
	require.NotNil(t, jobNode, "job-hist-1 must exist in tree")
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

func TestDashboard_MultiRepoTreeGrouping(t *testing.T) {
	snap := &monitor.Snapshot{
		RunnerName: "actr • multi-repo",
		ActiveJobs: []*monitor.JobInfo{
			{ID: "job-1", Repo: "adrijshikhar/aim", RepoAlias: "aim", Name: "CI Suite"},
			{ID: "job-2", Repo: "adrijshikhar/cxstatusline", RepoAlias: "cxstatusline", Name: "Vitest"},
		},
	}

	model := NewDashboardModel("/tmp/daemon.sock")
	_ = model.ApplySnapshot(snap)

	tree := model.Tree()
	if tree == nil || tree.Root == nil {
		t.Fatal("expected tree root")
	}

	liveNode := model.getLiveBranch()
	if liveNode == nil {
		t.Fatal("expected live-runs branch")
	}

	// Should contain 2 repo nodes under live-runs
	if len(liveNode.Children) != 2 {
		t.Fatalf("expected 2 repo nodes under live-runs, got %d", len(liveNode.Children))
	}

	repo1 := liveNode.Children[0]
	if repo1.Label != "adrijshikhar/aim" && repo1.Label != "adrijshikhar/cxstatusline" {
		t.Errorf("unexpected repo label %q", repo1.Label)
	}
}

func TestDashboard_MultiRepoExpansionAndRepoSelection(t *testing.T) {
	snap := &monitor.Snapshot{
		RunnerName: "actr • multi-repo",
		SupervisorStatus: &monitor.SupervisorStatus{
			GlobalMaxJobs: 2,
			GlobalActive:  1,
			RepoSlots: []*monitor.RepoSlotInfo{
				{Alias: "aim", MaxJobs: 1, ActiveJobs: 1, Status: "busy"},
				{Alias: "cxstatusline", MaxJobs: 1, ActiveJobs: 0, Status: "idle"},
			},
		},
		ActiveJobs: []*monitor.JobInfo{
			{ID: "job-1", Repo: "adrijshikhar/aim", RepoAlias: "aim", Name: "CI Suite", Status: monitor.StatusRunning},
		},
	}

	model := NewDashboardModel("/tmp/daemon.sock")
	model.width = 100
	model.height = 30
	model.ready = true
	_ = model.ApplySnapshot(snap)

	// Verify HUD rendered slots
	hud := model.telemetry.View(100)
	assert.Contains(t, hud, "aim (1/1 slots)")
	assert.Contains(t, hud, "cxstatusline (0/1 slots)")

	// Navigate to repo node: root -> live-runs -> repo:adrijshikhar/aim
	model.tree.SelectFirst(func(n *TreeNode) bool {
		return n.NodeType == NodeRepo
	})
	_ = model.updateActiveStepLogs()

	// Verify repo selection renders repository info in right pane
	repoView := model.viewport.View()
	assert.Contains(t, repoView, "Repository: aim")
	assert.Contains(t, repoView, "Active Jobs: 1")

	// Collapse repo node
	repoNode := model.tree.SelectedNode()
	require.NotNil(t, repoNode)
	repoNode.Expanded = false
	model.seenRepoExpansion[repoNode.ID] = false

	// Re-apply snapshot: expansion state must be preserved
	_ = model.ApplySnapshot(snap)
	repoNodeAfter := model.tree.FindNode(repoNode.ID)
	require.NotNil(t, repoNodeAfter)
	assert.False(t, repoNodeAfter.Expanded, "expected collapsed state to be preserved across snapshot hydration")
}

func TestDashboard_AlwaysVisibleLiveRepos_ZeroAndNonZeroActive(t *testing.T) {
	// 1. With 0 active jobs, both configured repos are visible under LIVE RUNS with idle badges
	snap0 := &monitor.Snapshot{
		RunnerName: "actr • multi-repo",
		SupervisorStatus: &monitor.SupervisorStatus{
			GlobalMaxJobs: 2,
			GlobalActive:  0,
			RepoSlots: []*monitor.RepoSlotInfo{
				{Alias: "cxstatusline", MaxJobs: 1, ActiveJobs: 0, Status: "idle"},
				{Alias: "aim", MaxJobs: 1, ActiveJobs: 0, Status: "idle"},
			},
		},
		ActiveJobs: []*monitor.JobInfo{},
	}

	model := NewDashboardModel("/tmp/daemon.sock")
	model.ready = true
	_ = model.ApplySnapshot(snap0)

	liveNode := model.getLiveBranch()
	require.NotNil(t, liveNode)
	assert.Equal(t, "LIVE RUNS (0 active)", liveNode.Label)

	// Repos should be sorted by alias ascending: aim then cxstatusline
	require.Len(t, liveNode.Children, 2)
	assert.Equal(t, "repo:aim", liveNode.Children[0].ID)
	assert.Equal(t, "aim (idle • 0/1 slots)", liveNode.Children[0].Label)
	assert.Empty(t, liveNode.Children[0].Children)

	assert.Equal(t, "repo:cxstatusline", liveNode.Children[1].ID)
	assert.Equal(t, "cxstatusline (idle • 0/1 slots)", liveNode.Children[1].Label)
	assert.Empty(t, liveNode.Children[1].Children)

	// 2. With 1 active job for aim, aim becomes busy with the job nested, cxstatusline remains idle
	snap1 := &monitor.Snapshot{
		RunnerName: "actr • multi-repo",
		SupervisorStatus: &monitor.SupervisorStatus{
			GlobalMaxJobs: 2,
			GlobalActive:  1,
			RepoSlots: []*monitor.RepoSlotInfo{
				{Alias: "aim", MaxJobs: 1, ActiveJobs: 1, Status: "busy"},
				{Alias: "cxstatusline", MaxJobs: 1, ActiveJobs: 0, Status: "idle"},
			},
		},
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:        "job-aim-1",
				Name:      "CI Test",
				RepoAlias: "aim",
				Status:    monitor.StatusRunning,
				Steps: []*monitor.StepInfo{
					{ID: "step-1", Name: "Run Tests", Status: monitor.StatusRunning},
				},
			},
		},
	}
	_ = model.ApplySnapshot(snap1)
	liveNode = model.getLiveBranch()
	require.NotNil(t, liveNode)

	assert.Equal(t, "LIVE RUNS (1 active)", liveNode.Label)
	require.Len(t, liveNode.Children, 2)

	assert.Equal(t, "aim (busy • 1/1 slots)", liveNode.Children[0].Label)
	require.Len(t, liveNode.Children[0].Children, 1)
	assert.Equal(t, "job-aim-1", liveNode.Children[0].Children[0].ID)
	require.Len(t, liveNode.Children[0].Children[0].Children, 1)
	assert.Equal(t, "step-1", liveNode.Children[0].Children[0].Children[0].ID)

	assert.Equal(t, "cxstatusline (idle • 0/1 slots)", liveNode.Children[1].Label)
	assert.Empty(t, liveNode.Children[1].Children)
}

func TestDashboard_HierarchicalHistoricalRuns_GroupedByRepoAlias(t *testing.T) {
	snap := &monitor.Snapshot{
		RunnerName: "actr • multi-repo",
		RecentJobs: []*monitor.JobInfo{
			{
				ID:        "hist-1",
				Name:      "Aim Build",
				RepoAlias: "aim",
				Status:    monitor.StatusSuccess,
			},
			{
				ID:        "hist-2",
				Name:      "CX Lint",
				RepoAlias: "cxstatusline",
				Status:    monitor.StatusSuccess,
			},
			{
				ID:        "hist-3",
				Name:      "Legacy Job",
				RepoAlias: "", // Empty should fallback to "default"
				Status:    monitor.StatusFailed,
			},
			{
				ID:        "hist-4",
				Name:      "Aim Tests",
				RepoAlias: "aim",
				Status:    monitor.StatusSuccess,
			},
		},
	}

	model := NewDashboardModel("/tmp/daemon.sock")
	model.ready = true
	_ = model.ApplySnapshot(snap)

	histNode := model.getHistoricalBranch()
	require.NotNil(t, histNode)
	// 4 total past runs across 3 distinct repo groups
	assert.Equal(t, "HISTORICAL RUNS (4 past runs)", histNode.Label)

	// Repos sorted alphabetically: aim, cxstatusline, default
	require.Len(t, histNode.Children, 3)

	// aim
	aimGroup := histNode.Children[0]
	assert.Equal(t, "hist-repo:aim", aimGroup.ID)
	assert.Equal(t, "aim", aimGroup.Label)
	assert.Equal(t, NodeRepo, aimGroup.NodeType)
	require.Len(t, aimGroup.Children, 2)
	assert.Equal(t, "hist-1", aimGroup.Children[0].ID)
	assert.Equal(t, "hist-4", aimGroup.Children[1].ID)

	// cxstatusline
	cxGroup := histNode.Children[1]
	assert.Equal(t, "hist-repo:cxstatusline", cxGroup.ID)
	assert.Equal(t, "cxstatusline", cxGroup.Label)
	assert.Equal(t, NodeRepo, cxGroup.NodeType)
	require.Len(t, cxGroup.Children, 1)
	assert.Equal(t, "hist-2", cxGroup.Children[0].ID)

	// default
	defaultGroup := histNode.Children[2]
	assert.Equal(t, "hist-repo:default", defaultGroup.ID)
	assert.Equal(t, "default", defaultGroup.Label)
	assert.Equal(t, NodeRepo, defaultGroup.NodeType)
	require.Len(t, defaultGroup.Children, 1)
	assert.Equal(t, "hist-3", defaultGroup.Children[0].ID)
}

func TestDashboard_NodeTraversalAndStepSelection(t *testing.T) {
	snap := &monitor.Snapshot{
		RunnerName: "actr • multi-repo",
		RecentJobs: []*monitor.JobInfo{
			{
				ID:        "job-aim-h1",
				Name:      "Aim Pipeline",
				RepoAlias: "aim",
				Status:    monitor.StatusSuccess,
				Steps: []*monitor.StepInfo{
					{ID: "step-aim-s1", Name: "Build Artifact", Status: monitor.StatusSuccess},
				},
			},
		},
		StepLogs: map[string][]string{
			"job-aim-h1/step-aim-s1": {"compiled successfully", "exit code 0"},
		},
	}

	model := NewDashboardModel("/tmp/daemon.sock")
	model.width = 100
	model.height = 30
	model.ready = true
	_ = model.ApplySnapshot(snap)

	histSummary := &HistoricalRunSummary{
		JobName:        "Aim Pipeline",
		Status:         monitor.StatusSuccess,
		DurationSec:    45,
		PeakCPUPercent: 78.5,
	}
	model.SetHistoricalRunSummary("job-aim-h1", histSummary)

	// 1. Select the repo group node: HUD should NOT treat it as a job
	foundRepo := model.tree.SelectFirst(func(n *TreeNode) bool { return n.ID == "hist-repo:aim" })
	require.True(t, foundRepo)
	_ = model.updateActiveStepLogs()
	assert.Nil(t, findHistoricalJobNode(model.tree.SelectedNode()))
	assert.False(t, model.telemetry.IsHistorical())

	// Viewport should show historical repository info
	repoView := model.viewport.View()
	assert.Contains(t, repoView, "Repository: aim")
	assert.Contains(t, repoView, "Historical Runs: 1")

	// 2. Select the historical job node: HUD switches to historical Peak Vitals mode
	foundJob := model.tree.SelectFirst(func(n *TreeNode) bool { return n.ID == "job-aim-h1" })
	require.True(t, foundJob)
	_ = model.updateActiveStepLogs()
	assert.Equal(t, "job-aim-h1", findHistoricalJobNode(model.tree.SelectedNode()).ID)
	assert.True(t, model.telemetry.IsHistorical())

	// 3. Expand the job and select its step: step node resolves enclosing parent job through repo branch
	jobNode := model.tree.FindNode("job-aim-h1")
	require.NotNil(t, jobNode)
	jobNode.Expanded = true
	model.tree.Rebuild()

	foundStep := model.tree.SelectFirst(func(n *TreeNode) bool { return n.ID == "step-aim-s1" })
	require.True(t, foundStep)
	_ = model.updateActiveStepLogs()

	stepNode := model.tree.SelectedNode()
	require.NotNil(t, stepNode)
	assert.Equal(t, "step-aim-s1", stepNode.ID)

	// Traversal resolves enclosing job
	resolvedJob := findHistoricalJobNode(stepNode)
	require.NotNil(t, resolvedJob)
	assert.Equal(t, "job-aim-h1", resolvedJob.ID)

	// HUD stays in Historical mode for the enclosing job
	assert.True(t, model.telemetry.IsHistorical())

	// Viewport contains step logs resolved by jobID/stepID key
	vp := model.viewport.View()
	assert.Contains(t, vp, "compiled successfully")
	assert.Contains(t, vp, "exit code 0")
}

func TestDashboard_SidebarSearch(t *testing.T) {
	m := NewDashboardModel("/tmp/test.sock")
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newModel.(*DashboardModel)

	// Build a snapshot with two repos: aim (with job containing smoke step) and cxstatusline
	now := time.Now()
	snap := &monitor.Snapshot{
		RunnerName: "actr",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:        "job-aim",
				Name:      "CI Test",
				RepoAlias: "aim",
				Repo:      "aim",
				Status:    monitor.StatusRunning,
				StartedAt: now,
				Steps: []*monitor.StepInfo{
					{ID: "s1", Name: "Setup", Status: monitor.StatusSuccess},
					{ID: "s2", Name: "Smoke Tests", Status: monitor.StatusRunning},
				},
			},
			{
				ID:        "job-cx",
				Name:      "Lint Check",
				RepoAlias: "cxstatusline",
				Repo:      "cxstatusline",
				Status:    monitor.StatusRunning,
				StartedAt: now,
				Steps: []*monitor.StepInfo{
					{ID: "s3", Name: "Run Linter", Status: monitor.StatusSuccess},
				},
			},
		},
	}
	m.ApplySnapshot(snap)

	// Verify initial state
	assert.False(t, m.IsTreeSearching())
	assert.Equal(t, "", m.Tree().Filter())

	// 1. Press '/' to activate search mode
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = newModel.(*DashboardModel)
	assert.True(t, m.IsTreeSearching())
	assert.Contains(t, m.View(), "Search:")

	// 2. Type "smoke" into search input
	for _, r := range "smoke" {
		newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = newModel.(*DashboardModel)
	}

	assert.Equal(t, "smoke", m.Tree().Filter())
	dashView := m.View()
	assert.Contains(t, dashView, "Smoke Tests")
	assert.NotContains(t, dashView, "cxstatusline")
	assert.NotContains(t, dashView, "Run Linter")

	// 3. Press Enter to commit search and return to tree navigation
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newModel.(*DashboardModel)
	assert.False(t, m.IsTreeSearching())
	assert.Equal(t, "smoke", m.Tree().Filter())

	// Navigate with down arrow among filtered rows
	initialCursor := m.Tree().Cursor
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(*DashboardModel)
	assert.GreaterOrEqual(t, m.Tree().Cursor, initialCursor)

	// 4. Press Esc to clear filter and restore full tree
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newModel.(*DashboardModel)
	assert.False(t, m.IsTreeSearching())
	assert.Equal(t, "", m.Tree().Filter())

	restoredView := m.View()
	assert.Contains(t, restoredView, "cxstatusline")

	// 5. Press '/' then immediately 'Esc' cancels search
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = newModel.(*DashboardModel)
	assert.True(t, m.IsTreeSearching())

	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newModel.(*DashboardModel)
	assert.False(t, m.IsTreeSearching())
	assert.Equal(t, "", m.Tree().Filter())
}

func TestDashboard_LogSearch_ActivationAndNavigation(t *testing.T) {
	m := NewDashboardModel("/tmp/test.sock")
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = newModel.(*DashboardModel)

	// Create 50 log lines with matches at lines 2, 25, and 45
	lines := make([]string, 50)
	for i := 0; i < 50; i++ {
		lines[i] = fmt.Sprintf("line %02d: info output log", i)
	}
	lines[2] = "line 02: test_error: assertion failed in module_a"
	lines[25] = "line 25: test_error: timeout in module_c"
	lines[45] = "line 45: test_error: final summary with 2 failures"

	snap := &monitor.Snapshot{
		RunnerName: "actr",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:     "job-1",
				Name:   "Test Job",
				Status: monitor.StatusRunning,
				Steps: []*monitor.StepInfo{
					{ID: "step-1", Name: "Run Tests", Status: monitor.StatusRunning},
				},
			},
		},
		StepLogs: map[string][]string{
			"job-1/step-1": lines,
		},
	}
	m.ApplySnapshot(snap)

	found := m.Tree().SelectFirst(func(n *TreeNode) bool {
		return n.ID == "step-1"
	})
	require.True(t, found, "step-1 should be selectable in tree")
	m.updateActiveStepLogs()

	// Initial assertions
	assert.False(t, m.IsLogSearching())
	assert.Equal(t, "", m.SearchQuery())
	assert.Empty(t, m.SearchMatches())

	// 1. Activate log search with Ctrl+F
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = newModel.(*DashboardModel)
	assert.True(t, m.IsLogSearching())
	assert.Equal(t, FocusViewport, m.Focus())

	view := m.View()
	assert.Contains(t, view, "Find in logs:")
	assert.Contains(t, view, "(No matches)")

	// 2. Type "test_error"
	query := "test_error"
	for _, r := range query {
		newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = newModel.(*DashboardModel)
	}

	assert.Equal(t, query, m.SearchQuery())
	expectedMatches := []int{2, 25, 45}
	assert.Equal(t, expectedMatches, m.SearchMatches())
	assert.Equal(t, 0, m.CurrentMatchIdx())

	view = m.View()
	assert.Contains(t, view, "(Match 1 of 3)")

	// 3. Press Enter to commit search and transition to match navigation
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newModel.(*DashboardModel)
	assert.False(t, m.IsLogSearching())
	assert.Equal(t, query, m.SearchQuery())
	assert.Equal(t, expectedMatches, m.SearchMatches())
	assert.Contains(t, m.View(), "(Match 1 of 3)")

	// 4. Navigate matches with 'n' and test scrolling
	// Next -> match index 1 (line 25)
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = newModel.(*DashboardModel)
	assert.Equal(t, 1, m.CurrentMatchIdx())
	assert.Contains(t, m.View(), "(Match 2 of 3)")
	assert.Greater(t, m.Viewport().YOffset, 0, "viewport should scroll down for match at line 25")

	// Next -> match index 2 (line 45)
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = newModel.(*DashboardModel)
	assert.Equal(t, 2, m.CurrentMatchIdx())
	assert.Contains(t, m.View(), "(Match 3 of 3)")
	assert.Greater(t, m.Viewport().YOffset, 20, "viewport should scroll further down for match at line 45")

	// Next -> wraps to match index 0 (line 2)
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = newModel.(*DashboardModel)
	assert.Equal(t, 0, m.CurrentMatchIdx())
	assert.Contains(t, m.View(), "(Match 1 of 3)")
	assert.Equal(t, 0, m.Viewport().YOffset, "viewport should scroll back to top for line 2")

	// 5. Navigate backwards with 'N'
	// Prev -> wraps to match index 2 (line 45)
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	m = newModel.(*DashboardModel)
	assert.Equal(t, 2, m.CurrentMatchIdx())
	assert.Contains(t, m.View(), "(Match 3 of 3)")
	assert.Greater(t, m.Viewport().YOffset, 20)

	// Prev -> match index 1 (line 25)
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	m = newModel.(*DashboardModel)
	assert.Equal(t, 1, m.CurrentMatchIdx())
	assert.Contains(t, m.View(), "(Match 2 of 3)")

	// 6. Test streaming new log event while search active: adds match without breaking current index
	newModel, _ = m.Update(EventMsg(monitor.Event{
		Type:    monitor.EventStepLog,
		JobID:   "job-1",
		StepID:  "step-1",
		LogLine: "line 50: test_error: appended stream log",
	}))
	m = newModel.(*DashboardModel)
	assert.Equal(t, 1, m.CurrentMatchIdx())
	assert.Equal(t, []int{2, 25, 45, 50}, m.SearchMatches())
	assert.Contains(t, m.View(), "(Match 2 of 4)")

	// 7. Dismiss search with Esc
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newModel.(*DashboardModel)
	assert.False(t, m.IsLogSearching())
	assert.Equal(t, "", m.SearchQuery())
	assert.Empty(t, m.SearchMatches())
	assert.NotContains(t, m.View(), "Find in logs:")

	// 8. Test activation via '/' when focus is FocusViewport
	m.SetFocus(FocusViewport)
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = newModel.(*DashboardModel)
	assert.True(t, m.IsLogSearching())
	assert.Contains(t, m.View(), "Find in logs:")

	// Dismiss with Esc while typing
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newModel.(*DashboardModel)
	assert.False(t, m.IsLogSearching())
	assert.NotContains(t, m.View(), "Find in logs:")
}

func TestDashboard_LogSearch_Highlighting(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(termenv.Ascii)
	})

	m := NewDashboardModel("/tmp/test.sock")
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newModel.(*DashboardModel)

	snap := &monitor.Snapshot{
		RunnerName: "actr",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:     "job-1",
				Name:   "Test Job",
				Status: monitor.StatusRunning,
				Steps: []*monitor.StepInfo{
					{ID: "step-1", Name: "Run Tests", Status: monitor.StatusRunning},
				},
			},
		},
		StepLogs: map[string][]string{
			"job-1/step-1": {
				"line 0: starting build",
				"line 1: connection failure in db",
				"line 2: retrying...",
				"line 3: fatal failure in network",
			},
		},
	}
	m.ApplySnapshot(snap)

	found := m.Tree().SelectFirst(func(n *TreeNode) bool {
		return n.ID == "step-1"
	})
	require.True(t, found)
	m.updateActiveStepLogs()

	// Initial clean content should not contain search highlight ANSI sequences
	initialView := m.Viewport().View()
	assert.Contains(t, initialView, "connection failure in db")
	assert.Contains(t, initialView, "fatal failure in network")

	// Activate search and search for "failure"
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = newModel.(*DashboardModel)
	for _, r := range "failure" {
		newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = newModel.(*DashboardModel)
	}

	// Active match on line 1, normal match on line 3
	vpContent := m.Viewport().View()
	assert.Contains(t, vpContent, "failure")

	// Rendered active match uses activeMatchStyle (Background 208)
	expectedActive := activeMatchStyle.Render("failure")
	// Rendered normal match uses normalMatchStyle (Background 226)
	expectedNormal := normalMatchStyle.Render("failure")

	assert.Contains(t, vpContent, expectedActive, "active match line should have activeMatchStyle (orange/208)")
	assert.Contains(t, vpContent, expectedNormal, "other match line should have normalMatchStyle (yellow/226)")

	// Commit search and advance to next match with 'n'
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newModel.(*DashboardModel)
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = newModel.(*DashboardModel)

	vpContentAfterNext := m.Viewport().View()
	assert.Contains(t, vpContentAfterNext, expectedActive)
	assert.Contains(t, vpContentAfterNext, expectedNormal)

	// Dismiss search with Esc: highlights removed
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newModel.(*DashboardModel)

	vpContentClean := m.Viewport().View()
	assert.NotContains(t, vpContentClean, expectedActive)
	assert.NotContains(t, vpContentClean, expectedNormal)
	assert.Contains(t, vpContentClean, "connection failure in db")
	assert.Contains(t, vpContentClean, "fatal failure in network")
}

func TestDashboard_MouseTreeSelection(t *testing.T) {
	m := NewDashboardModel(testSocketPath(t))
	m.ready = true
	resModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = resModel.(*DashboardModel)

	snap := &monitor.Snapshot{
		RunnerName: "test-runner",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:       "job-1",
				Name:     "Build Job",
				Workflow: "ci.yml",
				Repo:     "adrijshikhar/actr",
				Status:   monitor.StatusRunning,
				Steps: []*monitor.StepInfo{
					{ID: "step-1", Name: "Checkout", Status: monitor.StatusSuccess},
					{ID: "step-2", Name: "Compile", Status: monitor.StatusRunning},
				},
			},
		},
	}
	m.ApplySnapshot(snap)
	m.stepLogs["job-1/step-1"] = []string{"checkout finished successfully"}
	m.stepLogs["job-1/step-2"] = []string{"compiling binary..."}

	// Visible rows in tree:
	// row 0: root ("actr")
	// row 1: live-runs ("LIVE RUNS (1 active)")
	// row 2: repo ("adrijshikhar/actr")
	// row 3: job-1 ("Build Job")
	// row 4: step-1 ("Checkout")
	// row 5: step-2 ("Compile")
	rows := m.Tree().Rows()
	require.GreaterOrEqual(t, len(rows), 6)

	// treeTopOffset is 1 when not searching (Y=0 is top border, Y=1 is row 0)
	// Click on row 4 (step-1) at X=10, Y=5 (row = 5 - 1 = 4)
	newModel, _ := m.Update(tea.MouseMsg{
		X:      10,
		Y:      5,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	m = newModel.(*DashboardModel)

	assert.Equal(t, FocusTree, m.Focus())
	assert.Equal(t, 4, m.Tree().Cursor)
	assert.Equal(t, "step-1", m.Tree().SelectedNode().ID)
	assert.Contains(t, m.Viewport().View(), "checkout finished successfully")

	// Click on row 3 (job-1) label at X=15, Y=4 (row = 4 - 1 = 3)
	jobNode := m.Tree().NodeAtRow(3)
	require.NotNil(t, jobNode)
	assert.True(t, jobNode.Expanded)

	newModel, _ = m.Update(tea.MouseMsg{
		X:      15,
		Y:      4,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	m = newModel.(*DashboardModel)
	assert.Equal(t, 3, m.Tree().Cursor)

	// Double click: click again within 350ms -> toggles expand
	newModel, _ = m.Update(tea.MouseMsg{
		X:      15,
		Y:      4,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	m = newModel.(*DashboardModel)
	assert.False(t, jobNode.Expanded, "double clicking should collapse the node")

	// Wait past the 350ms double-click window to verify chevron click independently
	time.Sleep(400 * time.Millisecond)

	// Chevron click on row 3: job-1 is depth 3 (indent = 6 spaces, chevron around X=7..9)
	// Click chevron at X=7, Y=4
	newModel, _ = m.Update(tea.MouseMsg{
		X:      7,
		Y:      4,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	assert.NotNil(t, newModel)
	assert.True(t, jobNode.Expanded, "clicking on chevron should expand the node immediately")
}

func TestDashboard_MouseWheelScrolling(t *testing.T) {
	m := NewDashboardModel(testSocketPath(t))
	m.ready = true
	resModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = resModel.(*DashboardModel)

	snap := &monitor.Snapshot{
		RunnerName: "test-runner",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:       "job-1",
				Name:     "Build Job",
				Workflow: "ci.yml",
				Repo:     "adrijshikhar/actr",
				Status:   monitor.StatusRunning,
				Steps: []*monitor.StepInfo{
					{ID: "step-1", Name: "Checkout", Status: monitor.StatusSuccess},
					{ID: "step-2", Name: "Compile", Status: monitor.StatusRunning},
				},
			},
		},
	}
	m.ApplySnapshot(snap)

	var manyLogs []string
	for i := 1; i <= 50; i++ {
		manyLogs = append(manyLogs, fmt.Sprintf("Log line %02d", i))
	}
	m.stepLogs["job-1/step-2"] = manyLogs

	// Select step-2
	m.Tree().SelectRow(5)
	_ = m.updateActiveStepLogs()

	treeWidth := m.calcTreeWidth()
	sidebarWidth := treeWidth + 2

	// 1. Wheel over sidebar (X < sidebarWidth)
	assert.Equal(t, 5, m.Tree().Cursor)
	newModel, _ := m.Update(tea.MouseMsg{
		X:      5,
		Y:      5,
		Button: tea.MouseButtonWheelUp,
	})
	m = newModel.(*DashboardModel)
	assert.Equal(t, 4, m.Tree().Cursor, "wheel up over tree should move cursor up")

	newModel, _ = m.Update(tea.MouseMsg{
		X:      5,
		Y:      5,
		Button: tea.MouseButtonWheelDown,
	})
	m = newModel.(*DashboardModel)
	assert.Equal(t, 5, m.Tree().Cursor, "wheel down over tree should move cursor down")

	// 2. Wheel over viewport (X >= sidebarWidth)
	m.autoFollow = true
	m.viewport.GotoBottom()
	initYOffset := m.viewport.YOffset

	newModel, _ = m.Update(tea.MouseMsg{
		X:      sidebarWidth + 5,
		Y:      10,
		Button: tea.MouseButtonWheelUp,
	})
	m = newModel.(*DashboardModel)
	assert.False(t, m.autoFollow, "wheel scrolling viewport should disable autoFollow")
	assert.Less(t, m.viewport.YOffset, initYOffset, "wheel up should scroll viewport up")

	prevYOffset := m.viewport.YOffset
	newModel, _ = m.Update(tea.MouseMsg{
		X:      sidebarWidth + 5,
		Y:      10,
		Button: tea.MouseButtonWheelDown,
	})
	m = newModel.(*DashboardModel)
	assert.GreaterOrEqual(t, m.viewport.YOffset, prevYOffset, "wheel down should scroll viewport down")
}

func TestDashboard_DragToCopySelection(t *testing.T) {
	oldWriter := clipboardWriter
	oldGOOS := currentGOOS
	defer func() {
		clipboardWriter = oldWriter
		currentGOOS = oldGOOS
	}()

	currentGOOS = "linux"
	var clipBuf bytes.Buffer
	clipboardWriter = &clipBuf

	m := NewDashboardModel(testSocketPath(t))
	m.ready = true
	resModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = resModel.(*DashboardModel)

	snap := &monitor.Snapshot{
		RunnerName: "test-runner",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:       "job-1",
				Name:     "Build Job",
				Workflow: "ci.yml",
				Repo:     "adrijshikhar/actr",
				Status:   monitor.StatusRunning,
				Steps: []*monitor.StepInfo{
					{ID: "step-1", Name: "Build", Status: monitor.StatusRunning},
				},
			},
		},
	}
	m.ApplySnapshot(snap)

	lines := []string{
		"Starting build process",
		"Step 1: dependencies downloaded",
		"Step 2: compiling packages",
		"Step 3: tests executed",
		"Build succeeded completely",
	}
	m.stepLogs["job-1/step-1"] = lines
	m.Tree().SelectRow(4) // select step-1
	_ = m.updateActiveStepLogs()

	treeWidth := m.calcTreeWidth()
	sidebarWidth := treeWidth + 2

	// Viewport Y bounds: telemOuterHeight = 6, viewportTop = 7
	viewportTop := 7

	// 1. Mouse press inside viewport to start drag selection at line 1
	newModel, _ := m.Update(tea.MouseMsg{
		X:      sidebarWidth + 5,
		Y:      viewportTop + 1, // line 1: "Step 1: dependencies downloaded"
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	m = newModel.(*DashboardModel)

	assert.Equal(t, FocusViewport, m.Focus())
	assert.True(t, m.IsSelecting())
	startLine, endLine := m.DragSelection()
	assert.Equal(t, 1, startLine)
	assert.Equal(t, 1, endLine)

	// 2. Mouse motion to line 3 ("Step 3: tests executed")
	newModel, _ = m.Update(tea.MouseMsg{
		X:      sidebarWidth + 5,
		Y:      viewportTop + 3,
		Action: tea.MouseActionMotion,
	})
	m = newModel.(*DashboardModel)

	assert.True(t, m.IsSelecting())
	startLine, endLine = m.DragSelection()
	assert.Equal(t, 1, startLine)
	assert.Equal(t, 3, endLine)

	// Viewport content should show selection styling
	vpView := m.Viewport().View()
	assert.Contains(t, vpView, selectedLogStyle.Render("Step 1: dependencies downloaded"))

	// 3. Mouse release: copies lines 1..3 to clipboard, sets HUD toast
	newModel, _ = m.Update(tea.MouseMsg{
		X:      sidebarWidth + 5,
		Y:      viewportTop + 3,
		Action: tea.MouseActionRelease,
		Button: tea.MouseButtonNone,
	})
	m = newModel.(*DashboardModel)

	assert.False(t, m.IsSelecting(), "selection should end on release")
	assert.Equal(t, "✓ Copied selection to clipboard", m.ToastMsg())
	assert.Contains(t, m.View(), "✓ Copied selection to clipboard")

	// Verify clipboard output
	clipOutput := clipBuf.String()
	assert.Contains(t, clipOutput, "\x1b]52;c;")
	expectedSelected := strings.Join(lines[1:4], "\n")
	b64Expected := base64.StdEncoding.EncodeToString([]byte(expectedSelected))
	assert.Contains(t, clipOutput, b64Expected)
}

func TestDashboard_KeyboardCopyShortcut(t *testing.T) {
	oldWriter := clipboardWriter
	oldGOOS := currentGOOS
	defer func() {
		clipboardWriter = oldWriter
		currentGOOS = oldGOOS
	}()

	currentGOOS = "linux"
	var clipBuf bytes.Buffer
	clipboardWriter = &clipBuf

	m := NewDashboardModel(testSocketPath(t))
	m.ready = true
	resModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = resModel.(*DashboardModel)

	snap := &monitor.Snapshot{
		RunnerName: "test-runner",
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:       "job-1",
				Name:     "Build Job",
				Workflow: "ci.yml",
				Repo:     "adrijshikhar/actr",
				Status:   monitor.StatusRunning,
				Steps: []*monitor.StepInfo{
					{ID: "step-1", Name: "Build", Status: monitor.StatusRunning},
				},
			},
		},
	}
	m.ApplySnapshot(snap)

	lines := []string{
		"first line",
		"second line",
		"third line",
	}
	m.stepLogs["job-1/step-1"] = lines
	m.Tree().SelectRow(4) // select step-1
	_ = m.updateActiveStepLogs()

	// Press 'c' to copy entire step log
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = newModel.(*DashboardModel)

	expectedToast := fmt.Sprintf("✓ Copied %d lines to clipboard", len(lines))
	assert.Equal(t, expectedToast, m.ToastMsg())
	assert.Contains(t, m.View(), expectedToast)

	// Verify clipboard content
	clipOutput := clipBuf.String()
	assert.Contains(t, clipOutput, "\x1b]52;c;")
	b64Expected := base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
	assert.Contains(t, clipOutput, b64Expected)

	// Pressing another navigation key clears the toast
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = newModel.(*DashboardModel)
	assert.Empty(t, m.ToastMsg(), "toast should clear on next navigation key")
	assert.NotContains(t, m.View(), expectedToast)
}

func TestDashboard_ToastNotificationLifecycle(t *testing.T) {
	m := NewDashboardModel(testSocketPath(t))
	m.ready = true
	resModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = resModel.(*DashboardModel)

	m.setToast("✓ Copied 42 lines to clipboard")
	assert.Equal(t, "✓ Copied 42 lines to clipboard", m.ToastMsg())

	view := m.View()
	assert.Contains(t, view, "✓ Copied 42 lines to clipboard")

	// Trigger clearToastMsg
	newModel, _ := m.Update(clearToastMsg{id: m.toastID})
	m = newModel.(*DashboardModel)
	assert.Empty(t, m.ToastMsg(), "toast should be empty after clearToastMsg")
}

func TestDashboard_SidebarScrollingWithManyHistoricalRuns(t *testing.T) {
	m := NewDashboardModel(testSocketPath(t))
	m.ready = true
	resModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = resModel.(*DashboardModel)

	// Create 50 historical runs
	snap := &monitor.Snapshot{
		RunnerName: "test-runner",
	}
	for i := 1; i <= 50; i++ {
		snap.RecentJobs = append(snap.RecentJobs, &monitor.JobInfo{
			ID:        fmt.Sprintf("hist-job-%02d", i),
			Name:      fmt.Sprintf("workflow-%02d", i),
			RepoAlias: "default",
			Status:    monitor.StatusSuccess,
		})
	}

	m.ApplySnapshot(snap)

	// Expand historical root and default repo
	for _, r := range m.tree.Rows() {
		if r.Node != nil && (r.Node.ID == "historical-root" || r.Node.ID == "hist-repo:default") {
			r.Node.Expanded = true
		}
	}
	m.tree.Rebuild()

	totalRows := len(m.tree.Rows())
	assert.Greater(t, totalRows, 50, "should have > 50 rows in the tree")

	// Render view to populate sizes and heights
	v := m.View()
	assert.NotEmpty(t, v)
	assert.Greater(t, m.tree.Height, 0)
	assert.Equal(t, 0, m.tree.Offset)
	treeW := m.calcTreeWidth()
	treeV := m.tree.View(treeW)
	assert.NotContains(t, treeV, "█", "tree view should not render scrollbar block")
	assert.NotContains(t, treeV, "│", "tree view should not render scrollbar track")

	// Navigate down 30 rows
	m.focus = FocusTree
	for i := 0; i < 30; i++ {
		res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = res.(*DashboardModel)
	}

	assert.Equal(t, 30, m.tree.Cursor)
	assert.Greater(t, m.tree.Offset, 0, "offset should have increased to scroll down")
	assert.LessOrEqual(t, m.tree.Offset, m.tree.Cursor)
	assert.Greater(t, m.tree.Offset+m.tree.Height, m.tree.Cursor)

	treeVScrolled := m.tree.View(treeW)
	assert.NotContains(t, treeVScrolled, "█")
	assert.NotContains(t, treeVScrolled, "│")

	// Test PageDown key
	prevOffset := m.tree.Offset
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = res.(*DashboardModel)
	assert.GreaterOrEqual(t, m.tree.Offset, prevOffset)

	// Test PageUp key
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = res.(*DashboardModel)
	assert.LessOrEqual(t, m.tree.Offset, prevOffset+m.tree.Height)

	// Test Mouse click on a scrolled row
	clickedY := 5
	treeTopOffset := 1
	expectedRow := m.tree.Offset + clickedY - treeTopOffset
	res, _ = m.Update(tea.MouseMsg{
		X:      5,
		Y:      clickedY,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	m = res.(*DashboardModel)
	assert.Equal(t, expectedRow, m.tree.Cursor, "clicked row should account for m.tree.Offset")
}

func TestDashboard_SidebarHeightStabilityAndPreciseClickInteraction(t *testing.T) {
	termHeight := 30
	m := NewDashboardModel(testSocketPath(t))
	m.ready = true
	resModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: termHeight})
	m = resModel.(*DashboardModel)

	snap := &monitor.Snapshot{
		RunnerName: "test-runner",
	}
	for i := 1; i <= 50; i++ {
		snap.RecentJobs = append(snap.RecentJobs, &monitor.JobInfo{
			ID:        fmt.Sprintf("hist-job-%02d", i),
			Name:      fmt.Sprintf("workflow-%02d", i),
			RepoAlias: "default",
			Status:    monitor.StatusSuccess,
		})
	}
	m.ApplySnapshot(snap)

	// 1. When collapsed (only ~5 rows visible in tree, no scrollbar)
	viewCollapsed := m.View()
	heightCollapsed := lipgloss.Height(viewCollapsed)
	assert.Equal(t, termHeight, heightCollapsed, "view height when collapsed should exactly equal terminal height")

	// 2. Expand HISTORICAL RUNS (> 50 rows visible, scrollbar opens)
	for _, r := range m.tree.Rows() {
		if r.Node != nil && (r.Node.ID == "historical-runs" || r.Node.ID == "hist-repo:default") {
			r.Node.Expanded = true
		}
	}
	m.tree.Rebuild()
	assert.Greater(t, len(m.tree.Rows()), 50)

	viewExpanded := m.View()
	heightExpanded := lipgloss.Height(viewExpanded)
	assert.Equal(t, termHeight, heightExpanded, "view height when expanded with scrollbar MUST NOT increase")
	assert.Equal(t, heightCollapsed, heightExpanded, "view height must remain invariant between collapsed and expanded")

	// 3. Test single-click toggling of branch header
	histRow := -1
	for idx, r := range m.tree.Rows() {
		if r.Node != nil && r.Node.ID == "historical-runs" {
			histRow = idx
			break
		}
	}
	require.GreaterOrEqual(t, histRow, 0)
	histNode := m.tree.NodeAtRow(histRow)
	require.True(t, histNode.Expanded)

	// Click on the branch row to collapse it
	res, _ := m.Update(tea.MouseMsg{
		X:      10, // Clicking in the middle of "HISTORICAL RUNS" label
		Y:      histRow + 1,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	m = res.(*DashboardModel)
	assert.False(t, histNode.Expanded, "single click on branch header should collapse it")

	// Click again to expand it
	res, _ = m.Update(tea.MouseMsg{
		X:      10,
		Y:      histRow + 1,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	m = res.(*DashboardModel)
	assert.True(t, histNode.Expanded, "second click on branch header should re-expand it")

	// 4. Test clicking on an already-selected container node toggles it
	defaultRepoRow := -1
	for idx, r := range m.tree.Rows() {
		if r.Node != nil && r.Node.ID == "hist-repo:default" {
			defaultRepoRow = idx
			break
		}
	}
	require.GreaterOrEqual(t, defaultRepoRow, 0)
	defaultRepoNode := m.tree.NodeAtRow(defaultRepoRow)
	require.True(t, defaultRepoNode.Expanded)

	// Select it first
	m.tree.SelectRow(defaultRepoRow)
	assert.Equal(t, defaultRepoRow, m.tree.Cursor)

	// Click on already-selected row (on label) -> should toggle
	res, _ = m.Update(tea.MouseMsg{
		X:      8,
		Y:      defaultRepoRow + 1,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	m = res.(*DashboardModel)
	assert.False(t, defaultRepoNode.Expanded, "clicking on already-selected container node should toggle expansion")

	// 5. Verify no scrollbar characters in sidebar view even when rows exceed height
	defaultRepoNode.Expanded = true
	m.tree.Rebuild()
	m.tree.SetHeight(10)
	treeWidth := m.calcTreeWidth()
	treeView := m.tree.View(treeWidth)
	assert.NotContains(t, treeView, "█", "tree view should not render scrollbar block")
	assert.NotContains(t, treeView, "│", "tree view should not render scrollbar track")

	// 6. Verify CircleCI-style log line numbers gutter and pure text copy
	jobNode := m.tree.FindNode("hist-job-01")
	require.NotNil(t, jobNode)
	stepNode := &TreeNode{
		ID:       "step-1",
		Label:    "Setup Node",
		NodeType: NodeStep,
		Status:   monitor.StatusSuccess,
	}
	jobNode.AddChild(stepNode)
	jobNode.Expanded = true
	m.tree.Rebuild()
	m.tree.SelectNode(stepNode)
	m.stepLogs["hist-job-01/step-1"] = []string{
		"npm install",
		"npm test",
	}
	_ = m.updateActiveStepLogs()

	vpOutput := m.viewport.View()
	assert.Contains(t, vpOutput, "  1 │ npm install")
	assert.Contains(t, vpOutput, "  2 │ npm test")

	// Verify copying via 'c' key copies pure logs without line numbers or gutter
	mockClip := &mockClipboard{}
	m.SetClipboard(mockClip)
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = res.(*DashboardModel)
	assert.Equal(t, "npm install\nnpm test", mockClip.copied, "clipboard must receive pure log lines without gutter numbers")
}
