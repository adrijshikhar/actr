package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// FocusPanel indicates which pane currently has keyboard focus.
type FocusPanel int

const (
	FocusTree FocusPanel = iota
	FocusViewport
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

// clearToastMsg signals that a HUD toast notification has expired.
type clearToastMsg struct {
	id int
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

	focus           FocusPanel
	treeSearchInput textinput.Model
	treeSearching   bool

	logSearchInput  textinput.Model
	logSearching    bool
	searchQuery     string
	searchMatches   []int
	currentMatchIdx int

	// Mouse drag selection state
	isSelecting   bool
	dragStartLine int
	dragEndLine   int
	dragStartX    int
	dragStartY    int
	dragCurX      int
	dragCurY      int

	// Sidebar click tracking for double-click detection
	lastTreeClickTime   time.Time
	lastTreeClickNodeID string

	// HUD toast notification
	toastMsg       string
	toastExpiresAt time.Time
	toastID        int

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

	historyStore     monitor.HistoryStore
	clipboard        Clipboard
	supervisorStatus *monitor.SupervisorStatus
	isOffline        bool
	mouseEnabled     bool

	events <-chan monitor.Event
	errs   <-chan error
}

var (
	normalMatchStyle     = lipgloss.NewStyle().Background(lipgloss.Color("226")).Foreground(lipgloss.Color("0"))
	activeMatchStyle     = lipgloss.NewStyle().Background(lipgloss.Color("208")).Foreground(lipgloss.Color("0")).Bold(true)
	selectedLogStyle     = lipgloss.NewStyle().Background(lipgloss.Color("#2563EB")).Foreground(lipgloss.Color("#FFFFFF"))
	toastStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Bold(true)
	logGutterStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#475569"))
	logGutterActiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
	logGutterSep         = lipgloss.NewStyle().Foreground(lipgloss.Color("#334155")).Render("│ ")
)

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
		Expanded: false,
		NodeType: NodeBranch,
	}
	root.AddChild(liveNode)
	root.AddChild(histNode)

	ti := textinput.New()
	ti.Prompt = "Search: "
	ti.Placeholder = "[ query ]"
	ti.CharLimit = 64
	ti.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
	ti.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#64748B"))

	lti := textinput.New()
	lti.Prompt = "Find in logs: "
	lti.Placeholder = "[ query ]"
	lti.CharLimit = 64
	lti.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
	lti.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))
	lti.PlaceholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#64748B"))

	return &DashboardModel{
		sockPath:            sockPath,
		client:              monitor.NewClient(sockPath),
		tree:                NewTreeWidget(root),
		clipboard:           DefaultClipboard,
		telemetry:           NewTelemetryWidget(),
		viewport:            viewport.New(0, 0),
		autoFollow:          true,
		stepLogs:            make(map[string][]string),
		historicalSummaries: make(map[string]*HistoricalRunSummary),
		seenRepoExpansion:   make(map[string]bool),
		focus:               FocusTree,
		treeSearchInput:     ti,
		treeSearching:       false,
		logSearchInput:      lti,
		logSearching:        false,
		searchQuery:         "",
		searchMatches:       nil,
		currentMatchIdx:     0,
	}
}

// Focus returns the currently focused panel.
func (m *DashboardModel) Focus() FocusPanel {
	if m == nil {
		return FocusTree
	}
	return m.focus
}

// SetFocus sets the currently focused panel.
func (m *DashboardModel) SetFocus(f FocusPanel) {
	if m != nil {
		m.focus = f
	}
}

// MouseEnabled returns whether mouse tracking mode is currently enabled.
func (m *DashboardModel) MouseEnabled() bool {
	if m == nil {
		return false
	}
	return m.mouseEnabled
}

// SetMouseEnabled sets whether mouse tracking mode is enabled.
func (m *DashboardModel) SetMouseEnabled(enabled bool) {
	if m != nil {
		m.mouseEnabled = enabled
	}
}

// IsTreeSearching returns whether the dashboard is currently in tree search mode.
func (m *DashboardModel) IsTreeSearching() bool {
	if m == nil {
		return false
	}
	return m.treeSearching
}

// TreeSearchInput returns a pointer to the tree search input model.
func (m *DashboardModel) TreeSearchInput() *textinput.Model {
	if m == nil {
		return nil
	}
	return &m.treeSearchInput
}

// IsLogSearching returns whether the dashboard is currently in log search mode.
func (m *DashboardModel) IsLogSearching() bool {
	if m == nil {
		return false
	}
	return m.logSearching
}

// LogSearchInput returns a pointer to the log search input model.
func (m *DashboardModel) LogSearchInput() *textinput.Model {
	if m == nil {
		return nil
	}
	return &m.logSearchInput
}

// SearchQuery returns the active log search query string.
func (m *DashboardModel) SearchQuery() string {
	if m == nil {
		return ""
	}
	return m.searchQuery
}

// SearchMatches returns the 0-indexed line numbers of matching lines in the active step logs.
func (m *DashboardModel) SearchMatches() []int {
	if m == nil {
		return nil
	}
	return m.searchMatches
}

// CurrentMatchIdx returns the index in SearchMatches for the currently focused match.
func (m *DashboardModel) CurrentMatchIdx() int {
	if m == nil {
		return 0
	}
	return m.currentMatchIdx
}

// NextMatch advances to the next search match and scrolls it into view.
func (m *DashboardModel) NextMatch() {
	if len(m.searchMatches) == 0 {
		return
	}
	m.currentMatchIdx = (m.currentMatchIdx + 1) % len(m.searchMatches)
	m.autoFollow = false
	m.refreshHighlightedLogs()
	m.scrollToMatch(m.searchMatches[m.currentMatchIdx])
}

// PrevMatch moves to the previous search match and scrolls it into view.
func (m *DashboardModel) PrevMatch() {
	if len(m.searchMatches) == 0 {
		return
	}
	m.currentMatchIdx = (m.currentMatchIdx - 1 + len(m.searchMatches)) % len(m.searchMatches)
	m.autoFollow = false
	m.refreshHighlightedLogs()
	m.scrollToMatch(m.searchMatches[m.currentMatchIdx])
}

// setToast sets a temporary HUD toast message in the dashboard footer.
func (m *DashboardModel) setToast(msg string) tea.Cmd {
	if m == nil {
		return nil
	}
	m.toastMsg = msg
	m.toastExpiresAt = time.Now().Add(3 * time.Second)
	m.toastID++
	currID := m.toastID
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return clearToastMsg{id: currID}
	})
}

