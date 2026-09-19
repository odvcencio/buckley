package oneshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rlm"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/transparency"
)

func newAgentRunnerTestManager(t *testing.T, server *httptest.Server) *model.Manager {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

func TestNativeCodexReviewRunsWithoutCatalogPricing(t *testing.T) {
	if got := effectiveAgentMaxCostUSD("codex", 0.15); got != 0 {
		t.Fatalf("Codex cost budget = %v, want zero", got)
	}
	if got := effectiveAgentMaxCostUSD("openrouter", 0.15); got != 0.15 {
		t.Fatalf("OpenRouter cost budget = %v, want 0.15", got)
	}
	pricing := transparency.ModelPricing{InputPerMillion: 1, OutputPerMillion: 2}
	tokens := transparency.TokenUsage{Input: 1_000_000, Output: 1_000_000}
	if got := effectiveAgentInvocationCost("codex", pricing, tokens); got != 0 {
		t.Fatalf("Codex invocation cost = %v, want zero", got)
	}
	if got := effectiveAgentInvocationCost("openrouter", pricing, tokens); got != 3 {
		t.Fatalf("OpenRouter invocation cost = %v, want 3", got)
	}
}

func TestAgentRunnerPreservesModelExecutionIdentityInResultAndTrace(t *testing.T) {
	for _, tt := range []struct {
		name       string
		responseID string
		finish     string
		content    string
		message    string
		wantErr    bool
	}{
		{name: "complete stop", responseID: "chatcmpl-agent-complete", finish: "stop", content: "agent answer", message: `"content":"agent answer"`, wantErr: false},
		{name: "incomplete length", responseID: "chatcmpl-agent-length", finish: "length", content: "public partial draft", message: `"content":"public partial draft","reasoning":"private-reasoning-sentinel","reasoning_details":[{"type":"reasoning.text","text":"private-reasoning-sentinel"}]`, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{
					"id":%q,"model":"gpt-4o",
					"choices":[{"index":0,"message":{"role":"assistant",%s},"finish_reason":%q}],
					"usage":{"prompt_tokens":13,"completion_tokens":17,"total_tokens":30}
				}`, tt.responseID, tt.message, tt.finish)
			}))
			defer server.Close()

			runner := NewAgentRunner(AgentRunnerConfig{
				Models:   newAgentRunnerTestManager(t, server),
				Registry: tool.NewEmptyRegistry(),
				ModelID:  "gpt-4o",
			})
			result, err := runner.Run(context.Background(), "system", "task", nil, AgentExecutionOpts{MaxIterations: 1})
			if tt.wantErr && err == nil {
				t.Fatal("Run error = nil, want incomplete error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result == nil {
				t.Fatal("Run result = nil")
			}
			if result.Incomplete != tt.wantErr {
				t.Fatalf("Incomplete = %v, want %v", result.Incomplete, tt.wantErr)
			}
			if !strings.Contains(result.Response, tt.content) {
				t.Fatalf("Response = %q, want public content %q", result.Response, tt.content)
			}
			if result.TokensUsed != 30 || result.InputTokens != 13 || result.OutputTokens != 17 {
				t.Fatalf("tokens = %d/%d/%d, want retained usage 30/13/17", result.TokensUsed, result.InputTokens, result.OutputTokens)
			}
			if len(result.ModelExecutions) != 1 || result.ModelExecutions[0].ResponseID != tt.responseID {
				t.Fatalf("result model executions = %+v, want response identity", result.ModelExecutions)
			}
			if len(result.Trace.ModelExecutions) != 1 || result.Trace.ModelExecutions[0].ResponseID != tt.responseID {
				t.Fatalf("trace model executions = %+v, want response identity", result.Trace.ModelExecutions)
			}
			result.ModelExecutions[0].ResponseID = "mutated"
			if result.Trace.ModelExecutions[0].ResponseID != tt.responseID {
				t.Fatalf("trace identity aliased AgentResult slice: %+v", result.Trace.ModelExecutions)
			}
			encoded, err := json.Marshal(result.Trace)
			if err != nil {
				t.Fatalf("Marshal trace: %v", err)
			}
			raw := string(encoded)
			if !strings.Contains(raw, `"model_executions"`) {
				t.Fatalf("trace JSON = %s, want identity", raw)
			}
			if strings.Contains(raw, "private-reasoning-sentinel") || strings.Contains(result.Response, "private-reasoning-sentinel") {
				t.Fatalf("private reasoning leaked through result/trace: response=%q trace=%s", result.Response, raw)
			}
		})
	}
}

func TestReviewAgentOutputTokenLimitRequiresGovernedReasoning(t *testing.T) {
	if got := reviewAgentOutputTokenLimit(0); got != 0 {
		t.Fatalf("ungoverned output limit = %d, want zero", got)
	}
	if got := reviewAgentOutputTokenLimit(1024); got != 5120 {
		t.Fatalf("governed output limit = %d, want 5120", got)
	}
}

func TestClampAgentOutputTokenLimitUsesProviderCapability(t *testing.T) {
	tests := []struct {
		name        string
		configured  int
		providerMax int
		want        int
	}{
		{name: "provider ceiling", configured: 32768, providerMax: 131072, want: 32768},
		{name: "smaller provider", configured: 32768, providerMax: 8192, want: 8192},
		{name: "unknown provider ceiling", configured: 32768, providerMax: 0, want: 32768},
		{name: "unbounded request", configured: 0, providerMax: 8192, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampAgentOutputTokenLimit(tt.configured, tt.providerMax); got != tt.want {
				t.Fatalf("clampAgentOutputTokenLimit(%d, %d) = %d, want %d", tt.configured, tt.providerMax, got, tt.want)
			}
		})
	}
}

func TestFormatIncompleteAgentResponseRetainsCompletedEvidence(t *testing.T) {
	result := &rlm.SubAgentResult{
		Summary:      "Inspected the sharding contract.",
		InputTokens:  120,
		OutputTokens: 30,
		TokensUsed:   150,
		ToolCalls: []rlm.SubAgentToolCall{{
			Name:      "search_text",
			Arguments: `{"query":"race_root"}`,
			Result:    "found aggregate gate",
			Success:   true,
		}},
	}

	got := formatIncompleteAgentResponse(result, errors.Join(context.DeadlineExceeded, errors.New("provider still working")))
	for _, want := range []string{"Incomplete agent result", "not a completed or validated result", "Inspected the sharding contract", "search_text", "found aggregate gate", "120 input", "1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("salvage output missing %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatal("salvage output must end with newline")
	}
}

func TestAgentRunnerReviewSnapshotRetainsResultOnPostVerifyFailure(t *testing.T) {
	for _, tt := range []struct {
		name              string
		forceVerifyFailed bool
		skipToolLoop      bool
		finalFinish       string
		finalContent      string
		wantModelCalls    int32
		wantIdentities    int
		wantTokens        int
		wantInput         int
		wantOutput        int
		wantErrParts      []string
	}{
		{
			name:           "baseline snapshot success",
			finalFinish:    "stop",
			finalContent:   "final review from immutable snapshot",
			wantModelCalls: 2,
			wantIdentities: 2,
			wantTokens:     31,
			wantInput:      22,
			wantOutput:     9,
		},
		{
			name:              "verification failure retains completed work",
			forceVerifyFailed: true,
			finalFinish:       "stop",
			finalContent:      "final review before verification failure",
			wantModelCalls:    2,
			wantIdentities:    2,
			wantTokens:        31,
			wantInput:         22,
			wantOutput:        9,
			wantErrParts:      []string{"API review changed the captured source snapshot", "tracked source differs from immutable snapshot"},
		},
		{
			name:              "truncation and verification failure retain completed work",
			forceVerifyFailed: true,
			skipToolLoop:      true,
			finalFinish:       "length",
			finalContent:      "public partial draft before verification failure",
			wantModelCalls:    1,
			wantIdentities:    1,
			wantTokens:        18,
			wantInput:         7,
			wantOutput:        11,
			wantErrParts:      []string{"execute task", "API review changed the captured source snapshot", "tracked source differs from immutable snapshot"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newAgentRunnerSnapshotFixture(t)
			t.Chdir(repo)
			marker := filepath.Join(t.TempDir(), "fail-post-verify")
			installPostVerificationDiffGitWrapper(t, marker)
			snapshot, err := model.CaptureReviewSnapshot(context.Background(), repo, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead})
			if err != nil {
				t.Fatalf("CaptureReviewSnapshot: %v", err)
			}
			if snapshot == nil || strings.TrimSpace(snapshot.Commit()) == "" {
				t.Fatalf("snapshot = %#v, want captured commit", snapshot)
			}

			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch call {
				case 1:
					if tt.skipToolLoop {
						if tt.forceVerifyFailed {
							if err := os.WriteFile(marker, []byte("fail\n"), 0o600); err != nil {
								t.Errorf("write marker: %v", err)
							}
						}
						fmt.Fprintf(w, `{
							"id":"chatcmpl-snapshot-truncated","model":"gpt-4o",
							"choices":[{"index":0,"message":{"role":"assistant","content":%q,"reasoning":"private-reasoning-sentinel","reasoning_details":[{"type":"reasoning.text","text":"private-reasoning-sentinel"}]},"finish_reason":%q}],
							"usage":{"prompt_tokens":7,"completion_tokens":11,"total_tokens":18}
						}`, tt.finalContent, tt.finalFinish)
						return
					}
					fmt.Fprint(w, `{
						"id":"chatcmpl-snapshot-tool","model":"gpt-4o",
						"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_snapshot_read","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"review.txt\"}"}}]},"finish_reason":"tool_calls"}],
						"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
					}`)
				case 2:
					if tt.forceVerifyFailed {
						if err := os.WriteFile(marker, []byte("fail\n"), 0o600); err != nil {
							t.Errorf("write marker: %v", err)
						}
					}
					fmt.Fprintf(w, `{
						"id":"chatcmpl-snapshot-final","model":"gpt-4o",
						"choices":[{"index":0,"message":{"role":"assistant","content":%q,"reasoning":"private-reasoning-sentinel","reasoning_details":[{"type":"reasoning.text","text":"private-reasoning-sentinel"}]},"finish_reason":%q}],
						"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}
					}`, tt.finalContent, tt.finalFinish)
				default:
					t.Errorf("unexpected model call %d", call)
					fmt.Fprint(w, `{"id":"chatcmpl-extra","model":"gpt-4o","choices":[]}`)
				}
			}))
			defer server.Close()

			runner := NewAgentRunner(AgentRunnerConfig{
				Models:   newAgentRunnerTestManager(t, server),
				Registry: tool.NewEmptyRegistry(),
				ModelID:  "gpt-4o",
			})
			result, err := runner.Run(context.Background(), "system", "review", []string{"read_file"}, AgentExecutionOpts{
				ReviewSnapshot: snapshot,
				MaxIterations:  2,
			})
			if len(tt.wantErrParts) == 0 {
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if result == nil {
					t.Fatal("Run result = nil")
				}
				if result.Incomplete {
					t.Fatalf("Incomplete = true, want false: %s", result.Response)
				}
				if result.Response != tt.finalContent {
					t.Fatalf("Response = %q, want %q", result.Response, tt.finalContent)
				}
				if result.Trace == nil {
					t.Fatal("Trace = nil, want retained trace")
				}
				if result.Trace.Error != "" {
					t.Fatalf("trace error = %q, want empty", result.Trace.Error)
				}
			} else {
				if err == nil {
					t.Fatal("Run error = nil, want retained incomplete error")
				}
				for _, want := range tt.wantErrParts {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("Run error = %q, missing %q", err.Error(), want)
					}
				}
				if result == nil {
					t.Fatal("Run result = nil, want retained incomplete result")
				}
				if !result.Incomplete {
					t.Fatalf("Incomplete = false, want true")
				}
				retainedWants := []string{"Incomplete agent result", tt.finalContent}
				if !tt.skipToolLoop {
					retainedWants = append(retainedWants, "read_file", "31 total")
				}
				for _, want := range append(retainedWants, tt.wantErrParts...) {
					if !strings.Contains(result.Response, want) {
						t.Fatalf("retained response missing %q:\n%s", want, result.Response)
					}
				}
				if result.Trace == nil {
					t.Fatal("Trace = nil, want retained trace")
				}
				for _, want := range tt.wantErrParts {
					if !strings.Contains(result.Trace.Error, want) {
						t.Fatalf("trace error = %q, missing %q", result.Trace.Error, want)
					}
				}
			}
			if calls.Load() != tt.wantModelCalls {
				t.Fatalf("model calls = %d, want actual tool loop/finalization count %d", calls.Load(), tt.wantModelCalls)
			}
			if result.TokensUsed != tt.wantTokens || result.InputTokens != tt.wantInput || result.OutputTokens != tt.wantOutput {
				t.Fatalf("tokens = %d/%d/%d, want retained %d/%d/%d", result.TokensUsed, result.InputTokens, result.OutputTokens, tt.wantTokens, tt.wantInput, tt.wantOutput)
			}
			if tt.skipToolLoop {
				if len(result.ToolCalls) != 0 {
					t.Fatalf("tool calls = %+v, want none for one-turn truncation case", result.ToolCalls)
				}
			} else {
				if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "read_file" || !result.ToolCalls[0].Success {
					t.Fatalf("tool calls = %+v, want successful snapshot read_file", result.ToolCalls)
				}
				if !strings.Contains(result.ToolCalls[0].Result, "captured snapshot truth") {
					t.Fatalf("tool result = %q, want captured snapshot content", result.ToolCalls[0].Result)
				}
			}
			if len(result.ModelExecutions) != tt.wantIdentities {
				t.Fatalf("result model executions = %+v, want %d retained response identities", result.ModelExecutions, tt.wantIdentities)
			}
			if tt.skipToolLoop {
				if result.ModelExecutions[0].ResponseID != "chatcmpl-snapshot-truncated" {
					t.Fatalf("result model executions = %+v, want truncated identity", result.ModelExecutions)
				}
			} else if result.ModelExecutions[0].ResponseID != "chatcmpl-snapshot-tool" || result.ModelExecutions[1].ResponseID != "chatcmpl-snapshot-final" {
				t.Fatalf("result model executions = %+v, want retained loopback responses", result.ModelExecutions)
			}
			if result.Trace == nil || len(result.Trace.ModelExecutions) != tt.wantIdentities {
				t.Fatalf("trace model executions = %+v, want retained identities", result.Trace)
			}
			if tt.skipToolLoop {
				if result.Trace.ModelExecutions[0].ResponseID != "chatcmpl-snapshot-truncated" {
					t.Fatalf("trace model executions = %+v, want truncated identity", result.Trace.ModelExecutions)
				}
			} else if result.Trace.ModelExecutions[1].ResponseID != "chatcmpl-snapshot-final" {
				t.Fatalf("trace model executions = %+v, want final identity", result.Trace.ModelExecutions)
			}
			if strings.Contains(result.Response, "private-reasoning-sentinel") || strings.Contains(result.Trace.Content, "private-reasoning-sentinel") {
				t.Fatalf("private reasoning leaked into retained public surfaces: response=%q trace=%q", result.Response, result.Trace.Content)
			}
		})
	}
}

func TestReviewSnapshotRegistryReadsOnlyMaterializedState(t *testing.T) {
	repo := t.TempDir()
	runReviewRegistryGit(t, repo, "init", "-q")
	runReviewRegistryGit(t, repo, "config", "user.email", "test@example.com")
	runReviewRegistryGit(t, repo, "config", "user.name", "Test User")
	tracked := filepath.Join(repo, "behavior.txt")
	if err := os.WriteFile(tracked, []byte("captured behavior\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runReviewRegistryGit(t, repo, "add", "behavior.txt")
	runReviewRegistryGit(t, repo, "commit", "-m", "initial")

	snapshot, err := model.CaptureReviewSnapshot(context.Background(), repo, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	if err := os.WriteFile(tracked, []byte("newer live behavior\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(repo, "untracked-secret.txt")
	if err := os.WriteFile(untracked, []byte("untracked secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	workDir, cleanup, err := model.PrepareReviewWorkspace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("PrepareReviewWorkspace: %v", err)
	}
	t.Cleanup(cleanup)
	root, err := model.ReviewWorkspaceRepositoryRoot(context.Background(), workDir)
	if err != nil {
		t.Fatalf("ReviewWorkspaceRepositoryRoot: %v", err)
	}
	registry, err := newReviewSnapshotRegistry(root, []string{"read_file", "find_files", "search_text"})
	if err != nil {
		t.Fatalf("newReviewSnapshotRegistry: %v", err)
	}

	read, err := registry.Execute("read_file", map[string]any{"path": "behavior.txt"})
	if err != nil || !read.Success || !strings.Contains(read.Data["content"].(string), "captured behavior") {
		t.Fatalf("snapshot read = %#v, err=%v", read, err)
	}
	for _, path := range []string{"untracked-secret.txt", untracked} {
		outside, execErr := registry.Execute("read_file", map[string]any{"path": path})
		if execErr != nil {
			t.Fatalf("confined read %q: %v", path, execErr)
		}
		if outside.Success {
			t.Fatalf("confined read exposed %q: %#v", path, outside.Data)
		}
	}

	files, err := registry.Execute("find_files", map[string]any{"pattern": "*.txt", "base_path": "."})
	if err != nil || !files.Success {
		t.Fatalf("snapshot find_files = %#v, err=%v", files, err)
	}
	matches, _ := files.Data["matches"].([]string)
	if len(matches) != 1 || matches[0] != "behavior.txt" {
		t.Fatalf("snapshot file inventory = %#v, want only behavior.txt", matches)
	}

	search, err := registry.Execute("search_text", map[string]any{"query": "newer live|untracked secret", "path": "."})
	if err != nil || !search.Success {
		t.Fatalf("snapshot search_text = %#v, err=%v", search, err)
	}
	if count, _ := search.Data["count"].(int); count != 0 {
		t.Fatalf("snapshot search exposed excluded live state: %#v", search.Data)
	}
}

func TestReviewSnapshotRegistryRejectsNonReviewTools(t *testing.T) {
	if _, err := newReviewSnapshotRegistry(t.TempDir(), []string{"read_file", "run_shell"}); err == nil {
		t.Fatal("snapshot registry accepted an executable tool")
	}
}

func TestReviewSnapshotRegistryAcceptsAuditedCodeModeSurface(t *testing.T) {
	registry, err := newReviewSnapshotRegistry(t.TempDir(), []string{"exec_program", "read_file"})
	if err != nil {
		t.Fatalf("newReviewSnapshotRegistry: %v", err)
	}
	defer registry.Close()
	if _, exists := registry.Get("exec_program"); exists {
		t.Fatal("exec_program must be explicitly wired with durable stores")
	}
}

func TestRegisterReviewCodeModeToolReadsThroughAuditedSnapshotCapabilities(t *testing.T) {
	if execmode.DetectIsolation() != execmode.IsolationBwrap {
		t.Skip("bubblewrap is required for the real review code-mode test")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.txt"), []byte("snapshot truth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	ev, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := newReviewSnapshotRegistry(root, []string{"exec_program", "read_file"})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	runID, err := registerReviewCodeModeTool(context.Background(), registry, root, ledger, ev, "review-session", "x-ai/grok-4.6", "openrouter")
	if err != nil {
		t.Fatalf("registerReviewCodeModeTool: %v", err)
	}
	if runID == "" {
		t.Fatal("review code mode did not create a durable run")
	}
	result, err := registry.Execute("exec_program", map[string]any{
		"source": `package main
