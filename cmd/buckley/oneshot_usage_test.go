package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
)

// newOneShotUsageFixture serves a priced OpenRouter catalog and streams one
// final answer. usageChunk is added to the stream when it is not empty.
func newOneShotUsageFixture(t *testing.T, usageChunk string) (*config.Config, *model.Manager) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("BUCKLEY_SKILLS_PATH", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"vendor/model","context_length":100000,"pricing":{"prompt":"0.000002","completion":"0.00001"}}]}`)
		case "/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"id":"c1","model":"vendor/model","choices":[{"index":0,"delta":{"content":"the answer"},"finish_reason":"stop"}]}`+"\n\n")
			if usageChunk != "" {
				fmt.Fprint(w, "data: "+usageChunk+"\n\n")
			}
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
	return cfg, mgr
}

func oneShotUsageLines(stderr string) []string {
	var lines []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "One-shot usage:") {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestExecuteOneShot_UsageLineWithQuietAndWithoutBudget(t *testing.T) {
	previousQuiet := quietMode
	quietMode = true
	t.Cleanup(func() { quietMode = previousQuiet })
	cfg, mgr := newOneShotUsageFixture(t, `{"id":"c1","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,"prompt_tokens_details":{"cached_tokens":600},"completion_tokens_details":{"reasoning_tokens":150},"cost":0.004}}`)

	code := -1
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code = executeOneShot("Answer briefly.", cfg, mgr, nil, nil, oneShotOptions{
				ModelOverride: "vendor/model",
				AllowedTools:  []string{},
			})
		})
	})
	if code != 0 || !strings.Contains(stdout, "the answer") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	lines := oneShotUsageLines(stderr)
	if len(lines) != 1 {
		t.Fatalf("usage lines = %d, want exactly 1; stderr=%q", len(lines), stderr)
	}
	line := lines[0]
	for _, want := range []string{
		"requests=1 ", "tools=0 ", "input_tokens=1000 ", "cached_input_tokens=600 ",
		"output_tokens=200 ", "reasoning_tokens=150 ", "usage=reported ",
		"charged_usd=none ", "charge_basis=none ", "ceiling_usd=none ",
		"list_cost_usd=0.004000 ", "provider_cost_usd=0.004000 ",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("usage line lacks %q: %s", want, line)
		}
	}
	if !regexp.MustCompile(` elapsed_s=\d+$`).MatchString(line) {
		t.Errorf("usage line has no elapsed_s: %s", line)
	}
}

func TestExecuteOneShot_UsageLineMarksEstimates(t *testing.T) {
	cfg, mgr := newOneShotUsageFixture(t, "")

	code := -1
	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			code = executeOneShot("Answer briefly.", cfg, mgr, nil, nil, oneShotOptions{
				ModelOverride: "vendor/model",
				AllowedTools:  []string{},
				Limits:        acpLoopLimits{MaxCostUSD: 1},
			})
		})
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	lines := oneShotUsageLines(stderr)
	if len(lines) != 1 {
		t.Fatalf("usage lines = %d, want exactly 1; stderr=%q", len(lines), stderr)
	}
	for _, want := range []string{
		"usage=estimated ", "charge_basis=reserved ", "ceiling_usd=1.000000 ",
		"cached_input_tokens=unknown ", "reasoning_tokens=unknown ",
		"list_cost_usd=unknown ", "provider_cost_usd=unknown ",
	} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("usage line lacks %q: %s", want, lines[0])
		}
	}
}

func TestExecuteOneShot_UsageLineOnceOnEarlyExit(t *testing.T) {
	cfg := config.DefaultConfig()
	var mgr *model.Manager
	code := -1
	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			code = executeOneShot("x", cfg, mgr, nil, nil, oneShotOptions{
				ModelOverride: "vendor/model",
				Limits:        acpLoopLimits{RequiredSourceText: []string{"x"}},
			})
		})
	})
	if code == 0 {
		t.Fatalf("expected failure, stderr=%q", stderr)
	}
	lines := oneShotUsageLines(stderr)
	if len(lines) != 1 || !strings.Contains(lines[0], "requests=0 ") || !strings.Contains(lines[0], "usage=none ") {
		t.Fatalf("usage lines = %q, want one requests=0 usage=none line", lines)
	}
}
