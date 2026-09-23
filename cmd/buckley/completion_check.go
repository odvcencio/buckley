package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model/decisions"
	"m31labs.dev/buckley/pkg/orchestrator/completioncheck"
)

const completionNudgePrefix = "[Buckley completion check] "

type completionHookEvent struct {
	SessionID      string `json:"session_id"`
	TurnID         string `json:"turn_id"`
	Event          string `json:"hook_event_name"`
	Model          string `json:"model"`
	PermissionMode string `json:"permission_mode"`
	Prompt         string `json:"prompt"`
	Response       string `json:"last_assistant_message"`
	Active         bool   `json:"stop_hook_active"`
}

type completionHookState struct {
	TurnID    string    `json:"turn_id"`
	Prompt    string    `json:"prompt"`
	Created   time.Time `json:"created"`
	Attempted bool      `json:"attempted"`
}

func runCompletionCheckCommand(args []string) error {
	fs := flag.NewFlagSet("completion-check", flag.ContinueOnError)
	hook := fs.Bool("hook", false, "Read a Codex lifecycle event; emit only hook JSON")
	stateDir := fs.String("state-dir", "", "Override private hook state directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("completion-check accepts JSON on stdin")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if *stateDir == "" {
		*stateDir = filepath.Join(home, ".buckley", "completion-guard")
	}
	judge := func(ctx context.Context, in completioncheck.Input) (completioncheck.Result, error) {
		// Ignore project configuration and preserve the fixed provider/privacy route.
		cfg, err := config.LoadFromPath(filepath.Join(home, ".buckley", "config.yaml"))
		if err != nil {
			return completioncheck.Result{}, fmt.Errorf("Buckley user configuration unavailable")
		}
		j, err := completioncheck.NewOpenRouterJudge(cfg.Providers.OpenRouter.APIKey, decisions.Pricing{
			InputPerMillion:  cfg.Decisions.Pricing.InputPerMillion,
			OutputPerMillion: cfg.Decisions.Pricing.OutputPerMillion,
		})
		if err != nil {
			return completioncheck.Result{}, err
		}
		return completioncheck.Check(ctx, j, in)
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 32769))
	if err != nil || len(raw) > 32768 {
		if *hook {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{})
		}
		return fmt.Errorf("completion input unreadable or too large")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 13*time.Second)
	defer cancel()
	if *hook {
		var event completionHookEvent
		out := map[string]any{}
		if json.Unmarshal(raw, &event) == nil && os.Getenv("BUCKLEY_COMPLETION_GUARD") != "off" {
			out = handleCompletionHook(ctx, *stateDir, event, judge)
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	var input completioncheck.Input
	if err := json.Unmarshal(raw, &input); err != nil {
		return err
	}
	result, err := judge(ctx, input)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

type completionJudge func(context.Context, completioncheck.Input) (completioncheck.Result, error)

func handleCompletionHook(ctx context.Context, dir string, e completionHookEvent, judge completionJudge) map[string]any {
	quiet := map[string]any{}
	if e.SessionID == "" {
		return quiet
	}
	if _, err := os.Stat(filepath.Join(dir, "disabled")); err == nil {
		return quiet
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return quiet
	}
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(e.SessionID)))
	path := filepath.Join(dir, id+".json")
	cancelPath := filepath.Join(dir, id+".cancel")
	if e.Event == "Interrupt" || e.Event == "SessionEnd" {
		_ = os.WriteFile(cancelPath, []byte("stop"), 0600)
		_ = os.Remove(path)
		return quiet
	}
	if e.Event != "UserPromptSubmit" && e.Event != "Stop" {
		return quiet
	}
	if e.Event == "UserPromptSubmit" && strings.HasPrefix(e.Prompt, completionNudgePrefix) {
		return quiet
	}
	// Invalidate an in-flight check before trying to record a new human prompt.
	if e.Event == "UserPromptSubmit" {
		_ = os.WriteFile(cancelPath, []byte("new prompt"), 0600)
	}
	lock := filepath.Join(dir, id+".lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		return quiet
	}
	defer os.Remove(lock)
	if e.Event == "UserPromptSubmit" {
		_ = os.Remove(path)
		if !completionAstra(e.Model) || e.TurnID == "" || e.PermissionMode == "plan" || strings.TrimSpace(e.Prompt) == "" || len(e.Prompt) > 8192 {
			return quiet
		}
		s := completionHookState{TurnID: e.TurnID, Prompt: e.Prompt, Created: time.Now()}
		if writeCompletionState(path, s) != nil {
			return quiet
		}
		_ = os.Remove(cancelPath)
		return quiet
	}
	if !completionAstra(e.Model) || e.Active || e.TurnID == "" || e.PermissionMode == "plan" {
		return quiet
	}
	if _, err := os.Stat(cancelPath); err == nil {
		return quiet
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return quiet
	}
	var s completionHookState
	if json.Unmarshal(raw, &s) != nil || s.Attempted || s.TurnID != e.TurnID || time.Since(s.Created) > 12*time.Hour || time.Until(s.Created) > time.Minute {
		return quiet
	}
	// Consume the attempt before inference, including on timeout or failure.
	s.Attempted = true
	if writeCompletionState(path, s) != nil {
		return quiet
	}
	result, err := judge(ctx, completioncheck.Input{Request: s.Prompt, Response: e.Response})
	record := map[string]any{"at": time.Now().UTC(), "turn_id": e.TurnID, "result": result}
	if err != nil {
		record["error"] = err.Error()
	}
	if f, openErr := os.OpenFile(filepath.Join(dir, "decisions.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); openErr == nil {
		_ = json.NewEncoder(f).Encode(record)
		_ = f.Close()
	}
	if _, cancelled := os.Stat(cancelPath); cancelled == nil {
		return quiet
	}
	if err != nil || ctx.Err() != nil || result.Action != "continue" {
		return quiet
	}
	return map[string]any{"decision": "block", "reason": completionNudgePrefix + result.Reason +
		" Recheck the original request against the work already completed. Finish the specific remaining authorized step and its required verification. Reuse existing evidence; do not start a broad audit, add scope, repeat passing checks, or bypass approvals, user stops, or execution limits. If the work is complete or genuinely blocked, explain that briefly and stop. This automated check can be mistaken."}
}

func completionAstra(model string) bool {
	return model == "gpt-6-astra" || strings.HasPrefix(model, "gpt-6-astra-")
}

func writeCompletionState(path string, s completionHookState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".completion-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
