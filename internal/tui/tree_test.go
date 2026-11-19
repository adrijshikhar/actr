package tui

import (
	"strings"
	"testing"

	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestTreeWidget_NavigationAndExpansion(t *testing.T) {
	root := &TreeNode{
		ID:       "root",
		Label:    "adrijshikhar",
		Expanded: true,
		Children: []*TreeNode{
			{
				ID:       "repo",
				Label:    "actr",
				Expanded: true,
				Children: []*TreeNode{
					{
						ID:       "job",
						Label:    "Live Test",
						Expanded: true,
						Children: []*TreeNode{
							{ID: "s1", Label: "Clone", Status: monitor.StatusSuccess},
							{ID: "s2", Label: "Build", Status: monitor.StatusRunning},
						},
					},
				},
			},
		},
	}

	tree := NewTreeWidget(root)
	if tree.SelectedNode().ID != "root" {
		t.Fatalf("expected selected root, got %s", tree.SelectedNode().ID)
	}

	// Move down to repo
	tree.Down()
	if tree.SelectedNode().ID != "repo" {
		t.Fatalf("expected selected repo, got %s", tree.SelectedNode().ID)
	}

	// Move down to job, then s1, then s2
	tree.Down()
	tree.Down()
	tree.Down()
	if tree.SelectedNode().ID != "s2" {
		t.Fatalf("expected selected s2, got %s", tree.SelectedNode().ID)
	}

	// Render view
	view := tree.View(30)
	if !strings.Contains(view, "✓ Clone") {
		t.Fatalf("expected view to contain '✓ Clone', got:\n%s", view)
	}
	if !strings.Contains(view, "● Build") {
		t.Fatalf("expected view to contain '● Build', got:\n%s", view)
	}
}

func TestTreeWidget_CollapseHidesChildren(t *testing.T) {
	root := &TreeNode{
		ID:       "root",
		Label:    "adrijshikhar",
		Expanded: true,
		Children: []*TreeNode{
			{
				ID:       "repo",
				Label:    "actr",
				Expanded: true,
				Children: []*TreeNode{
					{ID: "s1", Label: "Step 1"},
				},
			},
		},
	}

	tree := NewTreeWidget(root)
	tree.Down()   // select repo
	tree.Toggle() // collapse repo

	view := tree.View(30)
	if strings.Contains(view, "Step 1") {
		t.Fatalf("expected collapsed view NOT to contain Step 1, got:\n%s", view)
	}
}

func TestTreeWidget_EmptyRootAndNilSafety(t *testing.T) {
	tree := NewTreeWidget(nil)
	if tree.SelectedNode() != nil {
		t.Fatalf("expected SelectedNode to be nil for nil root")
	}

	// Navigation on empty tree should not panic
	tree.Up()
	tree.Down()
	tree.Toggle()
	tree.Collapse()
	tree.Expand()

	if tree.View(20) != "" {
		t.Fatalf("expected empty view for nil root")
	}
}

func TestTreeWidget_ExpandAndCollapseMethods(t *testing.T) {
	child := &TreeNode{ID: "c1", Label: "Child"}
	root := &TreeNode{
		ID:       "parent",
		Label:    "Parent",
		Expanded: false,
		Children: []*TreeNode{child},
	}

	tree := NewTreeWidget(root)
	if strings.Contains(tree.View(30), "Child") {
		t.Fatalf("expected child to be hidden when root collapsed")
	}

	// Expand root
	tree.Expand()
	if !root.Expanded {
		t.Fatalf("expected root to be expanded")
	}
	if !strings.Contains(tree.View(30), "Child") {
		t.Fatalf("expected child to be visible after Expand()")
	}

	// Expand again (idempotent)
	tree.Expand()
	if !root.Expanded {
		t.Fatalf("expected root to remain expanded")
	}

	// Collapse root
	tree.Collapse()
	if root.Expanded {
		t.Fatalf("expected root to be collapsed")
	}
	if strings.Contains(tree.View(30), "Child") {
		t.Fatalf("expected child to be hidden after Collapse()")
	}

	// Collapse again (idempotent)
	tree.Collapse()
	if root.Expanded {
		t.Fatalf("expected root to remain collapsed")
	}
}

func TestTreeWidget_BoundaryNavigation(t *testing.T) {
	root := &TreeNode{
		ID:       "root",
		Label:    "Root",
		Expanded: true,
		Children: []*TreeNode{
			{ID: "c1", Label: "Child 1"},
		},
	}

	tree := NewTreeWidget(root)
	// Cursor is at 0 (root). Up() should stay at 0
	tree.Up()
	if tree.Cursor != 0 {
		t.Fatalf("expected cursor 0 after Up() at top, got %d", tree.Cursor)
	}

	// Down() to Child 1 (cursor 1)
	tree.Down()
	if tree.Cursor != 1 {
		t.Fatalf("expected cursor 1 after Down(), got %d", tree.Cursor)
	}

	// Down() again should stay at 1
	tree.Down()
	if tree.Cursor != 1 {
		t.Fatalf("expected cursor 1 after Down() at bottom, got %d", tree.Cursor)
	}
}

func TestTreeWidget_StatusGlyphs(t *testing.T) {
	root := &TreeNode{
		ID:       "root",
		Label:    "Root",
		Expanded: true,
		Children: []*TreeNode{
			{ID: "s_run", Label: "RunningStep", Status: monitor.StatusRunning},
			{ID: "s_ok", Label: "SuccessStep", Status: monitor.StatusSuccess},
			{ID: "s_fail", Label: "FailedStep", Status: monitor.StatusFailed},
			{ID: "s_skip", Label: "SkippedStep", Status: monitor.StatusSkipped},
			{ID: "s_queue", Label: "QueuedStep", Status: monitor.StatusQueued},
		},
	}

	tree := NewTreeWidget(root)
	view := tree.View(40)

	for _, expected := range []string{"● RunningStep", "✓ SuccessStep", "✖ FailedStep", "⊘ SkippedStep", "○ QueuedStep"} {
		if !strings.Contains(view, expected) {
			t.Errorf("expected view to contain %q, got:\n%s", expected, view)
		}
	}
}

func TestTreeWidget_ViewTruncation(t *testing.T) {
	root := &TreeNode{
		ID:    "root",
		Label: "This is a very long label that exceeds the width",
	}

	tree := NewTreeWidget(root)
	view := tree.View(15)
	if !strings.Contains(view, "...") {
		t.Fatalf("expected view to truncate with '...', got: %q", view)
	}
}

func TestTreeNode_AddChildAndWalk(t *testing.T) {
	root := &TreeNode{ID: "root", Label: "Root"}
	child1 := &TreeNode{ID: "c1", Label: "C1"}
	child2 := &TreeNode{ID: "c2", Label: "C2"}

	root.AddChild(child1).AddChild(child2)

	if len(root.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(root.Children))
	}
	if child1.Parent != root {
		t.Fatalf("expected child1 parent to be root")
	}
	if child2.Parent != root {
		t.Fatalf("expected child2 parent to be root")
	}

	var visited []string
	root.Walk(func(n *TreeNode) bool {
		visited = append(visited, n.ID)
		return true
	})

	expected := []string{"root", "c1", "c2"}
	if len(visited) != len(expected) {
		t.Fatalf("expected visited %v, got %v", expected, visited)
	}
	for i, id := range expected {
		if visited[i] != id {
			t.Errorf("expected visited[%d] = %s, got %s", i, id, visited[i])
		}
	}
}

