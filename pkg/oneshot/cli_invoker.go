package oneshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/tools"
	"github.com/odvcencio/buckley/pkg/transparency"
)

const (
	CLIBackendCodex  = "codex"
	CLIBackendClaude = "claude"
)

// CLICommand describes one non-interactive agent CLI invocation.
type CLICommand struct {
	Name  string
	Args  []string
	Stdin string
	Dir   string
}

// CLICommandResult captures output from an agent CLI invocation.
type CLICommandResult struct {
	Stdout []byte
	Stderr []byte
}

// CLICommandRunner executes an agent CLI command.
type CLICommandRunner func(ctx context.Context, cmd CLICommand) (CLICommandResult, error)

// CLIInvokerConfig configures a CLI-backed one-shot invoker.
type CLIInvokerConfig struct {
	Backend string
	Command string
	Model   string
	WorkDir string

	ExtraArgs []string
	Runner    CLICommandRunner
	TempDir   string
}

// CLIInvoker adapts Codex CLI and Claude Code into Buckley's tool-shaped one-shot pipeline.
type CLIInvoker struct {
	backend   string
	command   string
	model     string
	workDir   string
	extraArgs []string
	runner    CLICommandRunner
	tempDir   string
}

// NewCLIInvoker creates a CLI-backed invoker for supported external agent CLIs.
func NewCLIInvoker(cfg CLIInvokerConfig) (*CLIInvoker, error) {
	backend := NormalizeCLIBackend(cfg.Backend)
	switch backend {
	case CLIBackendCodex, CLIBackendClaude:
	default:
		return nil, fmt.Errorf("unsupported CLI backend %q", cfg.Backend)
	}

	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		command = backend
	}

	runner := cfg.Runner
	if runner == nil {
		runner = runCLICommand
	}

	return &CLIInvoker{
		backend:   backend,
		command:   command,
		model:     strings.TrimSpace(cfg.Model),
		workDir:   cfg.WorkDir,
		extraArgs: append([]string(nil), cfg.ExtraArgs...),
		runner:    runner,
		tempDir:   cfg.TempDir,
	}, nil
}

