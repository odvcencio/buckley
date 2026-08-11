package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxCanopyReviewInput    = 4 << 20
	maxCanopyHotspots       = 8
	maxCanopyCapabilities   = 8
	maxCanopyProjectBytes   = 24 << 10
	maxCanopyProjectSeeds   = 6
	maxCanopyProjectEdges   = 12
	canopyProjectGraphDepth = 2
	canopyReviewTimeout     = 12 * time.Second
	canopyProjectTimeout    = 45 * time.Second
	canopyPipeWaitDelay     = 2 * time.Second
)

type canopyReviewEvidence struct {
	Output           string
	Status           string
	Runtime          time.Duration
	IndexScope       string
	IndexScopeSource string
	BlastRadius      int
}

type canopyReviewReport struct {
	Base            string                    `json:"base"`
	ChangedFiles    int                       `json:"changed_files"`
	IndexScope      string                    `json:"index_scope"`
	ComplexityDelta []canopyComplexityHotspot `json:"complexity_delta"`
	NewCapabilities []canopyReviewCapability  `json:"new_capabilities"`
	BlastRadius     int                       `json:"blast_radius"`
}

type canopyComplexityHotspot struct {
	File       string `json:"file"`
	Name       string `json:"name"`
	Cyclomatic int    `json:"cyclomatic"`
	Cognitive  int    `json:"cognitive"`
	Lines      int    `json:"lines"`
}

type canopyReviewCapability struct {
	Name       string `json:"name"`
	Category   string `json:"category"`
	Confidence string `json:"confidence"`
	AttackID   string `json:"attack_id,omitempty"`
}

type compactCanopyReview struct {
	Base               string                    `json:"base"`
	ChangedFiles       int                       `json:"changed_files"`
	IndexScope         string                    `json:"index_scope"`
	IndexScopeSource   string                    `json:"index_scope_source,omitempty"`
	BlastRadius        int                       `json:"blast_radius"`
	ComplexityHotspots []canopyComplexityHotspot `json:"complexity_hotspots,omitempty"`
	Capabilities       []canopyReviewCapability  `json:"credible_capabilities,omitempty"`
}

// canopyProjectSummary is the stable subset of Canopy's repository summary
// that is useful as a review table of contents. Keeping the wire shape here
// lets us add richer navigation without passing the raw, provider-specific
// report straight into the model prompt.
type canopyProjectSummary struct {
	Files       canopyProjectFiles       `json:"files"`
	Symbols     canopyProjectSymbols     `json:"symbols"`
	References  canopyProjectReferences  `json:"references"`
	ParseErrors canopyProjectParseErrors `json:"parse_errors"`
	CallGraph   canopyProjectCallGraph   `json:"call_graph"`
	Complexity  canopyProjectComplexity  `json:"complexity"`
	TopComplex  []canopySummaryMetric    `json:"top_complex,omitempty"`
	TopFanIn    []canopySummaryMetric    `json:"top_fan_in,omitempty"`
}

type canopyProjectFiles struct {
	Total      int            `json:"total"`
	Generated  int            `json:"generated"`
	ByLanguage map[string]int `json:"by_language,omitempty"`
}

type canopyProjectSymbols struct {
	Total  int            `json:"total"`
	ByKind map[string]int `json:"by_kind,omitempty"`
}

type canopyProjectReferences struct {
	Total int `json:"total"`
}

type canopyProjectParseErrors struct {
	Count int `json:"count"`
}

type canopyProjectCallGraph struct {
	TotalEdges     int     `json:"total_edges"`
	Unresolved     int     `json:"unresolved"`
	ResolutionRate float64 `json:"resolution_rate"`
}

type canopyProjectComplexity struct {
	AvgCyclomatic float64 `json:"avg_cyclomatic"`
	MaxCyclomatic int     `json:"max_cyclomatic"`
	P90Cyclomatic int     `json:"p90_cyclomatic"`
	AvgCognitive  float64 `json:"avg_cognitive"`
	MaxCognitive  int     `json:"max_cognitive"`
}

