package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestWriteChatCheckArtifactsSuite(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 18, 1, 2, 3, 4, time.UTC)
	result := &chatcheck.SuiteResult{
		Name:            "chat-suite",
		Passed:          false,
		ScenarioCount:   2,
		PassedScenarios: 1,
		FailedScenarios: 1,
		DurationMillis:  350,
		Results: []chatcheck.Result{
			{
				Name:           "tools/no-tools",
				Passed:         true,
				DurationMillis: 100,
				Turns: []chatcheck.TurnResult{{
					Index:         1,
					User:          "say READY",
					Text:          "READY",
					Model:         "test-model",
					LatencyMillis: 25,
					Finish:        "stop",
					Passed:        true,
					Checks:        []chatcheck.CheckResult{{Name: "contains", Passed: true, Message: `found "READY"`}},
				}},
			},
			{
				Name:           "tools/no-tools",
				Passed:         false,
				Error:          "missing token",
				DurationMillis: 250,
				Turns: []chatcheck.TurnResult{{
					Index:  1,
					User:   "say TOKEN",
					Text:   "wrong",
					Err:    `turn 1 response missing "TOKEN"`,
					Passed: false,
					Checks: []chatcheck.CheckResult{{Name: "contains", Passed: false, Message: `missing "TOKEN"`}},
				}},
			},
		},
	}

	artifactContext := doctorChatArtifactContext{
		WorkDir:      "/repo",
		ScenarioPath: "/repo/.buckley/chatchecks",
		Project:      true,
		ArtifactRoot: root,
		Selector: doctorChatArtifactSelector{
			IDs:          []string{"tools"},
			Tags:         []string{"smoke"},
			NameContains: []string{"no tools"},
		},
		ScenarioCount: 2,
		Scenarios: []doctorChatScenarioSummary{
			{Name: "tools/no-tools", Turns: 1, Model: "test-model"},
			{Name: "tools/needs-token", Turns: 2, Model: "test-model"},
		},
		Git: &doctorChatArtifactGitContext{
			Branch: "main",
			SHA:    "abc123",
		},
	}
	runDir, err := writeChatCheckArtifacts(root, result, now, artifactContext)
	if err != nil {
		t.Fatalf("writeChatCheckArtifacts: %v", err)
	}
	wantDir := filepath.Join(root, "20260618T010203.000000004Z")
	if runDir != wantDir {
		t.Fatalf("runDir=%q want %q", runDir, wantDir)
	}

	reportData, err := os.ReadFile(filepath.Join(runDir, "report.json"))
	if err != nil {
		t.Fatalf("read report artifact: %v", err)
	}
	var gotReport chatcheck.SuiteResult
	if err := json.Unmarshal(reportData, &gotReport); err != nil {
		t.Fatalf("report artifact did not parse: %v\n%s", err, string(reportData))
	}
	if gotReport.Name != "chat-suite" || gotReport.FailedScenarios != 1 {
		t.Fatalf("unexpected report artifact: %+v", gotReport)
	}

	summaryData, err := os.ReadFile(filepath.Join(runDir, "summary.json"))
	if err != nil {
		t.Fatalf("read summary artifact: %v", err)
	}
	var summary doctorChatArtifactsManifest
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		t.Fatalf("summary artifact did not parse: %v\n%s", err, string(summaryData))
	}
	if summary.Report != "report.json" || len(summary.Results) != 2 {
		t.Fatalf("unexpected summary artifact: %+v", summary)
	}
	if summary.Artifacts.Summary != "summary.json" || summary.Artifacts.ResultsDir != "results" || summary.Artifacts.TranscriptsDir != "transcripts" {
		t.Fatalf("unexpected artifact locations: %+v", summary.Artifacts)
	}
	if summary.Context.WorkDir != "/repo" || summary.Context.ScenarioPath != "/repo/.buckley/chatchecks" || !summary.Context.Project {
		t.Fatalf("summary context missing run source: %+v", summary.Context)
	}
	if summary.Context.ArtifactRoot != root || summary.Context.ScenarioCount != 2 || len(summary.Context.Scenarios) != 2 {
		t.Fatalf("summary context missing scenario inventory: %+v", summary.Context)
	}
	if strings.Join(summary.Context.Selector.IDs, ",") != "tools" || strings.Join(summary.Context.Selector.Tags, ",") != "smoke" || strings.Join(summary.Context.Selector.NameContains, ",") != "no tools" {
		t.Fatalf("summary context selector = %+v", summary.Context.Selector)
	}
	if summary.Context.Git == nil || summary.Context.Git.Branch != "main" || summary.Context.Git.SHA != "abc123" {
		t.Fatalf("summary context git = %+v", summary.Context.Git)
	}
	if summary.Results[0].Path != "results/tools/no-tools.json" || summary.Results[1].Path != "results/tools/no-tools-2.json" {
		t.Fatalf("unexpected result artifact paths: %+v", summary.Results)
	}
	if summary.Results[0].Transcript != "transcripts/tools/no-tools.md" || summary.Results[1].Transcript != "transcripts/tools/no-tools-2.md" {
		t.Fatalf("unexpected transcript artifact paths: %+v", summary.Results)
	}
	for _, path := range []string{summary.Results[0].Path, summary.Results[1].Path} {
		if _, err := os.Stat(filepath.Join(runDir, filepath.FromSlash(path))); err != nil {
			t.Fatalf("missing result artifact %s: %v", path, err)
		}
	}
	transcriptPath := filepath.Join(runDir, filepath.FromSlash(summary.Results[1].Transcript))
	transcriptData, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("read transcript artifact: %v", err)
	}
	transcript := string(transcriptData)
	for _, want := range []string{"# Chat Check Transcript: tools/no-tools", "## Turn 1", "say TOKEN", "wrong", `missing "TOKEN"`} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
		}
	}
}

