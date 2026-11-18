package experiment

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/parallel"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// This opt-in experiment compares schema exposure using the real executor and
// tools. Only the synthetic repository below is available to the model.
func TestToolDiscoveryLive_PreloadedGitLog(t *testing.T) {
	if os.Getenv("BUCKLEY_DISCOVERY_LIVE") != "1" {
		t.Skip("set BUCKLEY_DISCOVERY_LIVE=1 to use the OpenAI API")
	}
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Fatal("OPENAI_API_KEY is required")
	}
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("cedar-orbit-29\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"add", "note.txt"},
		{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "Amber telescope"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("prepare fixture: %v: %s", err, out)
		}
	}
	executor := newOpenAIExperimentExecutor(t, "https://api.openai.com/v1", func(cfg *config.Config) {
		cfg.Providers.OpenAI.APIKey = key
		cfg.Experiment.MaxTokensPerRun = 30000
	})
	tests := []struct{ name, prompt, evidenceTool, answer string }{
		{"history", "Use git_log to inspect the newest commit. Return just its subject.", "git_log", "Amber telescope"},
		{"read", "Use read_file to read note.txt. Return just the text in the file.", "read_file", "cedar-orbit-29"},
		{"empty", "Use search_text to search note.txt for missing-planet-42. Return NONE if there are no matches.", "search_text", "NONE"},
	}
	type summary struct {
		verified, exact, errors, tokens int
		cost                            float64
		unknownCost                     bool
	}
	totals := [2]summary{}
	for sample := range 3 {
		for _, tc := range tests {
			for offset := range 2 {
				variant := (sample + offset) % 2
				name := [2]string{"discovery", "preloaded"}[variant]
				t.Run(tc.name+"/"+name, func(t *testing.T) {
					registry := executor.buildRegistry(&parallel.AgentTask{}, repo)
					if variant == 1 {
						core := append([]string{"git_log"}, tool.DefaultDiscoveryTools...)
						registry.EnableDynamicDiscovery(core)
					}
					registry.Filter(func(candidate tool.Tool) bool {
						switch candidate.Name() {
						case "discover_tools", "read_file", "search_text", "find_files", "git_log":
							return true
						default:
							return false
						}
					})
					start := time.Now()
					var firstUseful time.Duration
					registry.Use(func(next tool.Executor) tool.Executor {
						return func(call *tool.ExecutionContext) (*builtin.Result, error) {
							result, err := next(call)
							if firstUseful == 0 && call.ToolName == tc.evidenceTool && err == nil && result != nil && result.Success {
								if tc.name != "empty" || (call.Params["query"] == "missing-planet-42" && result.Data["count"] == 0) {
									firstUseful = time.Since(start)
								}
							}
							return result, err
						}
					})
					ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
					defer cancel()
					run, err := executor.runConversation(ctx, "gpt-4.1-mini", registry, tc.prompt, "", "0", "300")
					metrics := run.metrics
					exact := strings.TrimSpace(run.output) == tc.answer
					verified := err == nil && firstUseful > 0 && regexp.MustCompile(`\b`+regexp.QuoteMeta(tc.answer)+`\b`).MatchString(run.output)
					total := &totals[variant]
					if verified {
						total.verified++
					}
					if exact {
						total.exact++
					}
					if err != nil {
						total.errors++
					}
					total.tokens += metrics.promptTokens + metrics.completionTokens
					total.cost += metrics.totalCost
					total.unknownCost = total.unknownCost || metrics.costUnknown || err != nil
					t.Logf("sample=%d verified=%t exact=%t tokens=%d cost_usd=%.6f cost_unknown=%t first_useful_ms=%d elapsed_ms=%d tool_calls=%d output=%q error=%v",
						sample+1, verified, exact, metrics.promptTokens+metrics.completionTokens, metrics.totalCost, metrics.costUnknown,
						firstUseful.Milliseconds(), time.Since(start).Milliseconds(), metrics.toolCalls, run.output, err)
				})
			}
		}
	}
	for i, total := range totals {
		t.Logf("variant=%s verified=%d/9 exact=%d/9 provider_errors=%d tokens=%d total_cost_usd=%.6f cost_incomplete=%t",
			[2]string{"discovery", "preloaded"}[i], total.verified, total.exact, total.errors, total.tokens, total.cost, total.unknownCost)
	}
	if totals[0].errors == 9 && totals[1].errors == 9 {
		t.Fatal("no live samples reached a conclusive provider response")
	}
}
