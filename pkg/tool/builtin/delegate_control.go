package builtin

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/agentcoord"
)

type subagentControlFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

func (t *SubagentTool) messageTargets(ctx context.Context, coordinator agentcoord.Coordinator, action string, params map[string]any) (*Result, error) {
	selector := delegateStringParam(params, "id")
	targets, grouped, err := t.resolveTargets(ctx, coordinator, selector)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	content := delegateStringParam(params, "message")
	if content == "" {
		return &Result{Success: false, Error: "subagent message content is required"}, nil
	}
	messages := make([]agentcoord.Message, 0, len(targets))
	failures := make([]subagentControlFailure, 0)
	for _, id := range targets {
		var message agentcoord.Message
		if action == "steer" {
			message, err = coordinator.Steer(ctx, id, content)
		} else {
			message, err = coordinator.Send(ctx, agentcoord.Message{RunID: id, To: id, From: "parent", Kind: "message", Content: content})
		}
		if err != nil {
			failures = append(failures, subagentControlFailure{ID: id, Error: err.Error()})
			continue
		}
		messages = append(messages, message)
	}
	if !grouped {
		if len(failures) > 0 {
			return &Result{Success: false, Error: failures[0].Error}, nil
		}
		message := messages[0]
		label := "Message"
		if action == "steer" {
			label = "Steering"
		}
		return &Result{Success: true, Data: map[string]any{"message": message}, DisplayData: map[string]any{"summary": fmt.Sprintf("%s %s for subagent %s", label, message.Delivery, message.To)}, ShouldAbridge: true}, nil
	}
	return subagentBatchResult(action, selector, targets, messages, nil, failures), nil
}

func (t *SubagentTool) cancelTargets(ctx context.Context, coordinator agentcoord.Coordinator, params map[string]any) (*Result, error) {
	selector := delegateStringParam(params, "id")
	targets, grouped, err := t.resolveTargets(ctx, coordinator, selector)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	runs := make([]agentcoord.Run, 0, len(targets))
	failures := make([]subagentControlFailure, 0)
	for _, id := range targets {
		run, cancelErr := coordinator.Cancel(ctx, id, delegateStringParam(params, "reason"))
		if cancelErr != nil {
			failures = append(failures, subagentControlFailure{ID: id, Error: cancelErr.Error()})
			continue
		}
		runs = append(runs, run)
	}
	if !grouped {
		if len(failures) > 0 {
			return &Result{Success: false, Error: failures[0].Error}, nil
		}
		result := subagentRunResult(runs[0])
		result.DisplayData = map[string]any{"summary": "Subagent cancellation requested"}
		return result, nil
	}
	return subagentBatchResult("cancel", selector, targets, nil, runs, failures), nil
}

func (t *SubagentTool) resolveTargets(ctx context.Context, coordinator agentcoord.Coordinator, selector string) ([]string, bool, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, false, fmt.Errorf("subagent run selector is required")
	}
	lower := strings.ToLower(selector)
	groupName := ""
	grouped := false
	switch {
	case lower == "all" || lower == "active":
		grouped = true
	case strings.HasPrefix(lower, "agent:"):
		grouped = true
		groupName = strings.TrimSpace(selector[len("agent:"):])
	case strings.HasPrefix(lower, "role:"):
		grouped = true
		groupName = strings.TrimSpace(selector[len("role:"):])
	case strings.Contains(selector, ","):
		grouped = true
		ids := delegateStringSliceParam(map[string]any{"ids": selector}, "ids")
		if len(ids) == 0 {
			return nil, true, fmt.Errorf("subagent run selector is required")
		}
		if err := t.requireTargetsInCurrentSession(ctx, coordinator, ids); err != nil {
			return nil, true, err
		}
		return ids, true, nil
	default:
		if err := t.requireTargetsInCurrentSession(ctx, coordinator, []string{selector}); err != nil {
			return nil, false, err
		}
		return []string{selector}, false, nil
	}
	if grouped && lower != "all" && lower != "active" && groupName == "" {
		return nil, true, fmt.Errorf("subagent agent selector requires a name")
	}
	session, _, _, _ := t.runtimeContext()
	runs, err := coordinator.List(ctx, agentcoord.RunFilter{ParentSessionID: session})
	if err != nil {
		return nil, true, err
	}
	targets := make([]string, 0, len(runs))
	for _, run := range runs {
		if run.State.Terminal() {
			continue
		}
		if groupName != "" && !strings.EqualFold(strings.TrimSpace(run.Task.Agent), groupName) {
			continue
		}
		targets = append(targets, run.ID)
	}
	if len(targets) == 0 {
		return nil, true, fmt.Errorf("no active subagents match selector %q", selector)
	}
	return targets, true, nil
}

func (t *SubagentTool) requireTargetsInCurrentSession(ctx context.Context, coordinator agentcoord.Coordinator, ids []string) error {
	for _, id := range ids {
		if _, err := t.runInCurrentSession(ctx, coordinator, id); err != nil {
			return err
		}
	}
	return nil
}

func subagentBatchResult(action, selector string, targets []string, messages []agentcoord.Message, runs []agentcoord.Run, failures []subagentControlFailure) *Result {
	succeeded := len(targets) - len(failures)
	data := map[string]any{
		"action":    action,
		"selector":  selector,
		"targets":   targets,
		"succeeded": succeeded,
		"failed":    failures,
	}
	if len(messages) > 0 {
		data["messages"] = messages
	}
	if len(runs) > 0 {
		data["runs"] = runs
	}
	result := &Result{
		Success:       len(failures) == 0,
		Data:          data,
		DisplayData:   map[string]any{"summary": fmt.Sprintf("Subagent %s reached %d/%d target(s)", action, succeeded, len(targets))},
		ShouldAbridge: true,
	}
	if len(failures) > 0 {
		result.Error = fmt.Sprintf("subagent %s failed for %d/%d target(s)", action, len(failures), len(targets))
	}
	return result
}
