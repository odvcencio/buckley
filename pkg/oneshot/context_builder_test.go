package oneshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitIn runs a git command inside dir, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestBuildContextStagedDiffPrioritized reproduces the gosx hallucination
// bug: a huge minified bundle staged alphabetically before a small source
// change must not starve the source change out of the model context.
func TestBuildContextStagedDiffPrioritized(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")

	// Alphabetically-early minified bundle: one 50KB line.
	bundle := "(()=>{" + strings.Repeat("var a=1;", 6_250) + "})();"
	if err := os.WriteFile(filepath.Join(dir, "a_bundle.js"), []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}
	// The real hand-written change, alphabetically last.
	source := "package zz\n\n// RealChange is the actual hand-written edit.\nfunc RealChange() int { return 42 }\n"
	if err := os.WriteFile(filepath.Join(dir, "zz_source.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")

	t.Chdir(dir)

	ctx, err := BuildContext([]ContextSource{
		{Type: "git_diff", Params: map[string]string{"staged": "true"}},
	}, ContextOpts{MaxDiffBytes: 80_000})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}

	diff := ctx.Sources["git_diff:staged"]
	if diff == "" {
		t.Fatalf("no git_diff:staged source gathered; sources: %v", ctx.Sources)
	}
	if !strings.Contains(diff, "RealChange is the actual hand-written edit") {
		t.Errorf("hand-written source change missing from model context:\n%.1500s", diff)
	}
	if strings.Contains(diff, "var a=1;var a=1;") {
		t.Errorf("minified payload leaked into model context")
	}
	if !strings.Contains(diff, "a_bundle.js") {
		t.Errorf("minified file must still be visible as a summary line:\n%.1500s", diff)
	}
	if len(diff) > 80_000 {
		t.Errorf("context length %d exceeds MaxDiffBytes budget", len(diff))
	}
}
