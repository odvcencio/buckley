package commands

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/rules"
)

const checkDiff = "diff --git a/a.yaml b/a.yaml\n--- a/a.yaml\n+++ b/a.yaml\n@@\n-owner: zorblax-prod\n+owner: example-prod\n"

func withPolicy(t *testing.T, p commitmsg.Policy) {
	t.Helper()
	prev := commitPolicyLoader
	commitPolicyLoader = func() commitmsg.Policy { return p }
	t.Cleanup(func() { commitPolicyLoader = prev })
}

func TestCheckMessage(t *testing.T) {
	withPolicy(t, commitmsg.Policy{DenyTerms: []string{"quuxcorp"}})
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		msg        string
		stats      oneshot.DiffStats
		engine     *rules.Engine
		wantAction string
		wantSafety int
		wantStyle  int
		skipped    bool
	}{
		{name: "clean", msg: "update(deploy): rename the label\n\n- Use the new label.\n", engine: engine, wantAction: "allow"},
		{name: "removed name", msg: "update(deploy): rename zorblax-prod label\n\n- Use the new label.\n", engine: engine, wantAction: "repair", wantSafety: 1},
		{name: "removed name without engine", msg: "update(deploy): rename zorblax-prod label\n\n- Use the new label.\n", wantAction: "repair", wantSafety: 1},
		{name: "removed name in generated diff", msg: "update: regenerate zorblax bundle\n\n- Rebuild.\n", stats: oneshot.DiffStats{Files: 1, LowSignal: 1}, engine: engine, wantAction: "allow"},
		{name: "deny list in prose paragraph", msg: "update: tune cache\n\nThis helps QuuxCorp a lot.\n", engine: engine, wantAction: "repair", wantSafety: 1},
		{name: "repeated verb is style only", msg: "fix(a): fix cache\n\n- Handle reload.\n", engine: engine, wantAction: "allow", wantStyle: 1},
		{name: "non conventional header has no style checks", msg: "Tune the cache a bit\n", engine: engine, wantAction: "allow"},
		{name: "merge skipped", msg: "Merge branch 'x' into main\n", engine: engine, wantAction: "allow", skipped: true},
		{name: "trailers and comments ignored", msg: "update: tune cache\n\n- Faster.\n\nCo-Authored-By: A <a@example.org>\n# ops@example.org\n", engine: engine, wantAction: "allow"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := CheckMessage(tc.msg, checkDiff, tc.stats, tc.engine)
			if (r.Skipped != "") != tc.skipped {
				t.Fatalf("Skipped = %q", r.Skipped)
			}
			if r.Action != tc.wantAction || len(r.Safety) != tc.wantSafety || len(r.Style) != tc.wantStyle {
				t.Fatalf("report = %+v, want action=%s safety=%d style=%d", r, tc.wantAction, tc.wantSafety, tc.wantStyle)
			}
			for _, line := range append(append([]string{}, r.Safety...), r.Style...) {
				if l := strings.ToLower(line); strings.Contains(l, "zorblax") || strings.Contains(l, "quuxcorp") {
					t.Fatalf("report echoes private text: %q", line)
				}
			}
		})
	}
}

func TestCheckMessageFlagsCapitalAction(t *testing.T) {
	withPolicy(t, commitmsg.Policy{})
	r := CheckMessage("Fix: Tune cache\n\n- Faster.\n", checkDiff, oneshot.DiffStats{}, nil)
	if len(r.Style) < 2 {
		t.Fatalf("style = %v, want a capital action and a capital subject", r.Style)
	}
}