func TestTreeWidget_ZeroAndNegativeWidth(t *testing.T) {
	root := &TreeNode{ID: "root", Label: "Root"}
	tree := NewTreeWidget(root)

	if v := tree.View(0); v != "" {
		t.Fatalf("expected empty string for width 0, got %q", v)
	}
	if v := tree.View(-10); v != "" {
		t.Fatalf("expected empty string for negative width, got %q", v)
	}
}

func TestTreeNode_ParentPointerAndNavigation(t *testing.T) {
	root := &TreeNode{
		ID:       "root",
		Label:    "Root",
		Expanded: true,
		Children: []*TreeNode{
			{
				ID:       "job",
				Label:    "Job 1",
				Expanded: true,
				Children: []*TreeNode{
					{ID: "s1", Label: "Step 1"},
				},
			},
		},
	}

	tree := NewTreeWidget(root)

	// Verify Parent pointers automatically populated during flatten
	jobNode := root.Children[0]
	stepNode := jobNode.Children[0]
	if jobNode.Parent != root {
		t.Fatalf("expected jobNode.Parent to be root")
	}
	if stepNode.Parent != jobNode {
		t.Fatalf("expected stepNode.Parent to be jobNode")
	}

	// Navigate down to stepNode (leaf)
	tree.Down() // select jobNode
	tree.Down() // select stepNode
	if tree.SelectedNode().ID != "s1" {
		t.Fatalf("expected selected s1, got %s", tree.SelectedNode().ID)
	}

	// Collapse on leaf node traverses up to parent (jobNode)
	tree.Collapse()
	if tree.SelectedNode().ID != "job" {
		t.Fatalf("expected cursor to move to parent job, got %s", tree.SelectedNode().ID)
	}

	// Collapse on expanded branch collapses it
	if !jobNode.Expanded {
		t.Fatalf("expected jobNode to still be expanded before second Collapse")
	}
	tree.Collapse()
	if jobNode.Expanded {
		t.Fatalf("expected jobNode to be collapsed after second Collapse")
	}

	// Collapse on already collapsed branch traverses up to root
	tree.Collapse()
	if tree.SelectedNode().ID != "root" {
		t.Fatalf("expected cursor to move to parent root, got %s", tree.SelectedNode().ID)
	}
}

