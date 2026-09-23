package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/orchestrator/completioncheck"
)

func TestCompletionHook_OncePerPrompt(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	calls := 0
	judge := func(context.Context, completioncheck.Input) (completioncheck.Result, error) {
		calls++
		return completioncheck.Result{Action: "continue", Reason: "Missing implementation."}, nil
	}
	e := completionHookEvent{SessionID: "session", TurnID: "turn", Model: "gpt-6-astra", Event: "UserPromptSubmit", Prompt: "Implement the fix"}
	if out := handleCompletionHook(ctx, dir, e, judge); len(out) != 0 || calls != 0 {
		t.Fatal("prompt must not infer")
	}
	e.Event = "Stop"
	e.Response = "I can implement this next."
	out := handleCompletionHook(ctx, dir, e, judge)
	if out["decision"] != "block" || calls != 1 {
		t.Fatalf("got %v calls %d", out, calls)
	}
	if out := handleCompletionHook(ctx, dir, e, judge); len(out) != 0 || calls != 1 {
		t.Fatal("duplicate inference")
	}
	e.Event = "UserPromptSubmit"
	e.TurnID = "synthetic"
	e.Prompt = out["reason"].(string)
	handleCompletionHook(ctx, dir, e, judge)
	e.Event = "Stop"
	e.Active = true
	if out := handleCompletionHook(ctx, dir, e, judge); len(out) != 0 || calls != 1 {
		t.Fatal("recursive inference")
	}
	e.Event = "UserPromptSubmit"
	e.TurnID = "new-human"
	e.Prompt = "Now fix the second bug"
	e.Active = false
	handleCompletionHook(ctx, dir, e, judge)
	e.Event = "Stop"
	if out := handleCompletionHook(ctx, dir, e, judge); out["decision"] != "block" || calls != 2 {
		t.Fatal("new prompt should get new attempt")
	}
}

func TestCompletionHook_StopConditions(t *testing.T) {
	for _, scenario := range []string{"other model", "plan", "active", "wrong turn", "interrupt", "session end", "disabled", "oversize prompt", "provider failure", "cancel during call", "new prompt during call"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			ctx := context.Background()
			calls := 0
			e := completionHookEvent{SessionID: "session", TurnID: "turn", Model: "gpt-6-astra", Event: "UserPromptSubmit", Prompt: "Implement the fix"}
			judge := func(context.Context, completioncheck.Input) (completioncheck.Result, error) {
				calls++
				if scenario == "provider failure" {
					return completioncheck.Result{}, errors.New("unavailable")
				}
				if scenario == "cancel during call" || scenario == "new prompt during call" {
					interrupt := e
					interrupt.Event = "Interrupt"
					if scenario == "new prompt during call" {
						interrupt.Event = "UserPromptSubmit"
						interrupt.TurnID = "new"
						interrupt.Prompt = "Stop, explain instead"
					}
					handleCompletionHook(ctx, dir, interrupt, nil)
				}
				return completioncheck.Result{Action: "continue"}, nil
			}
			if scenario == "oversize prompt" {
				e.Prompt = strings.Repeat("x", 8193)
			}
			handleCompletionHook(ctx, dir, e, judge)
			e.Event = "Stop"
			e.Response = "Not implemented yet"
			switch scenario {
			case "other model":
				e.Model = "gpt-5.6-sol"
			case "plan":
				e.PermissionMode = "plan"
			case "active":
				e.Active = true
			case "wrong turn":
				e.TurnID = "wrong"
			case "interrupt", "session end":
				x := e
				x.Event = "Interrupt"
				if scenario == "session end" {
					x.Event = "SessionEnd"
				}
				handleCompletionHook(ctx, dir, x, judge)
			case "disabled":
				if err := os.WriteFile(filepath.Join(dir, "disabled"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if out := handleCompletionHook(ctx, dir, e, judge); len(out) != 0 {
				t.Fatalf("unexpected nudge %v", out)
			}
			want := 0
			if scenario == "provider failure" || scenario == "cancel during call" || scenario == "new prompt during call" {
				want = 1
			}
			if calls != want {
				t.Fatalf("calls %d want %d", calls, want)
			}
		})
	}
}
