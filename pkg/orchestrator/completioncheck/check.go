// Package completioncheck governs a bounded continuation recommendation.
// It evaluates supplied text; it does not certify repository state or run tools.
package completioncheck

import (
	"context"
	_ "embed"
	"fmt"
	"math"
	"strings"

	"m31labs.dev/arbiter"
)

const Model = "typesafe/jev-1.13"

type Input struct {
	Request  string `json:"request"`
	Response string `json:"response"`
}

type Evidence struct {
	Action      float64 `json:"action"`
	Unfinished  float64 `json:"unfinished"`
	Authorized  float64 `json:"authorized"`
	NeedsUser   float64 `json:"needs_user"`
	Model       string  `json:"model"`
	InputTokens int     `json:"input_tokens"`
	Cost        float64 `json:"cost"`
}

type Judge interface {
	Judge(context.Context, Input) (Evidence, error)
}

type Result struct {
	Action   string   `json:"action"`
	Reason   string   `json:"reason"`
	Evidence Evidence `json:"evidence"`
}

//go:embed policy.arb
var policy []byte

func Check(ctx context.Context, judge Judge, input Input) (Result, error) {
	if strings.TrimSpace(input.Request) == "" || strings.TrimSpace(input.Response) == "" {
		return Result{}, fmt.Errorf("completion check requires request and response")
	}
	// Reject instead of truncating away a constraint or a final blocker.
	if len(input.Request) > 8192 || len(input.Response) > 12288 {
		return Result{}, fmt.Errorf("completion check input exceeds bounded context")
	}
	e, err := judge.Judge(ctx, input)
	if err != nil {
		return Result{}, err
	}
	return Decide(e)
}

func Decide(e Evidence) (Result, error) {
	for _, v := range []float64{e.Action, e.Unfinished, e.Authorized, e.NeedsUser} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
			return Result{}, fmt.Errorf("invalid completion probability")
		}
	}
	if e.Model != Model && !strings.HasPrefix(e.Model, Model+"-") {
		return Result{}, fmt.Errorf("unexpected decision model")
	}
	p, err := arbiter.Compile(policy)
	if err != nil {
		return Result{}, fmt.Errorf("compile completion policy: %w", err)
	}
	r, err := p.Strategies.Evaluate("completion", map[string]any{"evidence": map[string]any{
		"action": e.Action, "unfinished": e.Unfinished, "authorized": e.Authorized, "needs_user": e.NeedsUser,
	}})
	if err != nil {
		return Result{}, fmt.Errorf("evaluate completion policy: %w", err)
	}
	action, ok := r.Params["action"].(string)
	if !ok || (action != "continue" && action != "stop") {
		return Result{}, fmt.Errorf("invalid completion outcome")
	}
	reason, _ := r.Params["reason"].(string)
	return Result{Action: action, Reason: reason, Evidence: e}, nil
}
