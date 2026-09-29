package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
)

// laneStep is one scripted model reply: a tool call, or final text.
type laneStep struct {
	tool        string
	args        map[string]any
	text        string
	beforeReply func()
}

func editStep(old, new string) laneStep {
	return laneStep{tool: "edit_file", args: map[string]any{"path": "target.txt", "old_string": old, "new_string": new}}
}

func verifyStep(command string) laneStep {
	return laneStep{tool: "run_verification", args: map[string]any{"command": command}}
}

func sayStep(text string) laneStep { return laneStep{text: text} }

type laneRun struct {
	code     int
	stdout   string
	stderr   string
	requests int
	// toolResults holds every tool message the model saw, in order.
	toolResults []string
	// userMessages holds every user message the model saw, in order.
	userMessages []string
}

// runScriptedLane runs one headless mutation lane against a fake model that
// replays script. The lane runs in a fresh git repo holding files.
func runScriptedLane(t *testing.T, files map[string]string, script []laneStep, setup ...func(*testing.T)) laneRun {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("GOWORK", "off")
	t.Setenv("BUCKLEY_SKILLS_PATH", t.TempDir())
	t.Setenv("BUCKLEY_APPROVAL_MODE", "yolo")
	t.Setenv("BUCKLEY_UNSAFE", "1")
	t.Setenv("BUCKLEY_TOOL_SANDBOX_MODE", "disabled")
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	for _, prepare := range setup {
		prepare(t)
	}

	var (
		requests     atomic.Int32
		mu           sync.Mutex
		toolResults  []string
		userMessages []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			fmt.Fprint(w, `{"data":[{"id":"lane-test","supported_parameters":["tools"]}]}`)
			return
		}
		var req model.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		n := int(requests.Add(1))
		mu.Lock()
		toolResults, userMessages = toolResults[:0], userMessages[:0]
		for _, msg := range req.Messages {
			switch msg.Role {
			case "tool":
				toolResults = append(toolResults, model.ExtractTextContentOrEmpty(msg.Content))
			case "user":
				userMessages = append(userMessages, model.ExtractTextContentOrEmpty(msg.Content))
			}
		}
		mu.Unlock()
		if n > len(script) {
			t.Errorf("unexpected request %d; the script has %d steps", n, len(script))
			writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": "Unexpected request"}, "stop")
			return
		}
		step := script[n-1]
		if step.beforeReply != nil {
			step.beforeReply()
		}
		if step.tool == "" {
			writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": step.text}, "stop")
			return
		}
		encoded, _ := json.Marshal(step.args)
		writeOneShotArtifactRouteSSE(t, w, map[string]any{"tool_calls": []map[string]any{{"index": 0, "id": fmt.Sprint(n), "type": "function", "function": map[string]any{"name": step.tool, "arguments": string(encoded)}}}}, "tool_calls")
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	config.ApplyEnvOverridesForTest(cfg)
	cfg.AgentController.EmergencyFuse.ModelRequests = 40
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Providers.OpenAICompatible.Models = []string{"lane-test"}
	cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{"lane-test": {"tools"}}
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	oldQuiet := quietMode
	quietMode = true
	t.Cleanup(func() { quietMode = oldQuiet })

	run := laneRun{code: -1}
	run.stderr = captureStderr(t, func() {
		run.stdout = captureStdout(t, func() {
			run.code = executeOneShotWithTaskIntent("Edit target.txt, then check it.", cfg, mgr, nil, nil, nil, nil, "openai_compatible/lane-test", []string{"edit_file", "run_verification", "run_shell", "run_tests"}, false, agentloop.MutationIntent)
		})
	})
	run.requests = int(requests.Load())
	mu.Lock()
	run.toolResults = append([]string(nil), toolResults...)
	run.userMessages = append([]string(nil), userMessages...)
	mu.Unlock()
	return run
}

const targetMakefile = "check:\n\t@test \"$$(cat target.txt)\" = after\n"

func requireMake(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make unavailable")
	}
}

