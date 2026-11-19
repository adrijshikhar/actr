package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// EventMsg wraps an incoming monitor.Event for the Bubbletea event loop.
type EventMsg monitor.Event

// SnapshotMsg wraps an incoming monitor.Snapshot for initial state hydration.
type SnapshotMsg *monitor.Snapshot

// ErrMsg wraps streaming or IPC errors.
type ErrMsg error

// ReconnectMsg triggers a reconnection attempt after a backoff.
type ReconnectMsg struct{}
type reconnectMsg = ReconnectMsg

// streamClosedMsg indicates the event stream closed.
type streamClosedMsg struct{}

// streamErrMsg wraps streaming errors.
type streamErrMsg struct {
	err error
}

// monitorEventMsg is an alias for EventMsg for internal compatibility.
type monitorEventMsg = EventMsg

// historicalLogsLoadedMsg delivers asynchronously fetched logs for a historical step.
type historicalLogsLoadedMsg struct {
	runID  string
	stepID string
	logs   []string
	err    error
}

const (
	StatusBadgeConnected = "● CONNECTED (LIVE)"
	StatusBadgeOffline   = "◉ OFFLINE (HISTORY)"
)

const maxStepLogLines = 10000

// DashboardModel is the unified Bubbletea model integrating the Runner Explorer tree,
// live process telemetry gauges, and ANSI log streaming viewport with zoom mode.
type DashboardModel struct {
	sockPath   string
	client     *monitor.Client
	tree       *TreeWidget
	telemetry  *TelemetryWidget
	viewport   viewport.Model
	isZoomed   bool
	autoFollow bool

	width        int
	height       int
	ready        bool
	ctx          context.Context
	cancel       context.CancelFunc
	activeStepID string
	stepLogs     map[string][]string
	lastErr      error

	autoFocusRunningStep bool
	autoFocused          bool

	historicalSummaries map[string]*HistoricalRunSummary
	seenRepoExpansion   map[string]bool

	historyStore monitor.HistoryStore
	isOffline    bool

	events <-chan monitor.Event
	errs   <-chan error
}

// NewDashboardModel creates a new DashboardModel configured to connect to the given socket.
func NewDashboardModel(sockPath string) *DashboardModel {
	root := &TreeNode{
		ID:       "root",
		Label:    "actr",
		Expanded: true,
		NodeType: NodeBranch,
	}
	liveNode := &TreeNode{
		ID:       "live-runs",
		Label:    "LIVE RUNS (0 active)",
		Expanded: true,
		NodeType: NodeBranch,
	}
	histNode := &TreeNode{
		ID:       "historical-runs",
		Label:    "HISTORICAL RUNS (0 past runs)",
		Expanded: true,
		NodeType: NodeBranch,
	}
	root.AddChild(liveNode)
	root.AddChild(histNode)

	return &DashboardModel{
		sockPath:            sockPath,
		client:              monitor.NewClient(sockPath),
		tree:                NewTreeWidget(root),
		telemetry:           NewTelemetryWidget(),
		viewport:            viewport.New(0, 0),
		autoFollow:          true,
		stepLogs:            make(map[string][]string),
		historicalSummaries: make(map[string]*HistoricalRunSummary),
		seenRepoExpansion:   make(map[string]bool),
	}
}

// AutoFocusRunningStep enables auto-focusing the active running step on initial snapshot.
func (m *DashboardModel) AutoFocusRunningStep() {
	m.autoFocusRunningStep = true
}

// StatusBadge returns the current connection status indicator badge string.
func (m *DashboardModel) StatusBadge() string {
	if m == nil {
		return ""
	}
	if m.isOffline {
		return StatusBadgeOffline
	}
	return StatusBadgeConnected
}

// IsOffline returns whether the dashboard is currently in offline history mode.
func (m *DashboardModel) IsOffline() bool {
	if m == nil {
		return false
	}
	return m.isOffline
}

// SetOffline sets the offline history mode status and refreshes branch labels.
func (m *DashboardModel) SetOffline(offline bool) {
	if m == nil {
		return
	}
	m.isOffline = offline
	m.updateBranchLabels()
}

// SetHistoryStore sets the history store for offline fallback log loading.
func (m *DashboardModel) SetHistoryStore(store monitor.HistoryStore) {
	if m == nil {
		return
	}
	m.historyStore = store
}

// WithHistoryStore sets the history store for offline fallback log loading and returns m for chaining.
func (m *DashboardModel) WithHistoryStore(store monitor.HistoryStore) *DashboardModel {
	if m != nil {
		m.historyStore = store
	}
	return m
}

// ApplySnapshot applies a monitor.Snapshot to rebuild tree and update telemetry.
func (m *DashboardModel) ApplySnapshot(snap *monitor.Snapshot) tea.Cmd {
	if m == nil {
		return nil
	}
	return m.applySnapshot(snap)
}

// Tree returns the underlying TreeWidget.
func (m *DashboardModel) Tree() *TreeWidget {
	if m == nil {
		return nil
	}
	return m.tree
}

// Viewport returns a pointer to the underlying viewport model.
func (m *DashboardModel) Viewport() *viewport.Model {
	if m == nil {
		return nil
	}
	return &m.viewport
}

// StepLogs returns the in-memory map of step logs.
func (m *DashboardModel) StepLogs() map[string][]string {
	if m == nil {
		return nil
	}
	return m.stepLogs
}

// Telemetry returns the underlying TelemetryWidget.
func (m *DashboardModel) Telemetry() *TelemetryWidget {
	if m == nil {
		return nil
	}
	return m.telemetry
}

