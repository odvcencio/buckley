package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
)

func TestOneShotReasoningBudget_Table(t *testing.T) {
	// Section 5.1 of the design: output cap, then the budget for each effort.
	// 0 means "no budget; the effort word goes out as today".
	rows := []struct {
		cap                        int
		high, medium, low, minimal int
	}{
		{8192, 6144, 4096, 1638, 0},
		{16384, 12288, 8192, 3276, 1638},
		{30000, 22500, 15000, 6000, 3000},
		{64000, 48000, 32000, 12800, 6400},
		{100000, 83616, 50000, 20000, 10000}, // reserve capped at 16,384
		{4096, 3072, 2048, 0, 0},             // low = 819, under the 1,024 provider minimum
		{4095, 0, 0, 0, 0},                   // cap under 4,096: no budget
		{0, 0, 0, 0, 0},                      // cap unknown: no budget
	}
	for _, row := range rows {
		for effort, want := range map[string]int{"high": row.high, "xhigh": row.high, "max": row.high, "medium": row.medium, "low": row.low, "minimal": row.minimal} {
			got, ok := oneShotReasoningBudget(row.cap, effort)
			if got != want || ok != (want > 0) {
				t.Errorf("oneShotReasoningBudget(%d, %q) = %d, %v; want %d", row.cap, effort, got, ok, want)
			}
		}
	}
}

func TestApplyOneShotAnswerReserve(t *testing.T) {
	base := model.ChatRequest{Model: "vendor/model", MaxTokens: 30000, Reasoning: &model.ReasoningConfig{Effort: "low"}}

	got := applyOneShotAnswerReserve(base, "openrouter", true)
	if got.Reasoning == nil || got.Reasoning.Effort != "" || got.Reasoning.MaxTokens != 6000 {
		t.Fatalf("reasoning = %+v, want budget 6000 and no effort", got.Reasoning)
	}
	if base.Reasoning.Effort != "low" || base.Reasoning.MaxTokens != 0 {
		t.Fatalf("caller's reasoning was mutated: %+v", base.Reasoning)
	}
	again := applyOneShotAnswerReserve(got, "openrouter", true)
	if again.Reasoning.MaxTokens != got.Reasoning.MaxTokens || again.Reasoning.Effort != "" {
		t.Fatalf("second application changed the request: %+v", again.Reasoning)
	}
	for name, req := range map[string]struct {
		provider string
		enabled  bool
		req      model.ChatRequest
	}{
		"disabled":      {"openrouter", false, base},
		"other-route":   {"openai", true, base},
		"no-cap":        {"openrouter", true, model.ChatRequest{Reasoning: &model.ReasoningConfig{Effort: "low"}}},
		"no-effort":     {"openrouter", true, model.ChatRequest{MaxTokens: 30000}},
		"effort-off":    {"openrouter", true, model.ChatRequest{MaxTokens: 30000, Reasoning: &model.ReasoningConfig{Enabled: new(bool)}}},
		"cap-too-small": {"openrouter", true, model.ChatRequest{MaxTokens: 2000, Reasoning: &model.ReasoningConfig{Effort: "low"}}},
	} {
		out := applyOneShotAnswerReserve(req.req, req.provider, req.enabled)
		if fmt.Sprint(out.Reasoning) != fmt.Sprint(req.req.Reasoning) {
			t.Errorf("%s: reasoning changed to %+v", name, out.Reasoning)
		}
	}
}

func TestResolvePromptEffort(t *testing.T) {
	t.Setenv("BUCKLEY_EFFORT_PROMPT", "")
	t.Setenv("BUCKLEY_ONESHOT_EFFORT", "")
	if got, err := resolvePromptEffort(""); got != "" || err != nil {
		t.Fatalf("nothing set = %q, %v", got, err)
	}
	t.Setenv("BUCKLEY_ONESHOT_EFFORT", "minimal")
	if got, err := resolvePromptEffort(""); got != "minimal" || err != nil {
		t.Fatalf("oneshot env = %q, %v", got, err)
	}
	t.Setenv("BUCKLEY_EFFORT_PROMPT", "HIGH")
	if got, err := resolvePromptEffort(""); got != "high" || err != nil {
		t.Fatalf("prompt env must win over oneshot env = %q, %v", got, err)
	}
	if got, err := resolvePromptEffort("off"); got != "off" || err != nil {
		t.Fatalf("flag must win = %q, %v", got, err)
	}
	if _, err := resolvePromptEffort("extreme"); err == nil || !strings.Contains(err.Error(), "unsupported reasoning effort") {
		t.Fatalf("invalid flag: %v", err)
	}
}

// reasoningWireFixture serves a priced OpenRouter catalog whose model lists
// the reasoning parameter, answers every request with "the answer" and records
// the decoded request bodies.
type reasoningWireFixture struct {
	mu       sync.Mutex
	requests []map[string]any
}

func (f *reasoningWireFixture) last(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("no chat request reached the fake provider")
	}
	return f.requests[len(f.requests)-1]
}

