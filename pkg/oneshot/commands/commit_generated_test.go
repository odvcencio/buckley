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
