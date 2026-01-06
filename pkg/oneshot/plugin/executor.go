package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// Executor runs plugin commands.
type Executor struct {
	invoker *oneshot.DefaultInvoker
	workDir string
}

// NewExecutor creates a plugin executor.
func NewExecutor(invoker *oneshot.DefaultInvoker, workDir string) *Executor {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	return &Executor{
		invoker: invoker,
		workDir: workDir,
	}
}

// ExecuteResult contains the result of plugin execution.
type ExecuteResult struct {
	// Output is the rendered output
	Output string

	// RawResult is the raw tool call result from the model
	RawResult map[string]interface{}

	// ContextAudit shows what context was gathered
	ContextAudit *transparency.ContextAudit

	// Trace contains model call details
	Trace *transparency.Trace

	// Error if execution failed
	Error error
}

// Execute runs a plugin with the given flags.
func (e *Executor) Execute(ctx context.Context, def *Definition, flags map[string]string) (*ExecuteResult, error) {
	result := &ExecuteResult{}

	// 1. Gather context
	gatherer := NewContextGatherer(e.workDir, flags)
	contextStr, err := gatherer.Gather(def.Context)
	if err != nil {
		return nil, fmt.Errorf("gather context: %w", err)
	}
	result.ContextAudit = gatherer.Audit()

	// 2. Build prompts
	systemPrompt := buildPluginSystemPrompt(def)
	userPrompt := buildPluginUserPrompt(def, contextStr, flags)

	// 3. Invoke model with tool
	toolDef := def.ToToolDefinition()
	invokeResult, trace, err := e.invoker.Invoke(ctx, systemPrompt, userPrompt, toolDef, result.ContextAudit)
	if err != nil {
		return nil, fmt.Errorf("invoke model: %w", err)
	}

	result.Trace = trace

	// 4. Parse tool result
	if invokeResult.ToolCall == nil {
		result.Error = fmt.Errorf("model did not call the expected tool")
		return result, nil
	}

	var toolResult map[string]interface{}
	if err := json.Unmarshal(invokeResult.ToolCall.Arguments, &toolResult); err != nil {
		result.Error = fmt.Errorf("parse tool result: %w", err)
		return result, nil
	}
	result.RawResult = toolResult

	// 5. Render output template
	engine, err := GetTemplateEngine(def.Output.Template)
	if err != nil {
		result.Error = err
		return result, nil
	}

	// Add flags to template data
	for k, v := range flags {
		toolResult["flag_"+k] = v
	}

	output, err := engine.Render(def.Output.Format, toolResult)
	if err != nil {
		result.Error = fmt.Errorf("render output: %w", err)
		return result, nil
	}
	result.Output = output

	return result, nil
}

// ExecuteAction runs a post-execution action.
func (e *Executor) ExecuteAction(action ActionDef, result *ExecuteResult) error {
	switch action.Command {
	case "prepend_file":
		return e.actionPrependFile(action, result)
	case "append_file":
		return e.actionAppendFile(action, result)
	case "write_file":
		return e.actionWriteFile(action, result)
	case "clipboard":
		return e.actionClipboard(action, result)
	case "exec":
		return e.actionExec(action, result)
	default:
		return fmt.Errorf("unknown action command: %s", action.Command)
	}
}

func (e *Executor) actionPrependFile(action ActionDef, result *ExecuteResult) error {
	path := action.Args["path"]
	if path == "" {
		return fmt.Errorf("prepend_file requires path arg")
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(e.workDir, path)
	}

	// Read existing content
	existing, _ := os.ReadFile(path) // Ignore error - file might not exist

	// Prepend new content
	content := result.Output + "\n\n" + string(existing)

	return os.WriteFile(path, []byte(content), 0644)
}

func (e *Executor) actionAppendFile(action ActionDef, result *ExecuteResult) error {
	path := action.Args["path"]
	if path == "" {
		return fmt.Errorf("append_file requires path arg")
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(e.workDir, path)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString("\n\n" + result.Output)
	return err
}

func (e *Executor) actionWriteFile(action ActionDef, result *ExecuteResult) error {
	path := action.Args["path"]
	if path == "" {
		return fmt.Errorf("write_file requires path arg")
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(e.workDir, path)
	}

	return os.WriteFile(path, []byte(result.Output), 0644)
}

func (e *Executor) actionClipboard(action ActionDef, result *ExecuteResult) error {
	// Try different clipboard commands
	cmds := [][]string{
		{"pbcopy"},                           // macOS
		{"xclip", "-selection", "clipboard"}, // Linux X11
		{"xsel", "--clipboard", "--input"},   // Linux X11 alt
		{"wl-copy"},                          // Linux Wayland
		{"clip.exe"},                         // WSL/Windows
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = strings.NewReader(result.Output)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}

	return fmt.Errorf("no clipboard command available")
}

func (e *Executor) actionExec(action ActionDef, result *ExecuteResult) error {
	cmdStr := action.Args["command"]
	if cmdStr == "" {
		return fmt.Errorf("exec requires command arg")
	}

	// Interpolate result into command
	cmdStr = strings.ReplaceAll(cmdStr, "${output}", result.Output)

	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Dir = e.workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func buildPluginSystemPrompt(def *Definition) string {
	var b strings.Builder

	b.WriteString("You are a specialized assistant for the '")
	b.WriteString(def.Name)
	b.WriteString("' task.\n\n")

	if def.Description != "" {
		b.WriteString("Task: ")
		b.WriteString(def.Description)
		b.WriteString("\n\n")
	}

	b.WriteString("You MUST call the ")
	b.WriteString(def.Tool.Name)
	b.WriteString(" tool with appropriate parameters based on the context provided.")

	return b.String()
}

func buildPluginUserPrompt(def *Definition, context string, flags map[string]string) string {
	var b strings.Builder

	// Add flags as context
	if len(flags) > 0 {
		b.WriteString("User-provided options:\n")
		for k, v := range flags {
			b.WriteString("- ")
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("Context:\n")
	b.WriteString(context)
	b.WriteString("\n\nCall the ")
	b.WriteString(def.Tool.Name)
	b.WriteString(" tool now.")

	return b.String()
}
