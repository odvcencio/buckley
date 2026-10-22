package commands

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestMarshalCanopyProjectTOC_TrimsPreviewsBeforeRoots(t *testing.T) {
	nodes, edges := 40, 50
	toc := canopyProjectTOC{Version: 1, Coverage: canopyProjectTOCCoverage{Summary: "available", FlowSeeds: 6, FlowsCollected: 6, CallGraphsComplete: true}}
	for i := 0; i < 6; i++ {
		flow := canopyTOCFlow{Root: canopyGraphSymbol{ID: fmt.Sprint(i), File: "a.go", Name: "Root", StartLine: i + 1}, Status: "available", GraphDirection: "callees", GraphDepth: 2, ReachableNodes: &nodes, GraphEdges: &edges}
		for j := 0; j < 12; j++ {
			edge := canopyTOCEdge{Name: fmt.Sprint(j), File: "callee.go", StartLine: j + 1, Count: 12 - j, Signature: strings.Repeat("argument ", 64)}
			flow.Callers = append(flow.Callers, edge)
			flow.Callees = append(flow.Callees, edge)
		}
		toc.ImportantFlows = append(toc.ImportantFlows, flow)
	}
	before, _ := json.Marshal(toc)
	if len(before) <= maxCanopyProjectBytes {
		t.Fatal("fixture must exceed the context budget")
	}
	encoded, err := marshalCanopyProjectTOC(&toc)
	if err != nil || len(encoded) > maxCanopyProjectBytes || !toc.Truncated || len(toc.ImportantFlows) != 6 {
		t.Fatalf("compaction lost roots or exceeded budget: bytes=%d flows=%d err=%v", len(encoded), len(toc.ImportantFlows), err)
	}
	if !toc.Coverage.CallGraphsComplete || toc.Coverage.FlowsCollected != 6 {
		t.Fatal("preview compaction changed measured coverage")
	}
	for i, flow := range toc.ImportantFlows {
		if flow.Root.ID != fmt.Sprint(i) || flow.GraphDepth != 2 || flow.GraphDirection != "callees" || *flow.ReachableNodes != nodes || *flow.GraphEdges != edges {
			t.Fatalf("flow identity or counts changed: %+v", flow)
		}
		if !flow.CallersTruncated || !flow.CalleesTruncated || len(flow.Callers) == 0 || len(flow.Callees) == 0 {
			t.Fatalf("preview omissions were not declared: %+v", flow)
		}
		for _, preview := range [][]canopyTOCEdge{flow.Callers, flow.Callees} {
			for j, edge := range preview {
				if edge.Name != fmt.Sprint(j) || edge.Count != 12-j {
					t.Fatal("compaction discarded a higher-ranked edge")
				}
			}
		}
	}
}

func TestMarshalCanopyProjectTOC_PreservesEvidenceWithinCompactBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		signature string
		flows     int
		truncated bool
	}{
		{"fits_after_compaction", "func Start(ctx context.Context) error", 6, false},
		{"oversized_flows_still_trimmed", strings.Repeat("Argument", maxCanopyProjectBytes/4), 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fanIn, cyclomatic, cognitive := 6, 4, 5
			nodes, edges := 13, 12
			toc := canopyProjectTOC{
				Version: 1,
				Source:  canopyProjectTOCSource{Tool: "canopy", IndexScope: "repository", RuntimeMS: 12},
				Summary: canopyProjectSummary{
					Files:       canopyProjectFiles{Total: 20, ByLanguage: map[string]int{"go": 20}},
					ParseErrors: canopyProjectParseErrors{Count: 1},
				},
				MajorCallSites: []canopyTOCCallSite{{File: "cmd/main.go", Name: "Start", StartLine: 10, FanIn: &fanIn}},
				Coverage: canopyProjectTOCCoverage{Summary: "available", FlowSeeds: 6, FlowsCollected: 6, CallGraphsComplete: true,
					Notes: []string{"one parse error requires source inspection"}},
			}
			edge := canopyTOCEdge{File: "pkg/session/review.go", Package: "session", Kind: "function_definition",
				Name: "Inspect", Signature: "func Inspect() error", StartLine: 44, Count: 2,
				Resolution: "package", SampleFile: "cmd/main.go", SampleLine: 12}
			for i := 0; i < 6; i++ {
				flow := canopyTOCFlow{
					Root: canopyGraphSymbol{ID: "start", File: "pkg/session/review.go", Package: "session", Kind: "function_definition",
						Name: "Start", Signature: tc.signature, StartLine: 10, EndLine: 15, Callable: true},
					Signal: "fan-in", FanIn: &fanIn, Cyclomatic: &cyclomatic, Cognitive: &cognitive, ReachableNodes: &nodes, GraphEdges: &edges, GraphDirection: "callees", Status: "available",
				}
				for j := 0; j < 6; j++ {
					flow.Callers = append(flow.Callers, edge)
					flow.Callees = append(flow.Callees, edge)
				}
				toc.ImportantFlows = append(toc.ImportantFlows, flow)
			}
			want := toc
			compact, err := json.Marshal(toc)
			if err != nil {
				t.Fatal(err)
			}
			pretty, err := json.MarshalIndent(toc, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if !tc.truncated && (len(compact) > maxCanopyProjectBytes || len(pretty) <= maxCanopyProjectBytes) {
				t.Fatalf("fixture must fit compactly but exceed budget when indented: compact=%d pretty=%d", len(compact), len(pretty))
			}
			encoded, err := marshalCanopyProjectTOC(&toc)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) > maxCanopyProjectBytes {
				t.Fatalf("TOC exceeded budget: %d", len(encoded))
			}
			var got canopyProjectTOC
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if got.Truncated != tc.truncated || len(got.ImportantFlows) != tc.flows {
				t.Fatalf("unexpected trimming: truncated=%v flows=%d", got.Truncated, len(got.ImportantFlows))
			}
			if !tc.truncated && !reflect.DeepEqual(got, want) {
				t.Fatal("compaction changed structural evidence")
			}
			if !reflect.DeepEqual(got.Summary, want.Summary) || !reflect.DeepEqual(got.Coverage, want.Coverage) ||
				!reflect.DeepEqual(got.MajorCallSites, want.MajorCallSites) || got.Source != want.Source {
				t.Fatal("compaction changed summary, coverage warnings, or navigation roots")
			}
			t.Logf("compact=%d pretty=%d emitted=%d retained_flows=%d truncated=%v", len(compact), len(pretty), len(encoded), len(got.ImportantFlows), got.Truncated)
		})
	}
}
