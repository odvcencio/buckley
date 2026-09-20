package rlm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func patchConflictCall(t *testing.T, patch string) model.ToolCall {
	t.Helper()
	arguments, err := json.Marshal(map[string]string{"patch": patch})
	if err != nil {
		t.Fatal(err)
	}
	return model.ToolCall{
		ID: "patch-call", Type: "function",
		Function: model.FunctionCall{Name: "apply_patch", Arguments: string(arguments)},
	}
}

func TestSubAgentExecuteTools_MultiFilePatchRespectsActiveLocks(t *testing.T) {
	requirePatchCommand(t)
	for _, mode := range []string{"read", "write", "exclusive"} {
		t.Run(mode, func(t *testing.T) {
			workDir := t.TempDir()
			for _, name := range []string{"one.txt", "two.txt"} {
				if err := os.WriteFile(filepath.Join(workDir, name), []byte("before\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			patchTool := &builtin.PatchFileTool{}
			patchTool.SetWorkDir(workDir)
			registry := tool.NewEmptyRegistry()
			registry.Register(patchTool)
			detector := NewConflictDetector()
			var err error
			switch mode {
			case "read":
				err = detector.AcquireRead("other-task", "two.txt")
			case "write":
				err = detector.AcquireWrite("other-task", "two.txt")
			case "exclusive":
				err = detector.AcquireExclusive("other-task")
			}
			if err != nil {
				t.Fatal(err)
			}
			patch := "--- one.txt\n+++ one.txt\n@@ -1 +1 @@\n-before\n+after\n" +
				"--- two.txt\n+++ two.txt\n@@ -1 +1 @@\n-before\n+after\n"
			agent := &SubAgent{id: "patcher", conflicts: detector}
			allowed := map[string]struct{}{"apply_patch": {}}
			result := &SubAgentResult{}
			calls, err := agent.executeTools(context.Background(), []model.ToolCall{patchConflictCall(t, patch)}, registry, allowed, result)
			if err != nil {
				t.Fatalf("executeTools: %v", err)
			}
			if len(calls) != 1 || calls[0].Success || len(result.ToolCalls) != 1 || result.ToolCalls[0].Success {
				t.Fatalf("calls=%+v result=%+v, want recorded conflict", calls, result.ToolCalls)
			}
			if !strings.Contains(calls[0].Result, "conflict") || strings.Contains(calls[0].Result, "other-task") {
				t.Fatalf("conflict result = %q, want safe conflict message", calls[0].Result)
			}
			for _, name := range []string{"one.txt", "two.txt"} {
				data, err := os.ReadFile(filepath.Join(workDir, name))
				if err != nil || string(data) != "before\n" {
					t.Fatalf("blocked patch changed %s: data=%q err=%v", name, data, err)
				}
			}

			detector.ReleaseAll("other-task")
			result = &SubAgentResult{}
			calls, err = agent.executeTools(context.Background(), []model.ToolCall{patchConflictCall(t, patch)}, registry, allowed, result)
			if err != nil || len(calls) != 1 || !calls[0].Success {
				t.Fatalf("retry calls=%+v err=%v, want success after release", calls, err)
			}
			for _, name := range []string{"one.txt", "two.txt"} {
				data, err := os.ReadFile(filepath.Join(workDir, name))
				if err != nil || string(data) != "after\n" {
					t.Fatalf("retry %s: data=%q err=%v", name, data, err)
				}
			}
			if err := detector.AcquireWrite("next-task", "one.txt"); err != nil {
				t.Fatalf("successful patch leaked lock: %v", err)
			}
			detector.ReleaseAll("next-task")

			result = &SubAgentResult{}
			calls, err = agent.executeTools(context.Background(), []model.ToolCall{patchConflictCall(t, "not a patch")}, registry, allowed, result)
			if err != nil || len(calls) != 1 || calls[0].Success || strings.Contains(calls[0].Result, "tool conflict") {
				t.Fatalf("invalid patch calls=%+v err=%v, want patch failure", calls, err)
			}
			if err := detector.AcquireWrite("next-task", "two.txt"); err != nil {
				t.Fatalf("failed patch leaked lock: %v", err)
			}
		})
	}
}

func TestSubAgentExecuteTools_ActivePatchBlocksOtherTools(t *testing.T) {
	for _, toolName := range []string{"read_file", "write_file", "run_shell"} {
		t.Run(toolName, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			patchFixture := &conflictExecutionTool{
				name: "apply_patch", startedFirst: make(chan struct{}), releaseFirst: make(chan struct{}),
			}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(patchFixture.releaseFirst) }) }
			t.Cleanup(release)
			otherFixture := &conflictExecutionTool{
				name: toolName, startedFirst: make(chan struct{}), releaseFirst: make(chan struct{}),
			}
			close(otherFixture.releaseFirst)
			registry := tool.NewEmptyRegistry()
			registry.Register(patchFixture)
			registry.Register(otherFixture)
			detector := NewConflictDetector()
			patcher := &SubAgent{id: "patcher", conflicts: detector}
			patchCall := patchConflictCall(t, "--- one.txt\n+++ one.txt\n@@ -1 +1 @@\n-before\n+after\n")
			patchResult := &SubAgentResult{}
			done := make(chan error, 1)
			go func() {
				_, err := patcher.executeTools(ctx, []model.ToolCall{patchCall}, registry, map[string]struct{}{"apply_patch": {}}, patchResult)
				done <- err
			}()
			select {
			case <-patchFixture.startedFirst:
			case <-ctx.Done():
				t.Fatal("patch execution did not start")
			}

			other := &SubAgent{id: "other-task", conflicts: detector}
			allowed := map[string]struct{}{toolName: {}}
			call := conflictToolCall("other-call", toolName, "two.txt")
			result := &SubAgentResult{}
			calls, err := other.executeTools(ctx, []model.ToolCall{call}, registry, allowed, result)
			if err != nil || len(calls) != 1 || calls[0].Success || !strings.Contains(calls[0].Result, "conflict") {
				t.Fatalf("other calls=%+v err=%v, want recorded conflict", calls, err)
			}
			if n, _ := otherFixture.snapshot(); n != 0 {
				t.Fatalf("%s executed %d times while patch active", toolName, n)
			}
			release()
			if err := <-done; err != nil {
				t.Fatalf("patch executeTools: %v", err)
			}
			if len(patchResult.ToolCalls) != 1 || !patchResult.ToolCalls[0].Success {
				t.Fatalf("patch result=%+v, want success", patchResult.ToolCalls)
			}
			result = &SubAgentResult{}
			calls, err = other.executeTools(ctx, []model.ToolCall{call}, registry, allowed, result)
			if err != nil || len(calls) != 1 || !calls[0].Success {
				t.Fatalf("retry calls=%+v err=%v, want success", calls, err)
			}
			if n, _ := otherFixture.snapshot(); n != 1 {
				t.Fatalf("%s calls=%d, want one after release", toolName, n)
			}
		})
	}
}
