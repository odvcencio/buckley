package builtin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func jestReportFixture(statuses ...string) map[string]any {
	assertions := []any{}
	passed, failed, pending, todo := 0, 0, 0, 0
	for _, status := range statuses {
		assertions = append(assertions, map[string]any{"status": status})
		switch status {
		case "passed":
			passed++
		case "failed":
			failed++
		case "todo":
			todo++
		default:
			pending++
		}
	}
	return map[string]any{"success": failed == 0, "wasInterrupted": false, "numPassedTests": passed, "numFailedTests": failed, "numPendingTests": pending, "numTodoTests": todo, "numTotalTests": len(statuses), "testResults": []any{map[string]any{"assertionResults": assertions}}}
}

func TestParseJestReport(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []string
		mutate   func(map[string]any)
		want     testReport
	}{
		{name: "mixed", statuses: []string{"passed", "pending", "todo"}, want: testReport{passed: 1, skipped: 2, complete: true}},
		{name: "empty", want: testReport{complete: true}},
		{name: "failed", statuses: []string{"passed", "failed"}, want: testReport{passed: 1, failed: 1, complete: true, verificationError: "jest reported an unsuccessful test run"}},
		{name: "suite error", statuses: []string{"passed"}, mutate: func(m map[string]any) { m["success"] = false }, want: testReport{passed: 1, complete: true, verificationError: "jest reported an unsuccessful test run"}},
		{name: "missing success", mutate: func(m map[string]any) { delete(m, "success") }},
		{name: "null tests", mutate: func(m map[string]any) { m["testResults"] = nil }},
		{name: "missing assertions", mutate: func(m map[string]any) { m["testResults"] = []any{map[string]any{}} }},
		{name: "interrupted", statuses: []string{"passed"}, mutate: func(m map[string]any) { m["wasInterrupted"] = true }},
		{name: "discovery", statuses: []string{"passed"}, mutate: func(m map[string]any) {
			m["testResults"] = []any{map[string]any{"assertionResults": []any{map[string]any{"status": "passed", "wouldRun": true}}}}
		}},
		{name: "discovery excluded", statuses: []string{"pending"}, mutate: func(m map[string]any) {
			m["testResults"] = []any{map[string]any{"assertionResults": []any{map[string]any{"status": "pending", "wouldRun": false}}}}
		}},
		{name: "null discovery marker", statuses: []string{"passed"}, mutate: func(m map[string]any) {
			m["testResults"] = []any{map[string]any{"assertionResults": []any{map[string]any{"status": "passed", "wouldRun": nil}}}}
		}},
		{name: "wrong total", statuses: []string{"passed"}, mutate: func(m map[string]any) { m["numTotalTests"] = 99 }},
		{name: "wrong counts", statuses: []string{"passed"}, mutate: func(m map[string]any) { m["numPassedTests"] = 0; m["numPendingTests"] = 1 }},
		{name: "negative", mutate: func(m map[string]any) { m["numPassedTests"] = -1 }},
		{name: "fraction", mutate: func(m map[string]any) { m["numPassedTests"] = 0.5 }},
		{name: "unknown status", statuses: []string{"unrecognized"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := jestReportFixture(tc.statuses...)
			if tc.mutate != nil {
				tc.mutate(m)
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if got := parseJestReport(raw); got != tc.want {
				t.Fatalf("got=%+v want=%+v", got, tc.want)
			}
		})
	}
	for _, raw := range []string{`{}`, `null`, `{"success":true`, `{} {}`} {
		if got := parseJestReport([]byte(raw)); got.complete {
			t.Fatalf("accepted malformed report %q: %+v", raw, got)
		}
	}
}

func TestRunTestsTool_JestReport(t *testing.T) {
	for _, tc := range []struct {
		name        string
		statuses    []string
		missing     bool
		wantSuccess bool
	}{
		{name: "mixed", statuses: []string{"passed", "pending"}, wantSuccess: true},
		{name: "failed", statuses: []string{"failed"}},
		{name: "empty"},
		{name: "all skipped", statuses: []string{"pending", "todo"}},
		{name: "missing", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { execCommandContext = exec.CommandContext })
			var reportPath string
			execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				if name != "npm" || len(args) < 5 || args[0] != "test" || args[1] != "--" || args[2] != "--json" || args[3] != "--outputFile" {
					t.Fatalf("bad invocation: %s %v", name, args)
				}
				reportPath = args[4]
				if !tc.missing {
					raw, _ := json.Marshal(jestReportFixture(tc.statuses...))
					if err := os.WriteFile(reportPath, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				return exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$1"`, "test", strings.Repeat("Tests: 999 passed\n", 400))
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := (&RunTestsTool{}).Execute(map[string]any{"path": dir})
			if err != nil || result.Success != tc.wantSuccess {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.Data["passed"] == 999 {
				t.Fatal("console count trusted")
			}
			if _, err := os.Stat(filepath.Dir(reportPath)); !os.IsNotExist(err) {
				t.Fatalf("report leaked: %v", err)
			}
			if !tc.wantSuccess && (result.Error == "" || result.DisplayData["error"] == "") {
				t.Fatal("verification error lost")
			}
		})
	}
}

func TestRunTestsTool_RealJestReport(t *testing.T) {
	for _, name := range []string{"node", "npm", "jest"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " not installed")
		}
	}
	dir := t.TempDir()
	for name, content := range map[string]string{
		"package.json": `{"name":"buckley-jest-report","private":true,"scripts":{"test":"jest --runInBand"}}`,
		"evidence.test.js": `test('passing', () => { console.log('Tests: 999 failed, 999 passed'); expect(2+2).toBe(4); });
test('failing', () => expect(1).toBe(2));
test.skip('skipped', () => {});
test.todo('unfinished');
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tool := &RunTestsTool{}
	tool.SetWorkDir(dir)
	for _, tc := range []struct {
		pattern                 string
		passed, failed, skipped int
		success                 bool
	}{
		{pattern: "passing", passed: 1, skipped: 3, success: true},
		{pattern: "failing", failed: 1, skipped: 3},
		{pattern: "skipped", skipped: 4},
		{pattern: "no_such_test", skipped: 4},
		{passed: 1, failed: 1, skipped: 2},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			result, err := tool.Execute(map[string]any{"path": ".", "pattern": tc.pattern, "timeout_seconds": float64(60)})
			if err != nil || result.Success != tc.success || result.Data["passed"] != tc.passed || result.Data["failed"] != tc.failed || result.Data["skipped"] != tc.skipped {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	t.Run("discovery only", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"buckley-jest-report","private":true,"scripts":{"test":"jest --runInBand --collectTests"}}`), 0600); err != nil {
			t.Fatal(err)
		}
		result, err := tool.Execute(map[string]any{"path": ".", "pattern": "passing", "timeout_seconds": float64(60)})
		if err != nil || result.Success != false || result.Data["passed"] != 0 || result.Error == "" || result.Data["exit_code"] != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}