func TestTreeWidget_AnsiWidthAndTruncation(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(termenv.Ascii)
	})

	root := &TreeNode{
		ID:       "root",
		Label:    "runner-daemon-long-title",
		Expanded: true,
		Children: []*TreeNode{
			{
				ID:     "s1",
				Label:  "A very long step description that must be cleanly truncated without ANSI corruption",
				Status: monitor.StatusRunning,
			},
		},
	}

	tree := NewTreeWidget(root)
	width := 25
	view := tree.View(width)

	lines := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d:\n%s", len(lines), view)
	}

	for i, line := range lines {
		visibleWidth := ansi.StringWidth(line)
		if visibleWidth > width {
			t.Errorf("line %d visible width %d exceeds allotted width %d: %q", i, visibleWidth, width, line)
		}
	}

	// Ensure step label was truncated with "..." and contains no raw escape leaks
	stepLine := lines[1]
	if !strings.Contains(stepLine, "...") {
		t.Fatalf("expected stepLine to contain ellipsis, got: %q", stepLine)
	}
}

func TestTreeWidget_SelectionBackground(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(termenv.Ascii)
	})

	root := &TreeNode{
		ID:       "root",
		Label:    "Runner",
		Expanded: true,
		Children: []*TreeNode{
			{ID: "s1", Label: "Build", Status: monitor.StatusRunning},
		},
	}

	tree := NewTreeWidget(root)
	tree.Down() // select s1
	width := 30
	view := tree.View(width)

	lines := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	selectedLine := lines[1]
	// #2563EB in 24-bit ANSI background is: 48;2;36;99;235
	if !strings.Contains(selectedLine, "48;2;36;99;235") {
		t.Fatalf("expected selected line to have #2563EB (48;2;36;99;235) background, got: %q", selectedLine)
	}

	visibleWidth := ansi.StringWidth(selectedLine)
	if visibleWidth != width {
		t.Fatalf("expected selected line to fill full width %d, got %d", width, visibleWidth)
	}
}

func TestTreeNode_FindParentJobAndRepo(t *testing.T) {
	root := &TreeNode{ID: "root", Label: "root", NodeType: NodeBranch}
	liveBranch := &TreeNode{ID: "live-runs", Label: "LIVE RUNS", NodeType: NodeBranch}
	root.AddChild(liveBranch)

	repoNode := &TreeNode{ID: "repo:aim", Label: "aim (busy • 1/1 slots)", NodeType: NodeRepo}
	liveBranch.AddChild(repoNode)

	jobNode := &TreeNode{ID: "job-100", Label: "CI Test", NodeType: NodeJob}
	repoNode.AddChild(jobNode)

	stepNode := &TreeNode{ID: "step-1", Label: "Checkout", NodeType: NodeStep}
	jobNode.AddChild(stepNode)

	// Step resolves parent job and parent repo
	if stepNode.FindParentJob() != jobNode {
		t.Fatalf("expected step to resolve jobNode, got %v", stepNode.FindParentJob())
	}
	if stepNode.FindParentRepo() != repoNode {
		t.Fatalf("expected step to resolve repoNode, got %v", stepNode.FindParentRepo())
	}

	// Job resolves itself as job and resolves parent repo
	if jobNode.FindParentJob() != jobNode {
		t.Fatalf("expected job to resolve itself, got %v", jobNode.FindParentJob())
	}
	if jobNode.FindParentRepo() != repoNode {
		t.Fatalf("expected job to resolve repoNode, got %v", jobNode.FindParentRepo())
	}

	// Repo resolves itself as repo and has no parent job
	if repoNode.FindParentJob() != nil {
		t.Fatalf("expected repo to have nil parent job, got %v", repoNode.FindParentJob())
	}
	if repoNode.FindParentRepo() != repoNode {
		t.Fatalf("expected repo to resolve itself, got %v", repoNode.FindParentRepo())
	}

	// Branch nodes have neither
	if liveBranch.FindParentJob() != nil || liveBranch.FindParentRepo() != nil {
		t.Fatalf("expected branch node to return nil for both job and repo")
	}

	// Nil safety
	var nilNode *TreeNode
	if nilNode.FindParentJob() != nil || nilNode.FindParentRepo() != nil {
		t.Fatalf("expected nil safety on FindParentJob and FindParentRepo")
	}
}

