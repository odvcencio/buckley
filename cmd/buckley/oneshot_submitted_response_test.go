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

func TestACPCompletionContract_SubmittedResponse(t *testing.T) {
	for _, depth := range []string{"none", "off", "legacy"} {
		t.Run(depth, func(t *testing.T) {
			calls := 0
			contract := acpCompletionContract(acpLoopLimits{
				TaskIntent: agentloop.ReadOnlyIntent, VerificationDepth: depth, MaxVerificationAttempts: 2,
				SubmittedResponse: func() (string, bool) { calls++; return "accepted", true },
			})
			if contract == nil || contract.SubmittedResponse == nil || contract.RequireObservableChange || contract.RequirePostChangeVerification || contract.MaxRepairAttempts != 2 {
				t.Fatalf("submission-only contract = %+v", contract)
			}
			if text, ready := contract.SubmittedResponse(); text != "accepted" || !ready || calls != 1 {
				t.Fatalf("reader not transferred: text=%q ready=%v calls=%d", text, ready, calls)
			}
		})
	}
}

func TestOneShotSubmittedResponse_CapturedNativeResult(t *testing.T) {
	for _, name := range []string{"completed", "incomplete", "failed", "title-repair", "reference-repair", "request-cap"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "key.go")
			const content = "package fixture\nfunc NormalizeKey(key string) string { return key }\n"
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			var sawCapture, sawRejection atomic.Bool
			// Preserve the existing last-request reservation for final synthesis.
			wantCalls, maxRequests, maxTools := int32(2), 3, 2
			if name == "title-repair" || name == "reference-repair" {
				wantCalls, maxRequests, maxTools = 3, 4, 3
			}
			if name == "request-cap" {
				wantCalls, maxTools = 3, 6
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []struct {
						Role    string
						Content json.RawMessage
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode request: %v", err)
				}
				for _, message := range request.Messages {
					if message.Role == "tool" {
						var text string
						_ = json.Unmarshal(message.Content, &text)
						if len(text) > 0 {
							// Inspect feedback without accepting model-provided evidence.
							if strings.Contains(text, "NormalizeKey") {
								sawCapture.Store(true)
							}
							if strings.Contains(text, "success: false") {
								sawRejection.Store(true)
							}
						}
					}
				}
				n := calls.Add(1)
				if n > wantCalls {
					t.Errorf("requested model again after accepted submission: request %d", n)
				}
				function, args := "read_file", `{"path":"key.go","line_numbers":false}`
				if n > 1 {
					function = "submit_artifact"
					status := "completed"
					if name == "incomplete" || name == "failed" {
						status = name
					}
					artifact := map[string]any{"kind": "subagent_result", "status": status, "title": "Key", "summary": "Model-written summary"}
					if status == "incomplete" {
						artifact["incomplete_reasons"] = []string{"additional context missing"}
					}
					refs := []string{"all"}
					if n == 2 && (name == "title-repair" || name == "request-cap") {
						delete(artifact, "title")
					}
					if n == 2 && name == "reference-repair" {
						refs = []string{"unobserved-source"}
					}
					body, err := json.Marshal(map[string]any{"artifact": artifact, "source_refs": refs})
					if err != nil {
						t.Errorf("encode submission: %v", err)
					}
					args = string(body)
					// A reader must render the original accepted capture, not reread this replacement.
					if err := os.WriteFile(path, []byte("replacement after captured read\n"), 0600); err != nil {
						t.Errorf("replace fixture: %v", err)
					}
				}
				body, err := json.Marshal(map[string]any{"id": "fixture", "model": "gpt-4o", "choices": []any{map[string]any{
					"index": 0, "finish_reason": "tool_calls", "delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
						"index": 0, "id": fmt.Sprintf("call-%d", n), "type": "function", "function": map[string]any{"name": function, "arguments": args},
					}}},
				}}})
				if err != nil {
					t.Errorf("encode response: %v", err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", body)
			}))
			t.Cleanup(server.Close)
			cfg := config.DefaultConfig()
			cfg.Providers.OpenAI.Enabled, cfg.Providers.OpenAI.APIKey, cfg.Providers.OpenAI.BaseURL = true, "test-key", server.URL
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
			outputContract := artifactv1.OutputContract{Mode: artifactv1.OutputSubmitArtifact}
			conv := conversation.New("native-result")
			conv.AddUserMessage("Read key.go and return captured evidence.")
			renders := 0
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			text, runErr := runACPLoopWithLimits(ctx, cfg, mgr, conv, registry, nil, engine, "gpt-4o", dir, "native-result", nil, func(string, ...interface{}) {}, nil, acpLoopLimits{
				TaskIntent: agentloop.ReadOnlyIntent, VerificationDepth: "none", MaxVerificationAttempts: 1, MaxModelRequests: maxRequests, MaxToolCalls: maxTools,
				ValidateFinalResponse: func(response string) error {
					_, err := resolveOneShotArtifact(response, outputContract, sink)
					return err
				},
				SubmittedResponse: func() (string, bool) {
					artifact, ok := sink.Artifact()
					if !ok {
						return "", false
					}
					renders++
					raw, err := artifactv1.RenderJSON(artifact)
					return string(raw), err == nil
				},
			})
			if calls.Load() != wantCalls || !sawCapture.Load() {
				t.Fatalf("calls=%d want=%d captured feedback=%v err=%v", calls.Load(), wantCalls, sawCapture.Load(), runErr)
			}
			if name == "request-cap" {
				if _, ok := sink.Artifact(); ok || runErr == nil || renders != 0 || len(sink.RecoveryArtifact().EvidenceRefs) != 1 {
					t.Fatalf("invalid submission escaped request cap or lost evidence: renders=%d err=%v", renders, runErr)
				}
				return
			}
			if runErr != nil || renders != 1 {
				t.Fatalf("native result renders=%d err=%v", renders, runErr)
			}
			if wantCalls == 3 && !sawRejection.Load() {
				t.Fatal("invalid native submission skipped normal tool feedback repair")
			}
			artifact, _, err := artifactv1.DecodeProviderOutput(ctx, []byte(text), artifactv1.OutputPromptJSON, artifactv1.DecodeOptions{})
			if err != nil || len(artifact.EvidenceRefs) != 1 || len(artifact.Blocks) != 1 || artifact.Blocks[0].Table == nil {
				t.Fatalf("missing accepted capture: artifact=%+v err=%v", artifact, err)
			}
			status := artifactv1.StatusCompleted
			if name == "incomplete" {
				status = artifactv1.StatusIncomplete
			}
			if name == "failed" {
				status = artifactv1.StatusFailed
			}
			if artifact.Status != status || (name == "incomplete" && len(artifact.IncompleteReasons) != 1) {
				t.Fatalf("changed declared result status: %+v", artifact)
			}
			row := artifact.Blocks[0].Table.Rows[0]
			if row[0] != artifact.EvidenceRefs[0].ID || row[1] != path || row[2] != "1" || row[3] != "2" || row[4] != content {
				t.Fatalf("capture differs from observed bytes: %#v", row)
			}
		})
	}
}
