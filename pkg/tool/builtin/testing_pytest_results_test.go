package builtin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePytestReport(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want testReport
	}{
		{raw: `<testsuites><testsuite><testcase/><testcase><failure>999 passed</failure><error/></testcase><testcase><skipped/></testcase></testsuite><testsuite><testcase/></testsuite></testsuites>`, want: testReport{passed: 2, failed: 1, skipped: 1, complete: true}},
		{raw: `<testsuites><testsuite tests="999" failures="0"><testcase><error/></testcase></testsuite></testsuites>`, want: testReport{failed: 1, complete: true}},
		{raw: `<testsuites><testsuite><system-out>99 passed</system-out></testsuite></testsuites>`, want: testReport{complete: true}},
		{raw: "<testsuites><testsuite/></testsuites>\n<!-- done -->", want: testReport{complete: true}},
		{raw: `<testsuites><testsuite><testcase>`},
		{raw: `<testsuite><testcase/></testsuite>`},
		{raw: `<testsuites/>`},
		{raw: `<testsuites><testsuite/></testsuites><another/>`},
		{raw: `<testsuites><testsuite/></testsuites>junk`},
		{raw: "1 passed in 0.01s"},
	} {
		if got := parsePytestReport([]byte(tc.raw)); got != tc.want {
			t.Errorf("parse(%q)=%+v want=%+v", tc.raw, got, tc.want)
		}
	}
}

func TestRunTestsTool_PytestReport(t *testing.T) {
	for _, tc := range []struct {
		name, report, output    string
		success                 bool
		passed, failed, skipped int
	}{
		{name: "passing despite printed failures", report: `<testsuites><testsuite><testcase/></testsuite></testsuites>`, output: "999 failed", success: true, passed: 1},
		{name: "failure despite printed passes", report: `<testsuites><testsuite><testcase><failure/></testcase></testsuite></testsuites>`, output: "999 passed", failed: 1},
		{name: "all skipped", report: `<testsuites><testsuite><testcase><skipped/></testcase></testsuite></testsuites>`, skipped: 1},
		{name: "empty", report: `<testsuites><testsuite/></testsuites>`},
		{name: "truncated", report: `<testsuites><testsuite><testcase/>`, output: "1 passed"},
		{name: "missing", output: "1 passed"},
		{name: "abridged missing", output: strings.Repeat("999 passed\n", 600)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { execCommandContext = exec.CommandContext })
			var reportPath string
			execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				if name != "pytest" || len(args) < 2 || args[0] != "--junitxml" {
					t.Fatalf("command %s %v", name, args)
				}
				reportPath = args[1]
				if tc.report != "" {
					if err := os.WriteFile(reportPath, []byte(tc.report), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$1"`, "test", tc.output)
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "pytest.ini"), []byte("[pytest]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := (&RunTestsTool{}).Execute(map[string]any{"path": dir})
			if err != nil || result.Success != tc.success || result.Data["passed"] != tc.passed || result.Data["failed"] != tc.failed || result.Data["skipped"] != tc.skipped {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.Data["output"] != tc.output {
				t.Fatal("lost console output")
			}
			if !tc.success && result.Error == "" {
				t.Fatal("missing verification error")
			}
			if _, err := os.Stat(filepath.Dir(reportPath)); !os.IsNotExist(err) {
				t.Fatalf("report directory not cleaned: %v", err)
			}
			if len(tc.output) > 5000 && (!result.ShouldAbridge || result.DisplayData["error"] == "") {
				t.Fatal("abridging hid failure")
			}
		})
	}
}

func TestRunTestsTool_RealPytestReport(t *testing.T) {
	if _, err := exec.LookPath("pytest"); err != nil {
		t.Skip("pytest not installed")
	}
	t.Setenv("PYTEST_ADDOPTS", "-s --color=yes -qq")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pytest.ini"), []byte("[pytest]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := "import pytest\ndef test_pass():\n    print('999 failed')\n    assert True\ndef test_fail():\n    print('999 passed')\n    assert False\n@pytest.mark.skip\ndef test_skip():\n    pass\n@pytest.mark.xfail\ndef test_xfail():\n    assert False\n@pytest.mark.xfail\ndef test_xpass():\n    assert True\n@pytest.fixture\ndef broken():\n    raise RuntimeError('setup')\ndef test_error(broken):\n    pass\n"
	if err := os.WriteFile(filepath.Join(dir, "test_evidence.py"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	tool := &RunTestsTool{}
	tool.SetWorkDir(dir)
	for _, tc := range []struct {
		pattern                 string
		passed, failed, skipped int
		success                 bool
	}{
		{pattern: "test_pass", passed: 1, success: true}, {pattern: "test_fail", failed: 1}, {pattern: "test_skip", skipped: 1}, {pattern: "test_xfail", skipped: 1}, {pattern: "test_xpass", passed: 1, success: true}, {pattern: "test_error", failed: 1}, {pattern: "no_such_test"},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			result, err := tool.Execute(map[string]any{"path": ".", "pattern": tc.pattern, "timeout_seconds": float64(60)})
			if err != nil || result.Success != tc.success || result.Data["passed"] != tc.passed || result.Data["failed"] != tc.failed || result.Data["skipped"] != tc.skipped {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}
