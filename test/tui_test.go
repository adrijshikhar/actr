package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTUI_RaceFreeConcurrentEventsAndReconnection simulates Bubble Tea's event loop
// with concurrent command execution, event streaming, and periodic reconnection.
// Under `go test -race`, this verifies that `waitForEvent` is pure and does not
// cause data races with model mutations in `Update()`.
func TestTUI_RaceFreeConcurrentEventsAndReconnection(t *testing.T) {
	sockPath := testSocketPath(t)

	server := monitor.NewServer(sockPath, "test-runner")
	require.NoError(t, server.Start())
	defer func() { _ = server.Stop() }()

	m := tui.NewDashboardModel(sockPath)
	initCmd := m.Init()
	require.NotNil(t, initCmd)

	// Hydrate initial window dimensions
	resModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = resModel.(*tui.DashboardModel)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Bubble Tea message channel (simulating tea.Program message loop)
	msgQueue := make(chan tea.Msg, 200)

	// Execute initial Init command
	go func() {
		if msg := initCmd(); msg != nil {
			msgQueue <- msg
		}
	}()

	// Background worker 1: Server publishing events continuously
	go func() {
		ticker := time.NewTicker(3 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				i++
				server.Publish(monitor.Event{
					Type:    monitor.EventStepLog,
					JobID:   "job-live-race",
					StepID:  "step-race-1",
					LogLine: fmt.Sprintf("race test log line %d", i),
				})
			}
		}
	}()

	// Background worker 2: Trigger periodic ReconnectMsg to simulate connection drops
	go func() {
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case msgQueue <- tui.ReconnectMsg{}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	// Main Bubble Tea event loop: processes messages sequentially, spawns commands in goroutines
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-msgQueue:
			var cmd tea.Cmd
			resModel, cmd = m.Update(msg)
			m = resModel.(*tui.DashboardModel)
			if cmd != nil {
				go func(c tea.Cmd) {
					if outMsg := c(); outMsg != nil {
						select {
						case msgQueue <- outMsg:
						case <-ctx.Done():
						}
					}
				}(cmd)
			}
		}
	}
}

// TestTUI_AsyncHistoricalLogLoading verifies that selecting a historical step node
// dispatches an asynchronous fetch command, displays "(loading logs...)" while pending,
// and renders the fetched logs once historicalLogsLoadedMsg is processed.
func TestTUI_AsyncHistoricalLogLoading(t *testing.T) {
	sockPath := testSocketPath(t)

	l, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	mux := http.NewServeMux()
	mux.HandleFunc("/runs/job-past-1/steps/step-checkout/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]string{
			"Cloning repository...",
			"HEAD is now at 8b4c3e1 Initial commit",
			"Checkout successful",
		})
	})
	httpServer := &http.Server{Handler: mux}
	go func() { _ = httpServer.Serve(l) }()
	defer func() { _ = httpServer.Close() }()

	m := tui.NewDashboardModel(sockPath)
	resModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = resModel.(*tui.DashboardModel)

	// Populate tree with historical job and step
	snap := &monitor.Snapshot{
		RunnerName: "runner-async",
		RecentJobs: []*monitor.JobInfo{
			{
				ID:     "job-past-1",
				Name:   "Historical Workflow",
				Status: monitor.StatusSuccess,
				Steps: []*monitor.StepInfo{
					{ID: "step-checkout", Name: "Checkout", Status: monitor.StatusSuccess},
				},
			},
		},
	}
	resModel, _ = m.Update(tui.SnapshotMsg(snap))
	m = resModel.(*tui.DashboardModel)

	// Expand historical runs branch and job node
	histBranch := m.Tree().Root.Children[1]
	require.Equal(t, "historical-runs", histBranch.ID)
	require.NotEmpty(t, histBranch.Children)
	jobNode := histBranch.Children[0]
	jobNode.Expanded = true
	m.Tree().Rebuild()

	// Select the historical step
	found := m.Tree().SelectFirst(func(n *tui.TreeNode) bool { return n.ID == "step-checkout" })
	require.True(t, found, "historical step-checkout must be in tree")

	// Trigger navigation update to load logs
	resModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = resModel.(*tui.DashboardModel)

	// Command must be returned for asynchronous fetching
	require.NotNil(t, cmd, "expected async log loading command")

	// Viewport must show subtle pending indicator while logs are being fetched
	assert.Contains(t, m.Viewport().View(), "loading logs", "viewport must display loading indicator while pending")

	// Execute command asynchronously and deliver loaded message back to model
	msg := cmd()
	require.NotNil(t, msg)

	resModel, _ = m.Update(msg)
	m = resModel.(*tui.DashboardModel)

	// Viewport must now display the loaded historical logs
	vpContent := m.Viewport().View()
	assert.Contains(t, vpContent, "HEAD is now at 8b4c3e1 Initial commit")
	assert.Contains(t, vpContent, "Checkout successful")
}

