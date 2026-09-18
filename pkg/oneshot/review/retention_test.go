package review

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/transparency"
)

const reviewRetentionPrivateReasoning = "PRIVATE_REVIEW_RETENTION_REASONING"

type reviewRetentionResponse struct {
	resp *model.ChatResponse
	err  error
}

type reviewRetentionClient struct {
	responses []reviewRetentionResponse
	calls     int
}

func (c *reviewRetentionClient) ChatCompletion(context.Context, model.ChatRequest) (*model.ChatResponse, error) {
	if c.calls >= len(c.responses) {
		return nil, errors.New("unexpected model call")
	}
	next := c.responses[c.calls]
	c.calls++
	return next.resp, next.err
}

func TestRunResultParseFailsClosedWhenErrorSet(t *testing.T) {
	preparsed := &ParsedReview{Grade: "A"}
	result := &RunResult{
		Review: "## Grade: A\n\n## Verdict\n- **Approved**: YES\n",
		Parsed: preparsed,
		Error:  errors.New("retained incomplete draft"),
	}

	if got := result.Parse(); got != nil {
		t.Fatalf("Parse() = %#v, want nil for errored retained review", got)
	}
	if result.Parsed != preparsed {
		t.Fatalf("Parse() mutated prepopulated parsed result: %#v", result.Parsed)
	}
}

func TestRunResultParseStillParsesSuccessfulReview(t *testing.T) {
	result := &RunResult{Review: "## Grade: B\n\n## Summary\nLooks reasonable.\n"}
	parsed := result.Parse()
	if parsed == nil || parsed.Grade != "B" {
		t.Fatalf("Parse() = %#v, want parsed successful review", parsed)
	}
	cached := &ParsedReview{Grade: "A"}
	result.Parsed = cached
	if got := result.Parse(); got != cached {
		t.Fatalf("Parse() = %#v, want cached parsed result", got)
	}
}