func TestRunDoctorChatRunsCommandListsArtifacts(t *testing.T) {
	root := t.TempDir()
	oldRun, err := writeChatCheckArtifacts(root, &chatcheck.Result{
		Name:           "chat/pass",
		Passed:         true,
		DurationMillis: 12,
		Turns: []chatcheck.TurnResult{{
			Index:  1,
			User:   "say READY",
			Text:   "READY",
			Passed: true,
		}},
	}, time.Date(2026, 6, 17, 1, 2, 3, 0, time.UTC), doctorChatArtifactContext{
		WorkDir:       "/repo",
		ArtifactRoot:  root,
		ScenarioCount: 1,
	})
	if err != nil {
		t.Fatalf("write old run: %v", err)
	}
	newRun, err := writeChatCheckArtifacts(root, &chatcheck.Result{
		Name:           "chat/fail",
		Passed:         false,
		Error:          "missing token",
		DurationMillis: 34,
		Turns: []chatcheck.TurnResult{{
			Index:  1,
			User:   "say TOKEN",
			Text:   "wrong",
			Err:    `missing "TOKEN"`,
			Passed: false,
		}},
	}, time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC), doctorChatArtifactContext{
		WorkDir:       "/repo",
		ArtifactRoot:  root,
		ScenarioCount: 1,
	})
	if err != nil {
		t.Fatalf("write new run: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runDoctorChatRunsCommand([]string{"--path", root}); err != nil {
			t.Fatalf("runDoctorChatRunsCommand: %v", err)
		}
	})
	for _, want := range []string{"Chat check runs: 2", filepath.Base(newRun), "fail, scenarios=1, passed=0, failed=1", "report=" + filepath.Join(newRun, "report.json"), "transcript=" + filepath.Join(newRun, "transcripts", "chat", "fail.md"), filepath.Base(oldRun)} {
		if !strings.Contains(out, want) {
			t.Fatalf("runs output missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, filepath.Base(newRun)) > strings.Index(out, filepath.Base(oldRun)) {
		t.Fatalf("newest run should be listed first:\n%s", out)
	}

	jsonOut := captureStdout(t, func() {
		if err := runDoctorChatRunsCommand([]string{"--path", root, "--limit", "1", "--json"}); err != nil {
			t.Fatalf("runDoctorChatRunsCommand json: %v", err)
		}
	})
	var inventory doctorChatRunsInventory
	if err := json.Unmarshal([]byte(jsonOut), &inventory); err != nil {
		t.Fatalf("unmarshal runs json: %v\n%s", err, jsonOut)
	}
	if inventory.Root != root || inventory.Count != 1 || len(inventory.Runs) != 1 {
		t.Fatalf("unexpected runs inventory: %+v", inventory)
	}
	if inventory.Runs[0].ID != filepath.Base(newRun) || inventory.Runs[0].Passed || inventory.Runs[0].FailedScenarios != 1 {
		t.Fatalf("unexpected run summary: %+v", inventory.Runs[0])
	}

	showOut := captureStdout(t, func() {
		if err := runDoctorChatRunsCommand([]string{"show", "--path", root}); err != nil {
			t.Fatalf("runDoctorChatRunsCommand show: %v", err)
		}
	})
	for _, want := range []string{
		"Chat check run: " + filepath.Base(newRun) + " (fail)",
		"Scenarios: 1 (passed=0 failed=1)",
		"Results:",
		"chat/fail: fail",
		"result=" + filepath.Join(newRun, "results", "chat", "fail.json"),
		"transcript=" + filepath.Join(newRun, "transcripts", "chat", "fail.md"),
	} {
		if !strings.Contains(showOut, want) {
			t.Fatalf("show output missing %q:\n%s", want, showOut)
		}
	}

	transcriptOut := captureStdout(t, func() {
		if err := runDoctorChatRunsCommand([]string{"show", "--path", root, "--transcript", filepath.Base(newRun)}); err != nil {
			t.Fatalf("runDoctorChatRunsCommand show transcript: %v", err)
		}
	})
	for _, want := range []string{"--- Transcript: chat/fail", "# Chat Check Transcript: chat/fail", "say TOKEN", "wrong", `missing "TOKEN"`} {
		if !strings.Contains(transcriptOut, want) {
			t.Fatalf("transcript output missing %q:\n%s", want, transcriptOut)
		}
	}

	jsonShow := captureStdout(t, func() {
		if err := runDoctorCommand([]string{"chat", "artifacts", "show", "--path", root, "--json", filepath.Base(oldRun)}); err != nil {
			t.Fatalf("runDoctorCommand artifacts show json: %v", err)
		}
	})
	var detail doctorChatRunDetail
	if err := json.Unmarshal([]byte(jsonShow), &detail); err != nil {
		t.Fatalf("unmarshal run detail json: %v\n%s", err, jsonShow)
	}
	if detail.Run.ID != filepath.Base(oldRun) || !detail.Run.Passed || detail.Manifest.Context.ArtifactRoot != root || len(detail.Transcripts) != 1 {
		t.Fatalf("unexpected run detail: %+v", detail)
	}
}

func TestRunDoctorChatRunsCommandUsesProjectRoot(t *testing.T) {
	root := t.TempDir()
	runsRoot := filepath.Join(root, ".buckley", "chatchecks", "runs")
	if _, err := writeChatCheckArtifacts(runsRoot, &chatcheck.Result{
		Name:   "project-chat",
		Passed: true,
		Turns: []chatcheck.TurnResult{{
			Index:  1,
			User:   "say READY",
			Text:   "READY",
			Passed: true,
		}},
	}, time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC), doctorChatArtifactContext{
		WorkDir:       root,
		ScenarioPath:  filepath.Join(root, ".buckley", "chatchecks"),
		Project:       true,
		ArtifactRoot:  runsRoot,
		ScenarioCount: 1,
	}); err != nil {
		t.Fatalf("write project run: %v", err)
	}
	nested := filepath.Join(root, "pkg", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	t.Chdir(nested)

	out := captureStdout(t, func() {
		if err := runDoctorCommand([]string{"chat", "runs", "--project"}); err != nil {
			t.Fatalf("runDoctorCommand chat runs project: %v", err)
		}
	})
	for _, want := range []string{"Chat check runs: 1", runsRoot, "project=true", "project-chat"} {
		if !strings.Contains(out, want) {
			t.Fatalf("project runs output missing %q:\n%s", want, out)
		}
	}
}

func TestBuildDoctorChatArtifactContext(t *testing.T) {
	context := buildDoctorChatArtifactContext(
		"/repo",
		"/repo/.buckley/chatchecks",
		"/repo/.buckley/chatchecks/runs",
		true,
		chatcheck.ScenarioSelector{
			IDs:          []string{"chat"},
			Tags:         []string{"smoke"},
			NameContains: []string{"continuity"},
		},
		[]chatcheck.Scenario{
			chatcheck.NormalizeScenario(chatcheck.Scenario{
				Name:      "chat/continuity",
				Tags:      []string{"smoke"},
				Model:     "test-model",
				Timeout:   time.Second,
				MaxTokens: 64,
				Turns: []chatcheck.Turn{{
					User:         "say READY",
					WantContains: []string{"READY"},
				}},
			}),
		},
	)

	if context.WorkDir != "/repo" || !context.Project || context.ArtifactRoot != "/repo/.buckley/chatchecks/runs" {
		t.Fatalf("unexpected context: %+v", context)
	}
	if strings.Join(context.Selector.IDs, ",") != "chat" || strings.Join(context.Selector.Tags, ",") != "smoke" || strings.Join(context.Selector.NameContains, ",") != "continuity" {
		t.Fatalf("unexpected selector: %+v", context.Selector)
	}
	if context.ScenarioCount != 1 || len(context.Scenarios) != 1 || context.Scenarios[0].Name != "chat/continuity" {
		t.Fatalf("unexpected scenarios: %+v", context.Scenarios)
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

func TestRunDoctorChatCommandProjectList(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, ".buckley", "chatchecks")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project scenarios: %v", err)
	}
	nested := filepath.Join(dir, "src", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested project dir: %v", err)
	}
	t.Chdir(nested)
	if err := os.WriteFile(filepath.Join(projectDir, "smoke.json"), []byte(`{"name":"project-smoke","tags":["smoke"],"turns":[{"user":"say READY","want_contains":["READY"]}]}`), 0o644); err != nil {
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
		runErr = runDoctorChatCommand([]string{"-project", "-list", "-tag", "smoke"})
	})
	if runErr != nil {
		t.Fatalf("runDoctorChatCommand: %v", runErr)
	}
	if called {
		t.Fatal("project list mode initialized dependencies")
	}
	for _, want := range []string{"Chat check scenarios: 1", "project-smoke", "tags=smoke"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %s", want, out)
		}
	}
}

