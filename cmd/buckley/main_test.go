package main

import (
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool"
)

func TestParseBoolEnv(t *testing.T) {
	t.Setenv("BUCKLEY_QUIET", "true")
	val, ok := parseBoolEnv("BUCKLEY_QUIET")
	if !ok || !val {
		t.Fatalf("expected true,true got %v,%v", val, ok)
	}

	t.Setenv("BUCKLEY_QUIET", "0")
	val, ok = parseBoolEnv("BUCKLEY_QUIET")
	if !ok || val {
		t.Fatalf("expected false,true got %v,%v", val, ok)
	}

	t.Setenv("BUCKLEY_QUIET", "maybe")
	_, ok = parseBoolEnv("BUCKLEY_QUIET")
	if ok {
		t.Fatalf("expected ok=false for invalid value")
	}
}

func TestParseStartupOptionsFlagsAndFiltering(t *testing.T) {
	t.Setenv("BUCKLEY_QUIET", "1")
	raw := []string{"--encoding=json", "-p", "hello", "--config=proj.yaml", "plan", "feat", "do", "thing"}
	opts, err := parseStartupOptions(raw)
	if err != nil {
		t.Fatalf("parseStartupOptions error: %v", err)
	}
	if !opts.quiet {
		t.Fatalf("expected quiet from env")
	}
	if opts.encodingOverride != "json" {
		t.Fatalf("encodingOverride=%q want json", opts.encodingOverride)
	}
	if opts.prompt != "hello" {
		t.Fatalf("prompt=%q want hello", opts.prompt)
	}
	if opts.configPath != "proj.yaml" {
		t.Fatalf("configPath=%q want proj.yaml", opts.configPath)
	}
	if got := opts.args; len(got) != 4 || got[0] != "plan" {
		t.Fatalf("args=%v want plan feat do thing", got)
	}
}

func TestParseStartupOptionsMissingValues(t *testing.T) {
	_, err := parseStartupOptions([]string{"-p"})
	if err == nil {
		t.Fatalf("expected error for missing -p value")
	}
	_, err = parseStartupOptions([]string{"--encoding"})
	if err == nil {
		t.Fatalf("expected error for missing --encoding value")
	}
	_, err = parseStartupOptions([]string{"--config"})
	if err == nil {
		t.Fatalf("expected error for missing --config value")
	}
}

func TestParseStartupOptionsPlainAndTUIFlags(t *testing.T) {
	opts, err := parseStartupOptions([]string{"--plain", "plan", "feat", "desc"})
	if err != nil {
		t.Fatalf("parseStartupOptions error: %v", err)
	}
	if !opts.plainModeSet || !opts.plainMode {
		t.Fatalf("expected plain mode override true, got set=%v plain=%v", opts.plainModeSet, opts.plainMode)
	}
	if len(opts.args) != 3 || opts.args[0] != "plan" {
		t.Fatalf("expected args without --plain, got %v", opts.args)
	}

	opts, err = parseStartupOptions([]string{"--tui"})
	if err != nil {
		t.Fatalf("parseStartupOptions error: %v", err)
	}
	if !opts.plainModeSet || opts.plainMode {
		t.Fatalf("expected tui override (plain=false), got set=%v plain=%v", opts.plainModeSet, opts.plainMode)
	}
}

func TestConsumeResumeCommand(t *testing.T) {
	opts := &startupOptions{args: []string{"resume", "sess-123"}}
	if err := opts.consumeResumeCommand(); err != nil {
		t.Fatalf("consumeResumeCommand error: %v", err)
	}
	if opts.resumeSessionID != "sess-123" {
		t.Fatalf("resumeSessionID=%q want sess-123", opts.resumeSessionID)
	}
	if len(opts.args) != 0 {
		t.Fatalf("expected args cleared, got %v", opts.args)
	}

	opts = &startupOptions{args: []string{"resume"}}
	if err := opts.consumeResumeCommand(); err == nil {
		t.Fatalf("expected usage error for resume without id")
	}
}

func TestApplySandboxOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	t.Setenv("BUCKLEY_SANDBOX", "off")
	applySandboxOverride(cfg)
	if cfg.Worktrees.UseContainers {
		t.Fatalf("expected UseContainers=false")
	}

	cfg = config.DefaultConfig()
	t.Setenv("BUCKLEY_SANDBOX", "containers")
	applySandboxOverride(cfg)
	if !cfg.Worktrees.UseContainers {
		t.Fatalf("expected UseContainers=true")
	}
}

