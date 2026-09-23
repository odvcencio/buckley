package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model/decisions"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

// Both Decisions gates below share one contract: they only ever narrow an
// otherwise-expensive default (lower reasoning effort, or choose among
// low/medium/high reasoning) -- never widen it, never skip a review
// verdict, and never block on a slow or failing Decisions call. Any
// error, timeout, or disabled configuration falls back silently to the
// caller's non-gated default behavior.

// reviewDepthGateConfig configures the optional Gate 1 review-depth
// Decisions call. See config.DecisionsGatesConfig.ReviewDepth. The zero
// value leaves the gate off.
type reviewDepthGateConfig struct {
	enabled            bool
	forceFullDepth     bool
	apiKey             string
	model              string
	endpoint           string
	timeout            time.Duration
	pricing            decisions.Pricing
	trivialProbability float64
	logPath            string
}

func reviewDepthGateConfigFromConfig(cfg *config.Config) reviewDepthGateConfig {
	if cfg == nil {
		return reviewDepthGateConfig{}
	}
	return reviewDepthGateConfig{
		enabled:  cfg.Decisions.Enabled && cfg.Decisions.Gates.ReviewDepth.Enabled,
		apiKey:   strings.TrimSpace(cfg.Providers.OpenRouter.APIKey),
		model:    cfg.Decisions.Model,
		endpoint: cfg.Decisions.Endpoint,
		timeout:  cfg.Decisions.Timeout,
		pricing: decisions.Pricing{
			InputPerMillion:  cfg.Decisions.Pricing.InputPerMillion,
			OutputPerMillion: cfg.Decisions.Pricing.OutputPerMillion,
		},
		trivialProbability: cfg.Decisions.Gates.ReviewDepth.TrivialProbability,
		logPath:            cfg.Decisions.LogPath,
	}
}

// maxReviewDepthGateDiffBytes bounds how much of the diff Gate 1 sends to
// the Decisions API as evidence. It is intentionally small: the gate only
// needs enough of the diff to judge apparent triviality, not full review
// coverage.
const maxReviewDepthGateDiffBytes = 4000

// applyReviewDepthGate is Gate 1: before an expensive review runs, ask a
// score question (trivial/light/standard/deep) for how deep a review this
// change needs. On a "trivial" answer at or above the configured
// threshold, it lowers opts.reasoningEffort by one step. It never changes
// opts.depth, never changes what evidence a review must collect, and
// never skips a verdict: the reviewer remains the only judge of pass/fail.
func applyReviewDepthGate(ctx context.Context, opts automatedReviewOptions, prCtx *commands.PRContext) automatedReviewOptions {
	gate := opts.decisionsGate
	if !gate.enabled || gate.forceFullDepth || prCtx == nil || prCtx.PR == nil {
		return opts
	}
	client, err := decisions.New(decisions.Config{
		APIKey: gate.apiKey, Model: gate.model, Endpoint: gate.endpoint,
		Timeout: gate.timeout, Pricing: gate.pricing,
	})
	if err != nil {
		return opts
	}

	diff := prCtx.Diff
	truncated := false
	if len(diff) > maxReviewDepthGateDiffBytes {
		diff = diff[:maxReviewDepthGateDiffBytes]
		truncated = true
	}
	state := fmt.Sprintf(
		"Pull request diff stats: %d files changed, +%d/-%d lines. Truncated diff follows (truncated=%t):\n%s",
		prCtx.PR.ChangedFiles, prCtx.PR.Additions, prCtx.PR.Deletions, truncated, diff,
	)

	resp, askErr := client.Ask(ctx, state, map[string]decisions.Question{
		"depth": {
			Type: decisions.TypeScore,
			Instructions: "How deep a code review does this change need? trivial is for changes with " +
				"no behavior risk, such as generated bundles, documentation, comments, or formatting-only " +
				"diffs. deep is for changes with broad blast radius, new logic, or security/data-handling " +
				"impact.",
			Scale: []string{"trivial", "light", "standard", "deep"},
		},
	})

	record := map[string]any{
		"changed_files": prCtx.PR.ChangedFiles, "additions": prCtx.PR.Additions, "deletions": prCtx.PR.Deletions,
		"threshold": gate.trivialProbability,
	}
	if askErr != nil {
		record["error"] = askErr.Error()
		logDecisionGate(gate.logPath, "review_depth", record)
		return opts
	}
	answer := resp.Answers["depth"]
	record["score"] = answer.Score
	record["raw_score"] = answer.Raw
	record["probabilities"] = answer.ScoreProbabilities
	record["cost"] = resp.Usage.Cost
	record["cost_source"] = resp.Usage.CostSource

	if answer.Score == "trivial" && answer.ScoreProbabilities["trivial"] >= gate.trivialProbability {
		lowered := lowerReasoningEffort(opts.reasoningEffort)
		record["reasoning_before"] = opts.reasoningEffort
		record["reasoning_after"] = lowered
		opts.reasoningEffort = lowered
		if !quietMode {
			termOut.Dim("Decisions gate: trivial change (p=%.2f) lowered review reasoning to %s", answer.ScoreProbabilities["trivial"], lowered)
		}
	} else {
		record["reasoning_after"] = opts.reasoningEffort
	}
	logDecisionGate(gate.logPath, "review_depth", record)
	return opts
}