// HydrateSnapshotFromStore reads up to limit runs from historyStore and builds a Snapshot
// populated with RecentJobs (including steps) ready for offline dashboard hydration.
func HydrateSnapshotFromStore(store monitor.HistoryStore, limit int) (*monitor.Snapshot, error) {
	if store == nil {
		return &monitor.Snapshot{
			RunnerName: "actr",
			ActiveJobs: make([]*monitor.JobInfo, 0),
			RecentJobs: make([]*monitor.JobInfo, 0),
		}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	runs, err := store.ListRuns(limit)
	if err != nil {
		return nil, fmt.Errorf("list runs from store: %w", err)
	}

	snap := &monitor.Snapshot{
		RunnerName: "actr",
		ActiveJobs: make([]*monitor.JobInfo, 0),
		RecentJobs: make([]*monitor.JobInfo, 0, len(runs)),
	}

	for _, r := range runs {
		if r == nil || r.ID == "" {
			continue
		}
		var steps []*monitor.StepInfo
		if meta, err := store.GetRun(r.ID); err == nil && meta != nil {
			steps = make([]*monitor.StepInfo, 0, len(meta.Steps))
			for _, st := range meta.Steps {
				if st == nil {
					continue
				}
				steps = append(steps, &monitor.StepInfo{
					ID:        st.ID,
					Name:      st.Name,
					Number:    st.Number,
					Status:    st.Status,
					StartedAt: st.StartedAt,
					EndedAt:   st.EndedAt,
				})
			}
		}
		if steps == nil {
			steps = make([]*monitor.StepInfo, 0)
		}

		job := &monitor.JobInfo{
			ID:        r.ID,
			Name:      r.Name,
			Workflow:  r.Workflow,
			Repo:      r.Repo,
			Owner:     r.Owner,
			Status:    r.Status,
			StartedAt: r.StartedAt,
			EndedAt:   r.EndedAt,
			Steps:     steps,
		}
		snap.RecentJobs = append(snap.RecentJobs, job)
	}

	return snap, nil
}

// SetHistoricalRunSummary caches or associates a historical run summary for a given job ID.
func (m *DashboardModel) SetHistoricalRunSummary(jobID string, summary *HistoricalRunSummary) {
	if m == nil {
		return
	}
	if m.historicalSummaries == nil {
		m.historicalSummaries = make(map[string]*HistoricalRunSummary)
	}
	m.historicalSummaries[jobID] = summary
}

// Init sets up context cancellation and fetches initial state (live snapshot or offline polling).
func (m *DashboardModel) Init() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel

	if m.isOffline {
		return tea.Tick(1*time.Second, func(time.Time) tea.Msg {
			return ReconnectMsg{}
		})
	}

	if m.client != nil {
		m.events, m.errs = m.client.StreamEvents(ctx)
	}

	client := m.client
	return func() tea.Msg {
		if client == nil {
			return ErrMsg(fmt.Errorf("client not configured"))
		}
		snap, err := client.GetSnapshot(ctx)
		if err != nil {
			return ErrMsg(err)
		}
		return SnapshotMsg(snap)
	}
}

// WaitForEvent returns a tea.Cmd that waits for an incoming event or error on the provided channels.
// It is pure and does not touch or mutate the model.
func WaitForEvent(events <-chan monitor.Event, errs <-chan error) tea.Cmd {
	return waitForEventWithContext(context.Background(), events, errs)
}

func waitForEventWithContext(ctx context.Context, events <-chan monitor.Event, errs <-chan error) tea.Cmd {
	return func() tea.Msg {
		if ctx != nil && ctx.Err() != nil {
			return nil
		}
		if events == nil && errs == nil {
			return nil
		}
		var done <-chan struct{}
		if ctx != nil {
			done = ctx.Done()
		}
		select {
		case <-done:
			return nil
		case ev, ok := <-events:
			if !ok {
				return streamClosedMsg{}
			}
			return monitorEventMsg(ev)
		case err, ok := <-errs:
			if !ok {
				return streamClosedMsg{}
			}
			return streamErrMsg{err: err}
		}
	}
}

func (m *DashboardModel) waitForEvents(ctx context.Context) tea.Cmd {
	return waitForEventWithContext(ctx, m.events, m.errs)
}

func fetchHistoricalLogsCmd(client *monitor.Client, runID, stepID string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return historicalLogsLoadedMsg{runID: runID, stepID: stepID, logs: nil, err: fmt.Errorf("client not configured")}
		}
		logs, err := client.GetHistoricalStepLogs(context.Background(), runID, stepID)
		return historicalLogsLoadedMsg{runID: runID, stepID: stepID, logs: logs, err: err}
	}
}