func TestNetworkHelpers(t *testing.T) {
	if !hasACPTLS(config.ACPConfig{TLSCertFile: "a", TLSKeyFile: "b", TLSClientCAFile: "c"}) {
		t.Fatalf("expected hasACPTLS true")
	}
	if hasACPTLS(config.ACPConfig{TLSCertFile: "a"}) {
		t.Fatalf("expected hasACPTLS false")
	}

	if !isLoopbackAddress("127.0.0.1:4488") {
		t.Fatalf("expected loopback true")
	}
	if isLoopbackAddress("0.0.0.0:4488") {
		t.Fatalf("expected loopback false for wildcard")
	}

	if url := humanReadableURL("0.0.0.0:4488"); url != "http://127.0.0.1:4488" {
		t.Fatalf("humanReadableURL=%q want http://127.0.0.1:4488", url)
	}
	if url := humanReadableURL("127.0.0.1"); url != "http://127.0.0.1" {
		t.Fatalf("humanReadableURL=%q want http://127.0.0.1", url)
	}
}

func TestChooseSecret(t *testing.T) {
	if got := chooseSecret("flag", "cfg"); got != "flag" {
		t.Fatalf("chooseSecret(flag,cfg)=%q want flag", got)
	}
	if got := chooseSecret("", "cfg"); got != "cfg" {
		t.Fatalf("chooseSecret(\"\",cfg)=%q want cfg", got)
	}
}

func TestDebugJSONWritesResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	debugJSON(rec, map[string]any{"ok": true}, 201)
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type=%q want application/json", ct)
	}
	if rec.Code != 201 {
		t.Fatalf("status=%d want 201", rec.Code)
	}
	body := strings.TrimSpace(rec.Body.String())
	if !strings.Contains(body, "\"ok\":true") {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestRunBatchCommandErrors(t *testing.T) {
	if err := runBatchCommand(nil); err == nil {
		t.Fatal("expected usage error for missing batch subcommand")
	}
	if err := runBatchCommand([]string{"nope"}); err == nil {
		t.Fatal("expected error for unknown batch subcommand")
	}
}

func TestIsInteractiveTerminalDoesNotPanic(t *testing.T) {
	_ = isInteractiveTerminal()
}

func TestDispatchSubcommandUnknownCommandHandled(t *testing.T) {
	var handled bool
	var exitCode int
	errOut := captureStderr(t, func() {
		handled, exitCode = dispatchSubcommand([]string{"nope"})
	})
	if !handled || exitCode != 1 {
		t.Fatalf("handled=%v exitCode=%d want true,1", handled, exitCode)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Fatalf("expected unknown command message, got %q", errOut)
	}
}

func TestDispatchSubcommandUnknownFlagHandled(t *testing.T) {
	var handled bool
	var exitCode int
	errOut := captureStderr(t, func() {
		handled, exitCode = dispatchSubcommand([]string{"--nope"})
	})
	if !handled || exitCode != 1 {
		t.Fatalf("handled=%v exitCode=%d want true,1", handled, exitCode)
	}
	if !strings.Contains(errOut, "unknown flag") {
		t.Fatalf("expected unknown flag message, got %q", errOut)
	}
}

func TestRunCommandUsesExitCodeOverrides(t *testing.T) {
	errOut := captureStderr(t, func() {
		code := runCommand(func(_ []string) error {
			return withExitCode(errors.New("bad config"), 2)
		}, nil)
		if code != 2 {
			t.Fatalf("exitCode=%d want 2", code)
		}
	})
	if !strings.Contains(errOut, "bad config") {
		t.Fatalf("expected error output, got %q", errOut)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out)
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestDispatchSubcommandHelpVersionAndBatch(t *testing.T) {
	helpOut := captureStdout(t, func() {
		handled, code := dispatchSubcommand([]string{"--help"})
		if !handled || code != 0 {
			t.Fatalf("help handled=%v code=%d", handled, code)
		}
	})
	if !strings.Contains(helpOut, "Buckley - AI Development Assistant") {
		t.Fatalf("unexpected help output: %q", helpOut)
	}
	if !strings.Contains(helpOut, "commit [--dry-run]") {
		t.Fatalf("expected help to include commit command, got: %q", helpOut)
	}
	if !strings.Contains(helpOut, "pr [--dry-run]") {
		t.Fatalf("expected help to include pr command, got: %q", helpOut)
	}

	versionOut := captureStdout(t, func() {
		handled, code := dispatchSubcommand([]string{"--version"})
		if !handled || code != 0 {
			t.Fatalf("version handled=%v code=%d", handled, code)
		}
	})
	if !strings.Contains(versionOut, "Buckley") {
		t.Fatalf("unexpected version output: %q", versionOut)
	}

	handled, code := dispatchSubcommand([]string{"batch"})
	if !handled || code == 0 {
		t.Fatalf("expected batch to be handled with error code, got handled=%v code=%d", handled, code)
	}
}

type fakeOrchestrator struct {
	planFeatureCalled bool
	featureName       string
	description       string
	loadedPlanID      string
	executedPlan      bool
	executedTaskID    string
	plan              *orchestrator.Plan
}

func (f *fakeOrchestrator) PlanFeature(featureName, description string) (*orchestrator.Plan, error) {
	f.planFeatureCalled = true
	f.featureName = featureName
	f.description = description
	if f.plan == nil {
		f.plan = &orchestrator.Plan{ID: "p1", FeatureName: featureName, CreatedAt: time.Now()}
	}
	return f.plan, nil
}

func (f *fakeOrchestrator) LoadPlan(planID string) (*orchestrator.Plan, error) {
	f.loadedPlanID = planID
	if f.plan == nil {
		f.plan = &orchestrator.Plan{ID: planID, FeatureName: "Feature", CreatedAt: time.Now()}
	}
	return f.plan, nil
}

func (f *fakeOrchestrator) ExecutePlan() error {
	f.executedPlan = true
	return nil
}

func (f *fakeOrchestrator) ExecuteTask(taskID string) error {
	f.executedTaskID = taskID
	return nil
}

func TestRunPlanAndExecuteCommandsViaHarness(t *testing.T) {
	origInit := initDependenciesFn
	origNewOrch := newOrchestratorFn
	t.Cleanup(func() {
		initDependenciesFn = origInit
		newOrchestratorFn = origNewOrch
	})

	tmpDB := filepath.Join(t.TempDir(), "cli.db")
	store, err := storage.New(tmpDB)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		return config.DefaultConfig(), nil, store, nil
	}

	fake := &fakeOrchestrator{}
	newOrchestratorFn = func(store *storage.Store, mgr *model.Manager, registry *tool.Registry, cfg *config.Config, workflow *orchestrator.WorkflowManager, planStore orchestrator.PlanStore) orchestratorRunner {
		return fake
	}

	out := captureStdout(t, func() {
		if err := runPlanCommand([]string{"feat", "do", "thing"}); err != nil {
			t.Fatalf("runPlanCommand: %v", err)
		}
	})
	if !fake.planFeatureCalled || fake.featureName != "feat" {
		t.Fatalf("expected PlanFeature called, got %+v", fake)
	}
	if !strings.Contains(out, "Plan created") {
		t.Fatalf("unexpected plan output: %q", out)
	}

	execOut := captureStdout(t, func() {
		if err := runExecuteCommand([]string{"p1"}); err != nil {
			t.Fatalf("runExecuteCommand: %v", err)
		}
	})
	if fake.loadedPlanID != "p1" || !fake.executedPlan {
		t.Fatalf("expected LoadPlan+ExecutePlan, got %+v", fake)
	}
	if !strings.Contains(execOut, "Plan execution complete") {
		t.Fatalf("unexpected execute output: %q", execOut)
	}
}

func TestRunExecuteTaskCommandHarness(t *testing.T) {
	origInit := initDependenciesFn
	origNewOrch := newOrchestratorFn
	t.Cleanup(func() {
		initDependenciesFn = origInit
		newOrchestratorFn = origNewOrch
	})

	tmpDB := filepath.Join(t.TempDir(), "cli.db")
	store, err := storage.New(tmpDB)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		return config.DefaultConfig(), nil, store, nil
	}

	fake := &fakeOrchestrator{}
	newOrchestratorFn = func(store *storage.Store, mgr *model.Manager, registry *tool.Registry, cfg *config.Config, workflow *orchestrator.WorkflowManager, planStore orchestrator.PlanStore) orchestratorRunner {
		return fake
	}

	if err := runExecuteTaskCommand([]string{"--plan", "p1", "--task", "t1", "--push=false"}); err != nil {
		t.Fatalf("runExecuteTaskCommand: %v", err)
	}
	if fake.loadedPlanID != "p1" || fake.executedTaskID != "t1" {
		t.Fatalf("expected LoadPlan+ExecuteTask, got %+v", fake)
	}
}
