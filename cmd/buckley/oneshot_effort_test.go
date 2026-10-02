package main

import (
	"context"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
)

func TestOneshotEffort_Precedence(t *testing.T) {
	for _, command := range []string{"commit", "pr", "review"} {
		for _, tt := range []struct {
			name, flag, specific, shared, want string
		}{
			{"flag", "max", "high", "medium", "max"},
			{"specific env", "", "high", "medium", "high"},
			{"oneshot env", "", "", "medium", "medium"},
			{"config", "", "", "", "low"},
			{"normalized", " XHIGH ", "", "", "xhigh"},
		} {
			t.Run(command+"/"+tt.name, func(t *testing.T) {
				t.Setenv("BUCKLEY_EFFORT_"+strings.ToUpper(command), tt.specific)
				t.Setenv("BUCKLEY_ONESHOT_EFFORT", tt.shared)
				effort, err := resolveOneshotEffort(command, tt.flag)
				if err != nil {
					t.Fatal(err)
				}
				cfg := config.DefaultConfig()
				cfg.Models.Reasoning = "low"
				cfg.Buckbot.Reasoning = "low"
				resolved := configWithOneshotEffort(cfg, effort)
				var got string
				if command == "review" {
					got = resolveReviewReasoningEffort(context.Background(), resolved, reviewReasoningChecker{supported: true}, "reasoning-model", effort)
					if reviewReasoningIsAdaptive(resolved, effort) {
						t.Fatal("explicit or configured effort must survive adaptive review planning")
					}
				} else {
					profile := resolveOneshotRequestProfile(command, resolved, oneshotProfileReasoningChecker{"reasoning-model": true}, "reasoning-model")
					if profile.Reasoning == nil {
						t.Fatal("API profile dropped effort")
					}
					got = profile.Reasoning.Effort
					if cliEffort := cliReasoningEffort(resolved, oneshot.CLIBackendCodex); cliEffort != tt.want {
						t.Fatalf("Codex effort = %q, want %q", cliEffort, tt.want)
					}
				}
				if got != tt.want {
					t.Fatalf("effort = %q, want %q", got, tt.want)
				}
				if cfg.Models.Reasoning != "low" || cfg.Buckbot.Reasoning != "low" {
					t.Fatal("command override changed the original config")
				}
			})
		}
	}
}

func TestOneshotEffort_CommandParsing(t *testing.T) {
	parsers := map[string]func([]string) (string, error){
		"commit": func(args []string) (string, error) {
			opts, err := parseCommitCommandOptions(args)
			return opts.effort, err
		},
		"pr": func(args []string) (string, error) {
			opts, err := parsePRCommandOptions(args)
			return opts.effort, err
		},
		"pr merge": func(args []string) (string, error) {
			opts, err := parsePRMergeOptions(append(args, "123"))
			return opts.effort, err
		},
		"review": func(args []string) (string, error) {
			opts, err := parseReviewCommandOptions(args)
			return opts.effort, err
		},
		"review-pr": func(args []string) (string, error) {
			opts, err := parseReviewPRCommandOptions(append([]string{"123"}, args...))
			return opts.effort, err
		},
	}
	for name, parse := range parsers {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BUCKLEY_EFFORT_COMMIT", "high")
			t.Setenv("BUCKLEY_EFFORT_PR", "high")
			t.Setenv("BUCKLEY_EFFORT_REVIEW", "high")
			t.Setenv("BUCKLEY_ONESHOT_EFFORT", "low")
			for _, value := range []string{"low", "medium", "high", "xhigh", "max"} {
				for _, args := range [][]string{{"-effort", value}, {"--effort=" + value}} {
					got, err := parse(args)
					if err != nil || got != value {
						t.Fatalf("parse(%v) = %q, %v", args, got, err)
					}
				}
			}
			if got, err := parse(nil); err != nil || got != "high" {
				t.Fatalf("env default = %q, %v", got, err)
			}
			for _, value := range []string{"auto", "off", "minimal", "extreme"} {
				if _, err := parse([]string{"-effort", value}); err == nil || !strings.Contains(err.Error(), "use low, medium, high, xhigh, or max") {
					t.Fatalf("invalid effort %q: %v", value, err)
				}
			}
		})
	}
}