// TestTUI_StepNodeScopedToParentRun verifies that active and historical runs sharing
// identical step IDs (e.g. "checkout") do not collide or mutate each other's tree nodes.
func TestTUI_StepNodeScopedToParentRun(t *testing.T) {
	sockPath := testSocketPath(t)
	m := tui.NewDashboardModel(sockPath)
	resModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = resModel.(*tui.DashboardModel)

	// Hydrate snapshot with historical run containing step "checkout"
	snap := &monitor.Snapshot{
		RunnerName: "runner-scoping",
		RecentJobs: []*monitor.JobInfo{
			{
				ID:     "job-historical",
				Name:   "Previous Run",
				Status: monitor.StatusSuccess,
				Steps: []*monitor.StepInfo{
					{
						ID:     "checkout",
						Name:   "Checkout (Historical v1)",
						Status: monitor.StatusSuccess,
					},
				},
			},
		},
		ActiveJobs: []*monitor.JobInfo{
			{
				ID:     "job-active",
				Name:   "Active Run",
				Status: monitor.StatusRunning,
			},
		},
	}
	resModel, _ = m.Update(tui.SnapshotMsg(snap))
	m = resModel.(*tui.DashboardModel)

	// Now emit EventStepStart for the ACTIVE run with the identical step ID "checkout"
	resModel, _ = m.Update(tui.EventMsg(monitor.Event{
		Type:   monitor.EventStepStart,
		JobID:  "job-active",
		StepID: "checkout",
		Step: &monitor.StepInfo{
			ID:     "checkout",
			Name:   "Checkout (Active v2)",
			Status: monitor.StatusRunning,
		},
	}))
	m = resModel.(*tui.DashboardModel)

	// Find historical run node
	var histJobNode *tui.TreeNode
	var activeJobNode *tui.TreeNode
	m.Tree().Root.Walk(func(n *tui.TreeNode) bool {
		if n.ID == "job-historical" {
			histJobNode = n
		}
		if n.ID == "job-active" {
			activeJobNode = n
		}
		return true
	})

	require.NotNil(t, histJobNode, "historical job node must exist")
	require.NotNil(t, activeJobNode, "active job node must exist")

	// Verify historical run's step was NOT mutated
	require.Len(t, histJobNode.Children, 1)
	histStep := histJobNode.Children[0]
	assert.Equal(t, "checkout", histStep.ID)
	assert.Equal(t, "Checkout (Historical v1)", histStep.Label)
	assert.Equal(t, monitor.StatusSuccess, histStep.Status)

	// Verify active run has its own step node with active attributes
	require.Len(t, activeJobNode.Children, 1)
	activeStep := activeJobNode.Children[0]
	assert.Equal(t, "checkout", activeStep.ID)
	assert.Equal(t, "Checkout (Active v2)", activeStep.Label)
	assert.Equal(t, monitor.StatusRunning, activeStep.Status)
}

