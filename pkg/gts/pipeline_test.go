package gts

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/rules"
)

func TestContextEnrichment_TypesCompile(t *testing.T) {
	// Verify all types are constructible and fields accessible.
	e := &ContextEnrichment{
		Map: []MapResult{
			{
				File: "main.go",
				Symbols: []Symbol{
					{Name: "main", Kind: "func", File: "main.go", Line: 1},
				},
			},
		},
		Scope: &ScopeResult{
			File: "main.go",
			Line: 10,
			InScope: []Symbol{
				{Name: "x", Kind: "var", File: "main.go", Line: 5},
			},
		},
		Callgraph: &CallgraphResult{
			Root: "main",
			Edges: []CallgraphEdge{
				{Caller: "main", Callee: "init", File: "main.go", Line: 1},
			},
			Depth: 2,
		},
		DeadCode: []Symbol{
			{Name: "unused", Kind: "func", File: "util.go", Line: 20},
		},
		Impact: []Symbol{
			{Name: "changed", Kind: "func", File: "api.go", Line: 30},
		},
		Degraded: false,
	}

	if len(e.Map) != 1 {
		t.Errorf("Map len = %d, want 1", len(e.Map))
	}
	if e.Scope.File != "main.go" {
		t.Errorf("Scope.File = %q, want %q", e.Scope.File, "main.go")
	}
	if e.Callgraph.Depth != 2 {
		t.Errorf("Callgraph.Depth = %d, want 2", e.Callgraph.Depth)
	}
	if len(e.DeadCode) != 1 {
		t.Errorf("DeadCode len = %d, want 1", len(e.DeadCode))
	}
	if len(e.Impact) != 1 {
		t.Errorf("Impact len = %d, want 1", len(e.Impact))
	}
}

func TestExtractToolList(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		want   int
	}{
		{
			name:   "nil params",
			params: nil,
			want:   0,
		},
		{
			name:   "no tools key",
			params: map[string]any{"action": "enrich"},
			want:   0,
		},
		{
			name: "any slice",
			params: map[string]any{
				"tools": []any{"callgraph", "dead", "impact"},
			},
			want: 3,
		},
		{
			name: "string slice",
			params: map[string]any{
				"tools": []string{"scope", "context"},
			},
			want: 2,
		},
		{
			name: "wrong type",
			params: map[string]any{
				"tools": "callgraph",
			},
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractToolList(tc.params)
			if len(got) != tc.want {
				t.Errorf("extractToolList() returned %d tools, want %d", len(got), tc.want)
			}
		})
	}
}

func TestPipeline_NewPipeline(t *testing.T) {
	runner := NewRunner()
	// Engine requires compiled .arb files; test construction without eval.
	p := NewPipeline(runner, nil, nil)
	if p == nil {
		t.Fatal("NewPipeline returned nil")
	}
	if p.LastOOM() {
		t.Error("new pipeline should not have lastOOM set")
	}
}

func TestGTSFacts_ArbTags(t *testing.T) {
	// Verify the facts type fields are accessible (compile check).
	facts := rules.GTSFacts{
		TaskType:   "refactor",
		FileCount:  5,
		RepoSizeMB: 100,
		IndexFresh: true,
		LastOOM:    false,
	}
	if facts.TaskType != "refactor" {
		t.Errorf("TaskType = %q, want %q", facts.TaskType, "refactor")
	}
}