// NormalizeCLIBackend canonicalizes user-facing CLI backend names.
func NormalizeCLIBackend(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// Invoke runs an external agent CLI and adapts its JSON output into a tool call.
func (inv *CLIInvoker) Invoke(ctx context.Context, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit) (*Result, *transparency.Trace, error) {
	if inv == nil {
		return nil, nil, fmt.Errorf("CLI invoker is nil")
	}

	traceID := fmt.Sprintf("cli-%s-%d", inv.backend, time.Now().UnixNano())
	modelID := inv.model
	if modelID == "" {
		modelID = inv.backend + "-default"
	}
	builder := transparency.NewTraceBuilder(traceID, modelID, inv.backend+"-cli")
	builder.WithContext(audit)
	builder.WithRequest(&transparency.RequestTrace{
		Messages: []transparency.MessageTrace{
			{Role: "system", Content: truncateForTrace(systemPrompt, 500), ContentLength: len(systemPrompt)},
			{Role: "user", Content: truncateForTrace(userPrompt, 500), ContentLength: len(userPrompt)},
		},
		Tools: []string{tool.Name},
	})

	schemaJSON, err := marshalCLISchema(tool.Parameters)
	if err != nil {
		builder.WithError(err)
		return nil, builder.Build(), fmt.Errorf("marshal CLI schema: %w", err)
	}

	prompt := buildCLIPrompt(systemPrompt, userPrompt, tool, schemaJSON)
	cmd, cleanup, err := inv.buildCommand(tool, prompt, schemaJSON)
	if err != nil {
		builder.WithError(err)
		return nil, builder.Build(), err
	}
	if cleanup != nil {
		defer cleanup()
	}

	output, err := inv.runner(ctx, cmd)
	if err != nil {
		wrapped := formatCLIError(inv.backend, err, output)
		builder.WithError(wrapped)
		return nil, builder.Build(), wrapped
	}

	arguments, err := parseCLIJSON(output.Stdout)
	if err != nil {
		wrapped := fmt.Errorf("parse %s CLI JSON output: %w", inv.backend, err)
		builder.WithError(wrapped)
		builder.WithContent(strings.TrimSpace(string(output.Stdout)))
		return nil, builder.Build(), wrapped
	}

	toolCall := tools.ToolCall{
		ID:        "cli-" + inv.backend,
		Name:      tool.Name,
		Arguments: arguments,
	}
	builder.WithToolCalls([]tools.ToolCall{toolCall})
	builder.WithContent(strings.TrimSpace(string(output.Stdout)))

	tokens := transparency.TokenUsage{
		Input:  estimateTokens(systemPrompt) + estimateTokens(userPrompt),
		Output: estimateTokens(string(output.Stdout)),
	}
	trace := builder.Complete(tokens, 0)

	return &Result{ToolCall: &toolCall}, trace, nil
}

func (inv *CLIInvoker) buildCommand(tool tools.Definition, prompt string, schemaJSON []byte) (CLICommand, func(), error) {
	switch inv.backend {
	case CLIBackendCodex:
		return inv.buildCodexCommand(tool, prompt, schemaJSON)
	case CLIBackendClaude:
		return inv.buildClaudeCommand(prompt, schemaJSON)
	default:
		return CLICommand{}, nil, fmt.Errorf("unsupported CLI backend %q", inv.backend)
	}
}

func (inv *CLIInvoker) buildCodexCommand(tool tools.Definition, prompt string, schemaJSON []byte) (CLICommand, func(), error) {
	tmp, err := os.CreateTemp(inv.tempDir, "buckley-"+tool.Name+"-schema-*.json")
	if err != nil {
		return CLICommand{}, nil, fmt.Errorf("create Codex output schema: %w", err)
	}
	schemaPath := tmp.Name()
	cleanup := func() { _ = os.Remove(schemaPath) }
	if _, err := tmp.Write(schemaJSON); err != nil {
		_ = tmp.Close()
		cleanup()
		return CLICommand{}, nil, fmt.Errorf("write Codex output schema: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return CLICommand{}, nil, fmt.Errorf("close Codex output schema: %w", err)
	}

	args := []string{"exec", "--output-schema", schemaPath}
	if inv.model != "" {
		args = append(args, "--model", inv.model)
	}
	args = append(args, inv.extraArgs...)
	args = append(args, "-")

	return CLICommand{
		Name:  inv.command,
		Args:  args,
		Stdin: prompt,
		Dir:   inv.workDir,
	}, cleanup, nil
}

func (inv *CLIInvoker) buildClaudeCommand(prompt string, schemaJSON []byte) (CLICommand, func(), error) {
	args := []string{
		"--print",
		"--input-format", "text",
		"--output-format", "json",
		"--json-schema", string(schemaJSON),
	}
	if inv.model != "" {
		args = append(args, "--model", inv.model)
	}
	args = append(args, inv.extraArgs...)

	return CLICommand{
		Name:  inv.command,
		Args:  args,
		Stdin: prompt,
		Dir:   inv.workDir,
	}, nil, nil
}

func buildCLIPrompt(systemPrompt, userPrompt string, tool tools.Definition, schemaJSON []byte) string {
	var b strings.Builder
	if strings.TrimSpace(systemPrompt) != "" {
		b.WriteString(systemPrompt)
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(userPrompt) != "" {
		b.WriteString(userPrompt)
		b.WriteString("\n\n")
	}
	b.WriteString("Return only a JSON object matching the schema below. ")
	b.WriteString("The object is the argument payload for `")
	b.WriteString(tool.Name)
	b.WriteString("`; do not wrap it in a tool-call envelope, markdown fence, or explanatory text.\n\n")
	b.WriteString("JSON schema:\n")
	b.Write(schemaJSON)
	b.WriteString("\n")
	return b.String()
}

func marshalCLISchema(schema tools.Schema) ([]byte, error) {
	data, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	enforceClosedObjects(value)
	return json.MarshalIndent(value, "", "  ")
}

func enforceClosedObjects(value any) {
	obj, ok := value.(map[string]any)
	if !ok {
		return
	}

	if obj["type"] == "object" {
		obj["additionalProperties"] = false
		if props, ok := obj["properties"].(map[string]any); ok {
			required := requiredNameSet(obj["required"])
			names := make([]string, 0, len(props))
			for name, prop := range props {
				if !required[name] {
					allowNull(prop)
				}
				enforceClosedObjects(prop)
				names = append(names, name)
			}
			sort.Strings(names)
			obj["required"] = names
		}
	}

	if items, ok := obj["items"]; ok {
		enforceClosedObjects(items)
	}
}

func requiredNameSet(value any) map[string]bool {
	required := make(map[string]bool)
	names, ok := value.([]any)
	if !ok {
		return required
	}
	for _, name := range names {
		if text, ok := name.(string); ok {
			required[text] = true
		}
	}
	return required
}

func allowNull(value any) {
	obj, ok := value.(map[string]any)
	if !ok {
		return
	}

	switch typ := obj["type"].(type) {
	case string:
		if typ != "null" {
			obj["type"] = []any{typ, "null"}
		}
	case []any:
		if !containsSchemaValue(typ, "null") {
			obj["type"] = append(typ, "null")
		}
	}

	if enum, ok := obj["enum"].([]any); ok && !containsSchemaValue(enum, nil) {
		obj["enum"] = append(enum, nil)
	}
}

func containsSchemaValue(values []any, want any) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func runCLICommand(ctx context.Context, req CLICommand) (CLICommandResult, error) {
	cmd := exec.CommandContext(ctx, req.Name, req.Args...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	cmd.Stdin = strings.NewReader(req.Stdin)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return CLICommandResult{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
	}, err
}

func formatCLIError(backend string, err error, output CLICommandResult) error {
	stderr := strings.TrimSpace(string(output.Stderr))
	stdout := strings.TrimSpace(string(output.Stdout))
	switch {
	case stderr != "" && stdout != "":
		return fmt.Errorf("%s CLI failed: %w\nstdout:\n%s\nstderr:\n%s", backend, err, stdout, stderr)
	case stderr != "":
		return fmt.Errorf("%s CLI failed: %w\nstderr:\n%s", backend, err, stderr)
	case stdout != "":
		return fmt.Errorf("%s CLI failed: %w\nstdout:\n%s", backend, err, stdout)
	default:
		return fmt.Errorf("%s CLI failed: %w", backend, err)
	}
}

func parseCLIJSON(stdout []byte) (json.RawMessage, error) {
	raw, err := extractJSONObject(stdout)
	if err != nil {
		return nil, err
	}

	if nested, ok := unwrapCLIResult(raw); ok {
		return nested, nil
	}
	return raw, nil
}

func unwrapCLIResult(raw json.RawMessage) (json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false
	}

	for _, key := range []string{"result", "content", "message", "output"} {
		value, ok := obj[key]
		if !ok {
			continue
		}

		var nested json.RawMessage
		if err := json.Unmarshal(value, &nested); err == nil && json.Valid(nested) && len(nested) > 0 && nested[0] == '{' {
			return nested, true
		}

		var text string
		if err := json.Unmarshal(value, &text); err == nil {
			if nested, err := extractJSONObject([]byte(text)); err == nil {
				return nested, true
			}
		}
	}

	return nil, false
}

func extractJSONObject(data []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty output")
	}
	if json.Valid(trimmed) && trimmed[0] == '{' {
		return json.RawMessage(append([]byte(nil), trimmed...)), nil
	}

	start := bytes.IndexByte(trimmed, '{')
	end := bytes.LastIndexByte(trimmed, '}')
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON object found")
	}

	candidate := bytes.TrimSpace(trimmed[start : end+1])
	if !json.Valid(candidate) {
		return nil, fmt.Errorf("invalid JSON object")
	}
	return json.RawMessage(append([]byte(nil), candidate...)), nil
}
