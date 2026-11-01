package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
)

// The plan, execute, and execute-task commands build their own registries, so
// each must pass on the operator's permissions.allow_agent_harness choice.
func TestOrchestratorCommands_CarryAllowAgentHarness(t *testing.T) {
	isolateBuckleyHome(t)
	// execute-task enters the repository root. Start in a scratch repository, and
	// let t.Chdir restore the working directory for the tests that follow.
	repo := t.TempDir()
	runGitIn(t, repo, "init", "-q")
	t.Chdir(repo)
	origInit := initDependenciesFn
	origNewOrch := newOrchestratorFn
	t.Cleanup(func() {
		initDependenciesFn = origInit
		newOrchestratorFn = origNewOrch
	})

	for _, allow := range []bool{false, true} {
		store, err := storage.New(filepath.Join(t.TempDir(), "cli.db"))
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })

		cfg := config.DefaultConfig()
		cfg.Permissions.AllowAgentHarness = allow
		initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
			return cfg, nil, store, nil
		}
		var registries []*tool.Registry
		newOrchestratorFn = func(_ *storage.Store, _ *model.Manager, registry *tool.Registry, _ *config.Config, _ *orchestrator.WorkflowManager, _ orchestrator.PlanStore) orchestratorRunner {
			registries = append(registries, registry)
			return &fakeOrchestrator{}
		}

		captureStdout(t, func() {
			if err := runPlanCommand([]string{"feat", "do", "thing"}); err != nil {
				t.Fatalf("runPlanCommand: %v", err)
			}
			if err := runExecuteCommand([]string{"p1"}); err != nil {
				t.Fatalf("runExecuteCommand: %v", err)
			}
			if err := runExecuteTaskCommand([]string{"--plan", "p1", "--task", "t1", "--push=false"}); err != nil {
				t.Fatalf("runExecuteTaskCommand: %v", err)
			}
		})
		if len(registries) != 3 {
			t.Fatalf("allow=%t: built %d registries, want 3", allow, len(registries))
		}
		for i, registry := range registries {
			if got := registry.AllowAgentHarness(); got != allow {
				t.Errorf("command %d: AllowAgentHarness() = %t, want %t", i, got, allow)
			}
		}
	}
}

func TestGetACPSessionState_CarriesAllowAgentHarness(t *testing.T) {
	isolateBuckleyHome(t)
	for _, allow := range []bool{false, true} {
		cfg := config.DefaultConfig()
		cfg.Permissions.AllowAgentHarness = allow
		workDir := t.TempDir()
		var mu sync.Mutex
		sessions := map[string]*acpSessionState{}

		state := getACPSessionState(context.Background(), &mu, sessions, &acp.AgentSession{ID: "session-agent-harness", WorkingDirectory: workDir}, nil, cfg, workDir, nil)
		if state == nil {
			t.Fatal("getACPSessionState returned no state")
		}
		t.Cleanup(func() { _ = state.registry.Close() })
		if got := state.registry.AllowAgentHarness(); got != allow {
			t.Fatalf("ACP registry AllowAgentHarness() = %t, want %t", got, allow)
		}
	}
}
