package commands

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
)

func TestGeneratedCommitTemplate(t *testing.T) {
	tests := []struct {
		name   string
		paths  []string
		source []string
		want   string
	}{
		{"one bundle", []string{"client/js/bootstrap-runtime.js"}, nil, "update(client): regenerate bootstrap-runtime.js"},
		{"two bundles", []string{"client/js/b.js", "client/js/a.js"}, nil, "update(client): regenerate a.js and b.js"},
		{"many files", []string{"client/a.js", "client/b.js", "client/c.js"}, nil, "update(client): regenerate 3 generated files"},
		{"mixed dirs no scope", []string{"client/a.js", "web/b.js"}, nil, "update: regenerate a.js and b.js"},
		{"root file no scope", []string{"bundle.js"}, nil, "update: regenerate bundle.js"},
		{"mixed commit gets no template", []string{"client/a.js"}, []string{"cmd/main.go"}, ""},
		{"long names fall back to count", []string{"client/" + strings.Repeat("x", 40) + ".js", "client/" + strings.Repeat("y", 40) + ".js"}, nil, "update(client): regenerate 2 generated files"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stats := oneshot.DiffStats{
				Files:          len(tc.paths) + len(tc.source),
				GeneratedPaths: tc.paths,
				SourcePaths:    tc.source,
			}
			cr := GeneratedCommit(stats)
			if tc.want == "" {
				if cr != nil {
					t.Fatalf("got %q, want no template", cr.Header())
				}
				return
			}
			if cr == nil || cr.Header() != tc.want {
				t.Fatalf("header = %v, want %q", cr, tc.want)
			}
			if err := commitmsg.ValidateCommitFields(cr.Action, cr.Scope, cr.Subject, cr.Body, nil); err != nil {
				t.Fatalf("template fails validation: %v", err)
			}
		})
	}
}

func TestGeneratedCommitNamesTheRegenerationSource(t *testing.T) {
	bundle := []string{"client/js/patch.js", "client/js/patch.js.br", "client/js/patch.js.gz", "client/js/patch.js.map"}
	tests := []struct {
		name       string
		paths, by  []string
		wantHeader string
		wantBody   string
	}{
		{"bundle and its copies from a mapped source", bundle, []string{"client/js/patch.ts"},
			"update(client): regenerate patch.js", "Regenerate from client/js/patch.ts; no hand-written changes."},
		{"tool from a header", []string{"gen/types.go"}, []string{"gentypes"},
			"update(gen): regenerate types.go", "Regenerate from gentypes; no hand-written changes."},
		{"several sources", bundle, []string{"a.ts", "b.ts", "c.ts"},
			"update(client): regenerate patch.js", "Regenerate from a.ts and 2 other sources; no hand-written changes."},
		{"source unknown", bundle, nil,
			"update(client): regenerate patch.js", "Rebuild generated files from their sources; no hand-written changes."},
		{"a lone map keeps its name", []string{"client/js/other.js.map"}, nil,
			"update(client): regenerate other.js.map", "Rebuild generated files from their sources; no hand-written changes."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cr := GeneratedCommit(oneshot.DiffStats{Files: len(tc.paths), GeneratedPaths: tc.paths, GeneratedBy: tc.by})
			if cr == nil {
				t.Fatal("no template for a generated-only change")
			}
			if cr.Header() != tc.wantHeader || len(cr.Body) != 1 || cr.Body[0] != tc.wantBody {
				t.Fatalf("message = %q / %q, want %q / %q", cr.Header(), cr.Body, tc.wantHeader, tc.wantBody)
			}
			if err := commitmsg.ValidateCommitFields(cr.Action, cr.Scope, cr.Subject, cr.Body, nil); err != nil {
				t.Fatalf("template fails validation: %v", err)
			}
		})
	}
}
