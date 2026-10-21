package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestCanopyGraphBatch_SelectSeedPreservesScopeDepthAndOrder(t *testing.T) {
	symbol := func(id, file, name string, line int) canopyGraphSymbol {
		return canopyGraphSymbol{ID: id, File: file, Name: name, StartLine: line, Signature: "func " + name + "()"}
	}
	a := symbol("a", "a.go", "Foo", 1)
	a2 := symbol("a2", "a.go", "Foo", 20)
	b := symbol("b", "b.go", "Foo", 1)
	m := symbol("m", "helper.go", "Middle", 1)
	l := symbol("l", "helper.go", "Leaf", 10)
	deep := symbol("deep", "helper.go", "Deep", 20)
	x := symbol("x", "helper.go", "Other", 30)
	c := symbol("c", "caller.go", "Caller", 1)
	top := symbol("top", "caller.go", "Top", 10)
	report := canopyGraphReport{Roots: []canopyGraphSymbol{a, a2, b}}
	for _, id := range []string{"a", "a2", "b", "m", "l", "deep", "x", "c", "top"} {
		report.Nodes = append(report.Nodes, canopyGraphNode{ID: id})
	}
	report.Edges = []canopyGraphEdge{
		{Caller: a, Callee: m, Count: 3, Resolution: "package", Samples: []canopyGraphSample{{File: "calls.go", StartLine: 7}}},
		{Caller: m, Callee: l}, {Caller: l, Callee: deep}, {Caller: m, Callee: a},
		{Caller: b, Callee: l}, {Caller: a2, Callee: x}, {Caller: x, Callee: l},
		{Caller: c, Callee: a}, {Caller: b, Callee: c}, {Caller: top, Callee: b},
	}
	for _, tc := range []struct {
		name    string
		reverse bool
		nodes   []string
		edges   []string
	}{
		{"forward", false, []string{"a", "a2", "m", "l", "x"}, []string{"a>m", "m>l", "m>a", "a2>x", "x>l"}},
		{"reverse", true, []string{"a", "a2", "b", "m", "c"}, []string{"a>m", "m>a", "c>a", "b>c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			batch := newCanopyGraphBatch(report, tc.reverse)
			got, ok := batch.selectSeed(canopyProjectSeed{Name: "Foo", File: "./a.go", StartLine: 20})
			if !ok || !reflect.DeepEqual(got.Roots, []canopyGraphSymbol{a, a2}) {
				t.Fatalf("same-file overloads or root order lost: %+v", got.Roots)
			}
			var nodes, edges []string
			for _, node := range got.Nodes {
				nodes = append(nodes, node.ID)
			}
			for _, edge := range got.Edges {
				edges = append(edges, edge.Caller.ID+">"+edge.Callee.ID)
			}
			if !reflect.DeepEqual(nodes, tc.nodes) || !reflect.DeepEqual(edges, tc.edges) {
				t.Fatalf("scope/depth/order changed: nodes=%v edges=%v", nodes, edges)
			}
			if !reflect.DeepEqual(got.Edges[0], report.Edges[0]) {
				t.Fatal("edge counts, resolution, or samples changed")
			}
			if _, ok := batch.selectSeed(canopyProjectSeed{Name: "Foo", File: "missing.go"}); ok {
				t.Fatal("same-name roots from another file satisfied a missing seed")
			}
		})
	}
}