// ToastMsg returns the active HUD toast notification message, if any.
func (m *DashboardModel) ToastMsg() string {
	if m == nil {
		return ""
	}
	if !m.toastExpiresAt.IsZero() && time.Now().After(m.toastExpiresAt) {
		m.toastMsg = ""
	}
	return m.toastMsg
}

// IsSelecting returns whether mouse text drag-selection is currently active.
func (m *DashboardModel) IsSelecting() bool {
	if m == nil {
		return false
	}
	return m.isSelecting
}

// DragSelection returns the start and end line indices of the current drag selection.
func (m *DashboardModel) DragSelection() (startLine, endLine int) {
	if m == nil {
		return 0, 0
	}
	return m.dragStartLine, m.dragEndLine
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

// SetClipboard sets the clipboard implementation for copying logs and selections.
func (m *DashboardModel) SetClipboard(c Clipboard) {
	if m == nil {
		return
	}
	m.clipboard = c
}

// WithClipboard sets the clipboard implementation and returns m for chaining.
func (m *DashboardModel) WithClipboard(c Clipboard) *DashboardModel {
	if m != nil {
		m.clipboard = c
	}
	return m
}

func (m *DashboardModel) copyText(text string) error {
	if m != nil && m.clipboard != nil {
		return m.clipboard.Copy(text)
	}
	return DefaultClipboard.Copy(text)
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
			RepoAlias: r.RepoAlias,
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

// isMouseEscapeArtifact detects fragmented SGR 1006 mouse tracking sequences
// that leak as tea.KeyMsg runes when scrolling fast or when escape sequences split.
func isMouseEscapeArtifact(msg tea.KeyMsg) bool {
	s := msg.String()
	if strings.Contains(s, "[<") {
		return true
	}
	if strings.HasPrefix(s, "<") && strings.Contains(s, ";") {
		return true
	}
	if strings.Contains(string(msg.Runes), "[<") {
		return true
	}
	if len(s) >= 2 && (s[len(s)-1] == 'M' || s[len(s)-1] == 'm') {
		allDigitsOrSemi := true
		for _, ch := range s[:len(s)-1] {
			if (ch < '0' || ch > '9') && ch != ';' && ch != '<' && ch != '[' {
				allDigitsOrSemi = false
				break
			}
		}
		if allDigitsOrSemi {
			return true
		}
	}
	return false
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
		if isMouseEscapeArtifact(msg) {
			return m, nil
		}
		if m.treeSearching {
			switch msg.Type {
			case tea.KeyEsc:
				m.treeSearching = false
				m.treeSearchInput.Blur()
				m.treeSearchInput.SetValue("")
				m.tree.RebuildWithFilter("")
				return m, m.updateActiveStepLogs()
			case tea.KeyEnter:
				m.treeSearching = false
				m.treeSearchInput.Blur()
				return m, m.updateActiveStepLogs()
			case tea.KeyCtrlC:
				if m.cancel != nil {
					m.cancel()
				}
				return m, tea.Quit
			default:
				var cmd tea.Cmd
				prevVal := m.treeSearchInput.Value()
				m.treeSearchInput, cmd = m.treeSearchInput.Update(msg)
				newVal := m.treeSearchInput.Value()
				if newVal != prevVal {
					m.tree.RebuildWithFilter(newVal)
					fetchCmd := m.updateActiveStepLogs()
					if fetchCmd != nil {
						cmd = tea.Batch(cmd, fetchCmd)
					}
				}
				return m, cmd
			}
		}

		if m.logSearching {
			switch msg.Type {
			case tea.KeyEsc:
				m.logSearching = false
				m.logSearchInput.Blur()
				m.logSearchInput.SetValue("")
				m.searchQuery = ""
				m.searchMatches = nil
				m.currentMatchIdx = 0
				m.refreshHighlightedLogs()
				return m, nil
			case tea.KeyEnter:
				m.logSearching = false
				m.logSearchInput.Blur()
				m.focus = FocusViewport
				return m, nil
			case tea.KeyCtrlC:
				if m.cancel != nil {
					m.cancel()
				}
				return m, tea.Quit
			default:
				var cmd tea.Cmd
				prevVal := m.logSearchInput.Value()
				m.logSearchInput, cmd = m.logSearchInput.Update(msg)
				newVal := m.logSearchInput.Value()
				if newVal != prevVal {
					m.searchQuery = newVal
					m.currentMatchIdx = 0
					lines := m.activeStepLogLines()
					m.updateSearchMatches(lines)
					m.refreshHighlightedLogs()
					if len(m.searchMatches) > 0 {
						m.autoFollow = false
						m.scrollToMatch(m.searchMatches[0])
					}
				}
				return m, cmd
			}
		}

		if msg.String() != "c" && msg.String() != "C" && m.toastMsg != "" {
			m.toastMsg = ""
		}

		switch msg.String() {
		case "q", "ctrl+c":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		case "c", "C":
			logs := m.activeStepLogLines()
			if len(logs) > 0 {
				cleanLogs := make([]string, len(logs))
				for i, l := range logs {
					cleanLogs[i] = ansi.Strip(l)
				}
				if err := m.copyText(strings.Join(cleanLogs, "\n")); err != nil {
					cmd := m.setToast("✖ Failed to copy to clipboard")
					return m, cmd
				}
				cmd := m.setToast(fmt.Sprintf("✓ Copied %d lines to clipboard", len(logs)))
				return m, cmd
			}
			return m, nil
		case "s", "S":
			if !m.isZoomed {
				m.treeSearching = true
				m.focus = FocusTree
				cmd := m.treeSearchInput.Focus()
				return m, cmd
			}
		case "m", "M":
			m.mouseEnabled = !m.mouseEnabled
			var cmd tea.Cmd
			if m.mouseEnabled {
				cmd = tea.Batch(tea.EnableMouseCellMotion, m.setToast("✓ Mouse tracking enabled"))
			} else {
				cmd = tea.Batch(tea.DisableMouse, m.setToast("○ Mouse tracking disabled (native text select enabled)"))
			}
			return m, cmd
		case "tab":
			if m.focus == FocusTree {
				m.focus = FocusViewport
			} else {
				m.focus = FocusTree
			}
			return m, nil
		case "ctrl+f":
			m.logSearching = true
			m.focus = FocusViewport
			cmd := m.logSearchInput.Focus()
			return m, cmd
		case "/":
			if m.focus == FocusViewport || m.isZoomed {
				m.logSearching = true
				cmd := m.logSearchInput.Focus()
				return m, cmd
			}
			if !m.isZoomed && m.focus == FocusTree {
				m.treeSearching = true
				cmd := m.treeSearchInput.Focus()
				return m, cmd
			}
		case "n":
			if len(m.searchMatches) > 0 {
				m.NextMatch()
				return m, nil
			}
		case "N":
			if len(m.searchMatches) > 0 {
				m.PrevMatch()
				return m, nil
			}
		case "esc":
			m.focus = FocusTree
			if m.searchQuery != "" || len(m.searchMatches) > 0 {
				m.logSearching = false
				m.logSearchInput.Blur()
				m.logSearchInput.SetValue("")
				m.searchQuery = ""
				m.searchMatches = nil
				m.currentMatchIdx = 0
				m.refreshHighlightedLogs()
				return m, nil
			}
			if m.tree != nil && (m.tree.Filter() != "" || m.treeSearchInput.Value() != "") {
				m.treeSearchInput.SetValue("")
				m.tree.RebuildWithFilter("")
				return m, m.updateActiveStepLogs()
			}
		case "z":
			m.isZoomed = !m.isZoomed
			m.updateSizes()
			return m, nil
		case "f":
			m.autoFollow = !m.autoFollow
			if m.autoFollow {
				m.viewport.GotoBottom()
				m.setToast("✓ Auto-follow ON (following live logs)")
			} else {
				m.setToast("○ Auto-follow OFF (manual scrolling)")
			}
			return m, nil
		case "k", "up":
			m.focus = FocusTree
			if !m.isZoomed {
				m.tree.Up()
				return m, m.updateActiveStepLogs()
			}
		case "j", "down":
			m.focus = FocusTree
			if !m.isZoomed {
				m.tree.Down()
				return m, m.updateActiveStepLogs()
			}
		case "enter", " ":
			m.focus = FocusTree
			if !m.isZoomed {
				m.tree.Toggle()
				if sel := m.tree.SelectedNode(); sel != nil && sel.NodeType == NodeRepo {
					m.seenRepoExpansion[sel.ID] = sel.Expanded
				}
				return m, m.updateActiveStepLogs()
			}
		case "h", "left":
			m.focus = FocusTree
			if !m.isZoomed {
				m.tree.Collapse()
				if sel := m.tree.SelectedNode(); sel != nil && sel.NodeType == NodeRepo {
					m.seenRepoExpansion[sel.ID] = sel.Expanded
				}
				return m, m.updateActiveStepLogs()
			}
		case "l", "right":
			m.focus = FocusTree
			if !m.isZoomed {
				m.tree.Expand()
				if sel := m.tree.SelectedNode(); sel != nil && sel.NodeType == NodeRepo {
					m.seenRepoExpansion[sel.ID] = sel.Expanded
				}
				return m, m.updateActiveStepLogs()
			}
		case "pgdown", "pagedown", "ctrl+d":
			if !m.isZoomed && m.focus == FocusTree && m.tree != nil && m.tree.Height > 0 {
				m.tree.ScrollDown(m.tree.Height)
				return m, m.updateActiveStepLogs()
			}
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		case "pgup", "pageup", "ctrl+u":
			if !m.isZoomed && m.focus == FocusTree && m.tree != nil && m.tree.Height > 0 {
				m.tree.ScrollUp(m.tree.Height)
				return m, m.updateActiveStepLogs()
			}
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}

	case clearToastMsg:
		if msg.id == m.toastID {
			m.toastMsg = ""
		}
		return m, nil

	case tea.MouseMsg:
		if m.toastMsg != "" && !m.toastExpiresAt.IsZero() && time.Now().After(m.toastExpiresAt) {
			m.toastMsg = ""
		}

		sidebarWidth := 0
		if !m.isZoomed {
			sidebarWidth = m.calcTreeWidth() + 2
		}

		// Handle mouse wheel scrolling
		if tea.MouseEvent(msg).IsWheel() || msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			isUp := msg.Button == tea.MouseButtonWheelUp
			if !m.isZoomed && msg.X < sidebarWidth {
				if isUp {
					m.tree.Up()
				} else {
					m.tree.Down()
				}
				return m, m.updateActiveStepLogs()
			}
			// Viewport scrolling
			m.autoFollow = false
			if isUp {
				m.viewport.LineUp(3)
			} else {
				m.viewport.LineDown(3)
			}
			return m, nil
		}

		// Calculate viewport vertical bounds
		viewportTop := 1
		if !m.isZoomed {
			telemOuterHeight := 6
			if m.height < 18 {
				telemOuterHeight = 4
			}
			viewportTop = telemOuterHeight + 1
		}
		viewportBottom := viewportTop + m.viewport.Height

		// Release event: finalize drag selection if in progress
		if msg.Action == tea.MouseActionRelease {
			if m.isSelecting {
				m.isSelecting = false
				didDrag := (m.dragStartLine != m.dragEndLine || m.dragStartX != msg.X || m.dragStartY != msg.Y)
				lines := m.activeStepLogLines()
				var cmd tea.Cmd
				if didDrag && len(lines) > 0 {
					minLine := m.dragStartLine
					maxLine := m.dragEndLine
					if minLine > maxLine {
						minLine, maxLine = maxLine, minLine
					}
					if minLine < 0 {
						minLine = 0
					}
					if maxLine >= len(lines) {
						maxLine = len(lines) - 1
					}
					if minLine <= maxLine && minLine < len(lines) {
						cleanLines := make([]string, 0, maxLine-minLine+1)
						for _, l := range lines[minLine : maxLine+1] {
							cleanLines = append(cleanLines, ansi.Strip(l))
						}
						selected := strings.Join(cleanLines, "\n")
						if selected != "" {
							if err := m.copyText(selected); err != nil {
								cmd = m.setToast("✖ Failed to copy to clipboard")
							} else {
								cmd = m.setToast("✓ Copied selection to clipboard")
							}
						}
					}
				}
				m.refreshHighlightedLogs()
				return m, cmd
			}
		}

		// Motion event: update selection range while dragging
		if msg.Action == tea.MouseActionMotion {
			if m.isSelecting {
				m.dragCurX = msg.X
				m.dragCurY = msg.Y
				relY := msg.Y - viewportTop
				if relY < 0 {
					relY = 0
				}
				if relY >= m.viewport.Height && m.viewport.Height > 0 {
					relY = m.viewport.Height - 1
				}
				curLine := m.viewport.YOffset + relY
				if curLine < 0 {
					curLine = 0
				}
				m.dragEndLine = curLine
				m.refreshHighlightedLogs()
				return m, nil
			}
			return m, nil
		}

		// Press event (left click)
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {

			if !m.isZoomed && msg.X < sidebarWidth {
				// Click in Sidebar Tree
				m.focus = FocusTree

				treeTopOffset := 1
				if m.treeSearching || m.treeSearchInput.Value() != "" {
					treeTopOffset = 2
					if msg.Y == 1 {
						m.treeSearching = true
						cmd := m.treeSearchInput.Focus()
						return m, cmd
					}
				}


				if msg.Y >= treeTopOffset && (m.tree.Height <= 0 || msg.Y < treeTopOffset+m.tree.Height) {
					row := m.tree.Offset + msg.Y - treeTopOffset
					if row >= 0 && row < len(m.tree.Rows()) {
						clickedNode := m.tree.NodeAtRow(row)

						// Double click detection: same node within 500ms
						isDoubleClick := !m.lastTreeClickTime.IsZero() &&
							time.Since(m.lastTreeClickTime) <= 500*time.Millisecond &&
							clickedNode != nil && m.lastTreeClickNodeID == clickedNode.ID

						// Chevron click detection: indent + glyph with generous hit target
						isChevron := false
						if clickedNode != nil && len(clickedNode.Children) > 0 {
							chevronStart := 1 + clickedNode.Depth*2
							chevronEnd := chevronStart + 3
							if msg.X >= 1 && msg.X <= chevronEnd {
								isChevron = true
							}
						}

						// Toggle expansion on:
						// 1. Double click anywhere on a container node
						// 2. Direct click on the chevron / arrow prefix
						// 3. Click on a container node that is already selected
						// 4. Click on a top-level branch header (like HISTORICAL RUNS or LIVE RUNS)
						wasAlreadySelected := (m.tree.Cursor == row)
						isBranchHeader := clickedNode != nil && (clickedNode.NodeType == NodeBranch || clickedNode.ID == "historical-runs" || clickedNode.ID == "live-runs")
						isContainer := clickedNode != nil && len(clickedNode.Children) > 0
						shouldToggle := (isContainer && (isDoubleClick || isChevron || wasAlreadySelected)) || isBranchHeader

						m.tree.SelectRow(row)
						if shouldToggle {
							m.tree.ToggleExpandSelected()
						}
						if clickedNode != nil && clickedNode.NodeType == NodeRepo {
							m.seenRepoExpansion[clickedNode.ID] = clickedNode.Expanded
						}

						m.lastTreeClickTime = time.Now()
						if clickedNode != nil {
							m.lastTreeClickNodeID = clickedNode.ID
						}

						return m, m.updateActiveStepLogs()
					}
				}
				return m, nil
			}

			// Click in Viewport
			m.focus = FocusViewport

			if msg.Y >= viewportTop && msg.Y < viewportBottom {
				relY := msg.Y - viewportTop
				startLine := m.viewport.YOffset + relY
				if startLine < 0 {
					startLine = 0
				}
				m.isSelecting = true
				m.dragStartLine = startLine
				m.dragEndLine = startLine
				m.dragStartX = msg.X
				m.dragStartY = msg.Y
				m.dragCurX = msg.X
				m.dragCurY = msg.Y
				m.refreshHighlightedLogs()
				return m, nil
			}

			if m.logSearching && msg.Y == viewportBottom {
				cmd := m.logSearchInput.Focus()
				return m, cmd
			}
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
				m.renderAndSetLogs(logs)
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
	treeWidth, rightWidth, targetOuterHeight, logHeight := m.calcDimensions()
	m.viewport.Width = rightWidth
	m.viewport.Height = logHeight
	m.treeSearchInput.Width = treeWidth - 9
	if m.treeSearchInput.Width < 5 {
		m.treeSearchInput.Width = 5
	}
	logInputW := rightWidth - 45
	if logInputW < 10 {
		logInputW = 10
	}
	if logInputW > 40 {
		logInputW = 40
	}
	m.logSearchInput.Width = logInputW

	treeInnerHeight := targetOuterHeight - 2
	if m.treeSearching || m.treeSearchInput.Value() != "" {
		treeInnerHeight--
	}
	if treeInnerHeight < 1 {
		treeInnerHeight = 1
	}
	if m.tree != nil {
		m.tree.SetHeight(treeInnerHeight)
	}
}

func (m *DashboardModel) applySnapshot(snap *monitor.Snapshot) tea.Cmd {
	if snap == nil {
		return nil
	}
	if snap.Telemetry != nil && len(snap.ActiveJobs) > 0 {
		m.telemetry.Update(snap.Telemetry)
	} else if len(snap.ActiveJobs) == 0 {
		m.telemetry.Update(nil)
	}
	if snap.SupervisorStatus != nil {
		m.supervisorStatus = snap.SupervisorStatus
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
		histExpanded := false
		if exp, ok := m.seenRepoExpansion["historical-runs"]; ok {
			histExpanded = exp
		}
		hist = &TreeNode{
			ID:       "historical-runs",
			Label:    "HISTORICAL RUNS (0 past runs)",
			Expanded: histExpanded,
			NodeType: NodeBranch,
		}
		m.tree.Root.AddChild(hist)
	}
	return live, hist
}

func formatRepoSlotLabel(alias, status string, active, max int) string {
	if status == "" {
		if active > 0 {
			status = "busy"
		} else {
			status = "idle"
		}
	}
	if max <= 0 {
		if active > 0 {
			max = active
		} else {
			max = 1
		}
	}
	return fmt.Sprintf("%s (%s • %d/%d slots)", alias, status, active, max)
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

			if m.supervisorStatus != nil {
				for _, ch := range live.Children {
					if ch.NodeType == NodeRepo {
						alias := strings.TrimPrefix(ch.ID, "repo:")
						var slot *monitor.RepoSlotInfo
						for _, s := range m.supervisorStatus.RepoSlots {
							if s.Alias == alias {
								slot = s
								break
							}
						}
						active := len(ch.Children)
						max := 1
						status := "idle"
						if slot != nil {
							max = slot.MaxJobs
							status = slot.Status
						}
						if status == "" {
							if active > 0 {
								status = "busy"
							} else {
								status = "idle"
							}
						} else if active > 0 && status == "idle" {
							status = "busy"
						} else if active == 0 && status == "busy" {
							status = "idle"
						}
						if max <= 0 {
							if active > 0 {
								max = active
							} else {
								max = 1
							}
						}
						ch.Label = formatRepoSlotLabel(alias, status, active, max)
					}
				}
			}
		}
	}
	hist := m.getHistoricalBranch()
	if hist != nil {
		totalPastRuns := 0
		for _, ch := range hist.Children {
			if ch.NodeType == NodeRepo {
				totalPastRuns += len(ch.Children)
			} else {
				totalPastRuns++
			}
		}
		hist.Label = fmt.Sprintf("HISTORICAL RUNS (%d past runs)", totalPastRuns)
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
	if sel == nil || !isHistoricalNode(sel) {
		return nil
	}
	if sel.ID == "historical-runs" || sel.NodeType == NodeRepo || sel.NodeType == NodeBranch {
		return nil
	}
	if job := sel.FindParentJob(); job != nil {
		return job
	}
	for curr := sel; curr != nil && curr.ID != "historical-runs"; curr = curr.Parent {
		if curr.Parent != nil && (curr.Parent.ID == "historical-runs" || curr.Parent.NodeType == NodeRepo) {
			if curr.NodeType != NodeRepo && curr.ID != "historical-runs" {
				return curr
			}
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

func (m *DashboardModel) resolveJobRepoAlias(repoAlias, repo string) string {
	if repoAlias != "" {
		return repoAlias
	}
	if repo != "" {
		if m.supervisorStatus != nil {
			for _, s := range m.supervisorStatus.RepoSlots {
				if repo == s.Alias || strings.HasSuffix(repo, "/"+s.Alias) {
					return s.Alias
				}
			}
		}
		return repo
	}
	if m.supervisorStatus != nil {
		var busySlots []string
		for _, s := range m.supervisorStatus.RepoSlots {
			if s.ActiveJobs > 0 {
				busySlots = append(busySlots, s.Alias)
			}
		}
		if len(busySlots) == 1 {
			return busySlots[0]
		}
		if len(m.supervisorStatus.RepoSlots) == 1 {
			return m.supervisorStatus.RepoSlots[0].Alias
		}
	}
	return "default"
}

func (m *DashboardModel) rebuildTreeFromSnapshot(snap *monitor.Snapshot) {
	savedExpanded := make(map[string]bool)
	if m.tree != nil && m.tree.Root != nil {
		m.tree.Root.Walk(func(n *TreeNode) bool {
			savedExpanded[n.ID] = n.Expanded
			return true
		})
	}

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

	histExpanded := false
	if exp, ok := m.seenRepoExpansion["historical-runs"]; ok {
		histExpanded = exp
	} else if existingHist := m.getHistoricalBranch(); existingHist != nil && existingHist.Expanded {
		histExpanded = true
	} else {
		histExpanded = m.isOffline
	}
	histNode := &TreeNode{
		ID:       "historical-runs",
		Expanded: histExpanded,
		NodeType: NodeBranch,
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

	if snap.SupervisorStatus != nil && len(snap.SupervisorStatus.RepoSlots) > 0 {
		slots := make([]*monitor.RepoSlotInfo, len(snap.SupervisorStatus.RepoSlots))
		copy(slots, snap.SupervisorStatus.RepoSlots)
		sort.Slice(slots, func(i, j int) bool {
			return slots[i].Alias < slots[j].Alias
		})

		repoJobs := make(map[string][]*monitor.JobInfo)
		for _, job := range snap.ActiveJobs {
			if job == nil {
				continue
			}
			matchedAlias := ""
			for _, s := range slots {
				if job.RepoAlias != "" && s.Alias == job.RepoAlias {
					matchedAlias = s.Alias
					break
				}
				if job.Repo != "" && (s.Alias == job.Repo || strings.HasSuffix(job.Repo, "/"+s.Alias)) {
					matchedAlias = s.Alias
					break
				}
			}
			if matchedAlias == "" {
				for _, s := range slots {
					sLower := strings.ToLower(s.Alias)
					if (job.Workflow != "" && strings.Contains(strings.ToLower(job.Workflow), sLower)) ||
						(job.Name != "" && strings.Contains(strings.ToLower(job.Name), sLower)) {
						matchedAlias = s.Alias
						break
					}
				}
			}
			if matchedAlias == "" {
				var busySlots []string
				for _, s := range slots {
					if s.ActiveJobs > 0 {
						busySlots = append(busySlots, s.Alias)
					}
				}
				if len(busySlots) == 1 {
					matchedAlias = busySlots[0]
				}
			}
			if matchedAlias == "" && len(slots) == 1 {
				matchedAlias = slots[0].Alias
			}
			if matchedAlias == "" {
				matchedAlias = m.resolveJobRepoAlias(job.RepoAlias, job.Repo)
			}
			if matchedAlias == "" {
				matchedAlias = "default"
			}
			repoJobs[matchedAlias] = append(repoJobs[matchedAlias], job)
		}

		for _, slot := range slots {
			jobs := repoJobs[slot.Alias]
			activeCount := len(jobs)
			status := slot.Status
			if status == "" {
				if activeCount > 0 {
					status = "busy"
				} else {
					status = "idle"
				}
			} else if activeCount > 0 && status == "idle" {
				status = "busy"
			} else if activeCount == 0 && status == "busy" {
				status = "idle"
			}
			max := slot.MaxJobs
			if max <= 0 {
				if activeCount > 0 {
					max = activeCount
				} else {
					max = 1
				}
			}
			label := formatRepoSlotLabel(slot.Alias, status, activeCount, max)
			repoID := "repo:" + slot.Alias
			expanded := true
			if exp, ok := m.seenRepoExpansion[repoID]; ok {
				expanded = exp
			}
			repoNode := &TreeNode{
				ID:       repoID,
				Label:    label,
				Expanded: expanded,
				NodeType: NodeRepo,
				Data:     slot,
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

		// Also handle any active jobs that didn't match any configured slot
		var unconfiguredAliases []string
		for alias := range repoJobs {
			isConfigured := false
			for _, s := range slots {
				if s.Alias == alias {
					isConfigured = true
					break
				}
			}
			if !isConfigured {
				unconfiguredAliases = append(unconfiguredAliases, alias)
			}
		}
		sort.Strings(unconfiguredAliases)
		for _, alias := range unconfiguredAliases {
			jobs := repoJobs[alias]
			repoID := "repo:" + alias
			expanded := true
			if exp, ok := m.seenRepoExpansion[repoID]; ok {
				expanded = exp
			}
			repoNode := &TreeNode{
				ID:       repoID,
				Label:    alias,
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
	} else if isMultiRepo {
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

	existingHistJobs := make(map[string]*TreeNode)
	type histEntry struct {
		alias string
		node  *TreeNode
	}
	var existingHistOrder []histEntry
	if oldHist := m.getHistoricalBranch(); oldHist != nil {
		for _, ch := range oldHist.Children {
			if ch.NodeType == NodeRepo {
				alias := ch.Label
				for _, jNode := range ch.Children {
					existingHistJobs[jNode.ID] = jNode
					existingHistOrder = append(existingHistOrder, histEntry{alias: alias, node: jNode})
				}
			} else {
				existingHistJobs[ch.ID] = ch
				existingHistOrder = append(existingHistOrder, histEntry{alias: "default", node: ch})
			}
		}
	}

	histRepoJobs := make(map[string][]*monitor.JobInfo)
	var histRepoAliases []string
	seenAliases := make(map[string]bool)
	snapHistIDs := make(map[string]bool)

	for _, job := range snap.RecentJobs {
		if job == nil {
			continue
		}
		snapHistIDs[job.ID] = true
		alias := m.resolveJobRepoAlias(job.RepoAlias, job.Repo)
		if !seenAliases[alias] {
			seenAliases[alias] = true
			histRepoAliases = append(histRepoAliases, alias)
		}
		histRepoJobs[alias] = append(histRepoJobs[alias], job)
	}

	// Also check preserved jobs from previous tree
	for _, entry := range existingHistOrder {
		jobID := entry.node.ID
		if !snapHistIDs[jobID] {
			alias := entry.alias
			if alias == "" {
				alias = "default"
			}
			if !seenAliases[alias] {
				seenAliases[alias] = true
				histRepoAliases = append(histRepoAliases, alias)
			}
		}
	}

	sort.Strings(histRepoAliases)

	for _, alias := range histRepoAliases {
		repoID := "hist-repo:" + alias
		expanded := false
		if exp, ok := m.seenRepoExpansion[repoID]; ok {
			expanded = exp
		} else if existingRepo := m.tree.FindNode(repoID); existingRepo != nil && existingRepo.Expanded {
			expanded = true
		} else {
			expanded = m.isOffline
		}
		repoNode := &TreeNode{
			ID:       repoID,
			Label:    alias,
			Expanded: expanded,
			NodeType: NodeRepo,
		}

		for _, job := range histRepoJobs[alias] {
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
			repoNode.AddChild(jobNode)
		}

		// Preserve any older jobs not in snapshot for this repo alias
		for _, entry := range existingHistOrder {
			if entry.alias == alias {
				if snapHistIDs[entry.node.ID] {
					continue
				}
				repoNode.AddChild(entry.node)
			}
		}

		histNode.AddChild(repoNode)
	}

	totalHistRuns := 0
	for _, ch := range histNode.Children {
		if ch.NodeType == NodeRepo {
			totalHistRuns += len(ch.Children)
		} else {
			totalHistRuns++
		}
	}
	histNode.Label = fmt.Sprintf("HISTORICAL RUNS (%d past runs)", totalHistRuns)

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
				m.renderAndSetLogs(logs)
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
	isMulti := (m.supervisorStatus != nil) || (job.Repo != "" || job.RepoAlias != "")
	if !isMulti {
		for _, ch := range live.Children {
			if ch.NodeType == NodeRepo {
				isMulti = true
				break
			}
		}
	}

	parentNode := live
	if isMulti {
		repoAlias := m.resolveJobRepoAlias(job.RepoAlias, job.Repo)
		repoID := "repo:" + repoAlias
		var repoNode *TreeNode
		for _, ch := range live.Children {
			if ch.ID == repoID || (ch.NodeType == NodeRepo && (strings.TrimPrefix(ch.ID, "repo:") == repoAlias || ch.Label == repoAlias || strings.HasPrefix(ch.Label, repoAlias+" "))) {
				repoNode = ch
				break
			}
		}
		if repoNode == nil {
			expanded := true
			if exp, ok := m.seenRepoExpansion[repoID]; ok {
				expanded = exp
			}
			label := repoAlias
			if m.supervisorStatus != nil {
				label = formatRepoSlotLabel(repoAlias, "busy", 1, 1)
			}
			repoNode = &TreeNode{
				ID:       repoID,
				Label:    label,
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
					if m.supervisorStatus == nil && len(ch.Children) == 0 {
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
		hist.Walk(func(n *TreeNode) bool {
			if n.ID == jobID || (job != nil && n.ID == job.ID) {
				target = n
				return false
			}
			return true
		})
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

	// Group into repo node in HISTORICAL RUNS
	var rAlias, rRepo string
	if job != nil {
		rAlias, rRepo = job.RepoAlias, job.Repo
	} else if ji, ok := target.Data.(*monitor.JobInfo); ok && ji != nil {
		rAlias, rRepo = ji.RepoAlias, ji.Repo
	}
	alias := m.resolveJobRepoAlias(rAlias, rRepo)

	histRepoID := "hist-repo:" + alias
	var repoNode *TreeNode
	for _, ch := range hist.Children {
		if ch.ID == histRepoID || (ch.NodeType == NodeRepo && ch.Label == alias) {
			repoNode = ch
			break
		}
	}
	if repoNode == nil {
		expanded := false
		if exp, ok := m.seenRepoExpansion[histRepoID]; ok {
			expanded = exp
		}
		repoNode = &TreeNode{
			ID:       histRepoID,
			Label:    alias,
			Expanded: expanded,
			NodeType: NodeRepo,
		}
		hist.AddChild(repoNode)
	}

	alreadyInHist := false
	for _, ch := range repoNode.Children {
		if ch.ID == target.ID {
			alreadyInHist = true
			break
		}
	}
	if !alreadyInHist {
		target.Parent = repoNode
		repoNode.Children = append([]*TreeNode{target}, repoNode.Children...)
	}

	if selectedBefore != nil && (selectedBefore.ID == target.ID || (selectedBefore.FindParentJob() != nil && selectedBefore.FindParentJob().ID == target.ID)) {
		hist.Expanded = true
		repoNode.Expanded = true
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
				hist.Walk(func(n *TreeNode) bool {
					if n.ID == "active-job" || n.ID == "job" {
						parentJob = n
						return false
					}
					return true
				})
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
	jobCount := len(sel.Children)
	var sb strings.Builder
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "  Repository: %s\n", title)
	if isHistoricalNode(sel) {
		fmt.Fprintf(&sb, "  Historical Runs: %d\n", jobCount)
		if jobCount > 0 {
			sb.WriteString("  Runs:\n")
			for _, j := range sel.Children {
				fmt.Fprintf(&sb, "    • %s [%s]\n", j.Label, j.Status)
			}
		} else {
			sb.WriteString("  Status: No historical runs recorded\n")
		}
	} else {
		fmt.Fprintf(&sb, "  Active Jobs: %d\n", jobCount)
		if jobCount > 0 {
			sb.WriteString("  Jobs:\n")
			for _, j := range sel.Children {
				fmt.Fprintf(&sb, "    • %s [%s]\n", j.Label, j.Status)
			}
		} else {
			sb.WriteString("  Status: Idle (no active jobs running)\n")
		}
	}
	return sb.String()
}

// UpdateActiveStepLogs triggers log loading for the currently selected step.
func (m *DashboardModel) UpdateActiveStepLogs() tea.Cmd {
	return m.updateActiveStepLogs()
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
		parentJob := sel.FindParentJob()
		if sel.NodeType == NodeStep && parentJob != nil {
			lines = m.stepLogs[parentJob.ID+"/"+sel.ID]
			if len(lines) == 0 && !isHistoricalNode(sel) {
				lines = m.stepLogs[sel.ID]
			}
		} else if parentJob != nil && sel != parentJob {
			lines = m.stepLogs[parentJob.ID+"/"+sel.ID]
			if len(lines) == 0 && !isHistoricalNode(sel) {
				lines = m.stepLogs[sel.ID]
			}
		} else {
			lines = m.stepLogs[sel.ID]
		}

		if len(lines) > 0 {
			m.renderAndSetLogs(lines)
			if m.autoFollow {
				m.viewport.GotoBottom()
			}
			return nil
		}

		// If logs are not in memory, check if this is a historical step
		if isHistoricalNode(sel) && sel.NodeType == NodeStep && parentJob != nil {
			jobID := parentJob.ID
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
					m.renderAndSetLogs(fetched)
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

	activeBorder := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#38BDF8"))
	inactiveBorder := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#334155"))

	var content string
	if m.isZoomed {
		_, zoomWidth, targetOuterHeight, zoomHeight := m.calcDimensions()
		_ = targetOuterHeight
		logContent := m.renderLogViewport(zoomWidth, zoomHeight)
		content = activeBorder.Width(zoomWidth).Height(zoomHeight).Render(logContent)
	} else {
		treeWidth, rightWidth, targetOuterHeight, logInnerHeight := m.calcDimensions()
		_ = logInnerHeight

		treeInnerHeight := targetOuterHeight - 2
		if treeInnerHeight < 1 {
			treeInnerHeight = 1
		}

		visibleTreeHeight := treeInnerHeight
		if m.treeSearching || m.treeSearchInput.Value() != "" {
			visibleTreeHeight--
		}
		if visibleTreeHeight < 1 {
			visibleTreeHeight = 1
		}
		if m.tree != nil {
			m.tree.SetHeight(visibleTreeHeight)
		}

		var treeContent string
		if m.treeSearching || m.treeSearchInput.Value() != "" {
			m.treeSearchInput.Width = treeWidth - 9
			if m.treeSearchInput.Width < 5 {
				m.treeSearchInput.Width = 5
			}
			searchBar := m.treeSearchInput.View()
			if ansi.StringWidth(searchBar) > treeWidth {
				searchBar = ansi.Truncate(searchBar, treeWidth, "")
			}
			treeContent = searchBar + "\n" + m.tree.View(treeWidth)
		} else {
			treeContent = m.tree.View(treeWidth)
		}

		treeStyle := inactiveBorder
		logStyle := inactiveBorder
		if m.focus == FocusTree {
			treeStyle = activeBorder
		} else {
			logStyle = activeBorder
		}

		treeBox := treeStyle.Width(treeWidth).Height(treeInnerHeight).Render(treeContent)

		telemWidth := rightWidth
		var telemContent string
		if m.height < 18 {
			telemContent = m.telemetry.ViewCompact(telemWidth)
		} else {
			telemContent = m.telemetry.View(telemWidth)
		}
		telemBox := inactiveBorder.Width(rightWidth).Render(telemContent)
		telemOuterHeight := lipgloss.Height(telemBox)

		logOuterHeight := targetOuterHeight - telemOuterHeight
		actualLogInnerHeight := logOuterHeight - 2
		if actualLogInnerHeight < 1 {
			actualLogInnerHeight = 1
		}
		logContent := m.renderLogViewport(rightWidth, actualLogInnerHeight)
		logBox := logStyle.Width(rightWidth).Height(actualLogInnerHeight).Render(logContent)
		rightPane := lipgloss.JoinVertical(lipgloss.Left, telemBox, logBox)
		content = lipgloss.JoinHorizontal(lipgloss.Top, treeBox, rightPane)
	}

	footer := m.renderFooter()
	return lipgloss.JoinVertical(lipgloss.Left, content, footer)
}

func leftNavText(width int, err error, autoFollow bool) string {
	followStr := "f follow:ON"
	if !autoFollow {
		followStr = "f follow:OFF"
	}
	var nav string
	switch {
	case width >= 140 && err == nil:
		nav = fmt.Sprintf(" ↑/k up • ↓/j down • Enter toggle • s search tree • ctrl+f find logs • Tab switch • c copy • z zoom • %s • q quit", followStr)
	case width >= 100 && err == nil:
		nav = fmt.Sprintf(" ↑/k up • ↓/j down • s search • z zoom • %s • q quit", followStr)
	case width >= 70 && err == nil:
		nav = fmt.Sprintf(" ↑/k up • ↓/j down • s search • %s • q quit", followStr)
	case width >= 50 && err == nil:
		nav = " ↑/k up • ↓/j down • s search • q quit"
	case err != nil:
		nav = "↑/k up • ↓/j down • Enter toggle • q quit"
	default:
		nav = " ↑/k up • ↓/j down • q quit"
	}
	if err != nil {
		return fmt.Sprintf(" Error: %v • %s", err, nav)
	}
	return nav
}

func (m *DashboardModel) renderFooter() string {
	hasToast := m.toastMsg != "" && (m.toastExpiresAt.IsZero() || time.Now().Before(m.toastExpiresAt))
	var leftStr string
	if hasToast {
		leftStr = " " + m.toastMsg
	} else if m.treeSearching {
		leftStr = " Esc cancel • Enter select"
	} else if m.logSearching {
		leftStr = " Esc cancel • Enter navigate"
	} else {
		leftStr = leftNavText(m.width, m.lastErr, m.autoFollow)
	}

	var left string
	if hasToast {
		left = toastStyle.Render(leftStr)
	} else {
		left = dimStyle.Render(leftStr)
	}
	leftW := lipgloss.Width(left)

	sample := m.telemetry.Sample()
	if sample == nil || m.width < 70 {
		if leftW > m.width && m.width > 0 {
			if hasToast {
				return toastStyle.Render(ansi.Truncate(leftStr, m.width, ""))
			}
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
		if hasToast {
			return toastStyle.Render(ansi.Truncate(leftStr, m.width, ""))
		}
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

func (m *DashboardModel) activeStepLogLines() []string {
	if m == nil {
		return nil
	}
	sel := m.tree.SelectedNode()
	var lines []string
	if sel != nil {
		parentJob := sel.FindParentJob()
		if sel.NodeType == NodeStep && parentJob != nil {
			lines = m.stepLogs[parentJob.ID+"/"+sel.ID]
			if len(lines) == 0 && !isHistoricalNode(sel) {
				lines = m.stepLogs[sel.ID]
			}
		} else if parentJob != nil && sel != parentJob {
			lines = m.stepLogs[parentJob.ID+"/"+sel.ID]
			if len(lines) == 0 && !isHistoricalNode(sel) {
				lines = m.stepLogs[sel.ID]
			}
		} else {
			lines = m.stepLogs[sel.ID]
		}
	}
	if len(lines) == 0 && m.activeStepID != "" {
		lines = m.stepLogs[m.activeStepID]
	}
	return lines
}

func (m *DashboardModel) updateSearchMatches(lines []string) {
	if m.searchQuery == "" {
		m.searchMatches = nil
		m.currentMatchIdx = 0
		return
	}
	var matches []int
	lowerQuery := strings.ToLower(m.searchQuery)
	for i, line := range lines {
		clean := ansi.Strip(line)
		if strings.Contains(strings.ToLower(clean), lowerQuery) {
			matches = append(matches, i)
		}
	}
	m.searchMatches = matches
	if len(matches) == 0 {
		m.currentMatchIdx = 0
	} else if m.currentMatchIdx >= len(matches) {
		m.currentMatchIdx = len(matches) - 1
	}
}

func highlightLine(line, query string, style lipgloss.Style) string {
	if query == "" {
		return line
	}
	clean := ansi.Strip(line)
	var sb strings.Builder
	remaining := clean
	for len(remaining) > 0 {
		start, end := findCaseInsensitiveSubstr(remaining, query)
		if start == -1 {
			sb.WriteString(remaining)
			break
		}
		if start > 0 {
			sb.WriteString(remaining[:start])
		}
		sb.WriteString(style.Render(remaining[start:end]))
		remaining = remaining[end:]
	}
	return sb.String()
}

func (m *DashboardModel) formatLogGutter(lineIdx, totalLines int, isActive bool) string {
	digits := len(strconv.Itoa(totalLines))
	if digits < 3 {
		digits = 3
	}
	numStr := fmt.Sprintf("%*d ", digits, lineIdx+1)
	if isActive {
		return logGutterActiveStyle.Render(numStr) + logGutterSep
	}
	return logGutterStyle.Render(numStr) + logGutterSep
}

func (m *DashboardModel) renderHighlightedLines(lines []string) string {
	minLine, maxLine := -1, -1
	if m.isSelecting {
		minLine = m.dragStartLine
		maxLine = m.dragEndLine
		if minLine > maxLine {
			minLine, maxLine = maxLine, minLine
		}
	}

	activeLine := -1
	if m.searchQuery != "" && len(m.searchMatches) > 0 && m.currentMatchIdx >= 0 && m.currentMatchIdx < len(m.searchMatches) {
		activeLine = m.searchMatches[m.currentMatchIdx]
	}

	totalLines := len(lines)
	renderedLines := make([]string, totalLines)
	for i, line := range lines {
		gutter := m.formatLogGutter(i, totalLines, activeLine == i)
		gutterWidth := ansi.StringWidth(gutter)

		var content string
		if minLine != -1 && i >= minLine && i <= maxLine {
			stripped := ansi.Strip(line)
			availWidth := m.viewport.Width - gutterWidth
			if availWidth > 0 && ansi.StringWidth(stripped) < availWidth {
				stripped += strings.Repeat(" ", availWidth-ansi.StringWidth(stripped))
			}
			content = selectedLogStyle.Render(stripped)
		} else if m.searchQuery != "" {
			if activeLine == i {
				content = highlightLine(line, m.searchQuery, activeMatchStyle)
			} else {
				content = highlightLine(line, m.searchQuery, normalMatchStyle)
			}
		} else {
			content = line
		}
		renderedLines[i] = gutter + content
	}
	return strings.Join(renderedLines, "\n")
}

func (m *DashboardModel) renderAndSetLogs(lines []string) {
	if len(lines) == 0 {
		return
	}
	if m.searchQuery != "" {
		m.updateSearchMatches(lines)
	}
	rendered := m.renderHighlightedLines(lines)
	m.viewport.SetContent(rendered)
}

func (m *DashboardModel) refreshHighlightedLogs() {
	lines := m.activeStepLogLines()
	if len(lines) == 0 {
		return
	}
	rendered := m.renderHighlightedLines(lines)
	m.viewport.SetContent(rendered)
}

func (m *DashboardModel) scrollToMatch(lineIdx int) {
	if m.viewport.Height <= 0 {
		m.viewport.SetYOffset(lineIdx)
		return
	}
	targetY := lineIdx - m.viewport.Height/3
	if targetY < 0 {
		targetY = 0
	}
	m.viewport.SetYOffset(targetY)
}

func (m *DashboardModel) renderFindBar(width int) string {
	var matchCounter string
	if len(m.searchMatches) == 0 {
		matchCounter = lipgloss.NewStyle().Foreground(lipgloss.Color("#F87171")).Render("(No matches)")
	} else {
		counterText := fmt.Sprintf("(Match %d of %d)", m.currentMatchIdx+1, len(m.searchMatches))
		matchCounter = lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true).Render(counterText)
	}

	shortcuts := lipgloss.NewStyle().Foreground(lipgloss.Color("#64748B")).Render("[n: next, N: prev, Esc: dismiss]")
	bar := fmt.Sprintf("%s %s %s", m.logSearchInput.View(), matchCounter, shortcuts)
	if width > 0 && ansi.StringWidth(bar) > width {
		bar = ansi.Truncate(bar, width, "")
	}
	return bar
}

func (m *DashboardModel) renderLogViewport(width, height int) string {
	if m.logSearching || m.searchQuery != "" {
		vpHeight := height - 1
		if vpHeight < 1 {
			vpHeight = 1
		}
		m.viewport.Width = width
		m.viewport.Height = vpHeight
		vpView := m.viewport.View()
		findBar := m.renderFindBar(width)
		return lipgloss.JoinVertical(lipgloss.Left, vpView, findBar)
	}
	m.viewport.Width = width
	m.viewport.Height = height
	return m.viewport.View()
}