// reasoningEffortScale is ordered low to high. lowerReasoningEffort steps
// exactly one level down; an unrecognized effort is returned unchanged.
var reasoningEffortScale = []string{"minimal", "low", "medium", "high", "xhigh"}

func lowerReasoningEffort(effort string) string {
	index := -1
	for i, level := range reasoningEffortScale {
		if strings.EqualFold(level, effort) {
			index = i
			break
		}
	}
	if index <= 0 {
		if index == 0 {
			return reasoningEffortScale[0]
		}
		return effort
	}
	return reasoningEffortScale[index-1]
}

// reasoningChoiceGate is Gate 2: when a role's reasoning is configured as
// "auto" (or otherwise unresolved), ask a choice question among
// low/medium/high instead of Buckley's fixed default and use the answer.
// It returns ("", false) whenever the gate is off, misconfigured, or the
// Decisions call fails or times out, so the caller falls back to its
// existing default-reasoning resolution unchanged.
func reasoningChoiceGate(ctx context.Context, cfg *config.Config) (string, bool) {
	if cfg == nil || !cfg.Decisions.Enabled || !cfg.Decisions.Gates.ReasoningChoice.Enabled {
		return "", false
	}
	apiKey := strings.TrimSpace(cfg.Providers.OpenRouter.APIKey)
	if apiKey == "" {
		return "", false
	}
	client, err := decisions.New(decisions.Config{
		APIKey: apiKey, Model: cfg.Decisions.Model, Endpoint: cfg.Decisions.Endpoint,
		Timeout: cfg.Decisions.Timeout,
		Pricing: decisions.Pricing{
			InputPerMillion:  cfg.Decisions.Pricing.InputPerMillion,
			OutputPerMillion: cfg.Decisions.Pricing.OutputPerMillion,
		},
	})
	if err != nil {
		return "", false
	}

	resp, askErr := client.Ask(ctx,
		"Buckley is about to run an automated code review for a role configured with reasoning: auto.",
		map[string]decisions.Question{
			"effort": {
				Type: decisions.TypeChoice,
				Instructions: "Choose the reasoning effort a code-review model should use for this run: " +
					"low for a small or routine change, medium for a typical change, high for a large, " +
					"risky, or architecturally significant change.",
				Options: map[string]string{
					"low":    "Minimal reasoning effort; a small or routine change.",
					"medium": "Moderate reasoning effort; a typical change.",
					"high":   "Maximum reasoning effort; a large, risky, or architecturally significant change.",
				},
			},
		},
	)
	record := map[string]any{}
	if askErr != nil {
		record["error"] = askErr.Error()
		logDecisionGate(cfg.Decisions.LogPath, "reasoning_choice", record)
		return "", false
	}
	answer := resp.Answers["effort"]
	record["choice"] = answer.Choice
	record["probabilities"] = answer.ChoiceProbabilities
	record["cost"] = resp.Usage.Cost
	record["cost_source"] = resp.Usage.CostSource
	logDecisionGate(cfg.Decisions.LogPath, "reasoning_choice", record)

	switch answer.Choice {
	case "low", "medium", "high":
		return answer.Choice, true
	default:
		return "", false
	}
}

// decisionsLogPath resolves the configured Decisions calibration log path,
// defaulting to ~/.buckley/decisions.jsonl.
func decisionsLogPath(configured string) string {
	if strings.TrimSpace(configured) != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".buckley", "decisions.jsonl")
}

// logDecisionGate appends one calibration record for one gate decision.
// It is best-effort: a logging failure never fails or blocks the review
// this decision was made for.
func logDecisionGate(configuredPath, gate string, record map[string]any) {
	path := decisionsLogPath(configuredPath)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	record["gate"] = gate
	record["at"] = time.Now().UTC()
	_ = json.NewEncoder(f).Encode(record)
}