// A workspace with no build or test command cannot be verified. The lane is
// asked once whether its work is finished, then ends with an explicit outcome,
// instead of looping on a guard that nothing can satisfy.
func TestOneShotLane_NoVerificationSurfaceEndsUnverifiedAfterOneQuestion(t *testing.T) {
	run := runScriptedLane(t, map[string]string{"target.txt": "before\n"}, []laneStep{
		editStep("before", "after"),
		sayStep("Updated target.txt."),
		sayStep("Updated target.txt; nothing could verify it."),
	})
	if run.code != 0 || run.requests != 3 {
		t.Fatalf("code=%d requests=%d stdout=%s stderr=%s", run.code, run.requests, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stderr, `One-shot status: complete (exit=0; stop_reason="completed_unverified: no check applies to this workspace`) {
		t.Fatalf("stderr lacks the explicit outcome:\n%s", run.stderr)
	}
	if strings.Count(run.stderr, "One-shot continuation:") != 1 {
		t.Fatalf("want exactly the one confirmation question:\n%s", run.stderr)
	}
	joined := strings.Join(run.userMessages, "\n")
	if !strings.Contains(joined, "nothing that can verify your change") {
		t.Fatalf("the model was never asked whether its work is finished:\n%s", joined)
	}
	if !strings.Contains(run.stdout, "Updated target.txt; nothing could verify it.") || !strings.Contains(run.stdout, "[Buckley] Completed without verification: no check applies to this workspace") {
		t.Fatalf("stdout must keep the model's confirmed answer and add the unverified note:\n%s", run.stdout)
	}
}

// An accepted check can be pointed at any path, so a runnable test in a hidden
// directory is a surface: the lane is not asked whether it is finished, and it
// verifies with that test.
func TestOneShotLane_RunnableTestInAHiddenDirectoryIsAVerificationSurface(t *testing.T) {
	if err := exec.Command("pytest", "--version").Run(); err != nil {
		t.Skip("pytest is not installed")
	}
	t.Setenv("PYTHONDONTWRITEBYTECODE", "1")
	run := runScriptedLane(t, map[string]string{"target.txt": "before\n", ".checks/test_change.py": "def test_change():\n    assert True\n"}, []laneStep{
		editStep("before", "after"),
		sayStep("Updated target.txt."),
		verifyStep("pytest -p no:cacheprovider .checks/test_change.py"),
		verifyStep("pytest -p no:cacheprovider .checks/test_change.py -q"),
		sayStep("Updated target.txt and the check passes."),
	})
	if run.code != 0 || run.requests != 5 {
		t.Fatalf("code=%d requests=%d stdout=%s stderr=%s", run.code, run.requests, run.stdout, run.stderr)
	}
	if strings.Contains(run.stderr, "completed_unverified") || strings.Contains(run.stdout, "without verification") {
		t.Fatalf("a workspace with a runnable test must not end unverified:\n%s", run.stderr)
	}
	if strings.Contains(strings.Join(run.userMessages, "\n"), "nothing that can verify your change") {
		t.Fatalf("the lane was asked whether it is finished although a check could run:\n%s", strings.Join(run.userMessages, "\n"))
	}
	if !strings.Contains(run.stderr, "passed=true") {
		t.Fatalf("the hidden test never counted as verification:\n%s", run.stderr)
	}
}

// An include hides rules from a scan of the Makefile alone, and make accepts a
// tab after the directive. The lane must treat it as a surface and verify.
func TestOneShotLane_TabSeparatedIncludeThatDefinesACheckIsAVerificationSurface(t *testing.T) {
	requireMake(t)
	run := runScriptedLane(t, map[string]string{
		"target.txt": "before\n",
		"Makefile":   "include\trules.txt\n",
		"rules.txt":  "check:\n\t@test \"$$(cat target.txt)\" = after\n",
	}, []laneStep{
		editStep("before", "after"),
		sayStep("Updated target.txt."),
		verifyStep("make check"),
		sayStep("Updated target.txt and make check passes."),
	})
	if run.code != 0 || run.requests != 4 {
		t.Fatalf("code=%d requests=%d stdout=%s stderr=%s", run.code, run.requests, run.stdout, run.stderr)
	}
	if strings.Contains(run.stderr, "completed_unverified") || !strings.Contains(run.stderr, "passed=true") {
		t.Fatalf("a check the included file defines must count:\n%s", run.stderr)
	}
	if strings.Contains(strings.Join(run.userMessages, "\n"), "nothing that can verify your change") {
		t.Fatal("the lane was asked whether it is finished although make check could run")
	}
}

// A Makefile with no accepted target is not a way to verify anything: the lane
// must take the same one-question path as a workspace with no Makefile at all.
func TestOneShotLane_MakefileWithoutAnAcceptedTargetEndsUnverifiedAfterOneQuestion(t *testing.T) {
	run := runScriptedLane(t, map[string]string{"target.txt": "before\n", "Makefile": "all:\n\t@true\n"}, []laneStep{
		editStep("before", "after"),
		sayStep("Updated target.txt."),
		sayStep("Updated target.txt; nothing could verify it."),
	})
	if run.code != 0 || run.requests != 3 {
		t.Fatalf("code=%d requests=%d stdout=%s stderr=%s", run.code, run.requests, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stderr, `stop_reason="completed_unverified: no check applies to this workspace`) || strings.Count(run.stderr, "One-shot continuation:") != 1 {
		t.Fatalf("stderr lacks the one question and the explicit outcome:\n%s", run.stderr)
	}
}

// Every refused command used to count as a failed test. Now they are recorded
// as checks that never ran, and three in a row end the lane as unverified.
func TestOneShotLane_RefusedChecksEndUnverifiedAfterThree(t *testing.T) {
	run := runScriptedLane(t, map[string]string{"target.txt": "before\n", "Makefile": targetMakefile}, []laneStep{
		editStep("before", "after"),
		verifyStep(`test "$(cat target.txt)" = after`),
		verifyStep("python -m unittest"),
		verifyStep("git diff --check HEAD^ HEAD"),
		sayStep("Updated target.txt."),
	})
	if run.code != 0 || run.requests != 5 {
		t.Fatalf("code=%d requests=%d stdout=%s stderr=%s", run.code, run.requests, run.stdout, run.stderr)
	}
	if strings.Count(run.stderr, "unavailable=") != 3 || strings.Contains(run.stderr, "passed=false") {
		t.Fatalf("refused calls must log as unavailable, never as passed=false:\n%s", run.stderr)
	}
	if !strings.Contains(run.stderr, `stop_reason="completed_unverified: 3 verification attempts in a row could not run`) {
		t.Fatalf("stderr lacks the explicit outcome:\n%s", run.stderr)
	}
	if strings.Contains(run.stderr, "One-shot continuation:") {
		t.Fatalf("the lane continued after three refused checks:\n%s", run.stderr)
	}
	joined := strings.Join(run.toolResults, "\n")
	for _, want := range []string{"nothing was executed", "not a test failure", "is not an accepted check", "Accepted commands start with", "Harness notice: 2 verification attempts in a row did not run"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the model never saw %q in a tool result:\n%s", want, joined)
		}
	}
	if !strings.Contains(run.stdout, "[Buckley] Completed without verification") {
		t.Fatalf("stdout lacks the unverified note:\n%s", run.stdout)
	}
}

// run_tests in a workspace with no test framework is not a failing test.
func TestOneShotLane_RunTestsWithNoFrameworkEndsUnverifiedAfterThree(t *testing.T) {
	requireMake(t)
	run := runScriptedLane(t, map[string]string{"target.txt": "before\n", "Makefile": targetMakefile}, []laneStep{
		editStep("before", "after"),
		{tool: "run_tests", args: map[string]any{}},
		{tool: "run_tests", args: map[string]any{"path": "."}},
		{tool: "run_tests", args: map[string]any{"path": "./"}},
		sayStep("Updated target.txt."),
	})
	if run.code != 0 || run.requests != 5 {
		t.Fatalf("code=%d requests=%d stdout=%s stderr=%s", run.code, run.requests, run.stdout, run.stderr)
	}
	if strings.Count(run.stderr, "tool=run_tests") != 3 || strings.Count(run.stderr, "unavailable=") != 3 || strings.Contains(run.stderr, "passed=false") {
		t.Fatalf("run_tests with no framework must log as unavailable:\n%s", run.stderr)
	}
	if !strings.Contains(run.stderr, `stop_reason="completed_unverified: 3 verification attempts in a row could not run, last reason: no test framework was detected`) {
		t.Fatalf("stderr lacks the explicit outcome:\n%s", run.stderr)
	}
}

// A real failing check keeps its old behavior: the model fixes the code, the
// check passes, and the lane finishes as verified.
func TestOneShotLane_FailingCheckThenFixThenPassFinishesVerified(t *testing.T) {
	requireMake(t)
	run := runScriptedLane(t, map[string]string{"target.txt": "before\n", "Makefile": targetMakefile}, []laneStep{
		editStep("before", "middle"),
		verifyStep("make check"),
		editStep("middle", "after"),
		verifyStep("make check"),
		sayStep("Fixed and verified."),
	})
	if run.code != 0 || run.requests != 5 {
		t.Fatalf("code=%d requests=%d stdout=%s stderr=%s", run.code, run.requests, run.stdout, run.stderr)
	}
	if strings.Count(run.stderr, "passed=false") != 1 || strings.Count(run.stderr, "passed=true") != 1 || strings.Contains(run.stderr, "unavailable=") {
		t.Fatalf("a real failure must log as passed=false and the fix as passed=true:\n%s", run.stderr)
	}
	if strings.Contains(run.stderr, "completed_unverified") || strings.Contains(run.stdout, "without verification") || strings.Contains(run.stderr, "One-shot continuation:") {
		t.Fatalf("a verified lane must not be reported as unverified or continued:\nstdout=%s\nstderr=%s", run.stdout, run.stderr)
	}
}

// The same failing check, re-run with no change to the workspace, must not
// spin until the general continuation limit.
func TestOneShotLane_RepeatedFailureWithNoChangeEndsStalled(t *testing.T) {
	requireMake(t)
	run := runScriptedLane(t, map[string]string{"target.txt": "before\n", "Makefile": targetMakefile}, []laneStep{
		editStep("before", "middle"),
		verifyStep("make check"),
		verifyStep("make check -k"),
		verifyStep("make check -s"),
		verifyStep("make check -j1"),
		sayStep("The check keeps failing."),
	})
	if run.code != 1 || run.requests != 6 {
		t.Fatalf("code=%d requests=%d stdout=%s stderr=%s", run.code, run.requests, run.stdout, run.stderr)
	}
	if strings.Count(run.stderr, "passed=false") != 4 {
		t.Fatalf("want four real failures:\n%s", run.stderr)
	}
	if !strings.Contains(run.stderr, "code=verification_stalled") || !strings.Contains(run.stderr, `verification_stalled: verification failed 4 times in a row with no workspace change between runs`) {
		t.Fatalf("stderr lacks the explicit outcome:\n%s", run.stderr)
	}
	if strings.Contains(run.stderr, "completed_unverified") {
		t.Fatalf("a real failing check must never be reported as unverified:\n%s", run.stderr)
	}
	joined := strings.Join(run.toolResults, "\n")
	if !strings.Contains(joined, "Harness notice: this verification failed 3 times in a row with no workspace change between runs") {
		t.Errorf("the model was not warned before the limit:\n%s", joined)
	}
}

func TestVerificationLimitsApplyOnlyToContinuingRuns(t *testing.T) {
	if got := verificationLimit(acpLoopLimits{}, 3); got != 0 {
		t.Fatalf("a run that does not continue must keep no verification cap, got %d", got)
	}
	if got := verificationLimit(acpLoopLimits{MaxContinuations: 200}, 3); got != 3 {
		t.Fatalf("a continuing run must get the cap, got %d", got)
	}
	contract := acpCompletionContract(acpLoopLimits{TaskIntent: agentloop.MutationIntent, MaxContinuations: 200})
	if contract == nil || contract.MaxVerificationUnavailable != defaultMaxVerificationUnavailable ||
		contract.MaxVerificationStalls != defaultMaxVerificationStalls ||
		contract.MaxVerificationContinuations != defaultMaxVerificationContinuations {
		t.Fatalf("contract = %+v", contract)
	}
}

func TestSummarizeOneShotCommandRedactsAndShortens(t *testing.T) {
	got := summarizeOneShotCommand("FAKE_API_KEY=sk-live-notreal123   pytest\n-x " + strings.Repeat("a", 300))
	if strings.Contains(got, "sk-live-notreal123") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("secret not redacted: %q", got)
	}
	if strings.Contains(got, "\n") || len([]rune(got)) > oneShotSummaryLimit+3 {
		t.Fatalf("summary not one short line: %q", got)
	}
}
