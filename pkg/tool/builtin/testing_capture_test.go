package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"m31labs.dev/buckley/pkg/encoding/toon"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func parseGoTestOutput(raw string) goTestReport {
	stream := testStream{framework: "go", goReport: &goTestStream{}}
	_, _ = stream.Write([]byte(raw))
	stream.finish()
	return stream.goReport.finish(stream.text.String())
}

func TestTestStream_FailureOutsideExcerpt(t *testing.T) {
	for _, framework := range []string{"jest", "pytest"} {
		t.Run(framework, func(t *testing.T) {
			stream := testStream{framework: framework}
			noise := strings.Repeat("unrelated output\n", 20000)
			for _, part := range []string{noise, "FAIL test_middle\nassertion: expected 4, got 5\n", noise} {
				_, _ = stream.Write([]byte(part))
			}
			stream.finish()
			if strings.Contains(stream.text.String(), "expected 4") {
				t.Fatal("fixture must place failure outside retained console excerpt")
			}
			if !strings.Contains(strings.Join(stream.failures, "\n"), "expected 4, got 5") || len(stream.failures) > 10 {
				t.Fatal("lost or unbounded failure details")
			}
		})
	}
}

func TestRunTestsCaptureProcess(t *testing.T) {
	if os.Getenv("BUCKLEY_CAPTURE_HELPER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-2]
	path := os.Args[len(os.Args)-1]
	noise := strings.Repeat("noisy output ", 128) + "\n"
	switch mode {
	case "go", "go-pass", "go-zero-fail", "go-oversize":
		enc := json.NewEncoder(os.Stdout)
		emit := func(action, test, out string) {
			if err := enc.Encode(goTestEvent{Action: action, Package: "fixture", Test: test, Output: out}); err != nil {
				panic(err)
			}
		}
		emit("start", "", "")
		if mode == "go" {
			emit("output", "TestBroken", "broken_test.go:7: expected 4, got 5\n")
			emit("fail", "TestBroken", "")
		}
		for i := 0; i < 5000; i++ {
			emit("output", "TestPass", noise)
		}
		if mode == "go-oversize" {
			emit("output", "TestPass", strings.Repeat("x", DefaultTestEventBytes+1))
		}
		emit("pass", "TestPass", "")
		if mode == "go" || mode == "go-zero-fail" {
			emit("fail", "", "")
			if mode == "go" {
				os.Exit(1)
			}
		} else {
			emit("pass", "", "")
		}
	case "cargo":
		fmt.Println("test result: ok. 2 passed; 0 failed; 1 ignored")
		for i := 0; i < 5000; i++ {
			fmt.Fprint(os.Stdout, noise)
		}
		fmt.Fprintln(os.Stderr, "test result: ok. 3 passed; 0 failed; 0 ignored")
	case "jest", "pytest":
		report := "<testsuites><testsuite><testcase/></testsuite></testsuites>"
		if mode == "jest" {
			fixture := jestReportFixture("passed")
			wd, err := os.Getwd()
			if err != nil {
				panic(err)
			}
			fixture["testResults"].([]any)[0].(map[string]any)["name"] = filepath.Join(wd, "test.test.js")
			raw, err := json.Marshal(fixture)
			if err != nil {
				panic(err)
			}
			report = string(raw)
		}
		if err := os.WriteFile(path, []byte(report+strings.Repeat(" ", DefaultTestReportBytes)), 0600); err != nil {
			panic(err)
		}
		fmt.Println("Tests complete")
	}
	os.Exit(0)
}
func TestRunTestsTool_BoundedStreamingEvidence(t *testing.T) {
	for _, tc := range []struct {
		mode, framework         string
		success                 bool
		passed, failed, skipped int
	}{
		{"go", "go", false, 1, 1, 0}, {"go-pass", "go", true, 1, 0, 0}, {"go-zero-fail", "go", false, 1, 0, 0}, {"go-oversize", "go", false, 1, 0, 0}, {"cargo", "cargo", true, 5, 0, 1},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			dir := t.TempDir()
			marker := "go.mod"
			if tc.framework == "cargo" {
				marker = "Cargo.toml"
			}
			writeFile(t, filepath.Join(dir, marker), "")
			old := execCommandContext
			t.Cleanup(func() { execCommandContext = old })
			execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunTestsCaptureProcess$", "--", tc.mode, "unused")
				cmd.Env = append(os.Environ(), "BUCKLEY_CAPTURE_HELPER=1")
				return cmd
			}
			tool := &RunTestsTool{}
			tool.SetWorkDir(dir)
			for _, budget := range []int{DefaultTestResultBytes, 6 << 10} {
				tool.SetMaxOutputBytes(budget)
				r, e := tool.Execute(map[string]any{"path": "."})
				if e != nil || r.Success != tc.success || r.Data["passed"] != tc.passed || r.Data["failed"] != tc.failed || r.Data["skipped"] != tc.skipped || r.Data["output_truncated"] != true {
					t.Fatalf("result=%+v err=%v", r, e)
				}
				if tc.mode == "go" && !strings.Contains(strings.Join(r.DisplayData["failures"].([]string), "\n"), "expected 4, got 5") {
					t.Fatal("lost early failing scope evidence")
				}
				for _, format := range []bool{true, false} {
					output, e := toon.New(format).MarshalString(r)
					if e != nil || len(output) > budget {
						t.Fatalf("toon=%v bytes=%d err=%v", format, len(output), e)
					}
				}
			}
		})
	}
}
func TestRunTestsTool_ReportReadBudgetAndCleanup(t *testing.T) {
	for _, framework := range []string{"pytest", "jest"} {
		t.Run(framework, func(t *testing.T) {
			dir := t.TempDir()
			marker := "pytest.ini"
			if framework == "jest" {
				marker = "package.json"
			}
			writeFile(t, filepath.Join(dir, marker), "")
			old := execCommandContext
			t.Cleanup(func() { execCommandContext = old })
			var reportPath string
			execCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
				for i, arg := range args {
					if arg == "--junitxml" || arg == "--outputFile" {
						reportPath = args[i+1]
						break
					}
				}
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunTestsCaptureProcess$", "--", framework, reportPath)
				cmd.Env = append(os.Environ(), "BUCKLEY_CAPTURE_HELPER=1")
				return cmd
			}
			tool := &RunTestsTool{}
			tool.SetWorkDir(dir)
			r, e := tool.Execute(map[string]any{"path": "."})
			if e != nil || r.Success || !strings.Contains(r.Error, "capture byte budget") {
				t.Fatalf("result=%+v err=%v", r, e)
			}
			if _, err := os.Stat(filepath.Dir(reportPath)); !os.IsNotExist(err) {
				t.Fatalf("report directory retained: %v", err)
			}
		})
	}
}
func TestTestText_ChunkingAndUTF8(t *testing.T) {
	var captured testText
	raw := "start\n" + strings.Repeat("界", 20000) + "\nfinish"
	for i := 0; i < len(raw); i += 17 {
		_, _ = captured.Write([]byte(raw[i:min(i+17, len(raw))]))
	}
	text := captured.String()
	if captured.total != int64(len(raw)) || len(captured.head)+len(captured.tail) > DefaultTestCaptureBytes || len(text) > DefaultTestCaptureBytes+64 || !utf8.ValidString(text) || !strings.HasPrefix(text, "start") || !strings.HasSuffix(text, "finish") {
		t.Fatalf("bytes=%d retained=%d valid=%v", captured.total, len(text), utf8.ValidString(text))
	}
}
func TestGoTestStream_StateBudget(t *testing.T) {
	s := testStream{framework: "go", goReport: &goTestStream{}}
	for i := 0; i < 10001; i++ {
		raw, _ := json.Marshal(goTestEvent{Action: "output", Package: fmt.Sprintf("p%d", i), Test: "Test", Output: "test output"})
		_, _ = s.Write(append(raw, '\n'))
	}
	s.finish()
	if s.goReport.problem == "" || len(s.goReport.packages) > 10000 || len(s.goReport.active) > 128 || s.goReport.finish("").complete {
		t.Fatal("unbounded or falsely complete stream")
	}
}
func TestRunTestsTool_ParameterBudgets(t *testing.T) {
	tool := &RunTestsTool{}
	for _, params := range []map[string]any{{"timeout_seconds": 0}, {"timeout_seconds": -1}, {"timeout_seconds": 1.5}, {"timeout_seconds": 3601}, {"pattern": strings.Repeat("x", 8193)}, {"path": strings.Repeat("x", 8193)}} {
		r, e := tool.Execute(params)
		if e != nil || r.Success || r.Error == "" {
			t.Fatalf("result=%+v err=%v", r, e)
		}
	}
}
