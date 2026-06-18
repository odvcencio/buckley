package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/chatcheck"
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
