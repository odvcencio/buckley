package widgets

import (
	"path/filepath"
	"sort"
	"strings"

	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

func summarizePlan(tasks []PlanTask) (completed, total int) {
	for _, task := range tasks {
		total++
		if task.Status == TaskCompleted {
			completed++
		}
	}
	return completed, total
}

func taskStatusLabel(status TaskStatus) string {
	switch status {
	case TaskCompleted:
		return "done"
	case TaskInProgress:
		return "running"
	case TaskFailed:
		return "failed"
	default:
		return "pending"
	}
}

func clampPercent(progress int) int {
	if progress < 0 {
		return 0
	}
	if progress > 100 {
		return 100
	}
	return progress
}

func formatContextLabel(used, budget, window int) string {
	if budget > 0 {
		return intToStr(used) + " / " + intToStr(budget)
	}
	if window > 0 {
		return intToStr(used) + " / " + intToStr(window)
	}
	if used > 0 {
		return intToStr(used)
	}
	return "0"
}

func splitPath(path string) []string {
	clean := filepath.Clean(path)
	if clean == "." || clean == string(filepath.Separator) {
		return []string{path}
	}
	parts := strings.Split(clean, string(filepath.Separator))
	if len(parts) == 0 {
		return []string{path}
	}
	for i := range parts {
		if parts[i] == "" {
			parts[i] = string(filepath.Separator)
		}
	}
	return parts
}

func buildTreeFromPaths(paths []string, rootLabel string) *uiwidgets.TreeNode {
	label := "Files"
	if strings.TrimSpace(rootLabel) != "" {
		label = filepath.Base(rootLabel)
	}
	root := &uiwidgets.TreeNode{Label: label, Expanded: true}
	if len(paths) == 0 {
		root.Children = []*uiwidgets.TreeNode{{Label: "(none)"}}
		return root
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	for _, path := range sorted {
		addPathNode(root, path)
	}
	return root
}

func buildTouchesTree(touches []TouchSummary) *uiwidgets.TreeNode {
	root := &uiwidgets.TreeNode{Label: "Touches", Expanded: true}
	if len(touches) == 0 {
		root.Children = []*uiwidgets.TreeNode{{Label: "(none)"}}
		return root
	}
	for _, touch := range touches {
		label := touch.Path
		if label == "" {
			label = "(unknown)"
		}
		child := &uiwidgets.TreeNode{Label: label}
		for _, r := range touch.Ranges {
			child.Children = append(child.Children, &uiwidgets.TreeNode{Label: rangeLabel(r)})
		}
		root.Children = append(root.Children, child)
	}
	return root
}

func addPathNode(root *uiwidgets.TreeNode, path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) == 1 {
		parts = strings.Split(path, "/")
	}
	cur := root
	for _, part := range parts {
		if part == "" {
			continue
		}
		next := findChild(cur, part)
		if next == nil {
			next = &uiwidgets.TreeNode{Label: part}
			cur.Children = append(cur.Children, next)
		}
		cur = next
	}
}

func findChild(node *uiwidgets.TreeNode, label string) *uiwidgets.TreeNode {
	for _, child := range node.Children {
		if child.Label == label {
			return child
		}
	}
	return nil
}

func rangeLabel(r TouchRange) string {
	if r.End > r.Start {
		return "lines " + intToStr(r.Start) + "-" + intToStr(r.End)
	}
	return "line " + intToStr(r.Start)
}
