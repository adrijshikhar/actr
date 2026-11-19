package tui

import (
	"strings"

	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// TreeNodeType represents the type and semantic level of a node in the tree hierarchy.
type TreeNodeType int

const (
	NodeBranch TreeNodeType = iota
	NodeRepo
	NodeJob
	NodeStep
)

// TreeNode represents a single node in the hierarchical tree structure.
type TreeNode struct {
	ID       string
	Label    string
	Depth    int
	Status   monitor.StepStatus
	Expanded bool
	Children []*TreeNode
	Parent   *TreeNode
	Data     any
	NodeType TreeNodeType
}

// AddChild appends a child node, wires its Parent pointer, and returns the receiver for chaining.
func (n *TreeNode) AddChild(child *TreeNode) *TreeNode {
	if child != nil {
		child.Parent = n
		n.Children = append(n.Children, child)
	}
	return n
}

// Walk traverses the node hierarchy depth-first. If fn returns false, traversal stops for that branch.
func (n *TreeNode) Walk(fn func(node *TreeNode) bool) {
	if !fn(n) {
		return
	}
	for _, ch := range n.Children {
		if ch != nil {
			ch.Walk(fn)
		}
	}
}

// FindParentJob walks up the tree from n and returns the enclosing NodeJob, or nil if none found.
// If n itself is a NodeJob, it returns n.
func (n *TreeNode) FindParentJob() *TreeNode {
	if n == nil {
		return nil
	}
	if n.NodeType == NodeJob {
		return n
	}
	for curr := n.Parent; curr != nil; curr = curr.Parent {
		if curr.NodeType == NodeJob {
			return curr
		}
		if curr.NodeType == NodeBranch || curr.ID == "root" || curr.ID == "live-runs" || curr.ID == "historical-runs" {
			break
		}
	}
	return nil
}

// FindParentRepo walks up the tree from n and returns the enclosing NodeRepo, or nil if none found.
// If n itself is a NodeRepo, it returns n.
func (n *TreeNode) FindParentRepo() *TreeNode {
	if n == nil {
		return nil
	}
	if n.NodeType == NodeRepo {
		return n
	}
	for curr := n.Parent; curr != nil; curr = curr.Parent {
		if curr.NodeType == NodeRepo {
			return curr
		}
		if curr.NodeType == NodeBranch || curr.ID == "root" || curr.ID == "live-runs" || curr.ID == "historical-runs" {
			break
		}
	}
	return nil
}

// FlatRow holds a flattened tree node with its display depth.
type FlatRow struct {
	Node  *TreeNode
	Depth int
}

// TreeWidget renders and navigates a hierarchical tree.
type TreeWidget struct {
	Root   *TreeNode
	Cursor int
	rows   []FlatRow
}

var (
	selectionBg   = lipgloss.Color("#2563EB")
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(selectionBg)
	normalStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#E2E8F0"))
	greenStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
	redStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	runningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981")).Bold(true)
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#64748B"))
	yellowStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
)

// NewTreeWidget constructs a new TreeWidget with the given root node.
func NewTreeWidget(root *TreeNode) *TreeWidget {
	tw := &TreeWidget{Root: root}
	tw.Rebuild()
	return tw
}

// Rebuild flattens the visible tree nodes based on current expansion state and adjusts the cursor.
func (tw *TreeWidget) Rebuild() {
	tw.rows = nil
	if tw.Root != nil {
		tw.flatten(tw.Root, 0)
	}
	if len(tw.rows) == 0 {
		tw.Cursor = 0
		return
	}
	if tw.Cursor >= len(tw.rows) {
		tw.Cursor = len(tw.rows) - 1
	}
	if tw.Cursor < 0 {
		tw.Cursor = 0
	}
}

func (tw *TreeWidget) flatten(node *TreeNode, depth int) {
	if node == nil {
		return
	}
	node.Depth = depth
	tw.rows = append(tw.rows, FlatRow{Node: node, Depth: depth})
	if node.Expanded && len(node.Children) > 0 {
		for _, ch := range node.Children {
			if ch != nil {
				if ch.Parent == nil {
					ch.Parent = node
				}
				tw.flatten(ch, depth+1)
			}
		}
	}
}

// Up moves the selection cursor up by one row if possible.
func (tw *TreeWidget) Up() {
	if tw.Cursor > 0 {
		tw.Cursor--
	}
}

// Down moves the selection cursor down by one row if possible.
func (tw *TreeWidget) Down() {
	if tw.Cursor < len(tw.rows)-1 {
		tw.Cursor++
	}
}

// Toggle toggles the expanded/collapsed state of the currently selected node if it has children.
func (tw *TreeWidget) Toggle() {
	if len(tw.rows) == 0 || tw.Cursor < 0 || tw.Cursor >= len(tw.rows) {
		return
	}
	node := tw.rows[tw.Cursor].Node
	if node == nil {
		return
	}
	if len(node.Children) > 0 {
		node.Expanded = !node.Expanded
		tw.Rebuild()
	}
}

// Collapse collapses the currently selected node if it is expanded and has children.
// If already collapsed or a leaf node, it navigates selection to the node's parent.
func (tw *TreeWidget) Collapse() {
	if len(tw.rows) == 0 || tw.Cursor < 0 || tw.Cursor >= len(tw.rows) {
		return
	}
	node := tw.rows[tw.Cursor].Node
	if node == nil {
		return
	}
	if len(node.Children) > 0 && node.Expanded {
		node.Expanded = false
		tw.Rebuild()
		return
	}
	if node.Parent != nil {
		tw.selectNode(node.Parent)
	}
}

func (tw *TreeWidget) selectNode(target *TreeNode) bool {
	if target == nil {
		return false
	}
	for i, r := range tw.rows {
		if r.Node == target {
			tw.Cursor = i
			return true
		}
	}
	return false
}

// SelectNode moves the cursor to the specified node if it is currently visible in rows. Returns true if found.
func (tw *TreeWidget) SelectNode(target *TreeNode) bool {
	return tw.selectNode(target)
}

// SelectByID moves the cursor to the visible node with the given ID. Returns true if found.
func (tw *TreeWidget) SelectByID(id string) bool {
	if tw == nil || id == "" {
		return false
	}
	for i, r := range tw.rows {
		if r.Node != nil && r.Node.ID == id {
			tw.Cursor = i
			return true
		}
	}
	return false
}

// FindNode walks the tree starting from Root and returns the first node with the matching ID, or nil if not found.
func (tw *TreeWidget) FindNode(id string) *TreeNode {
	if tw == nil || tw.Root == nil || id == "" {
		return nil
	}
	var found *TreeNode
	tw.Root.Walk(func(n *TreeNode) bool {
		if n.ID == id {
			found = n
			return false
		}
		return true
	})
	return found
}

// Expand expands the currently selected node if it is collapsed and has children.
func (tw *TreeWidget) Expand() {
	if len(tw.rows) == 0 || tw.Cursor < 0 || tw.Cursor >= len(tw.rows) {
		return
	}
	node := tw.rows[tw.Cursor].Node
	if node == nil {
		return
	}
	if len(node.Children) > 0 && !node.Expanded {
		node.Expanded = true
		tw.Rebuild()
	}
}

// SelectedNode returns the currently selected TreeNode, or nil if the tree is empty.
func (tw *TreeWidget) SelectedNode() *TreeNode {
	if tw == nil || len(tw.rows) == 0 || tw.Cursor < 0 || tw.Cursor >= len(tw.rows) {
		return nil
	}
	return tw.rows[tw.Cursor].Node
}

// SelectFirst finds the first visible node matching the predicate and sets the Cursor to it.
func (tw *TreeWidget) SelectFirst(fn func(node *TreeNode) bool) bool {
	if tw == nil || len(tw.rows) == 0 {
		return false
	}
	for i, row := range tw.rows {
		if row.Node != nil && fn(row.Node) {
			tw.Cursor = i
			return true
		}
	}
	return false
}

// View renders the tree within the specified width. If width <= 0, returns empty string.
func (tw *TreeWidget) View(width int) string {
	if width <= 0 {
		return ""
	}

	var b strings.Builder
	for i, r := range tw.rows {
		if r.Node == nil {
			continue
		}

		indent := strings.Repeat("  ", r.Depth)
		indentWidth := r.Depth * 2
		glyphWidth := 2
		prefixWidth := indentWidth + glyphWidth
		avail := width - prefixWidth

		label := r.Node.Label
		if avail <= 0 {
			label = ""
		} else {
			label = ansi.Truncate(label, avail, "...")
		}

		isSelected := (i == tw.Cursor)
		var glyph string

		if len(r.Node.Children) > 0 {
			symbol := "▶ "
			if r.Node.Expanded {
				symbol = "▼ "
			}
			if isSelected {
				glyph = selectedStyle.Render(symbol)
			} else {
				glyph = normalStyle.Render(symbol)
			}
		} else {
			var baseStyle lipgloss.Style
			var symbol string
			switch r.Node.Status {
			case monitor.StatusRunning:
				baseStyle = runningStyle
				symbol = "● "
			case monitor.StatusSuccess:
				baseStyle = greenStyle
				symbol = "✓ "
			case monitor.StatusFailed:
				baseStyle = redStyle
				symbol = "✖ "
			case monitor.StatusSkipped:
				baseStyle = yellowStyle
				symbol = "⊘ "
			default:
				baseStyle = dimStyle
				symbol = "○ "
			}

			if isSelected {
				glyph = baseStyle.Background(selectionBg).Render(symbol)
			} else {
				glyph = baseStyle.Render(symbol)
			}
		}

		if isSelected {
			renderedIndent := selectedStyle.Render(indent)
			renderedLabel := selectedStyle.Render(label)
			content := renderedIndent + glyph + renderedLabel

			contentWidth := ansi.StringWidth(content)
			if contentWidth < width {
				content += selectedStyle.Render(strings.Repeat(" ", width-contentWidth))
			} else if contentWidth > width {
				content = ansi.Truncate(content, width, "")
			}
			b.WriteString(content)
		} else {
			line := normalStyle.Render(indent) + glyph + normalStyle.Render(label)
			if ansi.StringWidth(line) > width {
				line = ansi.Truncate(line, width, "")
			}
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return b.String()
}