func TestRunDoctorChatInitCreatesProjectScenario(t *testing.T) {
	dir := t.TempDir()

	out := captureStdout(t, func() {
		if err := runDoctorCommand([]string{
			"chat", "init",
			"--path", dir,
			"--description", "Project chat smoke.",
			"--tag", "smoke,regression",
			"smoke",
		}); err != nil {
			t.Fatalf("runDoctorCommand chat init: %v", err)
		}
	})
	for _, want := range []string{"Created chat check scenario smoke", ".buckley/chatchecks/", ".buckley/chatchecks/smoke.yaml", "Next: buckley doctor chat -project -list"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor chat init output missing %q:\n%s", want, out)
		}
	}
	scenarioPath := filepath.Join(dir, ".buckley", "chatchecks", "smoke.yaml")
	data, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatalf("read generated scenario: %v", err)
	}
	for _, want := range []string{`description: "Project chat smoke."`, `name: "smoke"`, `model: "xiaomi/mimo-v2.5-pro"`, `BUCKLEY_CHAT_CHECK_ONE`, `BUCKLEY_CHAT_CHECK_TWO`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("generated scenario missing %q:\n%s", want, string(data))
		}
	}

	nested := filepath.Join(dir, "src", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested project dir: %v", err)
	}
	t.Chdir(nested)

	origInit := initDependenciesFn
	t.Cleanup(func() { initDependenciesFn = origInit })
	called := false
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		called = true
		return nil, nil, nil, errors.New("dependency initialization should not run")
	}

	listOut := captureStdout(t, func() {
		if err := runDoctorCommand([]string{"chat", "-project", "-list", "-tag", "regression"}); err != nil {
			t.Fatalf("runDoctorCommand chat project list: %v", err)
		}
	})
	if called {
		t.Fatal("project list mode initialized dependencies")
	}
	for _, want := range []string{"Chat check scenarios: 1", "smoke", "tags=regression,smoke", "contains=3"} {
		if !strings.Contains(listOut, want) {
			t.Fatalf("project list output missing %q:\n%s", want, listOut)
		}
	}

	custom := "custom scenario\n"
	if err := os.WriteFile(scenarioPath, []byte(custom), 0o644); err != nil {
		t.Fatalf("write custom scenario: %v", err)
	}
	if err := runDoctorCommand([]string{"chat", "init", "--path", dir, "--description", "Replacement smoke.", "smoke"}); err != nil {
		t.Fatalf("rerun doctor chat init: %v", err)
	}
	data, err = os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatalf("read preserved scenario: %v", err)
	}
	if string(data) != custom {
		t.Fatalf("doctor chat init should preserve existing scenario, got:\n%s", string(data))
	}
	if err := runDoctorCommand([]string{"chat", "init", "--path", dir, "--force", "--description", "Replacement smoke.", "smoke"}); err != nil {
		t.Fatalf("force doctor chat init: %v", err)
	}
	data, err = os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatalf("read overwritten scenario: %v", err)
	}
	if string(data) == custom || !strings.Contains(string(data), "Replacement smoke.") {
		t.Fatalf("doctor chat init --force should overwrite scenario, got:\n%s", string(data))
	}

	dryDir := filepath.Join(dir, "dry")
	jsonOut := captureStdout(t, func() {
		if err := runDoctorCommand([]string{"chat", "init", "--dry-run", "--json", "--path", dryDir, "chat/memory"}); err != nil {
			t.Fatalf("runDoctorCommand chat init dry-run: %v", err)
		}
	})
	var result doctorChatInitResult
	if err := json.Unmarshal([]byte(jsonOut), &result); err != nil {
		t.Fatalf("unmarshal doctor chat init json: %v\n%s", err, jsonOut)
	}
	if !result.DryRun || result.Name != "chat/memory" || !strings.HasSuffix(result.Path, filepath.Join(".buckley", "chatchecks", "chat", "memory.yaml")) {
		t.Fatalf("unexpected doctor chat init dry-run result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dryDir, ".buckley")); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create .buckley dir, stat err=%v", err)
	}

	if err := runDoctorCommand([]string{"chat", "init", "../escape"}); err == nil {
		t.Fatalf("expected invalid scenario name error")
	}
}

