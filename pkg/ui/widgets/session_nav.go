package widgets

import (
	"strings"

	"m31labs.dev/fluffyui/runtime"
)

// SessionNavNode is one row of the navigator's Sessions section: either a
// flat session summary (Depth 0, Status "") or a node in the current
// session's ledger run tree (subagent runs nested under their parent by
// Depth).
type SessionNavNode struct {
	ID     string
	Label  string
	Depth  int
	Active bool
	// Status is an ActivityStatus value ("running", "completed", "failed",
	// "cancelled") for run-tree nodes, or "" for flat session entries.
	Status string
}

// SetSessionNodes replaces the Sessions section content, preserving the
// selected node by ID when possible.
func (s *Sidebar) SetSessionNodes(nodes []SessionNavNode) {
	if s == nil {
		return
	}
	selectedID := ""
	if s.sessionSelected >= 0 && s.sessionSelected < len(s.sessionNodes) {
		selectedID = s.sessionNodes[s.sessionSelected].ID
	}
	s.sessionNodes = nodes
	s.sessionSelected = -1
	for i, node := range nodes {
		if node.ID == selectedID {
			s.sessionSelected = i
			break
		}
	}
}

// SelectedSessionNode returns the currently selected Sessions row, if any.
func (s *Sidebar) SelectedSessionNode() (SessionNavNode, bool) {
	if s == nil || s.sessionSelected < 0 || s.sessionSelected >= len(s.sessionNodes) {
		return SessionNavNode{}, false
	}
	return s.sessionNodes[s.sessionSelected], true
}

// SetOnSessionSelect sets the callback fired when a Sessions row is
// selected by mouse click.
func (s *Sidebar) SetOnSessionSelect(cb func(SessionNavNode)) {
	if s == nil {
		return
	}
	s.onSessionSelect = cb
}

func hasSessionsSection(s *Sidebar) bool {
	return len(s.sessionNodes) > 0
}

func (s *Sidebar) renderSessions(buf *runtime.Buffer, x, y, width, maxHeight int) int {
	icon := '▼'
	if !s.showSessions {
		icon = '▶'
	}
	buf.Set(x, y, icon, s.headerStyle)
	buf.SetString(x+2, y, "Sessions", s.headerStyle)
	y++

	s.sessionsRowsY = y
	s.sessionsRowCount = 0
	if !s.showSessions {
		return y
	}

	maxRows := maxHeight - 1
	if maxRows > len(s.sessionNodes) {
		maxRows = len(s.sessionNodes)
	}
	if maxRows < 0 {
		maxRows = 0
	}

	for i := 0; i < maxRows; i++ {
		node := s.sessionNodes[i]
		style := s.textStyle
		if i == s.sessionSelected {
			style = s.activeStyle
		}
		label := sessionNavLabel(node)
		buf.SetString(x+2, y, truncateSidebarText(label, width-2), style)
		y++
	}
	s.sessionsRowCount = maxRows

	return y
}

func sessionNavLabel(node SessionNavNode) string {
	prefix := strings.Repeat("  ", node.Depth)
	if node.Status != "" {
		prefix += activityStatusGlyph(ActivityStatus(node.Status)) + " "
	}
	label := strings.TrimSpace(node.Label)
	if label == "" {
		label = node.ID
	}
	line := prefix + label
	if node.Active {
		line += " *"
	}
	return line
}

func (s *Sidebar) handleMouse(msg runtime.MouseMsg) runtime.HandleResult {
	if !s.bounds.Contains(msg.X, msg.Y) {
		return runtime.Unhandled()
	}
	if msg.Button != runtime.MouseLeft || msg.Action != runtime.MousePress {
		return runtime.Unhandled()
	}
	if s.sessionsRowCount == 0 {
		return runtime.Unhandled()
	}
	row := msg.Y - s.sessionsRowsY
	if row < 0 || row >= s.sessionsRowCount {
		return runtime.Unhandled()
	}
	if row >= len(s.sessionNodes) {
		return runtime.Unhandled()
	}
	s.sessionSelected = row
	if s.onSessionSelect != nil {
		s.onSessionSelect(s.sessionNodes[row])
	}
	return runtime.Handled()
}
