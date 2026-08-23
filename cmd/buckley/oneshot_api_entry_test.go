package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/transparency"
)

type oneshotAPIEntryCalls struct {
	dependenciesAndCatalog int
	modelInfo              int
	invoker                int
}

func installOneshotAPIEntrySpies(t *testing.T) *oneshotAPIEntryCalls {
	t.Helper()
	calls := &oneshotAPIEntryCalls{}
	previousInit := initDependenciesFn
	previousModelInfo := oneshotModelInfoFn
	previousInvoker := newOneshotToolInvokerFn
	t.Cleanup(func() {
		initDependenciesFn = previousInit
		oneshotModelInfoFn = previousModelInfo
		newOneshotToolInvokerFn = previousInvoker
	})
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		calls.dependenciesAndCatalog++
		return nil, nil, nil, errors.New("unexpected dependency initialization")
	}
	oneshotModelInfoFn = func(*model.Manager, string) (*model.ModelInfo, error) {
		calls.modelInfo++
		return nil, errors.New("unexpected model-info lookup")
	}
	newOneshotToolInvokerFn = func(string, string, *config.Config, *model.Manager, transparency.ModelPricing, *transparency.CostLedger) (oneshot.ToolInvoker, error) {
		calls.invoker++
		return nil, errors.New("unexpected invoker construction")
	}
	return calls
}

func (c *oneshotAPIEntryCalls) assertZero(t *testing.T) {
	t.Helper()
	if c.dependenciesAndCatalog != 0 || c.modelInfo != 0 || c.invoker != 0 {
		t.Fatalf("calls = %+v, want rejection before dependencies, catalog initialization, model info, and invoker construction", c)
	}
}

func TestRunCommitCommand_APIEntersDependencyInitialization(t *testing.T) {
	calls := installOneshotAPIEntrySpies(t)
	repo := setupTwoAreaRepo(t)
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	err = runCommitCommand([]string{"-backend", "api", "-dry-run", "-model", "openai/gpt-5.6-luna-pro"})
	if err == nil {
		t.Fatal("expected dependency spy to stop command")
	}
	if !strings.Contains(err.Error(), "init dependencies: unexpected dependency initialization") {
		t.Fatalf("error = %q, want dependency initialization failure", err)
	}
	if calls.dependenciesAndCatalog != 1 || calls.modelInfo != 0 || calls.invoker != 0 {
		t.Fatalf("calls = %+v, want API command to reach dependency initialization once", calls)
	}
}

func TestRunPRCommand_APIFailsClosedBeforeDependencies(t *testing.T) {
	calls := installOneshotAPIEntrySpies(t)
	err := runPRCommand([]string{"-backend", "api"})
	if err == nil {
		t.Fatal("expected PR API backend to fail closed")
	}
	want := "buckley pr API backend unavailable: governed pr model data policy is not installed; use --backend codex or --backend claude"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	calls.assertZero(t)
}
