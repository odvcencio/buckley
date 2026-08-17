package tui

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

const subagentCommandUsage = `Subagent commands:
  /agents
  /agent spawn [@name] <task>
  /agent status <run-id>
  /agent messages <run-id>
  /agent send <run-id|id,id|agent:name|all> <message>
  /agent steer <run-id|id,id|agent:name|all> <message>
  /agent cancel <run-id|id,id|agent:name|all> [reason]`

type parsedSubagentCommand struct {
	action string
	params map[string]any
}

func parseSubagentCommand(args []string) (parsedSubagentCommand, error) {
	if len(args) == 0 {
		return parsedSubagentCommand{action: "list", params: map[string]any{"action": "list"}}, nil
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	params := map[string]any{"action": action}
	switch action {
	case "list":
		if len(args) != 1 {
			return parsedSubagentCommand{}, fmt.Errorf("usage: /agent list")
		}
	case "spawn":
		rest := append([]string(nil), args[1:]...)
		if len(rest) > 0 && strings.HasPrefix(rest[0], "@") {
			name := strings.TrimSpace(strings.TrimPrefix(rest[0], "@"))
			if name == "" {
				return parsedSubagentCommand{}, fmt.Errorf("named subagent cannot be empty")
			}
			params["agent"] = name
			rest = rest[1:]
		}
		if len(rest) > 0 && rest[0] == "-" {
			rest = rest[1:]
		}
		task := strings.TrimSpace(strings.Join(rest, " "))
		if task == "" {
			return parsedSubagentCommand{}, fmt.Errorf("usage: /agent spawn [@name] <task>")
		}
		params["initial_task"] = task
	case "status", "messages":
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return parsedSubagentCommand{}, fmt.Errorf("usage: /agent %s <run-id>", action)
		}
		params["id"] = args[1]
	case "send", "steer":
		if len(args) < 3 {
			return parsedSubagentCommand{}, fmt.Errorf("usage: /agent %s <target> <message>", action)
		}
		params["id"] = args[1]
		params["message"] = strings.Join(args[2:], " ")
	case "cancel":
		if len(args) < 2 {
			return parsedSubagentCommand{}, fmt.Errorf("usage: /agent cancel <target> [reason]")
		}
		params["id"] = args[1]
		if len(args) > 2 {
			params["reason"] = strings.Join(args[2:], " ")
		}
	case "help":
		return parsedSubagentCommand{action: "help"}, nil
	default:
		return parsedSubagentCommand{}, fmt.Errorf("unknown subagent action %q", action)
	}
	return parsedSubagentCommand{action: action, params: params}, nil
}

func (c *Controller) handleSubagentCommand(args []string) {
	command, err := parseSubagentCommand(args)
	if err != nil {
		c.app.AddMessage(err.Error()+"\n\n"+subagentCommandUsage, "system")
		return
	}
	if command.action == "help" {
		c.app.AddMessage(subagentCommandUsage, "system")
		return
	}
	sess := c.currentSessionState()
	if sess == nil || sess.ToolRegistry == nil {
		c.app.AddMessage("Subagent controls are unavailable in this session.", "system")
		return
	}
	if _, ok := sess.ToolRegistry.Get("spawn_subagent"); !ok {
		c.app.AddMessage("Subagent controls are unavailable in this session.", "system")
		return
	}
	ctx := builtin.WithUserInitiatedSubagentCommand(context.Background())
	result, err := sess.ToolRegistry.ExecuteWithContext(ctx, "spawn_subagent", command.params)
	if err != nil {
		c.app.AddMessage("Subagent command failed: "+err.Error(), "system")
		return
	}
	message, ok := formatSubagentCommandResult(command.action, result)
	if !ok {
		c.app.AddMessage("Subagent command returned no usable result.", "system")
		return
	}
	c.app.AddMessage(message, "system")
	c.refreshSessionNav()
}

func formatSubagentCommandResult(action string, result *builtin.Result) (string, bool) {
	if result == nil {
		return "", false
	}
	if !result.Success {
		if succeeded, ok := result.Data["succeeded"].(int); ok {
			targets, _ := result.Data["targets"].([]string)
			return fmt.Sprintf("Subagent %s reached %d/%d target(s); %s", action, succeeded, len(targets), result.Error), true
		}
		if strings.TrimSpace(result.Error) != "" {
			return "Subagent command failed: " + result.Error, true
		}
		return "Subagent command failed.", true
	}
	if run, ok := result.Data["run"].(agentcoord.Run); ok {
		return formatSubagentRun(action, run), true
	}
	if runs, ok := result.Data["runs"].([]agentcoord.Run); ok && action == "list" {
		if len(runs) == 0 {
			return "No subagents found in this session.", true
		}
		var output strings.Builder
		fmt.Fprintf(&output, "Subagents (%d):", len(runs))
		for _, run := range runs {
			fmt.Fprintf(&output, "\n- %s", formatSubagentRun("list", run))
		}
		return output.String(), true
	}
	if messages, ok := result.Data["messages"].([]agentcoord.Message); ok && action == "messages" {
		if len(messages) == 0 {
			return "No messages are queued for this subagent.", true
		}
		var output strings.Builder
		fmt.Fprintf(&output, "Subagent messages (%d):", len(messages))
		for _, message := range messages {
			fmt.Fprintf(&output, "\n- [%s/%s] %s", message.Kind, message.Delivery, message.Content)
		}
		return output.String(), true
	}
	if succeeded, ok := result.Data["succeeded"].(int); ok {
		targets, _ := result.Data["targets"].([]string)
		return fmt.Sprintf("Subagent %s reached %d/%d target(s).", action, succeeded, len(targets)), true
	}
	if message, ok := result.Data["message"].(agentcoord.Message); ok {
		return fmt.Sprintf("Subagent %s for %s is %s.", action, message.To, message.Delivery), true
	}
	return "", false
}

func formatSubagentRun(action string, run agentcoord.Run) string {
	agent := strings.TrimSpace(run.Task.Agent)
	if agent == "" {
		agent = "generic"
	}
	switch action {
	case "spawn":
		return fmt.Sprintf("Started %s subagent %s (%s).", agent, run.ID, run.State)
	case "cancel":
		return fmt.Sprintf("Cancellation requested for %s subagent %s.", agent, run.ID)
	default:
		return fmt.Sprintf("%s [%s] %s — %s", run.ID, run.State, agent, run.Task.Task)
	}
}