func TestNewCanopyGraphBatch_RejectsUnusableIdentityData(t *testing.T) {
	for _, tc := range []struct{ name, data string }{
		{"no_roots", `{"nodes":[{"id":"a"}]}`},
		{"empty_node_id", `{"roots":[{"id":"a"}],"nodes":[{}]}`},
		{"duplicate_node_id", `{"roots":[{"id":"a"}],"nodes":[{"id":"a"},{"id":"a"}]}`},
		{"missing_root_node", `{"roots":[{"id":"a"}],"nodes":[{"id":"b"}]}`},
		{"unknown_caller", `{"roots":[{"id":"a"}],"nodes":[{"id":"a"}],"edges":[{"caller":{"id":"b"},"callee":{"id":"a"}}]}`},
		{"unknown_callee", `{"roots":[{"id":"a"}],"nodes":[{"id":"a"}],"edges":[{"caller":{"id":"a"},"callee":{"id":"b"}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var report canopyGraphReport
			if err := json.Unmarshal([]byte(tc.data), &report); err != nil {
				t.Fatal(err)
			}
			if batch := newCanopyGraphBatch(report, false); batch != nil {
				t.Fatal("unusable batch prevented the per-seed fallback")
			}
		})
	}
	var missing *canopyGraphBatch
	if _, ok := missing.selectSeed(canopyProjectSeed{}); ok {
		t.Fatal("missing batch reported a usable graph")
	}
}

func TestCollectCanopyProjectSummary_BatchesGraphsAndFallsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	root := t.TempDir()
	a := canopyGraphSymbol{ID: "a", File: "a.go", Name: "Entry", StartLine: 1}
	b := canopyGraphSymbol{ID: "b", File: "b.go", Name: "Run", StartLine: 1}
	c := canopyGraphSymbol{ID: "c", File: "c.go", Name: "Helper", StartLine: 1}
	nodes := []canopyGraphNode{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	ab := canopyGraphEdge{Caller: a, Callee: b, Count: 2}
	bc := canopyGraphEdge{Caller: b, Callee: c, Count: 1}
	ca := canopyGraphEdge{Caller: c, Callee: a, Count: 1}
	fixtures := map[string]canopyGraphReport{
		"union":         {Roots: []canopyGraphSymbol{a, b}, Nodes: nodes, Edges: []canopyGraphEdge{ab, bc, ca}},
		"missing_root":  {Roots: []canopyGraphSymbol{a}, Nodes: nodes, Edges: []canopyGraphEdge{ab, bc, ca}},
		"invalid_ids":   {Roots: []canopyGraphSymbol{a, b}, Nodes: nodes[:1], Edges: []canopyGraphEdge{ab, bc, ca}},
		"Entry-forward": {Roots: []canopyGraphSymbol{a}, Nodes: nodes, Edges: []canopyGraphEdge{ab, bc}},
		"Entry-reverse": {Roots: []canopyGraphSymbol{a}, Nodes: nodes, Edges: []canopyGraphEdge{bc, ca}},
		"Run-forward":   {Roots: []canopyGraphSymbol{b}, Nodes: nodes, Edges: []canopyGraphEdge{bc, ca}},
		"Run-reverse":   {Roots: []canopyGraphSymbol{b}, Nodes: nodes, Edges: []canopyGraphEdge{ab, ca}},
	}
	for name, fixture := range fixtures {
		data, err := json.Marshal(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	helper := filepath.Join(root, "canopy")
	script := `#!/bin/sh
case "$1:$2" in
  index:build)
    previous=""
    for arg in "$@"; do
      if [ "$previous" = "--out" ]; then : > "$arg"; fi
      previous="$arg"
    done
    ;;
  analyze:summary)
    printf '%s\n' '{"files":{"total":3},"symbols":{"total":3},"top_fan_in":[{"file":"a.go","name":"Entry","start_line":1,"value":5},{"file":"b.go","name":"Run","start_line":1,"value":4}]}'
    ;;
  graph:calls)
    printf '%s\n' "$3" >> "$CANOPY_BATCH_TEST_LOG"
    direction=forward
    regex=false
    for arg in "$@"; do
      if [ "$arg" = "--reverse" ]; then direction=reverse; fi
      if [ "$arg" = "--regex" ]; then regex=true; fi
    done
    if $regex; then
      case "$CANOPY_BATCH_TEST_MODE" in
        unsupported) exit 64 ;;
        overflow) head -c 4194305 /dev/zero; exit 0 ;;
        missing_root|invalid_ids) cat "$CANOPY_BATCH_TEST_DATA/$CANOPY_BATCH_TEST_MODE.json" ;;
        *) cat "$CANOPY_BATCH_TEST_DATA/union.json" ;;
      esac
    else
      cat "$CANOPY_BATCH_TEST_DATA/$3-$direction.json"
    fi
    ;;
esac
`
	if err := os.WriteFile(helper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANOPY_BIN", helper)
	t.Setenv("CANOPY_BATCH_TEST_DATA", root)
	var expected []byte
	for _, tc := range []struct {
		mode  string
		calls int
	}{
		{"unsupported", 6}, {"supported", 2}, {"missing_root", 4}, {"invalid_ids", 6}, {"overflow", 6},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			log := filepath.Join(root, tc.mode+".log")
			t.Setenv("CANOPY_BATCH_TEST_MODE", tc.mode)
			t.Setenv("CANOPY_BATCH_TEST_LOG", log)
			output, status := collectCanopyProjectSummary(context.Background(), root)
			if status != "available" {
				t.Fatalf("status=%q, want available", status)
			}
			var toc canopyProjectTOC
			if err := json.Unmarshal([]byte(output), &toc); err != nil {
				t.Fatal(err)
			}
			if !toc.Coverage.CallGraphsComplete || toc.Coverage.FlowsCollected != 2 {
				t.Fatalf("coverage=%+v", toc.Coverage)
			}
			for _, flow := range toc.ImportantFlows {
				if flow.ReachableNodes != 3 || flow.GraphEdges != 2 || len(flow.Callers) != 1 || len(flow.Callees) != 1 {
					t.Fatalf("flow scope changed: %+v", flow)
				}
			}
			calls, err := os.ReadFile(log)
			if err != nil || strings.Count(string(calls), "\n") != tc.calls {
				t.Fatalf("graph commands=%s err=%v, want %s", calls, err, strconv.Itoa(tc.calls))
			}
			toc.Source.RuntimeMS = 0
			canonical, err := json.Marshal(toc)
			if err != nil {
				t.Fatal(err)
			}
			if expected == nil {
				expected = canonical
			} else if string(canonical) != string(expected) {
				t.Fatalf("batch/fallback changed structural context:\n%s\n%s", expected, canonical)
			}
		})
	}
}
