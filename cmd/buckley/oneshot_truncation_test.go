package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
)

// truncationFixture is an OpenRouter fake whose catalog lists reasoning. Each
// request is answered from script in order; the last entry repeats.
type truncationFixture struct {
	mu       sync.Mutex
	requests []model.ChatRequest
	script   []truncationReply
}

type truncationReply struct {
	content   string
	reasoning string
	finish    string
}

func (f *truncationFixture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func newTruncationFixture(t *testing.T, script ...truncationReply) (*truncationFixture, *config.Config, *model.Manager) {
	t.Helper()
	fixture := &truncationFixture{script: script}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"vendor/model","context_length":200000,"pricing":{"prompt":"0.000002","completion":"0.00001"},"supported_parameters":["reasoning","include_reasoning","max_tokens"]}]}`)
		case "/chat/completions":
			var req model.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode request: %v", err)
			}
			fixture.mu.Lock()
			fixture.requests = append(fixture.requests, req)
			reply := fixture.script[min(len(fixture.requests), len(fixture.script))-1]
			fixture.mu.Unlock()
			delta := map[string]any{}
			if reply.content != "" {
				delta["content"] = reply.content
			}
			if reply.reasoning != "" {
				delta["reasoning"] = reply.reasoning
			}
			chunk, _ := json.Marshal(map[string]any{
				"id": "c1", "model": "vendor/model",
				"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": reply.finish}},
			})
			usage := `{"id":"c1","choices":[],"usage":{"prompt_tokens":500,"completion_tokens":2000,"total_tokens":2500,"completion_tokens_details":{"reasoning_tokens":1990}}}`
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", chunk, usage)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cfg := pricingTestConfig()
	cfg.Providers.OpenRouter.BaseURL = server.URL
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.RefreshProviderCatalog("openrouter"); err != nil {
		t.Fatalf("RefreshProviderCatalog: %v", err)
	}
	return fixture, cfg, mgr
}

func runTruncationLoop(t *testing.T, cfg *config.Config, mgr *model.Manager, workDir string, limits acpLoopLimits) (string, error) {
	t.Helper()
	conv := conversation.New("session-truncation")
	conv.AddUserMessage("answer the question")
	return runACPLoopWithLimits(
		context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, nil,
		"vendor/model", workDir, "session-truncation", nil, func(string, ...interface{}) {},
		(&collectingStream{}).fn, limits,
	)
}

func lastUserMessage(req model.ChatRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return model.ExtractTextContentOrEmpty(req.Messages[i].Content)
		}
	}
	return ""
}

func TestRunACPLoop_TruncatedWithoutTextRetriesOnceWithLessReasoning(t *testing.T) {
	fixture, cfg, mgr := newTruncationFixture(t,
		truncationReply{reasoning: "thinking without end", finish: "length"},
		truncationReply{content: "the answer", finish: "stop"},
	)
	text, err := runTruncationLoop(t, cfg, mgr, t.TempDir(), acpLoopLimits{MaxOutputTokens: 16000, truncatedEmptyRetries: 1})
	if err != nil || text != "the answer" {
		t.Fatalf("text=%q err=%v, want the retry's answer", text, err)
	}
	if fixture.count() != 2 {
		t.Fatalf("requests = %d, want 2", fixture.count())
	}
	first, second := fixture.requests[0], fixture.requests[1]
	if got := lastUserMessage(second); got != truncationRetryInstruction {
		t.Fatalf("second request ends with user message %q, want the answer-now instruction", got)
	}
	if lastUserMessage(first) == truncationRetryInstruction {
		t.Fatal("first request already carries the instruction")
	}
	if second.Reasoning == nil || second.Reasoning.MaxTokens != 2000 || second.Reasoning.Effort != "" {
		t.Fatalf("second reasoning = %+v, want a 2000 token budget (cap 16000 / 8)", second.Reasoning)
	}
	if first.Reasoning != nil && first.Reasoning.MaxTokens != 0 {
		t.Fatalf("first request already limited reasoning: %+v", first.Reasoning)
	}
}

func TestRunACPLoop_TruncatedWithoutTextTwiceNamesCauseAndRemedy(t *testing.T) {
	fixture, cfg, mgr := newTruncationFixture(t, truncationReply{reasoning: "still thinking", finish: "length"})
	workDir := t.TempDir()
	text, err := runTruncationLoop(t, cfg, mgr, workDir, acpLoopLimits{MaxOutputTokens: 16000, truncatedEmptyRetries: 1})
	if text != "" {
		t.Fatalf("text = %q, want none", text)
	}
	var noAnswer *oneShotNoAnswerError
	if !errors.As(err, &noAnswer) {
		t.Fatalf("error = %v, want a no-answer error", err)
	}
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error %v no longer unwraps to an incomplete turn", err)
	}
	if fixture.count() != 2 {
		t.Fatalf("requests = %d, want exactly one retry", fixture.count())
	}
	for _, want := range []string{"output cap of 16000 tokens", "1990 reasoning tokens reported", "limited to 2000 tokens", "--effort low", "--max-output-tokens (now 16000)", "shorten the prompt (500 input tokens)"} {
		if !strings.Contains(noAnswer.Message(), want) {
			t.Errorf("message lacks %q: %s", want, noAnswer.Message())
		}
	}
	if noAnswer.ReasoningPath == "" {
		t.Fatalf("reasoning was not saved: %+v", noAnswer)
	}
	saved, readErr := os.ReadFile(noAnswer.ReasoningPath)
	if readErr != nil || !strings.Contains(string(saved), "still thinking") {
		t.Fatalf("saved reasoning = %q, %v", saved, readErr)
	}
	if line := noAnswer.CauseLine(); !strings.Contains(line, "no_answer_text finish=length output_cap=16000 reasoning_tokens=1990 retries=1 reasoning_saved="+noAnswer.ReasoningPath) {
		t.Fatalf("cause line = %q", line)
	}
}

func TestRunACPLoop_TruncatedWithTextIsNotRetried(t *testing.T) {
	fixture, cfg, mgr := newTruncationFixture(t, truncationReply{content: "partial answer", finish: "length"})
	text, err := runTruncationLoop(t, cfg, mgr, t.TempDir(), acpLoopLimits{MaxOutputTokens: 16000, truncatedEmptyRetries: 1})
	var incomplete *agentloop.IncompleteTurnError
	var noAnswer *oneShotNoAnswerError
	if !errors.As(err, &incomplete) || errors.As(err, &noAnswer) {
		t.Fatalf("error = %v, want the plain truncation error", err)
	}
	if text != "partial answer" || fixture.count() != 1 {
		t.Fatalf("text=%q requests=%d, want the partial text and no retry", text, fixture.count())
	}
}

func TestRunACPLoop_TruncationRetryOffByDefault(t *testing.T) {
	fixture, cfg, mgr := newTruncationFixture(t, truncationReply{reasoning: "thinking", finish: "length"})
	_, err := runTruncationLoop(t, cfg, mgr, t.TempDir(), acpLoopLimits{MaxOutputTokens: 16000})
	var noAnswer *oneShotNoAnswerError
	if err == nil || errors.As(err, &noAnswer) || fixture.count() != 1 {
		t.Fatalf("err=%v requests=%d, want one request and the unchanged truncation error", err, fixture.count())
	}
}