type canopySummaryMetric struct {
	File       string `json:"file"`
	Name       string `json:"name"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line,omitempty"`
	Value      int    `json:"value,omitempty"`
	Cyclomatic int    `json:"cyclomatic,omitempty"`
	Cognitive  int    `json:"cognitive,omitempty"`
	Lines      int    `json:"lines,omitempty"`
}

type canopyProjectTOC struct {
	Version            int                      `json:"version"`
	Source             canopyProjectTOCSource   `json:"source"`
	Summary            canopyProjectSummary     `json:"summary"`
	MajorCallSites     []canopyTOCCallSite      `json:"major_call_sites,omitempty"`
	ComplexityHotspots []canopyTOCHotspot       `json:"complexity_hotspots,omitempty"`
	ImportantFlows     []canopyTOCFlow          `json:"important_flows,omitempty"`
	Coverage           canopyProjectTOCCoverage `json:"coverage"`
	Truncated          bool                     `json:"truncated,omitempty"`
}

type canopyProjectTOCSource struct {
	Tool       string `json:"tool"`
	IndexScope string `json:"index_scope"`
	RuntimeMS  int64  `json:"runtime_ms"`
}

type canopyTOCCallSite struct {
	File      string `json:"file"`
	Name      string `json:"name"`
	StartLine int    `json:"start_line"`
	FanIn     int    `json:"fan_in"`
}

type canopyTOCHotspot struct {
	File       string `json:"file"`
	Name       string `json:"name"`
	StartLine  int    `json:"start_line"`
	Cyclomatic int    `json:"cyclomatic"`
	Cognitive  int    `json:"cognitive"`
	Lines      int    `json:"lines"`
}

type canopyProjectTOCCoverage struct {
	Summary            string   `json:"summary"`
	FlowSeeds          int      `json:"flow_seeds"`
	FlowsCollected     int      `json:"flows_collected"`
	CallGraphsComplete bool     `json:"call_graphs_complete"`
	Notes              []string `json:"notes,omitempty"`
}

type canopyTOCFlow struct {
	Root           canopyGraphSymbol `json:"root"`
	Signal         string            `json:"signal"`
	FanIn          int               `json:"fan_in,omitempty"`
	Cyclomatic     int               `json:"cyclomatic,omitempty"`
	Cognitive      int               `json:"cognitive,omitempty"`
	Callers        []canopyTOCEdge   `json:"callers,omitempty"`
	Callees        []canopyTOCEdge   `json:"callees,omitempty"`
	ReachableNodes int               `json:"reachable_nodes"`
	GraphEdges     int               `json:"graph_edges"`
	Status         string            `json:"status"`
}

type canopyGraphSymbol struct {
	ID        string `json:"id,omitempty"`
	File      string `json:"file"`
	Package   string `json:"package,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name"`
	Signature string `json:"signature,omitempty"`
	Receiver  string `json:"receiver,omitempty"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line,omitempty"`
	Callable  bool   `json:"callable,omitempty"`
}

type canopyGraphSample struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
}

type canopyGraphEdge struct {
	Caller     canopyGraphSymbol   `json:"caller"`
	Callee     canopyGraphSymbol   `json:"callee"`
	Resolution string              `json:"resolution,omitempty"`
	Count      int                 `json:"count,omitempty"`
	Samples    []canopyGraphSample `json:"samples,omitempty"`
}

type canopyGraphReport struct {
	Roots []canopyGraphSymbol `json:"roots"`
	Nodes []canopyGraphSymbol `json:"nodes"`
	Edges []canopyGraphEdge   `json:"edges"`
}