import (
    "fmt"
    "execprogram/caps"
)
func main() {
    body, _, err := caps.ReadFile("sample.txt")
    if err != nil { panic(err) }
    fmt.Print(body)
}`,
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("exec_program = %#v, %v", result, err)
	}
	stdout, _ := result.Data["stdout"].(string)
	if stdout != "snapshot truth\n" {
		t.Fatalf("stdout = %q, want immutable snapshot content", stdout)
	}
	objects, err := ev.Query(context.Background(), evidence.Query{RunID: runID})
	if err != nil || len(objects) < 2 {
		t.Fatalf("durable code-mode evidence = %d, %v; want source and output", len(objects), err)
	}
}

func TestReviewSnapshotRegistryExplicitlyRegistersSealedVerification(t *testing.T) {
	root := t.TempDir()
	registry, err := newReviewSnapshotRegistry(root, []string{"read_file", "run_verification"}, "/usr/bin/true")
	if err != nil {
		t.Fatalf("newReviewSnapshotRegistry: %v", err)
	}
	verification, ok := registry.Get("run_verification")
	if !ok {
		t.Fatal("snapshot registry omitted explicitly allowed run_verification")
	}
	if _, mutable := verification.(interface{ SetWorkDir(string) }); mutable {
		t.Fatal("run_verification root can be rebound through generic SetWorkDir")
	}
	if got := registry.ToolKind("run_verification"); got != "execute" {
		t.Fatalf("run_verification kind = %q, want execute", got)
	}
}

func TestCollectAgentEvidenceRetainsSnapshotVerificationResult(t *testing.T) {
	repo := t.TempDir()
	runReviewRegistryGit(t, repo, "init", "-q")
	runReviewRegistryGit(t, repo, "config", "user.email", "test@example.com")
	runReviewRegistryGit(t, repo, "config", "user.name", "Test User")
	for path, content := range map[string]string{
		"go.mod":           "module example.test/evidence\n\ngo 1.24\n",
		"evidence.go":      "package evidence\n\nfunc Value() int { return 1 }\n",
		"evidence_test.go": "package evidence\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 1 { t.Fatal(\"bad value\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runReviewRegistryGit(t, repo, "add", ".")
	runReviewRegistryGit(t, repo, "commit", "-m", "initial")

	snapshot, err := model.CaptureReviewSnapshot(context.Background(), repo, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	runner := &AgentRunner{}
	calls, err := runner.CollectAgentEvidence(context.Background(), []AgentEvidenceRequest{{
		Tool: "run_verification",
		Parameters: map[string]any{
			"kind": "test", "language": "go", "path": ".",
		},
	}}, AgentExecutionOpts{ReviewSnapshot: snapshot, VerificationTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("CollectAgentEvidence: %v", err)
	}
	if len(calls) != 1 || calls[0].ID != "host-evidence-1" || calls[0].Name != "run_verification" {
		t.Fatalf("calls = %#v, want one stable host evidence call", calls)
	}
	if status, _ := calls[0].Data["status"].(string); strings.TrimSpace(status) == "" {
		t.Fatalf("host evidence discarded verification status: %#v", calls[0])
	}
}

func TestCollectAgentEvidenceDoesNotReadUntrackedLiveSource(t *testing.T) {
	repo := t.TempDir()
	runReviewRegistryGit(t, repo, "init", "-q")
	runReviewRegistryGit(t, repo, "config", "user.email", "test@example.com")
	runReviewRegistryGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.test/evidence\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(repo, "evidence.go")
	if err := os.WriteFile(basePath, []byte("package evidence\n\nfunc Value() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runReviewRegistryGit(t, repo, "add", ".")
	runReviewRegistryGit(t, repo, "commit", "-m", "initial")
	if err := os.WriteFile(basePath, []byte("package evidence\n\nfunc Value() int { return missing() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "helper.go"), []byte("package evidence\n\nfunc missing() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snapshot, err := model.CaptureReviewSnapshot(context.Background(), repo, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotTrackedWorktree})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	calls, err := (&AgentRunner{}).CollectAgentEvidence(context.Background(), []AgentEvidenceRequest{{
		Tool: "run_verification",
		Parameters: map[string]any{
			"kind": "test", "language": "go", "path": ".",
		},
	}}, AgentExecutionOpts{ReviewSnapshot: snapshot, VerificationTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("CollectAgentEvidence: %v", err)
	}
	if len(calls) != 1 || calls[0].Success {
		t.Fatalf("calls = %#v, want failed verification without untracked helper", calls)
	}
	if status, _ := calls[0].Data["status"].(string); status != "FAIL" {
		t.Fatalf("status = %q, want FAIL: %#v", status, calls[0])
	}
}

func runReviewRegistryGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func newAgentRunnerSnapshotFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runReviewRegistryGit(t, repo, "init", "-q")
	runReviewRegistryGit(t, repo, "config", "user.email", "test@example.com")
	runReviewRegistryGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "review.txt"), []byte("captured snapshot truth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runReviewRegistryGit(t, repo, "add", "review.txt")
	runReviewRegistryGit(t, repo, "commit", "-m", "initial")
	return repo
}

func installPostVerificationDiffGitWrapper(t *testing.T, marker string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("look up real git: %v", err)
	}
	wrapperDir := t.TempDir()
	wrapperPath := filepath.Join(wrapperDir, "git")
	script := fmt.Sprintf(`#!/bin/sh
REAL_GIT=%q
FAIL_MARKER=%q
if [ -f "$FAIL_MARKER" ] && [ "$1" = "-C" ] && [ "$3" = "diff" ] && [ "$4" = "--binary" ] && [ "$8" = "HEAD" ] && [ "$9" = "--" ]; then
	printf 'diff --git a/review.txt b/review.txt\nindex 0000000000000000000000000000000000000000..1111111111111111111111111111111111111111 100644\n--- a/review.txt\n+++ b/review.txt\n@@ -1 +1 @@\n-captured snapshot truth\n+mutated snapshot truth\n'
	exit 0
fi
exec "$REAL_GIT" "$@"
`, realGit, marker)
	if err := os.WriteFile(wrapperPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
