package builtin

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	defaultDynamicCodeTimeout = 60
	maxDynamicCodeTimeout     = 300
	maxDynamicCodeBytes       = 256 * 1024
	maxDynamicCodeArgs        = 64
)

// DynamicCodeTool executes a bounded snippet through the same workspace,
// timeout, output-limit, sandbox, and container plumbing used by run_shell.
type DynamicCodeTool struct {
	ShellCommandTool
}

func (t *DynamicCodeTool) Name() string {
	return "run_code"
}

func (t *DynamicCodeTool) Description() string {
	return "Execute a bounded Python, JavaScript, Go, or Bash snippet for calculations, parsing, data transforms, and focused experiments. Prefer this over constructing elaborate shell pipelines. Execution uses Buckley's configured workspace/container sandbox, timeout, and output limits."
}

func (t *DynamicCodeTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"language": {
				Type:        "string",
				Description: "Snippet language",
				Enum:        []string{"python", "javascript", "go", "bash"},
			},
			"code": {
				Type:        "string",
				Description: "Complete source snippet to execute",
			},
			"args": {
				Type:        "array",
				Description: "Optional command-line arguments passed to the snippet",
				Items: &PropertySchema{
					Type:        "string",
					Description: "One command-line argument",
				},
			},
			"timeout_seconds": {
				Type:        "integer",
				Description: "Timeout in seconds (default 60, max 300)",
				Default:     defaultDynamicCodeTimeout,
			},
		},
		Required: []string{"language", "code"},
	}
}

func (t *DynamicCodeTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *DynamicCodeTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	language, _ := params["language"].(string)
	code, _ := params["code"].(string)
	language = canonicalDynamicLanguage(language)
	if language == "" {
		return &Result{Success: false, Error: "language must be one of: python, javascript, go, bash"}, nil
	}
	if strings.TrimSpace(code) == "" {
		return &Result{Success: false, Error: "code parameter must be a non-empty string"}, nil
	}
	if len(code) > maxDynamicCodeBytes {
		return &Result{Success: false, Error: fmt.Sprintf("code exceeds %d-byte limit", maxDynamicCodeBytes)}, nil
	}
	args, err := dynamicCodeArgs(params["args"])
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	command, err := dynamicCodeCommand(language, code, args)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	timeout := parseInt(params["timeout_seconds"], defaultDynamicCodeTimeout)
	if timeout <= 0 || timeout > maxDynamicCodeTimeout {
		timeout = defaultDynamicCodeTimeout
	}
	result, execErr := t.ShellCommandTool.ExecuteWithContext(ctx, map[string]any{
		"command":         command,
		"timeout_seconds": timeout,
	})
	if result == nil {
		return result, execErr
	}
	if result.Data == nil {
		result.Data = make(map[string]any)
	}
	delete(result.Data, "command")
	result.Data["language"] = language
	result.Data["code_bytes"] = len(code)
	result.DisplayData = map[string]any{
		"summary": fmt.Sprintf("Executed %s snippet (%d bytes)", language, len(code)),
	}
	return result, execErr
}

func canonicalDynamicLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "python", "python3", "py":
		return "python"
	case "javascript", "js", "node", "nodejs":
		return "javascript"
	case "go", "golang":
		return "go"
	case "bash", "sh", "shell":
		return "bash"
	default:
		return ""
	}
}

func dynamicCodeArgs(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	var args []string
	switch values := raw.(type) {
	case []string:
		args = append(args, values...)
	case []any:
		args = make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("args must contain only strings")
			}
			args = append(args, text)
		}
	default:
		return nil, fmt.Errorf("args must be an array of strings")
	}
	if len(args) > maxDynamicCodeArgs {
		return nil, fmt.Errorf("args exceeds %d-item limit", maxDynamicCodeArgs)
	}
	for _, arg := range args {
		if len(arg) > 4096 {
			return nil, fmt.Errorf("one code argument exceeds 4096-byte limit")
		}
	}
	return args, nil
}

func dynamicCodeCommand(language, code string, args []string) (string, error) {
	language = canonicalDynamicLanguage(language)
	if language == "" {
		return "", fmt.Errorf("unsupported dynamic-code language")
	}
	payload := shellEscapeSingleQuotes(base64.StdEncoding.EncodeToString([]byte(code)))
	encodedInput := "printf %s " + payload + " | base64 -d"
	argumentText := dynamicCodeArgumentText(args)

	switch language {
	case "python":
		return "runner=\"$(command -v python3 || command -v python)\" || { echo 'python interpreter not found' >&2; exit 127; }; " + encodedInput + " | \"$runner\" -" + argumentText, nil
	case "javascript":
		return "runner=\"$(command -v node)\" || { echo 'node interpreter not found' >&2; exit 127; }; " + encodedInput + " | \"$runner\" -" + argumentText, nil
	case "bash":
		return encodedInput + " | bash -s --" + argumentText, nil
	case "go":
		return "runner=\"$(command -v go)\" || { echo 'go toolchain not found' >&2; exit 127; }; tmp=\"${TMPDIR:-/tmp}/buckley-code-$$.go\"; trap 'rm -f \"$tmp\"' EXIT; " + encodedInput + " > \"$tmp\"; \"$runner\" run \"$tmp\"" + argumentText, nil
	default:
		return "", fmt.Errorf("unsupported dynamic-code language")
	}
}

func dynamicCodeArgumentText(args []string) string {
	if len(args) == 0 {
		return ""
	}
	var out strings.Builder
	for _, arg := range args {
		out.WriteByte(' ')
		out.WriteString(shellEscapeSingleQuotes(arg))
	}
	return out.String()
}
