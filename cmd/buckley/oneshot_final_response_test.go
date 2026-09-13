package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestACPCompletionContract_FinalResponsePredicate(t *testing.T) {
	for _, depth := range []string{"none", "off", "legacy"} {
		t.Run(depth, func(t *testing.T) {
			calls := 0
			contract := acpCompletionContract(acpLoopLimits{
				TaskIntent: agentloop.ReadOnlyIntent, VerificationDepth: depth,
				ValidateFinalResponse: func(text string) error {
					calls++
					if text != "exact response" {
						t.Fatalf("predicate text = %q", text)
					}
					return nil
				},
			})
			if contract == nil || contract.ValidateFinalResponse == nil || contract.RequireObservableChange || contract.RequirePostChangeVerification {
				t.Fatalf("output-only contract = %+v", contract)
			}
			if err := contract.ValidateFinalResponse("exact response"); err != nil || calls != 1 {
				t.Fatalf("predicate not transferred: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestOneShotFinalResponse_BoundedCaptureRepair(t *testing.T) {
	for _, tc := range []struct {
		name        string
		maxRequests int
		maxTools    int
		wantCalls   int32
		wantError   bool
	}{
		{"repair", 4, 2, 3, false},
		{"invalid-repeat", 4, 2, 2, true},
		{"request-cap", 2, 2, 2, true},
		{"invalid-synthesis", 4, 1, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			const content = "package fixture\nfunc NormalizeKey(key string) string { return key }\n"
			if err := os.WriteFile(filepath.Join(dir, "key.go"), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			var feedback atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Tools    []json.RawMessage
					Messages []struct {
						Role    string
						Content json.RawMessage
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
				}
				for _, msg := range req.Messages {
					if msg.Role == "user" && strings.Contains(string(msg.Content), "no captured sources") {
						feedback.Store(true)
					}
				}
				n := calls.Add(1)
				delta := map[string]any{"role": "assistant", "content": `{"artifact":{"kind":"subagent_result","status":"completed","title":"Key","summary":"NormalizeKey returns its input in key.go."},"source_refs":["all"]}`}
				if n == 3 && tc.name == "invalid-synthesis" {
					if len(req.Tools) != 0 {
						t.Error("stopped synthesis still offered tools")
					}
					delta["content"] = `{"artifact":{"kind":"subagent_result","status":"completed","title":"Key","summary":"NormalizeKey returns its input in key.go."},"source_refs":["unknown"]}`
				}
				finish := "stop"
				if n == 2 && tc.name != "invalid-repeat" {
					delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
						"index": 0, "id": "read-key", "type": "function",
						"function": map[string]any{"name": "read_file", "arguments": `{"path":"key.go"}`},
					}}}
					finish = "tool_calls"
				}
				body, err := json.Marshal(map[string]any{"id": "fixture", "model": "gpt-4o", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
				if err != nil {
					t.Errorf("encode response: %v", err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", body)
			}))
			t.Cleanup(server.Close)
			cfg := config.DefaultConfig()
			cfg.Providers.OpenAI.Enabled = true
			cfg.Providers.OpenAI.APIKey = "test-key"
			cfg.Providers.OpenAI.BaseURL = server.URL
			cfg.Models.DefaultProvider = "openai"
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatal(err)
			}
			engine, err := rules.NewDefaultEngine()
			if err != nil {
				t.Fatal(err)
			}
			sink := &builtin.ArtifactSubmission{}
			registry := tool.NewEmptyRegistry()
			registry.Register(&builtin.ReadFileTool{})
			registry.Register(&builtin.SubmitArtifactTool{Submission: sink})
			registry.SetWorkDir(dir)
			registry.SetArtifactSourceCapture(sink)
			contract := artifactv1.OutputContract{Mode: artifactv1.OutputSubmitArtifact}
			conv := conversation.New("capture-repair")
			conv.AddUserMessage("Read key.go and return its captured evidence.")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			text, runErr := runACPLoopWithLimits(ctx, cfg, mgr, conv, registry, nil, engine, "gpt-4o", dir, "capture-repair", nil, func(string, ...interface{}) {}, nil, acpLoopLimits{
				TaskIntent: agentloop.ReadOnlyIntent, VerificationDepth: "none", MaxVerificationAttempts: 1,
				MaxModelRequests: tc.maxRequests, MaxToolCalls: tc.maxTools,
				ValidateFinalResponse: func(response string) error {
					_, err := resolveOneShotArtifact(response, contract, sink)
					return err
				},
			})
			if (runErr != nil) != tc.wantError || calls.Load() != tc.wantCalls || !feedback.Load() {
				t.Fatalf("repair result: calls=%d feedback=%v err=%v", calls.Load(), feedback.Load(), runErr)
			}
			if tc.wantError {
				if _, submitted := sink.Artifact(); submitted {
					t.Fatal("rejected or budget-stopped output finalized the sink")
				}
				if (tc.name == "request-cap" || tc.name == "invalid-synthesis") && len(sink.RecoveryArtifact().EvidenceRefs) != 1 {
					t.Fatal("request ceiling discarded the successful read capture")
				}
				return
			}
			a, err := resolveOneShotArtifact(text, contract, sink)
			if err != nil || len(a.EvidenceRefs) != 1 || len(a.Blocks) != 1 || a.Blocks[0].Table == nil {
				t.Fatalf("final artifact missing trusted capture: %+v err=%v", a, err)
			}
			row := a.Blocks[0].Table.Rows[0]
			if row[1] != filepath.Join(dir, "key.go") || row[2] != "1" || row[3] != "2" || row[4] != content {
				t.Fatalf("capture differs from observed file: %#v", row)
			}
		})
	}
}
