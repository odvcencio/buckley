package rlm

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func requirePatchCommand(t *testing.T) {
	t.Helper()
	path, err := exec.LookPath("patch")
	if err != nil {
		t.Skipf("skipping apply_patch permission integration test: patch executable is unavailable: %v", err)
	}
	version, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(version), "GNU patch") {
		t.Skipf("skipping apply_patch permission integration test: GNU patch is unavailable (patch=%q, version=%q, err=%v)", path, strings.TrimSpace(string(version)), err)
	}
}

func TestSubAgentExecuteTools_ApplyPatchHonorsRoleWritePermission(t *testing.T) {
	requirePatchCommand(t)

	tests := []struct {
		name       string
		tier       string
		wantDenied bool
	}{
		{name: "read_only denied", tier: "read_only", wantDenied: true},
		{name: "standard allowed", tier: "standard"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := t.TempDir()
			target := filepath.Join(workDir, "target.txt")
			original := "before\n"
			if err := os.WriteFile(target, []byte(original), 0644); err != nil {
				t.Fatalf("write target: %v", err)
			}

			engine, err := rules.NewEngine()
			if err != nil {
				t.Fatalf("NewEngine: %v", err)
			}
			registry := tool.NewRegistry()
			registry.SetWorkDir(workDir)
			patchTool, ok := registry.Get("apply_patch")
			if !ok {
				t.Fatal("NewRegistry did not register apply_patch")
			}
			if _, ok := patchTool.(*builtin.PatchFileTool); !ok {
				t.Fatalf("apply_patch type = %T, want *builtin.PatchFileTool", patchTool)
			}

			arguments, err := json.Marshal(map[string]string{
				"patch": "--- target.txt\n" +
					"+++ target.txt\n" +
					"@@ -1,1 +1,1 @@\n" +
					"-before\n" +
					"+after\n",
			})
			if err != nil {
				t.Fatalf("marshal patch arguments: %v", err)
			}

			agent := &SubAgent{id: "patch-permission-test", engine: engine, toolTier: tt.tier}
			result := &SubAgentResult{}
			calls, executeErr := agent.executeTools(context.Background(), []model.ToolCall{{
				ID:   "patch-call",
				Type: "function",
				Function: model.FunctionCall{
					Name:      "apply_patch",
					Arguments: string(arguments),
				},
			}}, registry, map[string]struct{}{"apply_patch": {}}, result)

			updated, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("read target: %v", err)
			}
			if tt.wantDenied {
				if executeErr == nil {
					t.Fatalf("executeTools err=nil, calls=%+v result=%+v file=%q; want apply_patch denied", calls, result.ToolCalls, updated)
				}
				if !strings.Contains(executeErr.Error(), "write not permitted") {
					t.Fatalf("executeTools error = %q, want write permission denial", executeErr)
				}
				if len(calls) != 0 || len(result.ToolCalls) != 0 {
					t.Fatalf("denied calls=%+v result=%+v, want no executed tool call", calls, result.ToolCalls)
				}
				if string(updated) != original {
					t.Fatalf("denied apply_patch changed target to %q, want %q", updated, original)
				}
				return
			}

			if executeErr != nil {
				t.Fatalf("executeTools: %v", executeErr)
			}
			if len(calls) != 1 || len(result.ToolCalls) != 1 || !calls[0].Success || !result.ToolCalls[0].Success {
				t.Fatalf("calls=%+v result=%+v, want one successful apply_patch call", calls, result.ToolCalls)
			}
			if string(updated) != "after\n" {
				t.Fatalf("allowed apply_patch produced %q, want %q", updated, "after\n")
			}
		})
	}
}

func TestBatchDispatcherApplyRolePermissions_ApplyPatchMatchesRoleWriteCapability(t *testing.T) {
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	dispatcher := &BatchDispatcher{engine: engine}

	for _, tt := range []struct {
		name      string
		tier      string
		wantPatch bool
		wantRead  bool
	}{
		{name: "read_only denies patch", tier: "read_only", wantRead: true},
		{name: "standard allows patch", tier: "standard", wantPatch: true, wantRead: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			filtered := dispatcher.applyRolePermissions(tt.tier, []string{"apply_patch", "read_file"})
			seen := make(map[string]bool, len(filtered))
			for _, name := range filtered {
				seen[name] = true
			}
			if seen["apply_patch"] != tt.wantPatch {
				t.Fatalf("filtered tools = %v, apply_patch present = %v, want %v", filtered, seen["apply_patch"], tt.wantPatch)
			}
			if seen["read_file"] != tt.wantRead {
				t.Fatalf("filtered tools = %v, read_file present = %v, want %v", filtered, seen["read_file"], tt.wantRead)
			}
		})
	}
}
