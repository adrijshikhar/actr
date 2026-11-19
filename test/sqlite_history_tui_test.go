package test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/adrijshikhar/actr/internal/supervisor"
	"github.com/adrijshikhar/actr/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSQLiteHistory_TUI_EndToEndIntegration verifies the entire stack:
// 1. Pure-Go SQLite history store persistence and querying.
// 2. Multi-repo supervisor configuration and linked repo slot badges.
// 3. HydrateSnapshotFromStore and dashboard model multi-repo tree hydration.
// 4. Interactive tree search filtering.
// 5. In-log content search, match navigation, and scrolling.
// 6. Clipboard copy via CopyText and keyboard shortcut ('c').
func TestSQLiteHistory_TUI_EndToEndIntegration(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "history.db")
	sockPath := testSocketPath(t)

	// -------------------------------------------------------------------------
	// 1. Initialize Pure-Go SQLite History Store
	// -------------------------------------------------------------------------
	sqliteStore, err := history.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = sqliteStore.Close() }()

	key, err := protocol.GenerateRSAKeyPair(2048)
	require.NoError(t, err)

	aimDir := setupRepoCredentials(t, tempDir, "aim", "http://127.0.0.1:9091", key)
	cxDir := setupRepoCredentials(t, tempDir, "cxstatusline", "http://127.0.0.1:9092", key)

	// -------------------------------------------------------------------------
	// 2. Multi-Repo Supervisor Configuration with Concurrency Slots
	// -------------------------------------------------------------------------
	supCfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 2,
		},
		Repos: map[string]config.RepoConfig{
			"aim": {
				URL:               "https://github.com/adrijshikhar/aim",
				ConfigDir:         aimDir,
				MaxConcurrentJobs: 1,
				Limits: config.LimitsYAMLConfig{
					CPU:    "4.0",
					Memory: "8GB",
				},
			},
			"cxstatusline": {
				URL:               "https://github.com/adrijshikhar/cxstatusline",
				ConfigDir:         cxDir,
				MaxConcurrentJobs: 1,
				Limits: config.LimitsYAMLConfig{
					CPU:    "2.0",
					Memory: "4GB",
				},
			},
		},
	}

	sup, err := supervisor.NewSupervisor(supCfg, sockPath)
	require.NoError(t, err)

	monServer := sup.MonitorServer()
	require.NotNil(t, monServer)
	monServer.WithHistoryStore(sqliteStore)
	require.NoError(t, monServer.Start())
	defer func() { _ = monServer.Stop() }()

	// -------------------------------------------------------------------------
	// 3. Emit Workflow Executions (Historical and Active) across Repos
	// -------------------------------------------------------------------------
	now := time.Now()

	// Workflow 1 (Historical) on repo "aim": failed test run
	jobIDAimHist := "job-aim-ci-101"
	monServer.Publish(monitor.Event{
		Type:      monitor.EventJobStart,
		JobID:     jobIDAimHist,
		RepoAlias: "aim",
		Job: &monitor.JobInfo{
			ID:        jobIDAimHist,
			Name:      "Aim Core Pipeline",
			Workflow:  "ci.yml",
			Repo:      "adrijshikhar/aim",
			RepoAlias: "aim",
			Status:    monitor.StatusRunning,
			StartedAt: now.Add(-10 * time.Minute),
			Steps: []*monitor.StepInfo{
				{ID: "step-setup", Name: "Environment Setup", Number: 1, Status: monitor.StatusRunning},
			},
		},
	})
	monServer.Publish(monitor.Event{
		Type:    monitor.EventStepLog,
		JobID:   jobIDAimHist,
		StepID:  "step-setup",
		LogLine: "Setting up Go 1.27 environment...",
	})
	monServer.Publish(monitor.Event{
		Type:    monitor.EventStepFinish,
		JobID:   jobIDAimHist,
		StepID:  "step-setup",
		Step:    &monitor.StepInfo{ID: "step-setup", Status: monitor.StatusSuccess},
	})

	monServer.Publish(monitor.Event{
		Type:      monitor.EventStepStart,
		JobID:     jobIDAimHist,
		StepID:    "step-test",
		RepoAlias: "aim",
		Step:      &monitor.StepInfo{ID: "step-test", Name: "Run Tests", Number: 2, Status: monitor.StatusRunning},
	})
	aimLogLines := []string{
		"=== RUN   TestAuthTokenValidation",
		"--- PASS: TestAuthTokenValidation (0.01s)",
		"=== RUN   TestClusterConnectionTimeout",
		"FAIL: TestClusterConnectionTimeout: dial tcp 10.0.0.1: connection refused",
		"FATAL: TestClusterConnectionTimeout: failure in cluster handoff",
		"Summary: 1 failure, 24 passed",
	}
	for _, l := range aimLogLines {
		monServer.Publish(monitor.Event{
			Type:    monitor.EventStepLog,
			JobID:   jobIDAimHist,
			StepID:  "step-test",
			LogLine: l,
		})
	}
	monServer.Publish(monitor.Event{
		Type:    monitor.EventStepFinish,
		JobID:   jobIDAimHist,
		StepID:  "step-test",
		Step:    &monitor.StepInfo{ID: "step-test", Status: monitor.StatusFailed},
	})
	monServer.Publish(monitor.Event{
		Type:      monitor.EventJobFinish,
		JobID:     jobIDAimHist,
		RepoAlias: "aim",
		Job: &monitor.JobInfo{
			ID:      jobIDAimHist,
			Status:  monitor.StatusFailed,
			EndedAt: now.Add(-8 * time.Minute),
		},
	})

	// Workflow 2 (Historical) on repo "cxstatusline": successful release packaging
	jobIDCxHist := "job-cx-release-202"
	monServer.Publish(monitor.Event{
		Type:      monitor.EventJobStart,
		JobID:     jobIDCxHist,
		RepoAlias: "cxstatusline",
		Job: &monitor.JobInfo{
			ID:        jobIDCxHist,
			Name:      "CX Release Build",
			Workflow:  "release.yml",
			Repo:      "adrijshikhar/cxstatusline",
			RepoAlias: "cxstatusline",
			Status:    monitor.StatusRunning,
			StartedAt: now.Add(-5 * time.Minute),
			Steps: []*monitor.StepInfo{
				{ID: "step-pkg", Name: "Package Artifact", Number: 1, Status: monitor.StatusRunning},
			},
		},
	})
	cxLogLines := []string{
		"Building static binary cxstatusline...",
		"Strip symbols and compress payload...",
		"Artifact cxstatusline-v1.0.0.tar.gz generated cleanly",
	}
	for _, l := range cxLogLines {
		monServer.Publish(monitor.Event{
			Type:    monitor.EventStepLog,
			JobID:   jobIDCxHist,
			StepID:  "step-pkg",
			LogLine: l,
		})
	}
	monServer.Publish(monitor.Event{
		Type:    monitor.EventStepFinish,
		JobID:   jobIDCxHist,
		StepID:  "step-pkg",
		Step:    &monitor.StepInfo{ID: "step-pkg", Status: monitor.StatusSuccess},
	})
	monServer.Publish(monitor.Event{
		Type:      monitor.EventJobFinish,
		JobID:     jobIDCxHist,
		RepoAlias: "cxstatusline",
		Job: &monitor.JobInfo{
			ID:      jobIDCxHist,
			Status:  monitor.StatusSuccess,
			EndedAt: now.Add(-4 * time.Minute),
		},
	})

	// Workflow 3 (Active) on repo "aim": live running job
	jobIDAimActive := "job-aim-live-303"
	monServer.Publish(monitor.Event{
		Type:      monitor.EventJobStart,
		JobID:     jobIDAimActive,
		RepoAlias: "aim",
		Job: &monitor.JobInfo{
			ID:        jobIDAimActive,
			Name:      "Aim Integration Pipeline",
			Workflow:  "ci.yml",
			Repo:      "adrijshikhar/aim",
			RepoAlias: "aim",
			Status:    monitor.StatusRunning,
			StartedAt: now,
			Steps: []*monitor.StepInfo{
				{ID: "step-live-exec", Name: "Running Tasks", Number: 1, Status: monitor.StatusRunning},
			},
		},
	})
	monServer.Publish(monitor.Event{
		Type:    monitor.EventStepLog,
		JobID:   jobIDAimActive,
		StepID:  "step-live-exec",
		LogLine: "Live runner worker dispatching step...",
	})

	// -------------------------------------------------------------------------
	// 4. Verify SQLite History Store Persistence & Direct Queries
	// -------------------------------------------------------------------------
	runs, err := sqliteStore.ListRuns(10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(runs), 2, "SQLite store must have at least 2 completed runs")

	var foundAimRun, foundCxRun bool
	for _, r := range runs {
		if r.ID == jobIDAimHist {
			foundAimRun = true
			assert.Equal(t, "aim", r.RepoAlias)
			assert.Equal(t, monitor.StatusFailed, r.Status)
		}
		if r.ID == jobIDCxHist {
			foundCxRun = true
			assert.Equal(t, "cxstatusline", r.RepoAlias)
			assert.Equal(t, monitor.StatusSuccess, r.Status)
		}
	}
	assert.True(t, foundAimRun, "job-aim-ci-101 must be persisted in sqlite")
	assert.True(t, foundCxRun, "job-cx-release-202 must be persisted in sqlite")

	// Verify step logs persisted in SQLite
	persistedLogs, err := sqliteStore.GetStepLogs(jobIDAimHist, "step-test")
	require.NoError(t, err)
	assert.Equal(t, aimLogLines, persistedLogs, "persisted step logs in SQLite must match emitted logs")

	// -------------------------------------------------------------------------
	// 5. Test HydrateSnapshotFromStore & Dashboard Model Multi-Repo Tree
	// -------------------------------------------------------------------------
	offlineSnap, err := tui.HydrateSnapshotFromStore(sqliteStore, 50)
	require.NoError(t, err)
	require.NotNil(t, offlineSnap)
	require.GreaterOrEqual(t, len(offlineSnap.RecentJobs), 2)

	// Fetch live snapshot over Unix socket from monitor server
	client := monitor.NewClient(sockPath)
	liveSnap, err := client.GetSnapshot(context.Background())
	require.NoError(t, err)
	require.NotNil(t, liveSnap)

	// Initialize Bubbletea dashboard model
	model := tui.NewDashboardModel(sockPath)
	resModel, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m := resModel.(*tui.DashboardModel)

	// Hydrate snapshot combining live supervisor slot status and SQLite historical runs
	// (with empty in-memory stepLogs to verify asynchronous lazy fetch over Unix socket)
	combinedSnap := &monitor.Snapshot{
		RunnerName:       liveSnap.RunnerName,
		SupervisorStatus: liveSnap.SupervisorStatus,
		ActiveJobs:       liveSnap.ActiveJobs,
		RecentJobs:       offlineSnap.RecentJobs,
	}
	resModel, _ = m.Update(tui.SnapshotMsg(combinedSnap))
	m = resModel.(*tui.DashboardModel)

	// Verify linked repo slot badges under LIVE RUNS
	liveBranch := m.Tree().Root.Children[0]
	assert.Equal(t, "live-runs", liveBranch.ID)
	require.Len(t, liveBranch.Children, 2, "must have both configured repos (aim and cxstatusline)")

	aimLiveRepo := liveBranch.Children[0]
	assert.Equal(t, "repo:aim", aimLiveRepo.ID)
	assert.Contains(t, aimLiveRepo.Label, "aim")
	assert.Contains(t, aimLiveRepo.Label, "1/1 slots")
	assert.Equal(t, tui.NodeRepo, aimLiveRepo.NodeType)
	require.NotEmpty(t, aimLiveRepo.Children, "aim should have active job child")
	assert.Equal(t, jobIDAimActive, aimLiveRepo.Children[0].ID)

	cxLiveRepo := liveBranch.Children[1]
	assert.Equal(t, "repo:cxstatusline", cxLiveRepo.ID)
	assert.Contains(t, cxLiveRepo.Label, "cxstatusline")
	assert.Contains(t, cxLiveRepo.Label, "0/1 slots")
	assert.Contains(t, cxLiveRepo.Label, "idle")

	// Verify historical runs repo grouping under HISTORICAL RUNS
	histBranch := m.Tree().Root.Children[1]
	assert.Equal(t, "historical-runs", histBranch.ID)
	require.Len(t, histBranch.Children, 2, "must group historical runs by repo")

	aimHistRepo := histBranch.Children[0]
	assert.Equal(t, "hist-repo:aim", aimHistRepo.ID)
	assert.Equal(t, "aim", aimHistRepo.Label)
	require.Len(t, aimHistRepo.Children, 2, "aim historical branch should contain active run (started in store) and completed historical run")
	assert.Equal(t, jobIDAimActive, aimHistRepo.Children[0].ID)
	assert.Equal(t, jobIDAimHist, aimHistRepo.Children[1].ID)

	cxHistRepo := histBranch.Children[1]
	assert.Equal(t, "hist-repo:cxstatusline", cxHistRepo.ID)
	assert.Equal(t, "cxstatusline", cxHistRepo.Label)
	require.Len(t, cxHistRepo.Children, 1)
	assert.Equal(t, jobIDCxHist, cxHistRepo.Children[0].ID)

	// -------------------------------------------------------------------------
	// 6. Test Tree Search Filtering
	// -------------------------------------------------------------------------
	assert.False(t, m.IsTreeSearching())
	assert.Equal(t, "", m.Tree().Filter())

	// Activate search via '/'
	resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = resModel.(*tui.DashboardModel)
	assert.True(t, m.IsTreeSearching())
	assert.Contains(t, m.View(), "Search:")

	// Search for "cxstatusline"
	for _, r := range "cxstatusline" {
		resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = resModel.(*tui.DashboardModel)
	}
	assert.Equal(t, "cxstatusline", m.Tree().Filter())
	treeFilteredView := m.View()
	assert.Contains(t, treeFilteredView, "cxstatusline")
	assert.NotContains(t, treeFilteredView, "Aim Core Pipeline")

	// Commit search with Enter
	resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = resModel.(*tui.DashboardModel)
	assert.False(t, m.IsTreeSearching())
	assert.Equal(t, "cxstatusline", m.Tree().Filter())

	// Clear filter with Esc
	resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = resModel.(*tui.DashboardModel)
	assert.Equal(t, "", m.Tree().Filter())
	treeRestoredView := m.View()
	assert.Contains(t, treeRestoredView, "Aim Core Pipeline")

	// -------------------------------------------------------------------------
	// 7. Test In-Log Search & Match Navigation
	// -------------------------------------------------------------------------
	// Expand historical job node and select it so pressing 'j' moves down to step-test
	jobNode := m.Tree().FindNode(jobIDAimHist)
	require.NotNil(t, jobNode)
	jobNode.Expanded = true
	m.Tree().Rebuild()

	foundJob := m.Tree().SelectFirst(func(n *tui.TreeNode) bool { return n.ID == jobIDAimHist })
	require.True(t, foundJob, "historical job node should be selectable in tree")

	// Trigger navigation update (move down from job node to step-test) to fetch historical logs via monitor socket
	resModel, fetchCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = resModel.(*tui.DashboardModel)
	require.Equal(t, "step-test", m.Tree().SelectedNode().ID, "cursor should have moved to step-test")
	require.NotNil(t, fetchCmd, "selecting historical step must dispatch fetch command")

	// Execute fetch command and apply historicalLogsLoadedMsg
	loadedMsg := fetchCmd()
	require.NotNil(t, loadedMsg)
	resModel, _ = m.Update(loadedMsg)
	m = resModel.(*tui.DashboardModel)

	// Viewport must now display the loaded logs
	initialLogsView := m.Viewport().View()
	assert.Contains(t, initialLogsView, "TestClusterConnectionTimeout")

	// Activate log search via Ctrl+F
	resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = resModel.(*tui.DashboardModel)
	assert.True(t, m.IsLogSearching())
	assert.Contains(t, m.View(), "Find in logs:")

	// Type query "TestClusterConnectionTimeout"
	searchQuery := "TestClusterConnectionTimeout"
	for _, r := range searchQuery {
		resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = resModel.(*tui.DashboardModel)
	}

	assert.Equal(t, searchQuery, m.SearchQuery())
	require.Len(t, m.SearchMatches(), 3, "query should match lines 2, 3, and 4")
	assert.Equal(t, 0, m.CurrentMatchIdx())
	assert.Contains(t, m.View(), "(Match 1 of 3)")

	// Commit search with Enter
	resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = resModel.(*tui.DashboardModel)
	assert.False(t, m.IsLogSearching())
	assert.Equal(t, searchQuery, m.SearchQuery())

	// Next match with 'n' -> index 1
	resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = resModel.(*tui.DashboardModel)
	assert.Equal(t, 1, m.CurrentMatchIdx())
	assert.Contains(t, m.View(), "(Match 2 of 3)")

	// Next match with 'n' -> index 2
	resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = resModel.(*tui.DashboardModel)
	assert.Equal(t, 2, m.CurrentMatchIdx())
	assert.Contains(t, m.View(), "(Match 3 of 3)")

	// Wraparound next match with 'n' -> index 0
	resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = resModel.(*tui.DashboardModel)
	assert.Equal(t, 0, m.CurrentMatchIdx())
	assert.Contains(t, m.View(), "(Match 1 of 3)")

	// Previous match with 'N' (wraps to index 2)
	resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	m = resModel.(*tui.DashboardModel)
	assert.Equal(t, 2, m.CurrentMatchIdx())
	assert.Contains(t, m.View(), "(Match 3 of 3)")

	// Clear search with Esc
	resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = resModel.(*tui.DashboardModel)
	assert.Equal(t, "", m.SearchQuery())
	assert.Empty(t, m.SearchMatches())

	// -------------------------------------------------------------------------
	// 8. Test Clipboard Copy (CopyText and 'c' Keyboard Shortcut)
	// -------------------------------------------------------------------------
	// 8a. Standalone CopyText helper test
	copyInput := "actr clipboard end-to-end integration test payload"
	err = tui.CopyText(copyInput)
	assert.NoError(t, err, "CopyText must succeed without error")

	// 8b. Dashboard 'c' keyboard copy shortcut
	resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = resModel.(*tui.DashboardModel)

	expectedToast := fmt.Sprintf("✓ Copied %d lines to clipboard", len(aimLogLines))
	assert.Equal(t, expectedToast, m.ToastMsg())
	assert.Contains(t, m.View(), expectedToast)

	// Moving cursor or pressing navigation key clears toast
	resModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = resModel.(*tui.DashboardModel)
	assert.Empty(t, m.ToastMsg(), "toast message should clear on navigation")
	assert.NotContains(t, m.View(), expectedToast)

	// -------------------------------------------------------------------------
	// 9. Test Mouse Interaction (Tree Selection)
	// -------------------------------------------------------------------------
	// Click row 2 (repo:aim) in sidebar tree
	resModel, _ = m.Update(tea.MouseMsg{
		X:      5,
		Y:      3, // row 2 (with top border Y=1 offset)
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	m = resModel.(*tui.DashboardModel)
	assert.Equal(t, tui.FocusTree, m.Focus())
	assert.Equal(t, "repo:aim", m.Tree().SelectedNode().ID)
}

