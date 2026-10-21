package commands

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
)

type canopyProjectGraphs struct {
	forward *canopyGraphBatch
	reverse *canopyGraphBatch
}

type canopyGraphBatch struct {
	report      canopyGraphReport
	reverse     bool
	edgesByNode map[string][]int
}

func (graphs canopyProjectGraphs) collectSeed(ctx context.Context, executable, repoRoot, cachePath string, seed canopyProjectSeed, reverse bool) (canopyGraphReport, bool) {
	batch := graphs.forward
	if reverse {
		batch = graphs.reverse
	}
	if report, ok := batch.selectSeed(seed); ok {
		return report, true
	}
	report, err := runCanopyGraph(ctx, executable, repoRoot, cachePath, seed, reverse)
	if err != nil {
		return canopyGraphReport{}, false
	}
	return newCanopyGraphBatch(report, reverse).selectSeed(seed)
}

func collectCanopyProjectGraphs(ctx context.Context, executable, repoRoot, cachePath string, seeds []canopyProjectSeed) canopyProjectGraphs {
	names := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		names = append(names, regexp.QuoteMeta(seed.Name))
	}
	query := canopyProjectSeed{Name: "^(" + strings.Join(names, "|") + ")$"}
	var graphs canopyProjectGraphs
	if report, err := runCanopyGraph(ctx, executable, repoRoot, cachePath, query, false, "--regex"); err == nil {
		graphs.forward = newCanopyGraphBatch(report, false)
	}
	if report, err := runCanopyGraph(ctx, executable, repoRoot, cachePath, query, true, "--regex"); err == nil {
		graphs.reverse = newCanopyGraphBatch(report, true)
	}
	return graphs
}

func newCanopyGraphBatch(report canopyGraphReport, reverse bool) *canopyGraphBatch {
	if len(report.Roots) == 0 {
		return nil
	}
	nodes := make(map[string]bool, len(report.Nodes))
	for _, node := range report.Nodes {
		if node.ID == "" || nodes[node.ID] {
			return nil
		}
		nodes[node.ID] = true
	}
	for _, root := range report.Roots {
		if !nodes[root.ID] {
			return nil
		}
	}
	batch := &canopyGraphBatch{report: report, reverse: reverse, edgesByNode: make(map[string][]int)}
	for i, edge := range report.Edges {
		if !nodes[edge.Caller.ID] || !nodes[edge.Callee.ID] {
			return nil
		}
		id := edge.Caller.ID
		if reverse {
			id = edge.Callee.ID
		}
		batch.edgesByNode[id] = append(batch.edgesByNode[id], i)
	}
	return batch
}

// A union-root walk contains each root's depth-limited walk. Slice it using
// the same breadth-first depth bound, retaining the provider's output order.
func (b *canopyGraphBatch) selectSeed(seed canopyProjectSeed) (canopyGraphReport, bool) {
	if b == nil {
		return canopyGraphReport{}, false
	}
	var selected canopyGraphReport
	seen := make(map[string]bool)
	var frontier []string
	for _, root := range b.report.Roots {
		if root.Name != seed.Name || filepath.ToSlash(filepath.Clean(root.File)) != filepath.ToSlash(filepath.Clean(seed.File)) {
			continue
		}
		if seed.StartLine > 0 && root.StartLine != seed.StartLine {
			continue
		}
		selected.Roots = append(selected.Roots, root)
		seen[root.ID] = true
		frontier = append(frontier, root.ID)
	}
	if len(frontier) != 1 {
		return canopyGraphReport{}, false
	}
	edges := make(map[int]bool)
	for depth := 0; depth < canopyProjectGraphDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, id := range frontier {
			for _, i := range b.edgesByNode[id] {
				edges[i] = true
				edge := b.report.Edges[i]
				nextID := edge.Callee.ID
				if b.reverse {
					nextID = edge.Caller.ID
				}
				if !seen[nextID] {
					seen[nextID] = true
					next = append(next, nextID)
				}
			}
		}
		frontier = next
	}
	for _, node := range b.report.Nodes {
		if seen[node.ID] {
			selected.Nodes = append(selected.Nodes, node)
		}
	}
	for i, edge := range b.report.Edges {
		if edges[i] {
			selected.Edges = append(selected.Edges, edge)
		}
	}
	return selected, true
}
