package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
)

func TestPRResultHeaderComposesCommitGrammar(t *testing.T) {
	cases := []struct {
		name string
		pr   PRResult
		want string
	}{
		{"action+scope", PRResult{Action: "fix", Scope: "scene3d", Title: "restore pool shell"}, "fix(scene3d): restore pool shell"},
		{"action only", PRResult{Action: "docs", Title: "refresh README"}, "docs: refresh README"},
		{"no action falls back to raw title", PRResult{Title: "legacy freeform title"}, "legacy freeform title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pr.Header(); got != tc.want {
				t.Fatalf("Header() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPRFormatBodyNeverEmitsCloseDirectives(t *testing.T) {
	pr := PRResult{
		Action:  "fix",
		Title:   "sanitize refs",
		Summary: "This closes #12 by accident in prose.",
		Changes: []string{"Fixes #9 in the parser", "Plain bullet"},
		Testing: []string{"go test ./... (resolves #33)"},
		Issues:  []string{"14", "#15"},
	}
	body := pr.FormatBody()

	for _, banned := range []string{"Closes #", "closes #", "Fixes #", "fixes #", "resolves #"} {
		if strings.Contains(body, banned) {
			t.Fatalf("body contains close directive %q:\n%s", banned, body)
		}
	}
	for _, want := range []string{"Refs #12", "Refs #9", "Refs #33", "- Refs #14", "- Refs #15"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing neutralized reference %q:\n%s", want, body)
		}
	}
}

func TestPRFormatBodyOmitsEmptyTesting(t *testing.T) {
	pr := PRResult{Action: "docs", Title: "readme", Summary: "Docs only.", Changes: []string{"Update README"}}
	body := pr.FormatBody()
	if strings.Contains(body, "## Testing") {
		t.Fatalf("body must omit empty Testing section:\n%s", body)
	}

	pr.Testing = []string{"go test ./..."}
	if body = pr.FormatBody(); !strings.Contains(body, "## Testing") {
		t.Fatalf("body must include Testing when steps exist:\n%s", body)
	}
}

func TestPRBuildPromptUsesConfiguredBase(t *testing.T) {
	ctx := &oneshot.Context{Sources: map[string]string{
		"git_log:develop":   "abc123 fix: thing",
		"git_files:develop": "pkg/x/y.go",
		"git_diff:develop":  "+ real diff content",
		"agents_md":         "project rules",
	}}

	prompt := PRDefinition{BaseBranch: "develop"}.BuildPrompt(ctx)
	for _, want := range []string{"abc123 fix: thing", "pkg/x/y.go", "+ real diff content", "project rules", "base: develop"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt with base=develop missing %q:\n%s", want, prompt)
		}
	}

	// The old implementation hardcoded ":main" lookups and silently dropped
	// all git evidence for non-main bases. Guard against regression: a
	// default-base definition must NOT pick up develop-keyed sources.
	defPrompt := PRDefinition{}.BuildPrompt(ctx)
	if strings.Contains(defPrompt, "real diff content") {
		t.Fatalf("default base prompt must not read develop-keyed sources:\n%s", defPrompt)
	}
}

func TestPRValidateRequiresActionButNotTesting(t *testing.T) {
	valid := func(pr PRResult) error {
		raw, err := json.Marshal(pr)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return PRDefinition{}.Validate(raw)
	}

	ok := PRResult{Action: "docs", Title: "readme", Summary: "s", Changes: []string{"c"}}
	if err := valid(ok); err != nil {
		t.Fatalf("testing must be optional, got error: %v", err)
	}

	noAction := PRResult{Title: "readme", Summary: "s", Changes: []string{"c"}}
	if err := valid(noAction); err == nil {
		t.Fatal("missing action must fail validation")
	}
}