func TestTreeWidget_FilterMatching(t *testing.T) {
	root := &TreeNode{
		ID:       "root",
		Label:    "actr",
		Expanded: true,
		NodeType: NodeBranch,
	}
	liveNode := &TreeNode{
		ID:       "live-runs",
		Label:    "LIVE RUNS",
		Expanded: true,
		NodeType: NodeBranch,
	}
	root.AddChild(liveNode)

	aimRepo := &TreeNode{
		ID:       "repo:aim",
		Label:    "aim (busy • 1/1 slots)",
		Expanded: false,
		NodeType: NodeRepo,
	}
	liveNode.AddChild(aimRepo)

	job1 := &TreeNode{
		ID:       "job-1",
		Label:    "CI Test",
		Expanded: false,
		NodeType: NodeJob,
	}
	aimRepo.AddChild(job1)

	step1 := &TreeNode{ID: "s1", Label: "Checkout", Status: monitor.StatusSuccess, NodeType: NodeStep}
	step2 := &TreeNode{ID: "s2", Label: "Run smoke tests", Status: monitor.StatusRunning, NodeType: NodeStep}
	job1.AddChild(step1).AddChild(step2)

	otherRepo := &TreeNode{
		ID:       "repo:cxstatusline",
		Label:    "cxstatusline",
		Expanded: true,
		NodeType: NodeRepo,
	}
	liveNode.AddChild(otherRepo)

	job2 := &TreeNode{
		ID:       "job-2",
		Label:    "Lint",
		Expanded: true,
		NodeType: NodeJob,
	}
	otherRepo.AddChild(job2)
	step3 := &TreeNode{ID: "s3", Label: "golangci-lint", Status: monitor.StatusSuccess, NodeType: NodeStep}
	job2.AddChild(step3)

	tw := NewTreeWidget(root)
	if len(tw.Rows()) == 0 {
		t.Fatalf("expected non-empty initial rows")
	}

	// 1. Filter by "smoke"
	tw.RebuildWithFilter("smoke")
	rows := tw.Rows()

	// Should contain root, live-runs, repo:aim, job-1, step2
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.Node.ID)
	}

	expectedIDs := []string{"root", "live-runs", "repo:aim", "job-1", "s2"}
	if len(ids) != len(expectedIDs) {
		t.Fatalf("expected filtered rows %v, got %v", expectedIDs, ids)
	}
	for i, id := range expectedIDs {
		if ids[i] != id {
			t.Errorf("row %d: expected %s, got %s", i, id, ids[i])
		}
	}

	// Ancestors must be expanded
	if !aimRepo.Expanded {
		t.Errorf("expected repo:aim to be auto-expanded")
	}
	if !job1.Expanded {
		t.Errorf("expected job-1 to be auto-expanded")
	}

	// View should contain "smoke" and not cxstatusline
	view := tw.View(40)
	if !strings.Contains(view, "smoke") {
		t.Fatalf("expected view to contain 'smoke', got:\n%s", view)
	}
	if strings.Contains(view, "cxstatusline") {
		t.Fatalf("expected view NOT to contain 'cxstatusline', got:\n%s", view)
	}

	// 2. Clear filter
	tw.RebuildWithFilter("")
	if tw.Filter() != "" {
		t.Fatalf("expected empty filter after clear, got %q", tw.Filter())
	}

	// repo:aim was originally collapsed (Expanded = false); verify it is restored
	if aimRepo.Expanded {
		t.Errorf("expected repo:aim to be restored to collapsed after clearing filter")
	}

	// cxstatusline should be visible again
	clearView := tw.View(40)
	if !strings.Contains(clearView, "cxstatusline") {
		t.Fatalf("expected view to contain 'cxstatusline' after clearing filter, got:\n%s", clearView)
	}
}

func TestTreeWidget_StatusFilter(t *testing.T) {
	root := &TreeNode{
		ID:       "root",
		Label:    "actr",
		Expanded: true,
		NodeType: NodeBranch,
	}
	job := &TreeNode{
		ID:       "job-1",
		Label:    "BuildJob",
		Expanded: true,
		NodeType: NodeJob,
	}
	root.AddChild(job)

	stepPass := &TreeNode{ID: "s_ok", Label: "Compile", Status: monitor.StatusSuccess, NodeType: NodeStep}
	stepFail := &TreeNode{ID: "s_err", Label: "Test", Status: monitor.StatusFailed, NodeType: NodeStep}
	job.AddChild(stepPass).AddChild(stepFail)

	tw := NewTreeWidget(root)

	// Filter by "fail"
	tw.RebuildWithFilter("fail")
	rows := tw.Rows()

	var ids []string
	for _, r := range rows {
		ids = append(ids, r.Node.ID)
	}
	expected := []string{"root", "job-1", "s_err"}
	if len(ids) != len(expected) {
		t.Fatalf("expected rows %v, got %v", expected, ids)
	}
	for i, id := range expected {
		if ids[i] != id {
			t.Errorf("row %d: expected %s, got %s", i, id, ids[i])
		}
	}
}