func newReasoningWireFixture(t *testing.T) (*reasoningWireFixture, *config.Config, *model.Manager) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("BUCKLEY_SKILLS_PATH", t.TempDir())
	t.Setenv("BUCKLEY_EFFORT_PROMPT", "")
	t.Setenv("BUCKLEY_ONESHOT_EFFORT", "")
	fixture := &reasoningWireFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"vendor/model","context_length":200000,"pricing":{"prompt":"0.000002","completion":"0.00001"},"supported_parameters":["reasoning","include_reasoning","max_tokens"]}]}`)
		case "/chat/completions":
			body, _ := io.ReadAll(r.Body)
			var decoded map[string]any
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Errorf("decode request: %v", err)
			}
			fixture.mu.Lock()
			fixture.requests = append(fixture.requests, decoded)
			fixture.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"id":"c1","model":"vendor/model","choices":[{"index":0,"delta":{"content":"the answer"},"finish_reason":"stop"}]}`+"\n\n")
			fmt.Fprint(w, `data: {"id":"c1","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
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

func runReasoningWire(t *testing.T, cfg *config.Config, mgr *model.Manager, opts oneShotOptions) (int, string) {
	t.Helper()
	opts.ModelOverride = "vendor/model"
	opts.AllowedTools = []string{}
	code := -1
	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			code = executeOneShot("Answer briefly.", cfg, mgr, nil, nil, opts)
		})
	})
	return code, stderr
}

func wireReasoning(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	reasoning, _ := req["reasoning"].(map[string]any)
	return reasoning
}

func TestExecuteOneShot_EffortEnvReachesWire(t *testing.T) {
	fixture, cfg, mgr := newReasoningWireFixture(t)
	t.Setenv("BUCKLEY_ONESHOT_EFFORT", "low")
	code, stderr := runReasoningWire(t, cfg, mgr, oneShotOptions{
		Caps: oneShotCaps{MaxOutputTokens: 30000, Set: map[string]bool{"--max-output-tokens": true}},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	req := fixture.last(t)
	reasoning := wireReasoning(t, req)
	if req["max_tokens"] != float64(30000) || reasoning == nil || reasoning["max_tokens"] != float64(6000) {
		t.Fatalf("max_tokens=%v reasoning=%v, want 30000 and a 6000 token reasoning budget", req["max_tokens"], reasoning)
	}
	if _, hasEffort := reasoning["effort"]; hasEffort {
		t.Fatalf("effort word and budget were both sent: %v", reasoning)
	}
	if !strings.Contains(stderr, "reasoning: effort=low budget_tokens=6000 output_cap=30000") {
		t.Fatalf("header line missing: %q", stderr)
	}
}

func TestExecuteOneShot_EffortFlagBeatsEnvAndPromptEnv(t *testing.T) {
	fixture, cfg, mgr := newReasoningWireFixture(t)
	t.Setenv("BUCKLEY_ONESHOT_EFFORT", "low")
	t.Setenv("BUCKLEY_EFFORT_PROMPT", "high")
	code, stderr := runReasoningWire(t, cfg, mgr, oneShotOptions{
		Effort: "medium",
		Caps:   oneShotCaps{MaxOutputTokens: 8000, Set: map[string]bool{"--max-output-tokens": true}},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if got := wireReasoning(t, fixture.last(t))["max_tokens"]; got != float64(4000) {
		t.Fatalf("reasoning.max_tokens = %v, want 4000 (medium of cap 8000)", got)
	}
}

func TestExecuteOneShot_NoEffortSendsNoReasoning(t *testing.T) {
	fixture, cfg, mgr := newReasoningWireFixture(t)
	code, stderr := runReasoningWire(t, cfg, mgr, oneShotOptions{
		Caps: oneShotCaps{MaxOutputTokens: 30000, Set: map[string]bool{"--max-output-tokens": true}},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	req := fixture.last(t)
	if reasoning := wireReasoning(t, req); reasoning != nil {
		t.Fatalf("reasoning = %v, want none when no effort is chosen", reasoning)
	}
	if strings.Contains(stderr, "reasoning: effort=") {
		t.Fatalf("header names an effort that was not chosen: %q", stderr)
	}
}

func TestExecuteOneShot_EffortUnderCeilingKeepsReserveForDefaultCap(t *testing.T) {
	fixture, cfg, mgr := newReasoningWireFixture(t)
	t.Setenv("BUCKLEY_ONESHOT_EFFORT", "low")
	code, stderr := runReasoningWire(t, cfg, mgr, oneShotOptions{Limits: acpLoopLimits{MaxCostUSD: 1}})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	req := fixture.last(t)
	reasoning := wireReasoning(t, req)
	if req["max_tokens"] != float64(8192) || reasoning == nil || reasoning["max_tokens"] != float64(1638) {
		t.Fatalf("max_tokens=%v reasoning=%v, want 8192 and a 1638 token budget", req["max_tokens"], reasoning)
	}
	if !strings.Contains(stderr, "output cap: 8192 tokens per response (default while --budget is set; change with --max-output-tokens)") {
		t.Fatalf("default cap line missing: %q", stderr)
	}
}

func TestExecuteOneShot_AnswerReserveOffSendsEffortWord(t *testing.T) {
	fixture, cfg, mgr := newReasoningWireFixture(t)
	cfg.Oneshot.AnswerReserve = "off"
	t.Setenv("BUCKLEY_ONESHOT_EFFORT", "low")
	code, stderr := runReasoningWire(t, cfg, mgr, oneShotOptions{
		Caps: oneShotCaps{MaxOutputTokens: 30000, Set: map[string]bool{"--max-output-tokens": true}},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	reasoning := wireReasoning(t, fixture.last(t))
	if reasoning["effort"] != "low" || reasoning["max_tokens"] != nil {
		t.Fatalf("reasoning = %v, want the effort word only", reasoning)
	}
}

func TestExecuteOneShot_InvalidEffortExits2WithoutRequest(t *testing.T) {
	fixture, cfg, mgr := newReasoningWireFixture(t)
	t.Setenv("BUCKLEY_ONESHOT_EFFORT", "extreme")
	code, stderr := runReasoningWire(t, cfg, mgr, oneShotOptions{})
	if code != 2 || !strings.Contains(stderr, "unsupported reasoning effort") {
		t.Fatalf("code=%d stderr=%q, want exit 2 naming the effort", code, stderr)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.requests) != 0 {
		t.Fatalf("%d requests were sent", len(fixture.requests))
	}
}