type canopyTOCEdge struct {
	File       string `json:"file"`
	Package    string `json:"package,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name"`
	Signature  string `json:"signature,omitempty"`
	StartLine  int    `json:"start_line"`
	Count      int    `json:"count,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	SampleFile string `json:"sample_file,omitempty"`
	SampleLine int    `json:"sample_line,omitempty"`
}

func collectCanopyReview(parent context.Context, repoRoot, baseCommit string) (string, string) {
	evidence := collectCanopyReviewEvidence(parent, repoRoot, baseCommit)
	return evidence.Output, evidence.Status
}

func collectCanopyReviewEvidence(parent context.Context, repoRoot, baseCommit string) canopyReviewEvidence {
	executable, err := findCanopyExecutable()
	if err != nil {
		return canopyReviewEvidence{Status: "not installed"}
	}
	baseCommit = strings.TrimSpace(baseCommit)
	if baseCommit == "" {
		return canopyReviewEvidence{Status: "base commit unavailable"}
	}

	ctx, cancel := context.WithTimeout(nonNilReviewContext(parent), canopyReviewTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "analyze", "review", "--base", baseCommit, "--json", ".")
	cmd.Dir = repoRoot
	cmd.WaitDelay = canopyPipeWaitDelay
	started := time.Now()
	output, err := cmd.Output()
	elapsed := time.Since(started)
	if ctx.Err() != nil {
		return canopyReviewEvidence{Status: "timed out", Runtime: elapsed}
	}
	if err != nil {
		return canopyReviewEvidence{Status: "analysis failed", Runtime: elapsed}
	}
	output = []byte(strings.TrimSpace(string(output)))
	if len(output) == 0 {
		return canopyReviewEvidence{Status: "returned no evidence", Runtime: elapsed}
	}
	if len(output) > maxCanopyReviewInput {
		return canopyReviewEvidence{Status: "evidence exceeded input limit", Runtime: elapsed}
	}
	var report canopyReviewReport
	if err := json.Unmarshal(output, &report); err != nil {
		return canopyReviewEvidence{Status: "returned invalid JSON", Runtime: elapsed}
	}
	report.Base = strings.TrimSpace(report.Base)
	if report.Base != baseCommit {
		return canopyReviewEvidence{Status: "base revision mismatch", Runtime: elapsed}
	}
	indexScope, indexScopeSource, valid := resolveCanopyIndexScope(report.IndexScope)
	if !valid {
		return canopyReviewEvidence{Status: "index scope unavailable", Runtime: elapsed}
	}
	compact := compactCanopyReview{
		Base:               report.Base,
		ChangedFiles:       report.ChangedFiles,
		IndexScope:         indexScope,
		IndexScopeSource:   indexScopeSource,
		BlastRadius:        report.BlastRadius,
		ComplexityHotspots: selectCanopyHotspots(report.ComplexityDelta),
		Capabilities:       selectCredibleCanopyCapabilities(report.NewCapabilities),
	}
	output, err = json.MarshalIndent(compact, "", "  ")
	if err != nil {
		return canopyReviewEvidence{Status: "could not compact evidence", Runtime: elapsed}
	}
	return canopyReviewEvidence{
		Output:           string(output),
		Status:           "available",
		Runtime:          elapsed,
		IndexScope:       indexScope,
		IndexScopeSource: indexScopeSource,
		BlastRadius:      report.BlastRadius,
	}
}

func resolveCanopyIndexScope(value string) (scope, source string, valid bool) {
	switch scope = strings.TrimSpace(value); scope {
	case "":
		return "repository", "repository-root invocation", true
	case "changed", "repository":
		return scope, "Canopy report", true
	default:
		return "", "", false
	}
}

func selectCanopyHotspots(all []canopyComplexityHotspot) []canopyComplexityHotspot {
	selected := make([]canopyComplexityHotspot, 0, len(all))
	for _, hotspot := range all {
		if hotspot.Cognitive < 15 && hotspot.Cyclomatic < 10 && hotspot.Lines < 100 {
			continue
		}
		selected = append(selected, hotspot)
	}
	sort.Slice(selected, func(i, j int) bool {
		left, right := selected[i], selected[j]
		switch {
		case left.Cognitive != right.Cognitive:
			return left.Cognitive > right.Cognitive
		case left.Cyclomatic != right.Cyclomatic:
			return left.Cyclomatic > right.Cyclomatic
		case left.Lines != right.Lines:
			return left.Lines > right.Lines
		case left.File != right.File:
			return left.File < right.File
		default:
			return left.Name < right.Name
		}
	})
	if len(selected) > maxCanopyHotspots {
		selected = selected[:maxCanopyHotspots]
	}
	return selected
}

func selectCredibleCanopyCapabilities(all []canopyReviewCapability) []canopyReviewCapability {
	selected := make([]canopyReviewCapability, 0, len(all))
	seen := make(map[string]struct{}, len(all))
	for _, capability := range all {
		capability.Name = strings.TrimSpace(capability.Name)
		capability.Category = strings.TrimSpace(capability.Category)
		capability.Confidence = strings.ToLower(strings.TrimSpace(capability.Confidence))
		capability.AttackID = strings.TrimSpace(capability.AttackID)
		if capability.Confidence != "high" || capability.Name == "" {
			continue
		}
		key := capability.Name + "\x00" + capability.Category + "\x00" + capability.AttackID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		selected = append(selected, capability)
	}
	sort.Slice(selected, func(i, j int) bool {
		left, right := selected[i], selected[j]
		switch {
		case left.Category != right.Category:
			return left.Category < right.Category
		case left.Name != right.Name:
			return left.Name < right.Name
		default:
			return left.AttackID < right.AttackID
		}
	})
	if len(selected) > maxCanopyCapabilities {
		selected = selected[:maxCanopyCapabilities]
	}
	return selected
}

func collectCanopyProjectSummary(parent context.Context, repoRoot string) (string, string) {
	executable, err := findCanopyExecutable()
	if err != nil {
		return "", "not installed"
	}

	ctx, cancel := context.WithTimeout(nonNilReviewContext(parent), canopyProjectTimeout)
	defer cancel()
	started := time.Now()
	cachePath, cleanupCache, indexErr := buildCanopyProjectIndex(ctx, executable, repoRoot)
	if cleanupCache != nil {
		defer cleanupCache()
	}
	summaryArgs := []string{"analyze", "summary", "--json", "."}
	if indexErr == nil {
		summaryArgs = append(summaryArgs, "--cache", cachePath)
	} else {
		// A failed refresh must not make Buckbot consume a stale structural
		// cache. The summary remains useful, but flows are marked partial below.
		summaryArgs = append(summaryArgs, "--no-cache")
	}
	output, err := runCanopyJSON(ctx, executable, repoRoot, summaryArgs...)
	if ctx.Err() != nil {
		return "", fmt.Sprintf("timed out after %s", canopyProjectTimeout)
	}
	if err != nil {
		return "", "analysis failed"
	}
	if len(output) == 0 {
		return "", "returned no evidence"
	}

	var summary canopyProjectSummary
	if err := json.Unmarshal(output, &summary); err != nil {
		return "", "returned invalid JSON"
	}

	toc := canopyProjectTOC{
		Version: 1,
		Source: canopyProjectTOCSource{
			Tool:       "canopy",
			IndexScope: "repository",
		},
		Summary: summary,
		Coverage: canopyProjectTOCCoverage{
			Summary: "available",
		},
	}
	if indexErr != nil {
		toc.Coverage.Notes = append(toc.Coverage.Notes, "Canopy structural index refresh failed; summary was rebuilt without a reusable cache")
	}
	for _, metric := range summary.TopFanIn {
		if strings.TrimSpace(metric.File) == "" || strings.TrimSpace(metric.Name) == "" {
			continue
		}
		toc.MajorCallSites = append(toc.MajorCallSites, canopyTOCCallSite{
			File:      filepath.ToSlash(metric.File),
			Name:      metric.Name,
			StartLine: metric.StartLine,
			FanIn:     metric.Value,
		})
	}
	if len(toc.MajorCallSites) > maxCanopyProjectSeeds {
		toc.MajorCallSites = toc.MajorCallSites[:maxCanopyProjectSeeds]
	}
	for _, metric := range summary.TopComplex {
		if strings.TrimSpace(metric.File) == "" || strings.TrimSpace(metric.Name) == "" {
			continue
		}
		toc.ComplexityHotspots = append(toc.ComplexityHotspots, canopyTOCHotspot{
			File:       filepath.ToSlash(metric.File),
			Name:       metric.Name,
			StartLine:  metric.StartLine,
			Cyclomatic: metric.Cyclomatic,
			Cognitive:  metric.Cognitive,
			Lines:      metric.Lines,
		})
	}
	if len(toc.ComplexityHotspots) > maxCanopyHotspots {
		toc.ComplexityHotspots = toc.ComplexityHotspots[:maxCanopyHotspots]
	}

	seeds := selectCanopyProjectSeeds(summary)
	toc.Coverage.FlowSeeds = len(seeds)
	status := "available"
	if indexErr != nil {
		status = "available; structural flow graph partial"
	}
	for _, seed := range seeds {
		if indexErr != nil {
			toc.ImportantFlows = append(toc.ImportantFlows, canopyTOCFlow{
				Root: canopyGraphSymbol{
					File:      seed.File,
					Name:      seed.Name,
					StartLine: seed.StartLine,
				},
				Signal: seed.Signal,
				Status: "unavailable (index refresh failed)",
			})
			toc.Coverage.Notes = append(toc.Coverage.Notes,
				fmt.Sprintf("call graph skipped for %s:%d because the structural index could not be refreshed", seed.File, seed.StartLine))
			continue
		}
		flow := collectCanopyProjectFlow(ctx, executable, repoRoot, cachePath, seed)
		if flow.Status != "available" {
			status = "available; structural flow graph partial"
		}
		if flow.Status == "available" {
			toc.Coverage.FlowsCollected++
		} else {
			toc.Coverage.Notes = append(toc.Coverage.Notes,
				fmt.Sprintf("call graph unavailable for %s:%d (%s)", seed.File, seed.StartLine, flow.Status))
		}
		toc.ImportantFlows = append(toc.ImportantFlows, flow)
	}
	toc.Coverage.CallGraphsComplete = len(seeds) == toc.Coverage.FlowsCollected
	if len(seeds) == 0 {
		status = "available; no structural flow roots returned"
		toc.Coverage.Notes = append(toc.Coverage.Notes, "Canopy summary returned no callable fan-in or complexity roots")
	}
	toc.Source.RuntimeMS = time.Since(started).Milliseconds()

	encoded, err := marshalCanopyProjectTOC(&toc)
	if err != nil {
		return "", "could not compact evidence"
	}
	return string(encoded), status
}

func buildCanopyProjectIndex(ctx context.Context, executable, repoRoot string) (string, func(), error) {
	cacheDir, err := os.MkdirTemp("", "buckley-canopy-index-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(cacheDir) }
	cachePath := filepath.Join(cacheDir, "index.json")
	cmd := exec.CommandContext(ctx, executable, "index", "build", ".", "--out", cachePath)
	cmd.Dir = repoRoot
	cmd.WaitDelay = canopyPipeWaitDelay
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", nil, err
	}
	if _, err := os.Stat(cachePath); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("canopy index build did not create %s: %w", cachePath, err)
	}
	return cachePath, cleanup, nil
}

func runCanopyJSON(ctx context.Context, executable, repoRoot string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = repoRoot
	cmd.WaitDelay = canopyPipeWaitDelay
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(string(output))), nil
}

type canopyProjectSeed struct {
	File       string
	Name       string
	StartLine  int
	Signal     string
	FanIn      int
	Cyclomatic int
	Cognitive  int
}

func selectCanopyProjectSeeds(summary canopyProjectSummary) []canopyProjectSeed {
	seeds := make([]canopyProjectSeed, 0, maxCanopyProjectSeeds)
	seen := make(map[string]struct{}, maxCanopyProjectSeeds)
	add := func(metric canopySummaryMetric, signal string) {
		file := filepath.ToSlash(strings.TrimSpace(metric.File))
		name := strings.TrimSpace(metric.Name)
		if file == "" || name == "" {
			return
		}
		key := fmt.Sprintf("%s\x00%s\x00%d", file, name, metric.StartLine)
		if _, exists := seen[key]; exists || len(seeds) >= maxCanopyProjectSeeds {
			return
		}
		seen[key] = struct{}{}
		seeds = append(seeds, canopyProjectSeed{
			File:       file,
			Name:       name,
			StartLine:  metric.StartLine,
			Signal:     signal,
			FanIn:      metric.Value,
			Cyclomatic: metric.Cyclomatic,
			Cognitive:  metric.Cognitive,
		})
	}
	// Fan-in roots expose the most important call sites. Complexity roots then
	// add risky flows that may not be popular enough to appear in fan-in.
	for _, metric := range summary.TopFanIn {
		add(metric, "high-fan-in")
	}
	for _, metric := range summary.TopComplex {
		add(metric, "complexity-hotspot")
	}
	return seeds
}

func collectCanopyProjectFlow(ctx context.Context, executable, repoRoot, cachePath string, seed canopyProjectSeed) canopyTOCFlow {
	flow := canopyTOCFlow{
		Root: canopyGraphSymbol{
			File:      seed.File,
			Name:      seed.Name,
			StartLine: seed.StartLine,
		},
		Signal:     seed.Signal,
		FanIn:      seed.FanIn,
		Cyclomatic: seed.Cyclomatic,
		Cognitive:  seed.Cognitive,
		Status:     "unavailable",
	}

	forward, forwardErr := runCanopyGraph(ctx, executable, repoRoot, cachePath, seed, false)
	reverse, reverseErr := runCanopyGraph(ctx, executable, repoRoot, cachePath, seed, true)
	forwardOK := forwardErr == nil && len(forward.Roots) > 0
	reverseOK := reverseErr == nil && len(reverse.Roots) > 0
	if forwardOK {
		if len(forward.Roots) > 0 {
			flow.Root = forward.Roots[0]
		}
		flow.Callees = compactCanopyGraphEdges(forward, flow.Root, true)
		flow.ReachableNodes = len(forward.Nodes)
		flow.GraphEdges = len(forward.Edges)
	}
	if reverseOK {
		if len(reverse.Roots) > 0 && flow.Root.File == "" {
			flow.Root = reverse.Roots[0]
		}
		flow.Callers = compactCanopyGraphEdges(reverse, flow.Root, false)
		if flow.ReachableNodes == 0 {
			flow.ReachableNodes = len(reverse.Nodes)
		}
		if flow.GraphEdges == 0 {
			flow.GraphEdges = len(reverse.Edges)
		}
	}
	switch {
	case forwardOK && reverseOK:
		flow.Status = "available"
	case forwardOK || reverseOK:
		flow.Status = "partial"
	}
	return flow
}

func runCanopyGraph(ctx context.Context, executable, repoRoot, cachePath string, seed canopyProjectSeed, reverse bool) (canopyGraphReport, error) {
	args := []string{
		"graph", "calls", seed.Name, ".",
		"--json",
		"--depth", strconv.Itoa(canopyProjectGraphDepth),
		"--file", seed.File,
	}
	if reverse {
		args = append(args, "--reverse")
	}
	if cachePath != "" {
		args = append(args, "--cache", cachePath)
	} else {
		args = append(args, "--no-cache")
	}
	output, err := runCanopyJSON(ctx, executable, repoRoot, args...)
	if err != nil {
		return canopyGraphReport{}, err
	}
	var report canopyGraphReport
	if err := json.Unmarshal(output, &report); err != nil {
		return canopyGraphReport{}, err
	}
	return report, nil
}

func compactCanopyGraphEdges(report canopyGraphReport, root canopyGraphSymbol, forward bool) []canopyTOCEdge {
	result := make([]canopyTOCEdge, 0, maxCanopyProjectEdges)
	for _, edge := range report.Edges {
		var source, target canopyGraphSymbol
		if forward {
			source, target = edge.Caller, edge.Callee
			if !sameCanopyGraphSymbol(source, root) {
				continue
			}
		} else {
			source, target = edge.Callee, edge.Caller
			if !sameCanopyGraphSymbol(source, root) {
				continue
			}
		}
		entry := canopyTOCEdge{
			File:       target.File,
			Package:    target.Package,
			Kind:       target.Kind,
			Name:       target.Name,
			Signature:  target.Signature,
			StartLine:  target.StartLine,
			Count:      edge.Count,
			Resolution: edge.Resolution,
		}
		if len(edge.Samples) > 0 {
			entry.SampleFile = edge.Samples[0].File
			entry.SampleLine = edge.Samples[0].StartLine
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		if result[i].File != result[j].File {
			return result[i].File < result[j].File
		}
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].StartLine < result[j].StartLine
	})
	if len(result) > maxCanopyProjectEdges {
		result = result[:maxCanopyProjectEdges]
	}
	return result
}

func sameCanopyGraphSymbol(left, right canopyGraphSymbol) bool {
	if left.ID != "" && right.ID != "" {
		return left.ID == right.ID
	}
	return filepath.ToSlash(left.File) == filepath.ToSlash(right.File) &&
		left.Name == right.Name && left.StartLine == right.StartLine
}

func marshalCanopyProjectTOC(toc *canopyProjectTOC) ([]byte, error) {
	encoded, err := json.MarshalIndent(toc, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(encoded) <= maxCanopyProjectBytes {
		return encoded, nil
	}
	// Preserve valid JSON while degrading in a declared order. The model still
	// receives the summary and navigation roots even when a very large graph
	// cannot fit in the prompt budget.
	toc.Truncated = true
	for len(encoded) > maxCanopyProjectBytes && len(toc.ImportantFlows) > 0 {
		toc.ImportantFlows = toc.ImportantFlows[:len(toc.ImportantFlows)-1]
		encoded, err = json.MarshalIndent(toc, "", "  ")
		if err != nil {
			return nil, err
		}
	}
	for len(encoded) > maxCanopyProjectBytes && len(toc.ComplexityHotspots) > 0 {
		toc.ComplexityHotspots = toc.ComplexityHotspots[:len(toc.ComplexityHotspots)-1]
		encoded, err = json.MarshalIndent(toc, "", "  ")
		if err != nil {
			return nil, err
		}
	}
	for len(encoded) > maxCanopyProjectBytes && len(toc.MajorCallSites) > 0 {
		toc.MajorCallSites = toc.MajorCallSites[:len(toc.MajorCallSites)-1]
		encoded, err = json.MarshalIndent(toc, "", "  ")
		if err != nil {
			return nil, err
		}
	}
	if len(encoded) > maxCanopyProjectBytes {
		// Summary is compact in normal operation, but its language/kind maps can
		// be large in a polyglot repository. Keep the navigation contract valid.
		toc.Summary.TopComplex = nil
		toc.Summary.TopFanIn = nil
		encoded, err = json.MarshalIndent(toc, "", "  ")
	}
	return encoded, err
}

func nonNilReviewContext(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

func compactCanopyStatus(value string, maxLen int) string {
	value = strings.Join(strings.Fields(value), " ")
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen-3] + "..."
}

func findCanopyExecutable() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CANOPY_BIN")); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured, nil
		}
		return "", fmt.Errorf("CANOPY_BIN is not executable")
	}
	if path, err := exec.LookPath("canopy"); err == nil {
		return path, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, "go", "bin", "canopy")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("canopy executable not found")
}