// Update handles Bubbletea messages including window resize, keyboard navigation,
// zoom toggle, snapshots, and streaming IPC events.
func (m *DashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateSizes()
		m.ready = true

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		case "z":
			m.isZoomed = !m.isZoomed
			m.updateSizes()
			return m, nil
		case "f":
			m.autoFollow = !m.autoFollow
			if m.autoFollow {
				m.viewport.GotoBottom()
			}
			return m, nil
		case "k", "up":
			if !m.isZoomed {
				m.tree.Up()
				return m, m.updateActiveStepLogs()
			}
		case "j", "down":
			if !m.isZoomed {
				m.tree.Down()
				return m, m.updateActiveStepLogs()
			}
		case "enter", " ":
			if !m.isZoomed {
				m.tree.Toggle()
				if sel := m.tree.SelectedNode(); sel != nil && sel.NodeType == NodeRepo {
					m.seenRepoExpansion[sel.ID] = sel.Expanded
				}
				return m, m.updateActiveStepLogs()
			}
		case "h", "left":
			if !m.isZoomed {
				m.tree.Collapse()
				if sel := m.tree.SelectedNode(); sel != nil && sel.NodeType == NodeRepo {
					m.seenRepoExpansion[sel.ID] = sel.Expanded
				}
				return m, m.updateActiveStepLogs()
			}
		case "l", "right":
			if !m.isZoomed {
				m.tree.Expand()
				if sel := m.tree.SelectedNode(); sel != nil && sel.NodeType == NodeRepo {
					m.seenRepoExpansion[sel.ID] = sel.Expanded
				}
				return m, m.updateActiveStepLogs()
			}
		case "pgdown", "pagedown", "ctrl+d":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		case "pgup", "pageup", "ctrl+u":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}

	case SnapshotMsg:
		m.lastErr = nil
		wasOffline := m.isOffline
		m.isOffline = false
		cmd := m.applySnapshot((*monitor.Snapshot)(msg))
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if (wasOffline || m.events == nil) && m.client != nil {
			m.events, m.errs = m.client.StreamEvents(ctx)
		}
		var waitCmd tea.Cmd
		if ctx.Err() == nil {
			waitCmd = waitForEventWithContext(ctx, m.events, m.errs)
		}
		if cmd != nil && waitCmd != nil {
			return m, tea.Batch(cmd, waitCmd)
		} else if cmd != nil {
			return m, cmd
		}
		return m, waitCmd

	case EventMsg:
		cmd := m.handleEvent(monitor.Event(msg))
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		var waitCmd tea.Cmd
		if ctx.Err() == nil {
			waitCmd = waitForEventWithContext(ctx, m.events, m.errs)
		}
		if cmd != nil && waitCmd != nil {
			return m, tea.Batch(cmd, waitCmd)
		} else if cmd != nil {
			return m, cmd
		}
		return m, waitCmd

	case streamClosedMsg:
		m.lastErr = fmt.Errorf("stream closed")
		m.events = nil
		m.errs = nil
		m.isOffline = true
		m.updateBranchLabels()
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if ctx.Err() == nil {
			return m, tea.Tick(1*time.Second, func(time.Time) tea.Msg {
				return ReconnectMsg{}
			})
		}
		return m, nil

	case streamErrMsg:
		m.lastErr = msg.err
		m.events = nil
		m.errs = nil
		m.isOffline = true
		m.updateBranchLabels()
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if ctx.Err() == nil {
			return m, tea.Tick(1*time.Second, func(time.Time) tea.Msg {
				return ReconnectMsg{}
			})
		}
		return m, nil

	case ErrMsg:
		m.lastErr = error(msg)
		m.events = nil
		m.errs = nil
		m.isOffline = true
		m.updateBranchLabels()
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if ctx.Err() == nil {
			return m, tea.Tick(1*time.Second, func(time.Time) tea.Msg {
				return ReconnectMsg{}
			})
		}
		return m, nil

	case ReconnectMsg:
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if ctx.Err() == nil {
			client := m.client
			return m, func() tea.Msg {
				if client == nil {
					return ErrMsg(fmt.Errorf("client not configured"))
				}
				snap, err := client.GetSnapshot(ctx)
				if err != nil {
					return ErrMsg(err)
				}
				return SnapshotMsg(snap)
			}
		}
		return m, nil

	case historicalLogsLoadedMsg:
		if msg.err == nil && len(msg.logs) > 0 {
			logs := msg.logs
			if len(logs) > maxStepLogLines {
				logs = logs[len(logs)-maxStepLogLines:]
			}
			m.stepLogs[msg.runID+"/"+msg.stepID] = logs
			m.stepLogs[msg.stepID] = logs
			sel := m.tree.SelectedNode()
			if sel != nil && sel.ID == msg.stepID && (sel.Parent == nil || sel.Parent.ID == msg.runID) {
				m.viewport.SetContent(strings.Join(logs, "\n"))
				if m.autoFollow {
					m.viewport.GotoBottom()
				}
			}
		} else if msg.err != nil {
			sel := m.tree.SelectedNode()
			if sel != nil && sel.ID == msg.stepID && (sel.Parent == nil || sel.Parent.ID == msg.runID) {
				m.viewport.SetContent(fmt.Sprintf("Failed to load logs: %v", msg.err))
			}
		} else {
			sel := m.tree.SelectedNode()
			if sel != nil && sel.ID == msg.stepID && (sel.Parent == nil || sel.Parent.ID == msg.runID) {
				m.viewport.SetContent(renderStepEmptyState(sel))
			}
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// calcTreeWidth computes the responsive inner width of the tree pane.
func (m *DashboardModel) calcTreeWidth() int {
	if m.width <= 0 {
		return 20
	}
	if m.width < 60 {
		w := int(float64(m.width) * 0.28)
		if w < 14 {
			w = 14
		}
		return w
	}
	if m.width < 120 {
		w := int(float64(m.width) * 0.26)
		if w < 18 {
			w = 18
		}
		if w > 30 {
			w = 30
		}
		return w
	}
	w := int(float64(m.width) * 0.22)
	if w < 28 {
		w = 28
	}
	if w > 34 {
		w = 34
	}
	return w
}

// calcDimensions computes responsive layout dimensions based on terminal width and height.
// It returns treeWidth, rightWidth, targetOuterHeight, and logInnerHeight.
func (m *DashboardModel) calcDimensions() (treeWidth, rightWidth, targetOuterHeight, logInnerHeight int) {
	targetOuterHeight = m.height - 1
	if targetOuterHeight < 6 {
		targetOuterHeight = 6
	}

	if m.isZoomed {
		zoomWidth := m.width - 2
		if zoomWidth < 10 {
			zoomWidth = 10
		}
		zoomHeight := targetOuterHeight - 2
		if zoomHeight < 1 {
			zoomHeight = 1
		}
		return 0, zoomWidth, targetOuterHeight, zoomHeight
	}

	treeWidth = m.calcTreeWidth()
	rightWidth = m.width - treeWidth - 4
	if rightWidth < 16 {
		rightWidth = 16
	}

	telemOuterHeight := 6
	if m.height < 18 {
		telemOuterHeight = 4
	}

	logOuterHeight := targetOuterHeight - telemOuterHeight
	logInnerHeight = logOuterHeight - 2
	if logInnerHeight < 1 {
		logInnerHeight = 1
	}
	return treeWidth, rightWidth, targetOuterHeight, logInnerHeight
}

func (m *DashboardModel) updateSizes() {
	_, rightWidth, _, logHeight := m.calcDimensions()
	m.viewport.Width = rightWidth
	m.viewport.Height = logHeight
}

func (m *DashboardModel) applySnapshot(snap *monitor.Snapshot) tea.Cmd {
	if snap == nil {
		return nil
	}
	if snap.Telemetry != nil {
		m.telemetry.Update(snap.Telemetry)
	}
	if snap.SupervisorStatus != nil {
		m.telemetry.SetSupervisorStatus(snap.SupervisorStatus)
	}
	for stepKey, lines := range snap.StepLogs {
		if len(lines) > maxStepLogLines {
			lines = lines[len(lines)-maxStepLogLines:]
		}
		m.stepLogs[stepKey] = lines
	}
	m.rebuildTreeFromSnapshot(snap)
	if m.autoFocusRunningStep && !m.autoFocused {
		m.autoFocused = true
		found := m.tree.SelectFirst(func(node *TreeNode) bool {
			return node.Status == monitor.StatusRunning && node.Parent != nil && node.Parent.ID != "root" && node.Parent.ID != "live-runs" && node.Parent.ID != "historical-runs"
		})
		if !found {
			_ = m.tree.SelectFirst(func(node *TreeNode) bool {
				return node.Status == monitor.StatusRunning && node.ID != "root" && node.ID != "live-runs" && node.ID != "historical-runs"
			})
		}
	}
	return m.updateActiveStepLogs()
}

func formatRunnerRootLabel(runnerName string) string {
	if runnerName == "" || runnerName == "actr" {
		return "actr"
	}
	if strings.HasPrefix(runnerName, "actr • ") {
		return runnerName
	}
	return "actr • " + runnerName
}

func (m *DashboardModel) getLiveBranch() *TreeNode {
	if m.tree == nil || m.tree.Root == nil {
		return nil
	}
	for _, ch := range m.tree.Root.Children {
		if ch.ID == "live-runs" {
			return ch
		}
	}
	return nil
}

func (m *DashboardModel) getHistoricalBranch() *TreeNode {
	if m.tree == nil || m.tree.Root == nil {
		return nil
	}
	for _, ch := range m.tree.Root.Children {
		if ch.ID == "historical-runs" {
			return ch
		}
	}
	return nil
}

func (m *DashboardModel) ensureBranches() (*TreeNode, *TreeNode) {
	if m.tree.Root == nil {
		m.tree.Root = &TreeNode{ID: "root", Label: "actr", Expanded: true, NodeType: NodeBranch}
	}
	live := m.getLiveBranch()
	if live == nil {
		live = &TreeNode{
			ID:       "live-runs",
			Label:    "LIVE RUNS (0 active)",
			Expanded: true,
			NodeType: NodeBranch,
		}
		m.tree.Root.AddChild(live)
	}
	hist := m.getHistoricalBranch()
	if hist == nil {
		hist = &TreeNode{
			ID:       "historical-runs",
			Label:    "HISTORICAL RUNS (0 past runs)",
			Expanded: true,
			NodeType: NodeBranch,
		}
		m.tree.Root.AddChild(hist)
	}
	return live, hist
}

func (m *DashboardModel) updateBranchLabels() {
	live := m.getLiveBranch()
	if live != nil {
		if m.isOffline {
			live.Label = "LIVE RUNS (daemon offline • polling...)"
		} else {
			activeCount := 0
			for _, ch := range live.Children {
				if ch.NodeType == NodeRepo {
					activeCount += len(ch.Children)
				} else {
					activeCount++
				}
			}
			live.Label = fmt.Sprintf("LIVE RUNS (%d active)", activeCount)
		}
	}
	hist := m.getHistoricalBranch()
	if hist != nil {
		hist.Label = fmt.Sprintf("HISTORICAL RUNS (%d past runs)", len(hist.Children))
	}
}

func isHistoricalNode(node *TreeNode) bool {
	for curr := node; curr != nil; curr = curr.Parent {
		if curr.ID == "historical-runs" {
			return true
		}
	}
	return false
}

func findHistoricalJobNode(sel *TreeNode) *TreeNode {
	if sel == nil || sel.ID == "historical-runs" || sel.ID == "root" || sel.ID == "live-runs" {
		return nil
	}
	for curr := sel; curr != nil; curr = curr.Parent {
		if curr.Parent != nil && curr.Parent.ID == "historical-runs" {
			return curr
		}
	}
	return nil
}

func (m *DashboardModel) extractHistoricalSummary(jobNode *TreeNode) *HistoricalRunSummary {
	if jobNode == nil {
		return nil
	}

	if m.historicalSummaries != nil {
		if s, ok := m.historicalSummaries[jobNode.ID]; ok && s != nil {
			return s
		}
	}

	if m.historyStore != nil {
		if meta, err := m.historyStore.GetRun(jobNode.ID); err == nil && meta != nil {
			summary := &HistoricalRunSummary{
				JobName:        meta.Name,
				Status:         meta.Status,
				DurationSec:    meta.DurationSec,
				Driver:         meta.Driver,
				Limits:         meta.Limits,
				PeakCPUPercent: meta.MetricsSummary.PeakCPUPercent,
				PeakRSSBytes:   meta.MetricsSummary.PeakRSSBytes,
				TotalNetRx:     meta.MetricsSummary.TotalNetRxBytes,
				TotalNetTx:     meta.MetricsSummary.TotalNetTxBytes,
				TotalDiskWrite: meta.MetricsSummary.TotalDiskWrittenBytes,
			}
			m.SetHistoricalRunSummary(jobNode.ID, summary)
			return summary
		}
	}

	if jobNode.Data != nil {
		switch d := jobNode.Data.(type) {
		case *HistoricalRunSummary:
			return d
		case HistoricalRunSummary:
			return &d
		case *monitor.RunMetadata:
			return &HistoricalRunSummary{
				JobName:        d.Name,
				Status:         d.Status,
				DurationSec:    d.DurationSec,
				Driver:         d.Driver,
				Limits:         d.Limits,
				PeakCPUPercent: d.MetricsSummary.PeakCPUPercent,
				PeakRSSBytes:   d.MetricsSummary.PeakRSSBytes,
				TotalNetRx:     d.MetricsSummary.TotalNetRxBytes,
				TotalNetTx:     d.MetricsSummary.TotalNetTxBytes,
				TotalDiskWrite: d.MetricsSummary.TotalDiskWrittenBytes,
			}
		case *monitor.RunSummary:
			return &HistoricalRunSummary{
				JobName:        d.Name,
				Status:         d.Status,
				DurationSec:    d.DurationSec,
				Driver:         d.Driver,
				PeakCPUPercent: d.PeakCPUPercent,
				PeakRSSBytes:   d.PeakRSSBytes,
			}
		case *monitor.JobInfo:
			var dur int64
			if !d.EndedAt.IsZero() && !d.StartedAt.IsZero() {
				dur = int64(d.EndedAt.Sub(d.StartedAt).Seconds())
			}
			return &HistoricalRunSummary{
				JobName:     d.Name,
				Status:      d.Status,
				DurationSec: dur,
			}
		}
	}

	return &HistoricalRunSummary{
		JobName: jobNode.Label,
		Status:  jobNode.Status,
	}
}

func (m *DashboardModel) syncHistoricalHUD() {
	if m == nil || m.telemetry == nil || m.tree == nil {
		return
	}
	sel := m.tree.SelectedNode()
	if sel != nil && isHistoricalNode(sel) && sel.ID != "historical-runs" {
		jobNode := findHistoricalJobNode(sel)
		if jobNode != nil {
			summary := m.extractHistoricalSummary(jobNode)
			m.telemetry.SetHistoricalRun(summary)
			return
		}
	}
	m.telemetry.ClearHistoricalRun()
}

func (m *DashboardModel) rebuildTreeFromSnapshot(snap *monitor.Snapshot) {
	label := formatRunnerRootLabel(snap.RunnerName)
	root := &TreeNode{
		ID:       "root",
		Label:    label,
		Expanded: true,
		NodeType: NodeBranch,
	}

	liveNode := &TreeNode{
		ID:       "live-runs",
		Expanded: true,
		NodeType: NodeBranch,
	}
	if m.isOffline {
		liveNode.Label = "LIVE RUNS (daemon offline • polling...)"
	} else {
		liveNode.Label = fmt.Sprintf("LIVE RUNS (%d active)", len(snap.ActiveJobs))
	}

	histNode := &TreeNode{
		ID:       "historical-runs",
		Expanded: true,
		NodeType: NodeBranch,
	}

	existingHistJobs := make(map[string]*TreeNode)
	var existingHistOrder []*TreeNode
	if oldHist := m.getHistoricalBranch(); oldHist != nil {
		for _, ch := range oldHist.Children {
			existingHistJobs[ch.ID] = ch
			existingHistOrder = append(existingHistOrder, ch)
		}
	}

	isMultiRepo := snap.SupervisorStatus != nil
	if !isMultiRepo {
		for _, job := range snap.ActiveJobs {
			if job != nil && (job.Repo != "" || job.RepoAlias != "") {
				isMultiRepo = true
				break
			}
		}
	}

	if isMultiRepo {
		repoJobs := make(map[string][]*monitor.JobInfo)
		var repoOrder []string

		for _, job := range snap.ActiveJobs {
			if job == nil {
				continue
			}
			repoKey := job.Repo
			if repoKey == "" {
				repoKey = job.RepoAlias
			}
			if repoKey == "" {
				repoKey = "default"
			}
			if _, exists := repoJobs[repoKey]; !exists {
				repoJobs[repoKey] = []*monitor.JobInfo{}
				repoOrder = append(repoOrder, repoKey)
			}
			repoJobs[repoKey] = append(repoJobs[repoKey], job)
		}

		for _, rKey := range repoOrder {
			jobs := repoJobs[rKey]
			repoID := "repo:" + rKey
			expanded := true
			if exp, ok := m.seenRepoExpansion[repoID]; ok {
				expanded = exp
			}
			repoNode := &TreeNode{
				ID:       repoID,
				Label:    rKey,
				Expanded: expanded,
				NodeType: NodeRepo,
			}
			for _, job := range jobs {
				jobNode := &TreeNode{
					ID:       job.ID,
					Label:    job.Name,
					Status:   job.Status,
					Expanded: true,
					NodeType: NodeJob,
					Data:     job,
				}
				for _, step := range job.Steps {
					if step != nil {
						stepNode := &TreeNode{
							ID:       step.ID,
							Label:    step.Name,
							Status:   step.Status,
							NodeType: NodeStep,
						}
						jobNode.AddChild(stepNode)
					}
				}
				repoNode.AddChild(jobNode)
			}
			liveNode.AddChild(repoNode)
		}
	} else {
		for _, job := range snap.ActiveJobs {
			if job == nil {
				continue
			}
			jobNode := &TreeNode{
				ID:       job.ID,
				Label:    job.Name,
				Status:   job.Status,
				Expanded: true,
				NodeType: NodeJob,
				Data:     job,
			}
			for _, step := range job.Steps {
				if step != nil {
					stepNode := &TreeNode{
						ID:       step.ID,
						Label:    step.Name,
						Status:   step.Status,
						NodeType: NodeStep,
					}
					jobNode.AddChild(stepNode)
				}
			}
			liveNode.AddChild(jobNode)
		}
	}

	selectedID := ""
	selectedParentID := ""
	if sel := m.tree.SelectedNode(); sel != nil {
		selectedID = sel.ID
		if sel.Parent != nil {
			selectedParentID = sel.Parent.ID
		}
	}

	seenHistIDs := make(map[string]bool)
	for _, job := range snap.RecentJobs {
		if job == nil {
			continue
		}
		seenHistIDs[job.ID] = true
		expanded := false
		if oldJob, ok := existingHistJobs[job.ID]; ok {
			expanded = oldJob.Expanded
		}
		jobNode := &TreeNode{
			ID:       job.ID,
			Label:    job.Name,
			Status:   job.Status,
			Expanded: expanded,
			NodeType: NodeJob,
			Data:     job,
		}
		for _, step := range job.Steps {
			if step != nil {
				stepNode := &TreeNode{
					ID:       step.ID,
					Label:    step.Name,
					Status:   step.Status,
					NodeType: NodeStep,
				}
				jobNode.AddChild(stepNode)
			}
		}
		histNode.AddChild(jobNode)
	}

	// Preserve existing historical runs that were loaded but not present in the new snapshot
	for _, oldJob := range existingHistOrder {
		if !seenHistIDs[oldJob.ID] {
			seenHistIDs[oldJob.ID] = true
			histNode.AddChild(oldJob)
		}
	}

	histNode.Label = fmt.Sprintf("HISTORICAL RUNS (%d past runs)", len(histNode.Children))

	root.AddChild(liveNode)
	root.AddChild(histNode)

	m.tree.Root = root
	m.tree.Rebuild()

	if selectedID != "" {
		_ = m.tree.SelectFirst(func(n *TreeNode) bool {
			if selectedParentID != "" && n.Parent != nil {
				return n.ID == selectedID && n.Parent.ID == selectedParentID
			}
			return n.ID == selectedID
		})
	}
}

func (m *DashboardModel) handleEvent(ev monitor.Event) tea.Cmd {
	switch ev.Type {
	case monitor.EventTelemetry:
		m.telemetry.Update(ev.Telemetry)
	case monitor.EventStepLog:
		if ev.StepID != "" && ev.LogLine != "" {
			key := ev.StepID
			if ev.JobID != "" {
				key = ev.JobID + "/" + ev.StepID
			}
			logs := append(m.stepLogs[key], ev.LogLine)
			if len(logs) > maxStepLogLines {
				logs = logs[len(logs)-maxStepLogLines:]
			}
			m.stepLogs[key] = logs
			if key != ev.StepID {
				m.stepLogs[ev.StepID] = logs
			}
			if ev.StepID == m.activeStepID || key == m.activeStepID {
				m.viewport.SetContent(strings.Join(logs, "\n"))
				if m.autoFollow {
					m.viewport.GotoBottom()
				}
			}
		}
	case monitor.EventJobStart:
		if ev.Job != nil {
			m.handleJobStart(ev.Job)
		} else if ev.JobID != "" {
			live, _ := m.ensureBranches()
			var target *TreeNode
			for _, ch := range live.Children {
				if ch.ID == ev.JobID {
					target = ch
					break
				}
			}
			if target == nil {
				target = &TreeNode{
					ID:       ev.JobID,
					Label:    ev.JobID,
					Status:   monitor.StatusRunning,
					Expanded: true,
					NodeType: NodeJob,
				}
				live.AddChild(target)
			} else {
				target.Status = monitor.StatusRunning
			}
			m.updateBranchLabels()
			m.tree.Rebuild()
		}
	case monitor.EventJobFinish:
		if ev.Job != nil {
			return m.handleJobFinish(ev.Job.ID, ev.Job)
		} else if ev.JobID != "" {
			return m.handleJobFinish(ev.JobID, nil)
		}
	case monitor.EventStepStart, monitor.EventStepFinish:
		if ev.Step != nil {
			m.upsertStepNode(ev.JobID, ev.Step)
		} else if ev.StepID != "" {
			status := monitor.StatusRunning
			if ev.Type == monitor.EventStepFinish {
				status = monitor.StatusSuccess
			}
			m.updateNodeStatus(ev.StepID, status)
		}
	}
	return nil
}

func (m *DashboardModel) handleJobStart(job *monitor.JobInfo) {
	if job == nil {
		return
	}
	live, _ := m.ensureBranches()
	isMulti := (job.Repo != "" || job.RepoAlias != "")
	if !isMulti {
		for _, ch := range live.Children {
			if ch.NodeType == NodeRepo {
				isMulti = true
				break
			}
		}
	}

	var parentNode *TreeNode = live
	if isMulti {
		repoKey := job.Repo
		if repoKey == "" {
			repoKey = job.RepoAlias
		}
		if repoKey == "" {
			repoKey = "default"
		}
		repoID := "repo:" + repoKey
		var repoNode *TreeNode
		for _, ch := range live.Children {
			if ch.ID == repoID {
				repoNode = ch
				break
			}
		}
		if repoNode == nil {
			expanded := true
			if exp, ok := m.seenRepoExpansion[repoID]; ok {
				expanded = exp
			}
			repoNode = &TreeNode{
				ID:       repoID,
				Label:    repoKey,
				Expanded: expanded,
				NodeType: NodeRepo,
			}
			live.AddChild(repoNode)
		}
		parentNode = repoNode
	}

	var target *TreeNode
	for _, ch := range parentNode.Children {
		if ch.ID == job.ID {
			target = ch
			break
		}
	}
	if target == nil {
		target = &TreeNode{
			ID:       job.ID,
			Label:    job.Name,
			Status:   job.Status,
			Expanded: true,
			NodeType: NodeJob,
			Data:     job,
		}
		parentNode.AddChild(target)
	} else {
		target.Label = job.Name
		target.Status = job.Status
		target.Data = job
	}
	for _, st := range job.Steps {
		if st != nil {
			var stepNode *TreeNode
			for _, sc := range target.Children {
				if sc.ID == st.ID {
					stepNode = sc
					break
				}
			}
			if stepNode == nil {
				stepNode = &TreeNode{
					ID:       st.ID,
					Label:    st.Name,
					Status:   st.Status,
					NodeType: NodeStep,
				}
				target.AddChild(stepNode)
			} else {
				stepNode.Label = st.Name
				stepNode.Status = st.Status
			}
		}
	}
	m.updateBranchLabels()
	m.tree.Rebuild()
}

func (m *DashboardModel) handleJobFinish(jobID string, job *monitor.JobInfo) tea.Cmd {
	live, hist := m.ensureBranches()
	var target *TreeNode

	// Look for job in live branch (both direct children and inside NodeRepo children)
	for i, ch := range live.Children {
		if ch.ID == jobID || (job != nil && ch.ID == job.ID) {
			target = ch
			live.Children = append(live.Children[:i], live.Children[i+1:]...)
			break
		}
		if ch.NodeType == NodeRepo {
			for j, sub := range ch.Children {
				if sub.ID == jobID || (job != nil && sub.ID == job.ID) {
					target = sub
					ch.Children = append(ch.Children[:j], ch.Children[j+1:]...)
					if len(ch.Children) == 0 {
						live.Children = append(live.Children[:i], live.Children[i+1:]...)
					}
					break
				}
			}
			if target != nil {
				break
			}
		}
	}

	selectedBefore := m.tree.SelectedNode()

	if target == nil {
		// Check if already in historical branch
		for _, ch := range hist.Children {
			if ch.ID == jobID || (job != nil && ch.ID == job.ID) {
				target = ch
				break
			}
		}
		if target == nil {
			label := jobID
			status := monitor.StatusSuccess
			if job != nil {
				if job.Name != "" {
					label = job.Name
				}
				status = job.Status
			}
			target = &TreeNode{
				ID:       jobID,
				Label:    label,
				Status:   status,
				Expanded: false,
				NodeType: NodeJob,
			}
		}
	}

	if job != nil {
		target.Data = job
		if job.Name != "" {
			target.Label = job.Name
		}
		target.Status = job.Status
		target.NodeType = NodeJob
		for _, st := range job.Steps {
			if st != nil {
				var stepNode *TreeNode
				for _, sc := range target.Children {
					if sc.ID == st.ID {
						stepNode = sc
						break
					}
				}
				if stepNode == nil {
					stepNode = &TreeNode{
						ID:       st.ID,
						Label:    st.Name,
						Status:   st.Status,
						NodeType: NodeStep,
					}
					target.AddChild(stepNode)
				} else {
					stepNode.Label = st.Name
					stepNode.Status = st.Status
				}
			}
		}
	} else {
		if target.Status == monitor.StatusRunning || target.Status == "" {
			target.Status = monitor.StatusSuccess
		}
	}

	alreadyInHist := false
	for _, ch := range hist.Children {
		if ch == target {
			alreadyInHist = true
			break
		}
	}
	if !alreadyInHist {
		target.Parent = hist
		hist.Children = append([]*TreeNode{target}, hist.Children...)
	}

	m.updateBranchLabels()
	m.tree.Rebuild()

	if selectedBefore != nil {
		m.tree.SelectNode(selectedBefore)
	}
	return m.updateActiveStepLogs()
}

func (m *DashboardModel) upsertStepNode(jobID string, step *monitor.StepInfo) {
	if step == nil || step.ID == "" {
		return
	}
	live, hist := m.ensureBranches()

	// 1. Find or create the parent job container
	var parentJob *TreeNode
	if jobID != "" {
		m.tree.Root.Walk(func(n *TreeNode) bool {
			if n.ID == jobID {
				parentJob = n
				return false
			}
			return true
		})
		if parentJob == nil {
			parentJob = &TreeNode{
				ID:       jobID,
				Label:    jobID,
				Expanded: true,
				NodeType: NodeJob,
			}
			live.AddChild(parentJob)
			m.updateBranchLabels()
		}
	} else {
		// jobID is empty (e.g. from coordinator.NotifyStepStart)
		if len(live.Children) == 1 && live.Children[0].NodeType != NodeRepo {
			parentJob = live.Children[0]
		} else {
			for _, ch := range live.Children {
				if ch.NodeType == NodeRepo {
					for _, sub := range ch.Children {
						if sub.ID == "active-job" || sub.ID == "job" || sub.Status == monitor.StatusRunning {
							parentJob = sub
							break
						}
					}
					if parentJob != nil {
						break
					}
				} else if ch.ID == "active-job" || ch.ID == "job" || ch.Status == monitor.StatusRunning {
					parentJob = ch
					break
				}
			}
			if parentJob == nil {
				for _, ch := range hist.Children {
					if ch.ID == "active-job" || ch.ID == "job" {
						parentJob = ch
						break
					}
				}
			}
			if parentJob == nil {
				parentJob = &TreeNode{
					ID:       "active-job",
					Label:    "Active Job",
					Expanded: true,
					NodeType: NodeJob,
				}
				live.AddChild(parentJob)
				m.updateBranchLabels()
			}
		}
	}

	// 2. Search strictly within the parent run node's children to prevent cross-run ID collisions
	var existingStep *TreeNode
	for _, child := range parentJob.Children {
		if child.ID == step.ID {
			existingStep = child
			break
		}
	}

	if existingStep != nil {
		if step.Name != "" {
			existingStep.Label = step.Name
		}
		existingStep.Status = step.Status
		m.tree.Rebuild()
		return
	}

	// 3. Step is new, add child to parentJob
	label := step.Name
	if label == "" {
		label = step.ID
	}
	targetStep := &TreeNode{
		ID:       step.ID,
		Label:    label,
		Status:   step.Status,
		NodeType: NodeStep,
	}
	parentJob.AddChild(targetStep)
	m.tree.Rebuild()
}

func (m *DashboardModel) updateNodeStatus(nodeID string, status monitor.StepStatus) {
	if m.tree.Root == nil {
		return
	}
	m.tree.Root.Walk(func(n *TreeNode) bool {
		if n.ID == nodeID {
			n.Status = status
			return false
		}
		return true
	})
	m.tree.Rebuild()
}

func (m *DashboardModel) renderRepoState(sel *TreeNode) string {
	if sel == nil {
		return ""
	}
	title := sel.Label
	activeCount := len(sel.Children)
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  Repository: %s\n", title))
	sb.WriteString(fmt.Sprintf("  Active Jobs: %d\n", activeCount))
	if activeCount > 0 {
		sb.WriteString("  Jobs:\n")
		for _, j := range sel.Children {
			sb.WriteString(fmt.Sprintf("    • %s [%s]\n", j.Label, j.Status))
		}
	} else {
		sb.WriteString("  Status: Idle (no active jobs running)\n")
	}
	return sb.String()
}

func (m *DashboardModel) updateActiveStepLogs() tea.Cmd {
	if m.width > 0 && m.height > 0 && (m.viewport.Width == 0 || m.viewport.Height == 0) {
		m.updateSizes()
	}
	m.syncHistoricalHUD()
	sel := m.tree.SelectedNode()
	if sel != nil {
		m.activeStepID = sel.ID

		if sel.NodeType == NodeRepo {
			m.viewport.SetContent(m.renderRepoState(sel))
			return nil
		}

		var lines []string
		hasParentJob := sel.NodeType == NodeStep || (sel.Parent != nil && sel.Parent.NodeType == NodeJob) || (sel.Parent != nil && sel.Parent.ID != "" && sel.Parent.ID != "root" && sel.Parent.ID != "live-runs" && sel.Parent.ID != "historical-runs" && sel.Parent.NodeType != NodeRepo)
		if hasParentJob && sel.Parent != nil {
			lines = m.stepLogs[sel.Parent.ID+"/"+sel.ID]
			if len(lines) == 0 && !isHistoricalNode(sel) {
				lines = m.stepLogs[sel.ID]
			}
		} else {
			lines = m.stepLogs[sel.ID]
		}

		if len(lines) > 0 {
			m.viewport.SetContent(strings.Join(lines, "\n"))
			if m.autoFollow {
				m.viewport.GotoBottom()
			}
			return nil
		}

		// If logs are not in memory, check if this is a historical step
		if isHistoricalNode(sel) && sel.Parent != nil && sel.Parent.ID != "" && sel.Parent.ID != "historical-runs" && sel.Parent.NodeType != NodeRepo {
			jobID := sel.Parent.ID
			stepID := sel.ID
			// In offline mode with a direct history store, read immediately without IPC
			if m.isOffline && m.historyStore != nil {
				fetched, _ := m.historyStore.GetStepLogs(jobID, stepID)
				if len(fetched) > 0 {
					if len(fetched) > maxStepLogLines {
						fetched = fetched[len(fetched)-maxStepLogLines:]
					}
					m.stepLogs[jobID+"/"+stepID] = fetched
					m.stepLogs[stepID] = fetched
					m.viewport.SetContent(strings.Join(fetched, "\n"))
					if m.autoFollow {
						m.viewport.GotoBottom()
					}
					return nil
				}
			} else if m.client != nil {
				m.viewport.SetContent("(loading logs...)")
				return fetchHistoricalLogsCmd(m.client, jobID, stepID)
			}
		}

		if sel.ID == "root" || sel.Parent == nil || sel.ID == "live-runs" || sel.ID == "historical-runs" || sel.NodeType == NodeBranch {
			m.viewport.SetContent(m.renderRootState(m.tree.Root.Label))
		} else {
			m.viewport.SetContent(renderStepEmptyState(sel))
		}
	}
	return nil
}

// View renders the split-pane dashboard or 100% full-screen zoom mode.
func (m *DashboardModel) View() string {
	if !m.ready {
		return "Initializing dashboard..."
	}
	m.telemetry.SetOffline(m.isOffline)
	m.syncHistoricalHUD()

	borderStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#475569"))

	var content string
	if m.isZoomed {
		_, zoomWidth, targetOuterHeight, zoomHeight := m.calcDimensions()
		_ = targetOuterHeight
		m.viewport.Width = zoomWidth
		m.viewport.Height = zoomHeight
		content = borderStyle.Width(zoomWidth).Height(zoomHeight).Render(m.viewport.View())
	} else {
		treeWidth, rightWidth, targetOuterHeight, logInnerHeight := m.calcDimensions()
		_ = logInnerHeight

		treeInnerHeight := targetOuterHeight - 2
		if treeInnerHeight < 1 {
			treeInnerHeight = 1
		}
		treeBox := borderStyle.Width(treeWidth).Height(treeInnerHeight).Render(m.tree.View(treeWidth))

		telemWidth := rightWidth
		var telemContent string
		if m.height < 18 {
			telemContent = m.telemetry.ViewCompact(telemWidth)
		} else {
			telemContent = m.telemetry.View(telemWidth)
		}
		telemBox := borderStyle.Width(rightWidth).Render(telemContent)
		telemOuterHeight := lipgloss.Height(telemBox)

		logOuterHeight := targetOuterHeight - telemOuterHeight
		actualLogInnerHeight := logOuterHeight - 2
		if actualLogInnerHeight < 1 {
			actualLogInnerHeight = 1
		}
		m.viewport.Width = rightWidth
		m.viewport.Height = actualLogInnerHeight

		logBox := borderStyle.Width(rightWidth).Height(actualLogInnerHeight).Render(m.viewport.View())
		rightPane := lipgloss.JoinVertical(lipgloss.Left, telemBox, logBox)
		content = lipgloss.JoinHorizontal(lipgloss.Top, treeBox, rightPane)
	}

	footer := m.renderFooter()
	return lipgloss.JoinVertical(lipgloss.Left, content, footer)
}

func leftNavText(width int, err error) string {
	var nav string
	switch {
	case width >= 125 && err == nil:
		nav = " ↑/k up • ↓/j down • Enter/Space toggle • z zoom • f follow • q quit"
	case width >= 100 && err == nil:
		nav = " ↑/k up • ↓/j down • Enter toggle • z zoom • f follow • q quit"
	case width >= 50 && err == nil:
		nav = " ↑/k up • ↓/j down • Enter toggle • q quit"
	case err != nil:
		nav = "↑/k up • ↓/j down • Enter toggle • q quit"
	default:
		nav = " ↑/↓ nav • Enter toggle • q quit"
	}
	if err != nil {
		return fmt.Sprintf(" Error: %v • %s", err, nav)
	}
	return nav
}

func (m *DashboardModel) renderFooter() string {
	leftStr := leftNavText(m.width, m.lastErr)
	left := dimStyle.Render(leftStr)
	leftW := lipgloss.Width(left)

	sample := m.telemetry.Sample()
	if sample == nil || m.width < 70 {
		if leftW > m.width && m.width > 0 {
			return dimStyle.Render(ansi.Truncate(leftStr, m.width, ""))
		}
		return left
	}

	memPct := 0.0
	if sample.HostMemTotal > 0 {
		memPct = float64(sample.HostMemUsed) / float64(sample.HostMemTotal) * 100
	}

	// 1. Try wide format if clearance allows (needs at least 1 space gap)
	wideText := fmt.Sprintf("HOST: %.1f%% CPU │ %.0f%% RAM │ %s Free │ Load: %.2f ",
		sample.HostCPU, memPct, formatBytes(sample.DiskFreeBytes), sample.HostLoad1m)
	wideRight := lipgloss.NewStyle().Foreground(lipgloss.Color("#94A3B8")).Render(wideText)
	wideW := lipgloss.Width(wideRight)

	if m.width >= 100 && m.width >= leftW+wideW+1 {
		gap := m.width - leftW - wideW
		return left + strings.Repeat(" ", gap) + wideRight
	}

	// 2. Try compact format if clearance allows
	compactText := fmt.Sprintf("HOST: %.0f%%C • %.0f%%M • %s ",
		sample.HostCPU, memPct, formatBytes(sample.DiskFreeBytes))
	compactRight := lipgloss.NewStyle().Foreground(lipgloss.Color("#94A3B8")).Render(compactText)
	compactW := lipgloss.Width(compactRight)

	if m.width >= leftW+compactW+1 {
		gap := m.width - leftW - compactW
		return left + strings.Repeat(" ", gap) + compactRight
	}

	// 3. Fallback: insufficient clearance for host vitals
	if leftW > m.width && m.width > 0 {
		return dimStyle.Render(ansi.Truncate(leftStr, m.width, ""))
	}
	return left
}

func (m *DashboardModel) renderRootState(runnerName string) string {
	var activeJob, activeStep string
	if m.tree != nil && m.tree.Root != nil {
		live := m.getLiveBranch()
		if live != nil {
			for _, j := range live.Children {
				if j.Status == monitor.StatusRunning {
					activeJob = j.Label
					for _, s := range j.Children {
						if s.Status == monitor.StatusRunning {
							activeStep = s.Label
							break
						}
					}
					break
				}
			}
		}
		if activeJob == "" {
			for _, j := range m.tree.Root.Children {
				if j.Status == monitor.StatusRunning && j.ID != "live-runs" && j.ID != "historical-runs" {
					activeJob = j.Label
					for _, s := range j.Children {
						if s.Status == monitor.StatusRunning {
							activeStep = s.Label
							break
						}
					}
					break
				}
			}
		}
	}
	return renderRunnerState(m.viewport.Width, runnerName, m.sockPath, activeJob, activeStep, m.isOffline)
}

func renderRunnerState(width int, runnerName, sockPath, activeJob, activeStep string, offline bool) string {
	_ = width
	if runnerName == "" {
		runnerName = "actr"
	}
	if sockPath == "" {
		sockPath = "~/.actr/actr.sock"
	}

	var b strings.Builder
	b.WriteString("\n")

	if activeJob != "" {
		badge := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#38BDF8")).
			Render("⚡ ACTR RUNNER ACTIVE")

		statusLine := greenStyle.Render(fmt.Sprintf("● Workflow execution in progress: %s", activeJob))
		b.WriteString("  " + badge + "\n")
		b.WriteString("  " + statusLine + "\n\n")

		b.WriteString("  " + normalStyle.Bold(true).Render("Runner Details:") + "\n")
		fmt.Fprintf(&b, "    Name   : %s\n", normalStyle.Render(runnerName))
		fmt.Fprintf(&b, "    Socket : %s\n", dimStyle.Render(sockPath))
		fmt.Fprintf(&b, "    Status : %s\n\n", greenStyle.Render("Active • Executing "+activeJob))

		b.WriteString("  " + normalStyle.Bold(true).Render("Active Step:") + "\n")
		if activeStep != "" {
			fmt.Fprintf(&b, "    %s\n\n", greenStyle.Render("● "+activeStep))
		} else {
			fmt.Fprintf(&b, "    %s\n\n", dimStyle.Render("Initializing step execution..."))
		}
	} else if offline {
		badge := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#F59E0B")).
			Render("⚡ ACTR RUNNER OFFLINE")

		statusLine := yellowStyle.Render("◉ OFFLINE (HISTORY) • Daemon offline, browsing local history")
		b.WriteString("  " + badge + "\n")
		b.WriteString("  " + statusLine + "\n\n")

		b.WriteString("  " + normalStyle.Bold(true).Render("Runner Details:") + "\n")
		fmt.Fprintf(&b, "    Name   : %s\n", normalStyle.Render(runnerName))
		fmt.Fprintf(&b, "    Socket : %s\n", dimStyle.Render(sockPath))
		fmt.Fprintf(&b, "    Status : %s\n\n", yellowStyle.Render("◉ OFFLINE (HISTORY) • Polling daemon socket for reconnection..."))
	} else {
		badge := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#38BDF8")).
			Render("⚡ ACTR RUNNER STANDBY")

		statusLine := greenStyle.Render("● CONNECTED (LIVE) • Daemon listening for GitHub Actions workflow runs")
		b.WriteString("  " + badge + "\n")
		b.WriteString("  " + statusLine + "\n\n")

		b.WriteString("  " + normalStyle.Bold(true).Render("Runner Details:") + "\n")
		fmt.Fprintf(&b, "    Name   : %s\n", normalStyle.Render(runnerName))
		fmt.Fprintf(&b, "    Socket : %s\n", dimStyle.Render(sockPath))
		fmt.Fprintf(&b, "    Status : %s\n\n", greenStyle.Render("● CONNECTED (LIVE) • Ready • Polling jobs"))
	}

	b.WriteString("  " + normalStyle.Bold(true).Render("Keyboard Navigation:") + "\n")
	b.WriteString("    ↑ / k , ↓ / j     Navigate jobs and execution steps\n")
	b.WriteString("    Enter , Space     Expand or collapse job containers\n")
	b.WriteString("    z                 Toggle full-screen 100% log zoom\n")
	b.WriteString("    f                 Toggle auto-follow log streaming\n")
	b.WriteString("    q                 Exit monitor\n\n")

	if activeJob != "" {
		b.WriteString("  " + dimStyle.Render("Press ↓ / j to select the active step and inspect live streaming logs.") + "\n")
	} else {
		b.WriteString("  " + dimStyle.Render("Trigger a workflow run on GitHub to inspect live execution telemetry and logs.") + "\n")
	}

	return b.String()
}

func renderStepEmptyState(sel *TreeNode) string {
	if sel == nil {
		return dimStyle.Render("No logs recorded for this item.")
	}

	switch sel.Status {
	case monitor.StatusRunning:
		return "\n  " + runningStyle.Render("● Step execution in progress...") + "\n  " + dimStyle.Render("Streaming stdout and stderr logs...")
	case monitor.StatusSuccess:
		return "\n  " + greenStyle.Render("✓ Step completed successfully.") + "\n  " + dimStyle.Render("(No stdout/stderr output was emitted by this step)")
	case monitor.StatusFailed:
		return "\n  " + redStyle.Render("✖ Step failed.") + "\n  " + dimStyle.Render("(No logs captured)")
	case monitor.StatusSkipped:
		return "\n  " + yellowStyle.Render("⊘ Step was skipped during workflow execution.")
	default:
		return "\n  " + dimStyle.Render("○ Step is queued or waiting to execute.") + "\n  " + dimStyle.Render("Logs will stream here automatically when execution starts.")
	}
}