// TestTUI_ModeB_MonospaceRAMAlignment verifies that in historical Peak Vitals mode,
// the opening bracket '[' aligns at identical column indices across diverse RAM byte sizes.
func TestTUI_ModeB_MonospaceRAMAlignment(t *testing.T) {
	testRAMValues := []uint64{
		0,
		1024,                      // 1 KB
		10 * 1024 * 1024,          // 10 MB
		100 * 1024 * 1024,         // 100 MB
		105381888,                 // 100.5 MB (8-char formatBytes)
		1073641882,                // 1023.9 MB (9-char formatBytes)
		1610612736,                // 1.5 GB
		16 * 1024 * 1024 * 1024,   // 16 GB
	}

	for _, ramBytes := range testRAMValues {
		t.Run(fmt.Sprintf("ram_bytes_%d", ramBytes), func(t *testing.T) {
			tw := tui.NewTelemetryWidget()
			summary := &tui.HistoricalRunSummary{
				JobName:        "ram-align-test",
				Status:         monitor.StatusSuccess,
				DurationSec:    60,
				Driver:         "docker",
				PeakCPUPercent: 75.5,
				PeakRSSBytes:   ramBytes,
				Limits: &config.LimitsConfig{
					CPU:    2.0,
					Memory: 32 * 1024 * 1024 * 1024,
				},
			}
			tw.SetHistoricalRun(summary)

			view := tw.View(90)
			lines := strings.Split(view, "\n")
			var cpuLine, ramLine string
			for _, l := range lines {
				if strings.Contains(l, "PEAK CPU:") {
					cpuLine = l
				}
				if strings.Contains(l, "PEAK RAM:") {
					ramLine = l
				}
			}

			require.NotEmpty(t, cpuLine, "view must contain PEAK CPU line")
			require.NotEmpty(t, ramLine, "view must contain PEAK RAM line")

			cpuIdx := strings.Index(cpuLine, "[")
			ramIdx := strings.Index(ramLine, "[")
			require.NotEqual(t, -1, cpuIdx)
			require.NotEqual(t, -1, ramIdx)
			assert.Equal(t, 21, cpuIdx, "PEAK CPU bracket '[' should be at column 21")
			assert.Equal(t, 21, ramIdx, "PEAK RAM bracket '[' should be at column 21")
			assert.Equal(t, cpuIdx, ramIdx, "Peak CPU and Peak RAM bar brackets must align")
			if ramBytes == 1073641882 {
				assert.Contains(t, ramLine, "1023.9 MB", "Peak RAM line must not truncate 9-character unit string")
			}
		})
	}
}

func TestTUI_SnapshotPropagatesLogLoadingCommand(t *testing.T) {
	tempDir := t.TempDir()
	sockPath := testSocketPath(t)

	store, err := history.NewDiskStore(tempDir)
	require.NoError(t, err)

	// Create completed run in history before starting server
	jobID := "job-hist-snap-cmd"
	stepID := "step-build"
	require.NoError(t, store.StartRun(&monitor.JobInfo{
		ID:        jobID,
		Name:      "Build App",
		Status:    monitor.StatusRunning,
		StartedAt: time.Now().Add(-10 * time.Second),
	}, nil, "docker"))
	require.NoError(t, store.AppendStepLog(jobID, stepID, "echo building artifact"))
	require.NoError(t, store.FinishStep(jobID, stepID, "success", 0, time.Now()))
	require.NoError(t, store.FinishRun(jobID, monitor.StatusSuccess, nil))

	server := monitor.NewServer(sockPath, "test-runner").WithHistoryStore(store)
	require.NoError(t, server.Start())
	defer func() {
		_ = server.Stop()
	}()

	// Instantiate dashboard model
	model := tui.NewDashboardModel(sockPath)
	client := monitor.NewClient(sockPath)
	snap, err := client.GetSnapshot(context.Background())
	require.NoError(t, err)
	require.NotNil(t, snap)

	// Dispatch initial snapshot message
	updatedModel, cmd := model.Update(tui.SnapshotMsg(snap))
	_ = cmd
	dm, ok := updatedModel.(*tui.DashboardModel)
	require.True(t, ok)

	// Expand historical node and select the step node
	require.True(t, dm.Tree().SelectFirst(func(n *tui.TreeNode) bool {
		if n.ID == jobID {
			n.Expanded = true
			return true
		}
		return false
	}))
	dm.Tree().Rebuild()
	require.True(t, dm.Tree().SelectFirst(func(n *tui.TreeNode) bool {
		return n.ID == stepID
	}))

	// Re-applying snapshot or receiving update while step is selected must produce a batched command containing log fetch
	updatedModel2, cmd2 := dm.Update(tui.SnapshotMsg(snap))
	require.NotNil(t, cmd2, "SnapshotMsg with historical step selected must return command to fetch logs")
	_ = updatedModel2

	msgCh := make(chan tea.Msg, 1)
	go func() {
		msgCh <- cmd2()
	}()
	select {
	case msg2 := <-msgCh:
		batchMsg, isBatch := msg2.(tea.BatchMsg)
		require.True(t, isBatch, "SnapshotMsg must batch the historical log fetch command with waitCmd")
		require.Len(t, batchMsg, 2, "batch must contain both log fetch command and event listener")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for command execution: cmd2 did not return tea.BatchMsg immediately")
	}
}

