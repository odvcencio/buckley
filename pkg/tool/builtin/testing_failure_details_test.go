package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTestsAbridgedGoFailureDetails(t *testing.T) {
	event := func(action, pkg, test, output string) string {
		fields := map[string]string{"Action": action, "Test": test, "Output": output}
		if strings.HasPrefix(action, "build-") {
			fields["ImportPath"] = pkg
		} else {
			fields["Package"] = pkg
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw) + "\n"
	}
	noise := strings.Repeat("unrelated successful output\n", 200)
	passed := event("output", "p", "TestPass", noise+"--- FAIL: FAKE_PRINTED_FAILURE\n") + event("pass", "p", "TestPass", "")
	assertion := event("output", "p", "TestBroken", noise+"broken_test.go:7: expected 4, got 5\n") + event("fail", "p", "TestBroken", "") + passed + event("fail", "p", "", "")
	build := event("build-output", "broken", "", noise+"broken.go:9: undefined: missingSymbol\n") + event("build-fail", "broken", "", "") + passed + event("pass", "p", "", "")
	many := ""
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("TestBroken%d", i)
		many += event("output", "p", name, noise+"assertion mismatch\n") + event("fail", "p", name, "")
	}
	many += event("fail", "p", "", "")
	for _, tc := range []struct {
		name, raw, wantDetail, wantTail string
		failed                          int
		omitted                         bool
	}{
		{name: "assertion before unrelated passing output", raw: assertion, wantDetail: "broken_test.go:7: expected 4, got 5", failed: 1},
		{name: "structured compiler diagnostics", raw: build, wantDetail: "broken.go:9: undefined: missingSymbol"},
		{name: "plain compiler diagnostics", raw: noise + "broken.go:9: undefined: missingSymbol\n", wantTail: "broken.go:9: undefined: missingSymbol"},
		{name: "bounded failing scopes", raw: many, wantDetail: "assertion mismatch", failed: 12, omitted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/diagnostics\n"), 0600); err != nil {
				t.Fatal(err)
			}
			original := execCommandContext
			t.Cleanup(func() { execCommandContext = original })
			execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "sh", "-c", "printf '%s' \"$1\"; exit 1", "test", tc.raw)
			}
			tool := &RunTestsTool{}
			tool.SetWorkDir(root)
			result, err := tool.Execute(map[string]any{"path": "."})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success || !result.ShouldAbridge || result.Data["failed"] != tc.failed || result.DisplayData["exit_code"] != 1 {
				t.Fatalf("incorrect verdict: %+v", result)
			}
			details, _ := result.DisplayData["failures"].([]string)
			joined := strings.Join(details, "\n")
			if tc.wantDetail != "" && !strings.Contains(joined, tc.wantDetail) {
				t.Errorf("lost failing-scope diagnostic %q: %s", tc.wantDetail, joined)
			}
			if strings.Contains(joined, "FAKE_PRINTED_FAILURE") {
				t.Error("passing-test text was attributed to a failure")
			}
			if len(joined) > 12000 {
				t.Errorf("unbounded failure details: %d", len(joined))
			}
			if tc.omitted && !strings.Contains(strings.ToLower(joined), "omitted") {
				t.Error("missing scope omission notice")
			}
			tail, _ := result.DisplayData["output_tail"].(string)
			if tail == "" || len(tail) > verificationTailBytes+8 {
				t.Errorf("missing or unbounded output tail: %d", len(tail))
			}
			if tc.wantTail != "" && !strings.Contains(tail, tc.wantTail) {
				t.Errorf("lost compiler error: %s", tail)
			}
		})
	}
}
