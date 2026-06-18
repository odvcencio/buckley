package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/chatcheck"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

func TestRunDoctorCommandUnknown(t *testing.T) {
	err := runDoctorCommand([]string{"bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown doctor command") {
		t.Fatalf("err=%v want unknown doctor command", err)
	}
}

func TestPrintChatCheckJSON(t *testing.T) {
	var out bytes.Buffer
	result := &chatcheck.Result{
		Name:      "multi-turn-chat",
		Model:     "test-model",
		SessionID: "session-1",
		Passed:    true,
		Turns: []chatcheck.TurnResult{{
			Index:         1,
			Model:         "test-model",
			LatencyMillis: 12,
			CharLength:    32,
			Passed:        true,
			Checks:        []chatcheck.CheckResult{{Name: "contains", Passed: true}},
		}},
	}
	if err := printChatCheckJSON(&out, result); err != nil {
		t.Fatalf("printChatCheckJSON: %v", err)
	}
	var got chatcheck.Result
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json output did not parse: %v\n%s", err, out.String())
	}
	if !got.Passed || got.Turns[0].LatencyMillis != 12 {
		t.Fatalf("unexpected json result: %+v", got)
	}
}

func TestWriteChatCheckReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reports", "chat.json")
	result := &chatcheck.Result{
		Name:   "multi-turn-chat",
		Model:  "test-model",
		Passed: true,
	}
	if err := writeChatCheckReport(path, result); err != nil {
		t.Fatalf("writeChatCheckReport: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var got chatcheck.Result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("report did not parse: %v\n%s", err, string(data))
	}
	if got.Name != "multi-turn-chat" || !got.Passed {
		t.Fatalf("unexpected report: %+v", got)
	}
}

func TestWriteChatCheckJUnitReportSingleFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reports", "chat.xml")
	result := &chatcheck.Result{
		Name:           "single-chat",
		Model:          "test-model",
		SessionID:      "session-1",
		Passed:         false,
		Error:          "missing token",
		DurationMillis: 1234,
		Turns: []chatcheck.TurnResult{{
			Index:         1,
			Err:           "turn failed",
			LatencyMillis: 42,
			CharLength:    12,
			Checks: []chatcheck.CheckResult{{
				Name:    "contains",
				Passed:  false,
				Message: "missing TOKEN",
			}},
		}},
	}
	if err := writeChatCheckJUnitReport(path, result); err != nil {
		t.Fatalf("writeChatCheckJUnitReport: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var got junitTestSuite
	if err := xml.Unmarshal(data, &got); err != nil {
		t.Fatalf("junit report did not parse: %v\n%s", err, string(data))
	}
	if got.Name != "buckley.doctor.chat" || got.Tests != 1 || got.Failures != 1 {
		t.Fatalf("unexpected suite: %+v", got)
	}
	if len(got.TestCases) != 1 || got.TestCases[0].Name != "single-chat" || got.TestCases[0].Failure == nil {
		t.Fatalf("unexpected testcase: %+v", got.TestCases)
	}
	if !strings.Contains(got.TestCases[0].Failure.Text, "missing TOKEN") {
		t.Fatalf("failure text missing check context: %+v", got.TestCases[0].Failure)
	}
}

func TestWriteChatCheckJUnitReportSuite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.xml")
	result := &chatcheck.SuiteResult{
		Name: "chat-suite",
		Results: []chatcheck.Result{
			{Name: "one", Passed: true, DurationMillis: 100},
			{Name: "two", Passed: false, Error: "failed", DurationMillis: 250},
		},
	}
	if err := writeChatCheckJUnitReport(path, result); err != nil {
		t.Fatalf("writeChatCheckJUnitReport: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var got junitTestSuite
	if err := xml.Unmarshal(data, &got); err != nil {
		t.Fatalf("junit report did not parse: %v\n%s", err, string(data))
	}
	if got.Name != "chat-suite" || got.Tests != 2 || got.Failures != 1 || got.Time != "0.350" {
		t.Fatalf("unexpected suite: %+v", got)
	}
}

func TestResolveDoctorChatScenarioFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scenario.json")
	data := `{
  "name": "custom-chat",
  "model": "file-model",
  "timeout": "2s",
  "turns": [
    {"user": "say TOKEN", "want_contains": ["TOKEN"]}
  ]
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	got, err := resolveDoctorChatScenario("default-model", 45*time.Second, path, false, false)
	if err != nil {
		t.Fatalf("resolveDoctorChatScenario: %v", err)
	}
	if got.Name != "custom-chat" || got.Model != "file-model" || got.Timeout != 2*time.Second {
		t.Fatalf("scenario file values not preserved: %+v", got)
	}

	got, err = resolveDoctorChatScenario("override-model", 3*time.Second, path, true, true)
	if err != nil {
		t.Fatalf("resolveDoctorChatScenario override: %v", err)
	}
	if got.Model != "override-model" || got.Timeout != 3*time.Second {
		t.Fatalf("explicit flags should override scenario values: %+v", got)
	}
}

func TestResolveDoctorChatScenariosDirectory(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{
		"b.json": `{"name":"second","model":"file-model","turns":[{"user":"say B"}]}`,
		"a.json": `{"name":"first","turns":[{"user":"say A"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	got, err := resolveDoctorChatScenarios("override-model", 3*time.Second, dir, true, true)
	if err != nil {
		t.Fatalf("resolveDoctorChatScenarios: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("scenarios=%d want 2", len(got))
	}
	if got[0].Name != "first" || got[1].Name != "second" {
		t.Fatalf("unexpected scenario order: %+v", got)
	}
	for _, scenario := range got {
		if scenario.Model != "override-model" || scenario.Timeout != 3*time.Second {
			t.Fatalf("explicit flags should apply to every scenario: %+v", scenario)
		}
	}
}

func TestRunDoctorChatCommandListDoesNotInitDependencies(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "chat.json"), []byte(`{"name":"offline","description":"offline smoke","tags":["smoke","chat"],"turns":[{"user":"say READY","want_contains":["READY"],"want_not_contains":["FAIL"],"want_regex":["READY"],"max_chars":32,"max_tool_calls":0}]}`), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	origInit := initDependenciesFn
	t.Cleanup(func() { initDependenciesFn = origInit })
	called := false
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		called = true
		return nil, nil, nil, errors.New("dependency initialization should not run")
	}

	var runErr error
	out := captureStdout(t, func() {
		runErr = runDoctorChatCommand([]string{"-scenario", dir, "-list", "-tag", "SMOKE", "-name", "offline"})
	})
	if runErr != nil {
		t.Fatalf("runDoctorChatCommand: %v", runErr)
	}
	if called {
		t.Fatal("list mode initialized dependencies")
	}
	for _, want := range []string{"Chat check scenarios: 1", "offline", "tags=chat,smoke", "contains=1", "not_contains=1", "regex=1", "max_chars=1", "max_tool_calls=1", `description="offline smoke"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %s", want, out)
		}
	}
}

func TestRunDoctorChatCommandListRejectsEmptyFilter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "chat.json"), []byte(`{"name":"offline","tags":["smoke"],"turns":[{"user":"say READY"}]}`), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	err := runDoctorChatCommand([]string{"-scenario", dir, "-list", "-tag", "regression"})
	if err == nil || !strings.Contains(err.Error(), "no chat check scenarios matched filters") {
		t.Fatalf("err=%v want empty filter error", err)
	}
}

func TestRunDoctorChatCommandListRejectsJUnit(t *testing.T) {
	err := runDoctorChatCommand([]string{"-list", "-junit", filepath.Join(t.TempDir(), "chat.xml")})
	if err == nil || !strings.Contains(err.Error(), "cannot be used with -list") {
		t.Fatalf("err=%v want list junit conflict", err)
	}
}

func TestPrintDoctorChatScenarioInventoryJSON(t *testing.T) {
	inventory := buildDoctorChatScenarioInventory([]chatcheck.Scenario{
		chatcheck.NormalizeScenario(chatcheck.Scenario{
			Description: "custom smoke",
			Name:        "custom",
			Tags:        []string{"smoke", "chat"},
			Model:       "test-model",
			Timeout:     2 * time.Second,
			MaxTokens:   64,
			Turns: []chatcheck.Turn{{
				User:            "say TOKEN",
				WantContains:    []string{"TOKEN"},
				WantNotContains: []string{"ERROR"},
				WantRegex:       []string{`TOKEN$`},
				MinChars:        5,
				MaxChars:        64,
				MaxToolCalls:    intPtr(0),
			}},
		}),
	})
	var out bytes.Buffer
	if err := printChatCheckJSON(&out, inventory); err != nil {
		t.Fatalf("printChatCheckJSON: %v", err)
	}
	var got doctorChatScenarioInventory
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json output did not parse: %v\n%s", err, out.String())
	}
	if got.ScenarioCount != 1 || got.Scenarios[0].ExpectedMatches != 1 || got.Scenarios[0].MinCharChecks != 1 {
		t.Fatalf("unexpected inventory: %+v", got)
	}
	if got.Scenarios[0].ForbiddenChecks != 1 || got.Scenarios[0].RegexChecks != 1 || got.Scenarios[0].MaxCharChecks != 1 || got.Scenarios[0].MaxToolChecks != 1 {
		t.Fatalf("inventory missing extended assertion counts: %+v", got)
	}
	if got.Scenarios[0].Description != "custom smoke" || strings.Join(got.Scenarios[0].Tags, ",") != "chat,smoke" {
		t.Fatalf("inventory missing metadata: %+v", got)
	}
}

func TestPrintChatCheckSuiteResult(t *testing.T) {
	var out bytes.Buffer
	printChatCheckSuiteResult(&out, &chatcheck.SuiteResult{
		PassedScenarios: 1,
		FailedScenarios: 1,
		Results: []chatcheck.Result{
			{
				Name:           "one",
				Model:          "test-model",
				Passed:         true,
				DurationMillis: 12,
				Turns: []chatcheck.TurnResult{{
					Index:         1,
					Model:         "test-model",
					LatencyMillis: 12,
					CharLength:    8,
					Passed:        true,
				}},
			},
			{
				Name:           "two",
				Model:          "test-model",
				Error:          "missing token",
				DurationMillis: 5,
			},
		},
	})
	got := out.String()
	for _, want := range []string{`scenario "one"`, `scenario "two"`, "missing token", "suite: 1 passed, 1 failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %s", want, got)
		}
	}
}

func TestPrintChatCheckResult(t *testing.T) {
	var out bytes.Buffer
	printChatCheckResult(&out, &chatcheck.Result{
		Turns: []chatcheck.TurnResult{{
			Index:      1,
			Model:      "test-model",
			Latency:    1500 * time.Millisecond,
			CharLength: 32,
			Finish:     "stop",
		}},
	})
	got := out.String()
	for _, want := range []string{"[ok] turn 1", "1.5s", "32 chars", "model=test-model", "finish=stop"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %s", want, got)
		}
	}
}

func intPtr(value int) *int {
	return &value
}
