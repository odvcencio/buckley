package commands

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
)

func TestCollectCanopyProjectFlow_BindsRootAndCountDirection(t *testing.T) {
	first := canopyGraphSymbol{ID: "first", File: "a.go", Name: "Process", StartLine: 4, Receiver: "First", Signature: "func (First) Process()"}
	second := canopyGraphSymbol{ID: "second", File: "a.go", Name: "Process", StartLine: 10, Receiver: "Second", Signature: "func (Second) Process()"}
	leafA := canopyGraphSymbol{ID: "leaf-a", File: "a.go", Name: "FirstLeaf", StartLine: 6}
	leafB := canopyGraphSymbol{ID: "leaf-b", File: "a.go", Name: "SecondLeaf", StartLine: 12}
	caller := canopyGraphSymbol{ID: "caller", File: "a.go", Name: "Call", StartLine: 15}
	report := canopyGraphReport{
		Roots: []canopyGraphSymbol{first, second, leafA, leafB, caller},
		Edges: []canopyGraphEdge{{Caller: first, Callee: leafA}, {Caller: second, Callee: leafB}, {Caller: caller, Callee: second}},
	}
	for _, root := range report.Roots {
		report.Nodes = append(report.Nodes, canopyGraphNode{ID: root.ID})
	}
	forward, reverse := newCanopyGraphBatch(report, false), newCanopyGraphBatch(report, true)
	conflicting := second
	conflicting.ID = "conflicting"
	conflict := newCanopyGraphBatch(canopyGraphReport{Roots: []canopyGraphSymbol{conflicting}, Nodes: []canopyGraphNode{{ID: conflicting.ID}}}, true)
	for _, tc := range []struct {
		name, symbol, status, direction, rootID string
		line, nodes, edges                      int
		graphs                                  canopyProjectGraphs
		callees, callers                        []string
	}{
		{"second_overload", "Process", "available", "callees", second.ID, 10, 2, 1, canopyProjectGraphs{forward, reverse}, []string{"SecondLeaf:12"}, []string{"Call:15"}},
		{"leaf_zero_outgoing_edges", "SecondLeaf", "available", "callees", leafB.ID, 12, 1, 0, canopyProjectGraphs{forward, reverse}, nil, []string{"Process:10"}},
		{"reverse_only", "Process", "partial", "callers", second.ID, 10, 2, 1, canopyProjectGraphs{nil, reverse}, nil, []string{"Call:15"}},
		{"forward_only", "Process", "partial", "callees", first.ID, 4, 2, 1, canopyProjectGraphs{forward, nil}, []string{"FirstLeaf:6"}, nil},
		{"ambiguous_without_line", "Process", "unavailable", "", "", 0, 0, 0, canopyProjectGraphs{forward, reverse}, nil, nil},
		{"missing_line", "Process", "unavailable", "", "", 99, 0, 0, canopyProjectGraphs{forward, reverse}, nil, nil},
		{"inconsistent_direction_identity", "Process", "unavailable", "", "", 10, 0, 0, canopyProjectGraphs{forward, conflict}, nil, nil},
		{"unique_without_line", "Call", "available", "callees", caller.ID, 0, 3, 2, canopyProjectGraphs{forward, reverse}, []string{"Process:10"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Empty executable makes an uncached direction unavailable without a subprocess.
			flow := collectCanopyProjectFlow(context.Background(), "", "", "", canopyProjectSeed{File: "./a.go", Name: tc.symbol, StartLine: tc.line}, tc.graphs)
			if flow.Status != tc.status || flow.GraphDirection != tc.direction || flow.Root.ID != tc.rootID {
				t.Fatalf("root/status/direction mismatch: %+v", flow)
			}
			if tc.rootID == second.ID && (flow.Root.StartLine != second.StartLine || flow.Root.Signature != second.Signature || flow.Root.Receiver != second.Receiver) {
				t.Fatalf("selected metadata belongs to another overload: %+v", flow.Root)
			}
			if tc.direction == "" {
				if flow.ReachableNodes != nil || flow.GraphEdges != nil {
					t.Fatalf("unavailable graph manufactured numeric evidence: %+v", flow)
				}
			} else if flow.ReachableNodes == nil || *flow.ReachableNodes != tc.nodes || flow.GraphEdges == nil || *flow.GraphEdges != tc.edges {
				t.Fatalf("counts mixed roots or directions: %+v", flow)
			}
			var callees, callers []string
			for _, edge := range flow.Callees {
				callees = append(callees, edge.Name+":"+strconv.Itoa(edge.StartLine))
			}
			for _, edge := range flow.Callers {
				callers = append(callers, edge.Name+":"+strconv.Itoa(edge.StartLine))
			}
			if !reflect.DeepEqual(callees, tc.callees) || !reflect.DeepEqual(callers, tc.callers) {
				t.Fatalf("edges leaked from another overload: callees=%v callers=%v", callees, callers)
			}
			encoded, err := json.Marshal(flow)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"reachable_nodes", "graph_edges", "graph_direction"} {
				if _, present := fields[key]; present != (tc.direction != "") {
					t.Fatalf("graph availability was not preserved in JSON: %s", encoded)
				}
			}
			if tc.direction != "" && tc.edges == 0 && string(fields["graph_edges"]) != "0" {
				t.Fatalf("measured zero edges was omitted: %s", encoded)
			}
		})
	}
}
