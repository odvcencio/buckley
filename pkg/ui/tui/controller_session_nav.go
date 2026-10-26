package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/ui/widgets"
)

// refreshSessionNav rebuilds the navigator's Sessions section: the current
// session's ledger run tree when one exists, otherwise the flat session
// list (today's behavior). Safe to call from any goroutine (turn
// completion runs it from streamResponse's goroutine); it only performs
// read-only ledger queries and posts the result through SetSessionNav.
func (c *Controller) refreshSessionNav() {
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		return
	}
	sessionID := c.sessions[c.currentSession].ID
	runLedger := c.runLedger
	sessions := append([]*SessionState(nil), c.sessions...)
	currentIdx := c.currentSession
	c.mu.Unlock()

	if runLedger != nil {
		if nodes, runsByID, ok := buildRunTreeNav(context.Background(), runLedger, sessionID); ok {
			c.mu.Lock()
			c.sessionRunNodes = runsByID
			c.mu.Unlock()
			c.app.SetSessionNav(nodes)
			return
		}
	}

	nodes := make([]widgets.SessionNavNode, len(sessions))
	for i, sess := range sessions {
		nodes[i] = widgets.SessionNavNode{ID: sess.ID, Label: sess.ID, Active: i == currentIdx}
	}
	c.mu.Lock()
	c.sessionRunNodes = nil
	c.mu.Unlock()
	c.app.SetSessionNav(nodes)
}

type runOverviewReader interface {
	SessionRunOverview(context.Context, string) (*runledger.RunOverview, error)
}

// buildRunTreeNav loads sessionID's most recent root run from store and
// flattens its overview into navigator rows. ok is false when the
// session has no run in the ledger yet, so the caller falls back to the
// flat session list.
func buildRunTreeNav(ctx context.Context, store runledger.Store, sessionID string) ([]widgets.SessionNavNode, map[string]*runledger.RunOverview, bool) {
	var tree *runledger.RunOverview
	if reader, ok := store.(runOverviewReader); ok {
		var err error
		tree, err = reader.SessionRunOverview(ctx, sessionID)
		if err != nil {
			return nil, nil, false
		}
	} else {
		runs, err := store.ListRuns(ctx, runledger.RunQuery{SessionID: sessionID})
		if err != nil || len(runs) == 0 {
			return nil, nil, false
		}
		root, ok := latestRootRun(runs)
		if !ok {
			return nil, nil, false
		}
		fullTree, err := runledger.LoadGoalTree(ctx, store, root.RunID)
		if err != nil {
			return nil, nil, false
		}
		tree = runTreeOverview(fullTree)
	}
	if tree == nil {
		return nil, nil, false
	}

	byID := make(map[string]*runledger.RunOverview)
	var nodes []widgets.SessionNavNode
	flattenRunTree(tree, 0, byID, &nodes)
	return nodes, byID, true
}

func runTreeOverview(node *runledger.RunNode) *runledger.RunOverview {
	run := node.Run
	run.Budget, run.Outcome = nil, nil
	overview := &runledger.RunOverview{Run: run, Status: node.State.Status, EventCount: node.State.EventCount}
	for _, child := range node.Children {
		overview.Children = append(overview.Children, runTreeOverview(child))
	}
	return overview
}

// latestRootRun returns the most recently started run with no parent
// (a session can accumulate more than one top-level run over time; the
// navigator shows the current one).
func latestRootRun(runs []runledger.AgentRun) (runledger.AgentRun, bool) {
	var best runledger.AgentRun
	found := false
	for _, r := range runs {
		if r.ParentRunID != "" {
			continue
		}
		if !found || r.StartedAt.After(best.StartedAt) {
			best = r
			found = true
		}
	}
	return best, found
}

func flattenRunTree(node *runledger.RunOverview, depth int, byID map[string]*runledger.RunOverview, out *[]widgets.SessionNavNode) {
	if node == nil {
		return
	}
	byID[node.Run.RunID] = node
	*out = append(*out, widgets.SessionNavNode{
		ID:     node.Run.RunID,
		Label:  runNavLabel(node),
		Depth:  depth,
		Status: node.Status,
	})
	for _, child := range node.Children {
		flattenRunTree(child, depth+1, byID, out)
	}
}

func runNavLabel(node *runledger.RunOverview) string {
	label := strings.TrimSpace(node.Run.AgentID)
	if label == "" {
		label = strings.TrimSpace(node.Run.TaskID)
	}
	if label == "" {
		label = node.Run.RunID
	}
	return label
}

// handleSessionNodeSelected shows a selected run-tree node's state in the
// inspector (the existing ActivityPanel). Flat session-list nodes (no
// matching run) are selection-only, matching today's session list, which
// has no side effect on click.
func (c *Controller) handleSessionNodeSelected(node widgets.SessionNavNode) {
	c.mu.Lock()
	runNode, ok := c.sessionRunNodes[node.ID]
	c.mu.Unlock()
	if !ok {
		return
	}
	c.app.SetActivities([]widgets.ActivityRecord{runNodeToActivityRecord(runNode)})
	c.app.SetActivityPanelVisible(true)
}

func runNodeToActivityRecord(node *runledger.RunOverview) widgets.ActivityRecord {
	status := widgets.ActivityStatus(node.Status)
	if status == "" {
		status = widgets.ActivityRunning
	}
	title := strings.TrimSpace(node.Run.AgentID)
	if title == "" {
		title = node.Run.RunID
	}

	var detail strings.Builder
	fmt.Fprintf(&detail, "Run: %s\n", node.Run.RunID)
	if node.Run.TaskID != "" {
		fmt.Fprintf(&detail, "Task: %s\n", node.Run.TaskID)
	}
	if node.Run.ModelID != "" {
		fmt.Fprintf(&detail, "Model: %s\n", node.Run.ModelID)
	}
	fmt.Fprintf(&detail, "Status: %s\n", node.Run.Status)
	fmt.Fprintf(&detail, "Events: %d\n", node.EventCount)

	var finishedAt time.Time
	if node.Run.EndedAt != nil {
		finishedAt = *node.Run.EndedAt
	}

	return widgets.ActivityRecord{
		ID:         node.Run.RunID,
		Kind:       "run",
		Title:      title,
		Summary:    "Ledger run " + node.Run.RunID,
		Detail:     detail.String(),
		Status:     status,
		StartedAt:  node.Run.StartedAt,
		FinishedAt: finishedAt,
	}
}