func TestRunDoctorChatCommandProjectListUsesEvalFallback(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "evals", "chat")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir eval scenarios: %v", err)
	}
	nested := filepath.Join(dir, "src", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested project dir: %v", err)
	}
	t.Chdir(nested)
	if err := os.WriteFile(filepath.Join(projectDir, "memory.eval.yaml"), []byte(`
tags:
  - smoke
turns:
  - user: say READY
    want_contains: [READY]
`), 0o644); err != nil {
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
		runErr = runDoctorChatCommand([]string{"-project", "-list", "-tag", "smoke", "chat"})
	})
	if runErr != nil {
		t.Fatalf("runDoctorChatCommand: %v", runErr)
	}
	if called {
		t.Fatal("project list mode initialized dependencies")
	}
	for _, want := range []string{"Chat check scenarios: 1", "chat/memory", "tags=smoke"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %s", want, out)
		}
	}
}

func TestRunEvalCommandListDefaultsToProjectEvals(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "evals", "chat")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir eval scenarios: %v", err)
	}
	nested := filepath.Join(dir, "src", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested project dir: %v", err)
	}
	t.Chdir(nested)
	if err := os.WriteFile(filepath.Join(projectDir, "memory.eval.yaml"), []byte(`
tags:
  - smoke
turns:
  - user: say READY
    want_contains: [READY]
`), 0o644); err != nil {
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
		runErr = runEvalCommand([]string{"list", "--tag", "smoke"})
	})
	if runErr != nil {
		t.Fatalf("runEvalCommand list: %v", runErr)
	}
	if called {
		t.Fatal("eval list initialized dependencies")
	}
	for _, want := range []string{"Chat check scenarios: 1", "chat/memory", "tags=smoke"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %s", want, out)
		}
	}

	jsonOut := captureStdout(t, func() {
		runErr = runEvalCommand([]string{"list", "--json"})
	})
	if runErr != nil {
		t.Fatalf("runEvalCommand list json: %v", runErr)
	}
	var inventory doctorChatScenarioInventory
	if err := json.Unmarshal([]byte(jsonOut), &inventory); err != nil {
		t.Fatalf("unmarshal eval inventory: %v\n%s", err, jsonOut)
	}
	if inventory.ScenarioCount != 1 || inventory.Scenarios[0].Name != "chat/memory" {
		t.Fatalf("unexpected eval inventory: %+v", inventory)
	}
}