func TestReviewAgentWrappersRetainPartialOnError(t *testing.T) {
	for _, tt := range []struct {
		name string
		run  func(*Runner) (*RunResult, error)
	}{
		{
			name: "branch",
			run: func(r *Runner) (*RunResult, error) {
				return r.reviewWithAgent(context.Background(), "system", "review branch", transparency.NewContextAudit())
			},
		},
		{
			name: "pr",
			run: func(r *Runner) (*RunResult, error) {
				return r.reviewPRWithAgent(context.Background(), &PRContext{PR: &PRInfo{Number: 12}}, "system", "review pr", transparency.NewContextAudit())
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{
					"id":"resp-agent-%s","model":"gpt-4o",
					"choices":[{"index":0,"message":{"role":"assistant","content":"public agent partial","reasoning":%q,"reasoning_details":[{"type":"reasoning.text","text":%q}]},"finish_reason":"length"}],
					"usage":{"prompt_tokens":13,"completion_tokens":17,"total_tokens":30}
				}`, tt.name, reviewRetentionPrivateReasoning, reviewRetentionPrivateReasoning)
			}))
			defer server.Close()

			runner := NewRunner(RunnerConfig{
				Models:   reviewRetentionModelManager(t, server),
				Registry: tool.NewEmptyRegistry(),
				ModelID:  "gpt-4o",
			})

			result, err := tt.run(runner)
			if err != nil {
				t.Fatalf("wrapper outer error = %v, want nil", err)
			}
			if result == nil || result.Error == nil {
				t.Fatalf("result = %#v, want retained errored result", result)
			}
			if !strings.Contains(result.Review, "Incomplete agent result") || !strings.Contains(result.Review, "public agent partial") {
				t.Fatalf("review = %q, want retained incomplete public draft", result.Review)
			}
			if result.Parse() != nil {
				t.Fatalf("Parse() accepted errored retained review: %#v", result.Parsed)
			}
			if result.Trace == nil || result.Trace.Error == "" || result.Trace.Tokens.Input != 13 || result.Trace.Tokens.Output != 17 {
				t.Fatalf("trace = %#v, want retained failed trace with usage", result.Trace)
			}
			if got := result.Trace.ModelExecutions; len(got) != 1 || got[0].ResponseID != "resp-agent-"+tt.name {
				t.Fatalf("trace model executions = %+v, want retained response identity", got)
			}
			if strings.Contains(result.Review, reviewRetentionPrivateReasoning) || strings.Contains(result.Trace.Content, reviewRetentionPrivateReasoning) {
				t.Fatalf("private reasoning leaked: review=%q trace=%q", result.Review, result.Trace.Content)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want one terminal partial request", requests)
			}
		})
	}
}

func TestReviewAgentWrapperOrdinaryModelErrorIsStillIncomplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "provider unavailable", http.StatusBadGateway)
	}))
	defer server.Close()
	runner := NewRunner(RunnerConfig{
		Models:   reviewRetentionModelManager(t, server),
		Registry: tool.NewEmptyRegistry(),
		ModelID:  "gpt-4o",
	})

	result, err := runner.reviewWithAgent(context.Background(), "system", "review", transparency.NewContextAudit())
	if err != nil {
		t.Fatalf("wrapper outer error = %v, want nil", err)
	}
	if result == nil || result.Error == nil {
		t.Fatalf("result = %#v, want ordinary model error retained as RunResult.Error", result)
	}
	if result.Parse() != nil {
		t.Fatalf("Parse() accepted ordinary errored review: %#v", result.Parsed)
	}
}

func TestReviewLegacyWrappersRetainPartialOnError(t *testing.T) {
	for _, tt := range []struct {
		name       string
		response   reviewRetentionResponse
		run        func(*Runner) (*RunResult, error)
		wantReview string
		wantID     string
	}{
		{
			name: "text partial response error",
			response: reviewRetentionResponse{
				resp: reviewRetentionChatResponse("resp-text-partial", model.Message{
					Role:             "assistant",
					Content:          "legacy text partial",
					Reasoning:        reviewRetentionPrivateReasoning,
					ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.text", Text: reviewRetentionPrivateReasoning}},
				}, "stop", 6, 7),
				err: errors.New("text transport failed"),
			},
			run: func(r *Runner) (*RunResult, error) {
				return r.reviewWithLegacyInvoker(context.Background(), "system", "review", transparency.NewContextAudit())
			},
			wantReview: "legacy text partial",
			wantID:     "resp-text-partial",
		},
		{
			name: "pr tool loop partial response error",
			response: reviewRetentionResponse{
				resp: reviewRetentionChatResponse("resp-pr-partial", model.Message{
					Role:             "assistant",
					Content:          "legacy pr partial",
					Reasoning:        reviewRetentionPrivateReasoning,
					ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.text", Text: reviewRetentionPrivateReasoning}},
				}, "stop", 8, 9),
				err: errors.New("tool loop transport failed"),
			},
			run: func(r *Runner) (*RunResult, error) {
				return r.reviewPRWithLegacyTools(context.Background(), &PRContext{PR: &PRInfo{Number: 7}}, "system", "review", transparency.NewContextAudit())
			},
			wantReview: "legacy pr partial",
			wantID:     "resp-pr-partial",
		},
		{
			name: "pr tool loop truncation",
			response: reviewRetentionResponse{
				resp: reviewRetentionChatResponse("resp-pr-length", model.Message{
					Role:             "assistant",
					Content:          "legacy pr truncated",
					Reasoning:        reviewRetentionPrivateReasoning,
					ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.text", Text: reviewRetentionPrivateReasoning}},
				}, "length", 10, 11),
			},
			run: func(r *Runner) (*RunResult, error) {
				return r.reviewPRWithLegacyTools(context.Background(), &PRContext{PR: &PRInfo{Number: 8}}, "system", "review", transparency.NewContextAudit())
			},
			wantReview: "legacy pr truncated",
			wantID:     "resp-pr-length",
		},
		{
			name:     "nil response ordinary error",
			response: reviewRetentionResponse{err: errors.New("no response")},
			run: func(r *Runner) (*RunResult, error) {
				return r.reviewWithLegacyInvoker(context.Background(), "system", "review", transparency.NewContextAudit())
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &reviewRetentionClient{responses: []reviewRetentionResponse{tt.response}}
			runner := &Runner{invoker: oneshot.NewInvoker(oneshot.InvokerConfig{Client: client, Model: "gpt-test", Provider: "test-provider"})}

			result, err := tt.run(runner)
			if err != nil {
				t.Fatalf("wrapper outer error = %v, want nil", err)
			}
			if result == nil || result.Error == nil {
				t.Fatalf("result = %#v, want retained errored result", result)
			}
			if result.Review != tt.wantReview {
				t.Fatalf("review = %q, want %q", result.Review, tt.wantReview)
			}
			if result.Parse() != nil {
				t.Fatalf("Parse() accepted errored retained review: %#v", result.Parsed)
			}
			if tt.wantID != "" {
				if result.Trace == nil || result.Trace.Error == "" || result.Trace.Tokens.Input == 0 || result.Trace.Tokens.Output == 0 {
					t.Fatalf("trace = %#v, want retained failed trace with usage", result.Trace)
				}
				if got := result.Trace.ModelExecutions; len(got) != 1 || got[0].ResponseID != tt.wantID {
					t.Fatalf("trace model executions = %+v, want %q", got, tt.wantID)
				}
				if strings.Contains(result.Review, reviewRetentionPrivateReasoning) || strings.Contains(result.Trace.Content, reviewRetentionPrivateReasoning) {
					t.Fatalf("private reasoning leaked: review=%q trace=%q", result.Review, result.Trace.Content)
				}
			}
			if client.calls != 1 {
				t.Fatalf("model calls = %d, want one", client.calls)
			}
		})
	}
}

func reviewRetentionModelManager(t *testing.T, server *httptest.Server) *model.Manager {
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

func reviewRetentionChatResponse(id string, msg model.Message, finish string, input, output int) *model.ChatResponse {
	return &model.ChatResponse{
		ID:      id,
		Model:   "wire-model",
		Choices: []model.Choice{{Message: msg, FinishReason: finish}},
		Usage:   model.Usage{PromptTokens: input, CompletionTokens: output, TotalTokens: input + output},
		ExecutionIdentity: &model.ExecutionIdentity{
			RequestedModel: "gpt-test",
			SelectedModel:  "gpt-test",
			ProviderID:     "test-provider",
			ResponseModel:  "wire-model",
			ResponseID:     id,
		},
		UsagePresent: true,
	}
}
