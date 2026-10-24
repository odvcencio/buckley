package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/goalloop"
)

func TestParseStartupOptions_CapFlagsAreOptIn(t *testing.T) {
	opts, err := parseStartupOptions([]string{"-p", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.maxToolCalls != 0 || opts.maxElapsed != 0 || opts.maxOutputTokens != 0 || opts.budgetUSD != 0 {
		t.Fatalf("caps set without flags: %+v", opts)
	}
	opts, err = parseStartupOptions([]string{"--max-tool-calls", "40", "--max-elapsed=90m", "--max-output-tokens", "8000", "--budget", "2.5", "-p", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.maxToolCalls != 40 || opts.maxElapsed != 90*time.Minute || opts.maxOutputTokens != 8000 || opts.budgetUSD != 2.5 {
		t.Fatalf("caps = %+v", opts)
	}
	for _, bad := range [][]string{{"--max-tool-calls", "-1"}, {"--max-elapsed", "soon"}, {"--budget", "-3"}, {"--max-output-tokens"}} {
		if _, err := parseStartupOptions(append(bad, "-p", "hi")); err == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
}

// Read-only intent no longer carries an implicit tool-call, time or output cap.
func TestReadOnlyIntentHasNoImplicitCaps(t *testing.T) {
	limits := applyOneShotTaskIntentDefaults(acpLoopLimits{TaskIntent: agentloop.ReadOnlyIntent})
	if limits.MaxToolCalls != 0 || limits.MaxElapsedSeconds != 0 || limits.MaxOutputTokens != 0 {
		t.Fatalf("read-only defaults = calls %d, seconds %d, tokens %d; want no caps", limits.MaxToolCalls, limits.MaxElapsedSeconds, limits.MaxOutputTokens)
	}
	if limits.MaxReadOnlyCalls != 0 || limits.ReadOnlyActionAt != 0 {
		t.Fatalf("read-only ladder set: %+v", limits)
	}
	// A read-only run with no flags builds a governor that does not stop at 16 calls.
	gov := newACPToolLoopGovernorWithLimits(config.DefaultConfig(), withoutFuse(limits))
	for i := 0; i < 60; i++ {
		if d := gov.Observe("read_file", fmt.Sprintf(`{"path":"f%d"}`, i), fmt.Sprintf("c%d", i), true); d.Stop {
			t.Fatalf("read-only governor stopped at call %d: %s", i+1, d.Reason)
		}
	}
}

func withoutFuse(l acpLoopLimits) acpLoopLimits { return l }

func TestExplicitCapsLimitAOneShot(t *testing.T) {
	cfg := config.DefaultConfig()
	limits := applyOneShotTaskIntentDefaults(applyOneShotCaps(acpLoopLimits{TaskIntent: agentloop.ReadOnlyIntent}, cfg,
		oneShotCaps{MaxToolCalls: 5, MaxElapsed: 90 * time.Second, MaxOutputTokens: 2000, BudgetUSD: 1.5}))
	if limits.MaxToolCalls != 5 || limits.MaxElapsedSeconds != 90 || limits.MaxOutputTokens != 2000 || limits.MaxCostUSD != 1.5 {
		t.Fatalf("flag caps = %+v", limits)
	}
	cfg.Oneshot.MaxToolCalls, cfg.Oneshot.MaxElapsed, cfg.Oneshot.MaxOutputTokens, cfg.Oneshot.BudgetUSD = 7, 30*time.Minute, 3000, 4
	fromConfig := applyOneShotCaps(acpLoopLimits{}, cfg, oneShotCaps{})
	if fromConfig.MaxToolCalls != 7 || fromConfig.MaxElapsedSeconds != 1800 || fromConfig.MaxOutputTokens != 3000 || fromConfig.MaxCostUSD != 4 {
		t.Fatalf("config caps = %+v", fromConfig)
	}
	flagWins := applyOneShotCaps(acpLoopLimits{}, cfg, oneShotCaps{MaxToolCalls: 2})
	if flagWins.MaxToolCalls != 2 {
		t.Fatalf("flag did not override config: %d", flagWins.MaxToolCalls)
	}
	none := applyOneShotCaps(acpLoopLimits{}, config.DefaultConfig(), oneShotCaps{})
	if none.MaxToolCalls != 0 || none.MaxElapsedSeconds != 0 || none.MaxOutputTokens != 0 || none.MaxCostUSD != 0 {
		t.Fatalf("caps appeared with nothing set: %+v", none)
	}
	// The cap reaches the governor and stops it with a message that says how to raise it.
	gov := newACPToolLoopGovernorWithLimits(cfg, acpLoopLimits{MaxToolCalls: 3})
	var stop agentloop.Decision
	for i := 0; i < 6 && !stop.Stop; i++ {
		stop = gov.Observe("read_file", fmt.Sprintf(`{"path":"f%d"}`, i), fmt.Sprintf("c%d", i), true)
	}
	if !stop.Stop || !strings.Contains(stop.Reason, "--max-tool-calls") {
		t.Fatalf("explicit cap decision = %+v", stop)
	}
}

func TestGoalEngineHasNoDefaultReadOnlyCap(t *testing.T) {
	for _, phase := range []string{goalloop.PhaseExecute, goalloop.PhaseVerify} {
		cfg := goalLoopGovernorConfig(config.DefaultConfig(), phase)
		if cfg.MaxReadOnlyCalls != 0 || cfg.ReadOnlyActionAt != 0 || cfg.ReadOnlyWarningAt != 0 {
			t.Fatalf("%s read-only ladder = %+v", phase, cfg)
		}
	}
}

func TestOneShotCapHintNamesTheCapAndHowToRaiseIt(t *testing.T) {
	got := oneShotCapHint(context.DeadlineExceeded, acpLoopLimits{MaxElapsedSeconds: 5400})
	if !strings.Contains(got, "1h30m0s") || !strings.Contains(got, "--max-elapsed") || !strings.Contains(got, "set 0") {
		t.Fatalf("elapsed hint = %q", got)
	}
	if got := oneShotCapHint(context.DeadlineExceeded, acpLoopLimits{}); got != "" {
		t.Fatalf("a deadline with no elapsed cap must not blame one: %q", got)
	}
	spend := oneShotCapHint(errors.New("model spend reached the explicit $2.0000 child limit"), acpLoopLimits{MaxCostUSD: 2})
	if !strings.Contains(spend, "--budget") {
		t.Fatalf("spend hint = %q", spend)
	}
}