func TestResolveOneshotEffort_InvalidEnv(t *testing.T) {
	for _, env := range []string{"BUCKLEY_EFFORT_COMMIT", "BUCKLEY_ONESHOT_EFFORT"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("BUCKLEY_EFFORT_COMMIT", "")
			t.Setenv("BUCKLEY_ONESHOT_EFFORT", "")
			t.Setenv(env, "extreme")
			if _, err := resolveOneshotEffort("commit", ""); err == nil || !strings.Contains(err.Error(), "unsupported reasoning effort") {
				t.Fatalf("invalid environment effort: %v", err)
			}
			if got, err := resolveOneshotEffort("commit", "high"); err != nil || got != "high" {
				t.Fatalf("explicit flag did not override invalid env: %q, %v", got, err)
			}
		})
	}
}

func TestResolveCLIModelID_DefaultAndExplicit(t *testing.T) {
	for command, resolve := range map[string]func(string, *config.Config, string) string{"commit": resolveCommitModelID, "pr": resolvePRModelID} {
		t.Run(command, func(t *testing.T) {
			env := "BUCKLEY_MODEL_" + strings.ToUpper(command)
			t.Setenv(env, "")
			cfg := config.DefaultConfig()
			cfg.Models.DefaultProvider = "codex"
			cfg.Models.Utility.Commit = config.DefaultCodexModel
			cfg.Models.Utility.PR = config.DefaultCodexModel
			cfg.Providers.Codex.Models = []string{config.DefaultCodexModel}
			for _, config := range []*config.Config{nil, cfg} {
				if got := resolve("", config, oneshot.CLIBackendCodex); got != "" {
					t.Fatalf("model = %q, want Codex CLI configured default", got)
				}
			}
			t.Setenv(env, "codex/gpt-env")
			if got := resolve("", cfg, oneshot.CLIBackendCodex); got != "gpt-env" {
				t.Fatalf("env model = %q", got)
			}
			if got := resolve("codex/gpt-6.1-sol", cfg, oneshot.CLIBackendCodex); got != "gpt-6.1-sol" {
				t.Fatalf("explicit model = %q", got)
			}
		})
	}
}

func TestReviewEffort_ExplicitSurvivesAdaptivePlan(t *testing.T) {
	cfg := configWithOneshotEffort(config.DefaultConfig(), "max")
	policy := defaultAutomatedReviewOptions(cfg)
	policy.reasoningEffort = resolveReviewReasoningEffort(context.Background(), cfg, reviewReasoningChecker{supported: true}, "codex/gpt-6.1-sol", "max")
	plan := policy.withExecutionPlan(reviewExecutionPlan{reasoningEffort: "medium", sizeClass: "broad"})
	if plan.reasoningEffort != "max" {
		t.Fatalf("review plan lowered explicit effort to %q", plan.reasoningEffort)
	}
}

func TestReviewEffort_RuntimeOverridesLegacySuffixAndDepthGate(t *testing.T) {
	t.Setenv(envBuckleyDataDir, t.TempDir())
	previous := modelOverrideFlag
	modelOverrideFlag = "codex/gpt-5.6-sol-low"
	t.Cleanup(func() { modelOverrideFlag = previous })
	cfg := config.DefaultConfig()
	cfg.Providers.Codex.Enabled = true
	cfg.Providers.Codex.Models = []string{"codex/gpt-5.6-sol"}
	cfg.Models.DefaultProvider = "codex"
	cfg.Buckbot.Reasoning = "auto"
	cfg.Decisions.Gates.ReviewDepth.Enabled = true
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newReviewCommandRuntime(context.Background(), cfg, mgr, nil, "max")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.reasoningEffort != "max" || runtime.policy.adaptiveReasoning || !runtime.policy.decisionsGate.forceFullDepth {
		t.Fatalf("explicit effort lost to review policy: effort=%q adaptive=%t forceFullDepth=%t", runtime.reasoningEffort, runtime.policy.adaptiveReasoning, runtime.policy.decisionsGate.forceFullDepth)
	}
}
