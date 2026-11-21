package test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adrijshikhar/actr/internal/demo"
	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/tui"
)

// TestDemoServer_E2E_MultiRepoContracts verifies the full end-to-end integration
// of the demonstration monitor server and dashboard against the multi-repo supervisor
// and slot contracts.
func TestDemoServer_E2E_MultiRepoContracts(t *testing.T) {
	sockPath := testSocketPath(t)
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "e2e_demo_history.db")

	store, err := history.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	require.NoError(t, demo.SeedHistoryStore(store))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, err := demo.StartServerWithStore(ctx, sockPath, store)
	require.NoError(t, err)
	defer func() { _ = server.Stop() }()

	// 1. Client Snapshot Verification against new contracts
	client := monitor.NewClient(sockPath)
	snap, err := client.GetSnapshot(ctx)
	require.NoError(t, err)
	require.NotNil(t, snap)

	// Verify SupervisorStatus contracts
	require.NotNil(t, snap.SupervisorStatus, "demo snapshot must populate SupervisorStatus")
	assert.Equal(t, 2, snap.SupervisorStatus.GlobalMaxJobs)
	assert.Equal(t, 1, snap.SupervisorStatus.GlobalActive)
	require.Len(t, snap.SupervisorStatus.RepoSlots, 2)

	slotMap := make(map[string]*monitor.RepoSlotInfo)
	for _, s := range snap.SupervisorStatus.RepoSlots {
		slotMap[s.Alias] = s
	}
	require.Contains(t, slotMap, "aim")
	assert.Equal(t, "idle", slotMap["aim"].Status)
	assert.Equal(t, 0, slotMap["aim"].ActiveJobs)

	require.Contains(t, slotMap, "cxstatusline")
	assert.Equal(t, "busy", slotMap["cxstatusline"].Status)
	assert.Equal(t, 1, slotMap["cxstatusline"].ActiveJobs)

	// Verify ActiveJobs contract
	require.Len(t, snap.ActiveJobs, 1)
	assert.Equal(t, "job-live-1", snap.ActiveJobs[0].ID)
	assert.Equal(t, "cxstatusline", snap.ActiveJobs[0].RepoAlias)
	assert.Equal(t, "cxstatusline", snap.ActiveJobs[0].Repo)

	// Verify RecentJobs contract: must have partitioned repo aliases
	require.NotEmpty(t, snap.RecentJobs)
	for _, rj := range snap.RecentJobs {
		assert.NotEmpty(t, rj.RepoAlias, "recent job %s must have non-empty RepoAlias", rj.ID)
		assert.NotEqual(t, "default", rj.RepoAlias, "recent job %s must not be dumped into default", rj.ID)
	}

	// 2. TUI Dashboard Integration Verification
	m := tui.NewDashboardModel(sockPath).WithHistoryStore(store)
	initCmd := m.Init()
	require.NotNil(t, initCmd)

	// Feed window size
	res, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})
	m = res.(*tui.DashboardModel)

	// Execute Init cmd to fetch live snapshot
	snapMsg := initCmd()
	res, _ = m.Update(snapMsg)
	m = res.(*tui.DashboardModel)

	// Verify Live Tree Structure
	tree := m.Tree()
	require.NotNil(t, tree)
	require.NotNil(t, tree.Root)

	// Expand historical-runs and all child repo containers for complete tree verification
	histNode := tree.FindNode("historical-runs")
	require.NotNil(t, histNode)
	histNode.Expanded = true
	for _, ch := range histNode.Children {
		ch.Expanded = true
	}
	tree.Rebuild()

	// Verify slots rendered in tree
	aimSlotNode := tree.FindNode("repo:aim")
	require.NotNil(t, aimSlotNode, "aim slot node must exist in live tree")
	assert.Contains(t, aimSlotNode.Label, "aim")

	cxSlotNode := tree.FindNode("repo:cxstatusline")
	require.NotNil(t, cxSlotNode, "cxstatusline slot node must exist in live tree")
	assert.Contains(t, cxSlotNode.Label, "cxstatusline")

	// Verify active job nested under cxstatusline slot
	liveJobNode := tree.FindNode("job-live-1")
	require.NotNil(t, liveJobNode, "active job must be present in tree")
	require.NotNil(t, liveJobNode.Parent)
	assert.Equal(t, cxSlotNode.ID, liveJobNode.Parent.ID, "job-live-1 must be parented by cxstatusline slot")

	// Verify historical runs grouped under repository branches, not 'default'
	assert.Nil(t, tree.FindNode("hist-repo:default"), "hist-repo:default must not exist when repo aliases are populated")
	assert.NotNil(t, tree.FindNode("hist-repo:actr"), "hist-repo:actr must exist")
	assert.NotNil(t, tree.FindNode("hist-repo:aim"), "hist-repo:aim must exist")
	assert.NotNil(t, tree.FindNode("hist-repo:cxstatusline"), "hist-repo:cxstatusline must exist")

	// 3. Event Streaming Verification
	events, errs := client.StreamEvents(ctx)
	select {
	case ev, ok := <-events:
		require.True(t, ok)
		assert.NotEmpty(t, ev.Type)
		if ev.JobID == "job-live-1" {
			assert.Equal(t, "cxstatusline", ev.RepoAlias)
		}
	case err := <-errs:
		t.Fatalf("stream error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for live simulation event")
	}
}