func TestRunDoctorChatCommandProjectListFiltersByID(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, ".buckley", "chatchecks")
	if err := os.MkdirAll(filepath.Join(projectDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir project scenarios: %v", err)
	}
	nested := filepath.Join(dir, "src", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested project dir: %v", err)
	}
	t.Chdir(nested)
	for name, body := range map[string]string{
		"smoke.json":          `{"tags":["smoke"],"turns":[{"user":"say READY"}]}`,
		"tools/no-tools.json": `{"tags":["smoke"],"turns":[{"user":"say READY","max_tool_calls":0}]}`,
		"tools/shell.json":    `{"tags":["regression"],"turns":[{"user":"say READY"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(projectDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
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
		runErr = runDoctorChatCommand([]string{"-project", "-list", "-tag", "smoke", "tools"})
	})
	if runErr != nil {
		t.Fatalf("runDoctorChatCommand: %v", runErr)
	}
	if called {
		t.Fatal("project list mode initialized dependencies")
	}
	for _, want := range []string{"Chat check scenarios: 1", "tools/no-tools", "tags=smoke", "max_tool_calls=1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %s", want, out)
		}
	}
	for _, notWant := range []string{"smoke:", "tools/shell"} {
		if strings.Contains(out, notWant) {
			t.Fatalf("output should not contain %q: %s", notWant, out)
		}
	}
}

func TestResolveDoctorChatScenarioPathProject(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if _, err := resolveDoctorChatScenarioPath("custom.json", true); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("err=%v want conflict", err)
	}
	if _, err := resolveDoctorChatScenarioPath("", true); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err=%v want missing project scenarios", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, ".buckley", "chatchecks"), 0o755); err != nil {
		t.Fatalf("mkdir project scenarios: %v", err)
	}
	got, err := resolveDoctorChatScenarioPath("", true)
	if err != nil {
		t.Fatalf("resolveDoctorChatScenarioPath: %v", err)
	}
	want := filepath.Join(dir, ".buckley", "chatchecks")
	if got != want {
		t.Fatalf("path=%q want %q", got, want)
	}
}

func TestResolveDoctorChatScenarioPathProjectPrefersBuckleyChatchecks(t *testing.T) {
	dir := t.TempDir()
	buckleyDir := filepath.Join(dir, ".buckley", "chatchecks")
	evalsDir := filepath.Join(dir, "evals")
	if err := os.MkdirAll(buckleyDir, 0o755); err != nil {
		t.Fatalf("mkdir chatchecks: %v", err)
	}
	if err := os.MkdirAll(evalsDir, 0o755); err != nil {
		t.Fatalf("mkdir evals: %v", err)
	}
	t.Chdir(dir)

	got, err := resolveDoctorChatScenarioPath("", true)
	if err != nil {
		t.Fatalf("resolveDoctorChatScenarioPath: %v", err)
	}
	if got != buckleyDir {
		t.Fatalf("path=%q want %q", got, buckleyDir)
	}
}

func TestResolveDoctorChatScenarioPathProjectFindsAncestor(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, ".buckley", "chatchecks")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project scenarios: %v", err)
	}
	nested := filepath.Join(root, "pkg", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}
	t.Chdir(nested)

	got, err := resolveDoctorChatScenarioPath("", true)
	if err != nil {
		t.Fatalf("resolveDoctorChatScenarioPath: %v", err)
	}
	if got != projectDir {
		t.Fatalf("path=%q want %q", got, projectDir)
	}
}

func TestResolveChatCheckArtifactRoot(t *testing.T) {
	projectScenarios := filepath.Join(t.TempDir(), ".buckley", "chatchecks")
	if got := resolveChatCheckArtifactRoot(defaultChatCheckArtifactRoot, true, projectScenarios, false); got != filepath.Join(projectScenarios, "runs") {
		t.Fatalf("project artifact root=%q", got)
	}
	if got := resolveChatCheckArtifactRoot("custom", true, projectScenarios, true); got != "custom" {
		t.Fatalf("explicit artifact root=%q want custom", got)
	}
	if got := resolveChatCheckArtifactRoot(defaultChatCheckArtifactRoot, false, projectScenarios, false); got != defaultChatCheckArtifactRoot {
		t.Fatalf("non-project artifact root=%q", got)
	}
}

func TestShouldWriteChatCheckArtifacts(t *testing.T) {
	passed := &chatcheck.Result{Passed: true}
	failed := &chatcheck.Result{Passed: false}
	failedSuite := &chatcheck.SuiteResult{Passed: false}

	if !shouldWriteChatCheckArtifacts(passed, true, false) {
		t.Fatal("explicit artifacts should write passed reports")
	}
	if shouldWriteChatCheckArtifacts(passed, false, true) {
		t.Fatal("successful reports should not write automatic failure artifacts")
	}
	if !shouldWriteChatCheckArtifacts(failed, false, true) {
		t.Fatal("failed reports should write automatic failure artifacts")
	}
	if !shouldWriteChatCheckArtifacts(failedSuite, false, true) {
		t.Fatal("failed suite reports should write automatic failure artifacts")
	}
	if shouldWriteChatCheckArtifacts(failed, false, false) {
		t.Fatal("disabled failure artifacts should not write")
	}
	if shouldWriteChatCheckArtifacts(nil, true, true) {
		t.Fatal("nil reports should not write artifacts")
	}
}

func TestRunDoctorChatCommandWritesFailureArtifactsByDefault(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := filepath.Join(dir, "failing-chat.yaml")
	if err := os.WriteFile(scenarioPath, []byte(`
name: failing-chat
model: litellm/test-model
turns:
  - user: say EXPECTED
    want_contains: [EXPECTED]
`), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(model.ChatResponse{
			ID:    "chatcmpl-test",
			Model: "test-model",
			Choices: []model.Choice{{
				Index:        0,
				Message:      model.Message{Role: "assistant", Content: "WRONG ANSWER"},
				FinishReason: "stop",
			}},
			Usage: model.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "litellm"
	cfg.Models.Execution = "litellm/test-model"
	cfg.Models.FallbackChains = map[string][]string{}
	cfg.Providers.OpenRouter.Enabled = false
	cfg.Providers.LiteLLM.Enabled = true
	cfg.Providers.LiteLLM.BaseURL = server.URL
	cfg.Providers.LiteLLM.Models = []string{"test-model"}
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("new model manager: %v", err)
	}
	store, err := storage.New(filepath.Join(dir, "buckley.db"))
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}

	origInit := initDependenciesFn
	t.Cleanup(func() { initDependenciesFn = origInit })
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		return cfg, mgr, store, nil
	}

	artifactsDir := filepath.Join(dir, "runs")
	var runErr error
	out := captureStdout(t, func() {
		runErr = runDoctorChatCommand([]string{"-scenario", scenarioPath, "-artifacts-dir", artifactsDir})
	})
	if runErr == nil || exitCodeForError(runErr) != 1 {
		t.Fatalf("runErr=%v want exit code 1", runErr)
	}
	if requests != 1 {
		t.Fatalf("requests=%d want 1", requests)
	}
	if !strings.Contains(out, "Artifacts: "+artifactsDir) {
		t.Fatalf("output missing artifact path:\n%s", out)
	}

	entries, err := os.ReadDir(artifactsDir)
	if err != nil {
		t.Fatalf("read artifacts dir: %v", err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("unexpected artifact dirs: %+v", entries)
	}
	runDir := filepath.Join(artifactsDir, entries[0].Name())
	for _, rel := range []string{"report.json", "summary.json", filepath.Join("results", "failing-chat.json"), filepath.Join("transcripts", "failing-chat.md")} {
		if _, err := os.Stat(filepath.Join(runDir, rel)); err != nil {
			t.Fatalf("missing artifact %s: %v", rel, err)
		}
	}
	transcriptData, err := os.ReadFile(filepath.Join(runDir, "transcripts", "failing-chat.md"))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	transcript := string(transcriptData)
	for _, want := range []string{"# Chat Check Transcript: failing-chat", "say EXPECTED", "WRONG ANSWER", `missing "EXPECTED"`} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
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

func TestRunDoctorChatCommandListRejectsArtifacts(t *testing.T) {
	err := runDoctorChatCommand([]string{"-list", "-artifacts"})
	if err == nil || !strings.Contains(err.Error(), "cannot be used with -list") {
		t.Fatalf("err=%v want list artifacts conflict", err)
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
