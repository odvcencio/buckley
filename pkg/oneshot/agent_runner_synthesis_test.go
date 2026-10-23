package oneshot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
)

func TestAgentRunnerRun_SynthesisAvoidsSnapshotMaterialization(t *testing.T) {
	for _, tc := range []struct {
		name          string
		iterations    int
		requestTool   bool
		wantWorkspace bool
		wantCalls     int32
		wantExecuted  int
	}{
		{"synthesis", 1, false, false, 1, 0},
		{"rejected synthesis tool request", 1, true, false, 1, 0},
		{"inspection retains isolated workspace", 2, true, true, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newAgentRunnerSnapshotFixture(t)
			t.Chdir(repo)
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			snapshot, err := model.CaptureReviewSnapshot(t.Context(), repo, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				var req model.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				workspaces, err := filepath.Glob(filepath.Join(tmp, "buckley-codex-review-*"))
				if err != nil {
					t.Error(err)
				}
				if (len(workspaces) > 0) != tc.wantWorkspace {
					t.Errorf("workspaces=%v, want materialized=%v", workspaces, tc.wantWorkspace)
				}
				if req.Metadata[model.RequestMetadataReadOnly] != "true" || req.Metadata[model.RequestMetadataReviewSnapshot] != snapshot.ID() {
					t.Errorf("lost snapshot policy: %v", req.Metadata)
				}
				if tc.iterations == 1 && len(req.Tools) != 0 {
					t.Errorf("synthesis advertised tools: %v", req.Tools)
				}
				w.Header().Set("Content-Type", "application/json")
				if call == 1 && tc.requestTool {
					fmt.Fprint(w, `{"id":"tool","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"read","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"review.txt\"}"}}]},"finish_reason":"tool_calls"}]}`)
					return
				}
				if tc.wantExecuted > 0 {
					contents := fmt.Sprint(req.Messages)
					if !strings.Contains(contents, "captured snapshot truth") {
						t.Error("inspection lost snapshot read evidence")
					}
				}
				fmt.Fprint(w, `{"id":"final","choices":[{"message":{"role":"assistant","content":"complete review"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			registry := tool.NewRegistry()
			t.Cleanup(func() { _ = registry.Close() })
			runner := NewAgentRunner(AgentRunnerConfig{Models: newAgentRunnerTestManager(t, server), Registry: registry, ModelID: "gpt-4o"})
			result, err := runner.Run(context.Background(), "review system", "review supplied evidence", []string{"read_file", "run_verification", "exec_program"}, AgentExecutionOpts{ReviewSnapshot: snapshot, MaxIterations: tc.iterations})
			if tc.iterations == 1 && tc.requestTool {
				if err == nil || !strings.Contains(err.Error(), "did not offer tools") || result == nil || !result.Incomplete || len(result.ToolCalls) != 0 || calls.Load() != 1 {
					t.Fatalf("tool request must fail closed: result=%+v err=%v calls=%d", result, err, calls.Load())
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if result.Response != "complete review" || result.Incomplete || len(result.ToolCalls) != tc.wantExecuted || calls.Load() != tc.wantCalls {
					t.Fatalf("result=%+v calls=%d", result, calls.Load())
				}
			}
			workspaces, _ := filepath.Glob(filepath.Join(tmp, "buckley-codex-review-*"))
			if len(workspaces) > 0 {
				t.Fatalf("workspace leaked: %v", workspaces)
			}
		})
	}
}
