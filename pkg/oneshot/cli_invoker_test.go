package oneshot

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/tools"
)

func TestCLIInvokerCodexUsesOutputSchemaFile(t *testing.T) {
	tool := testCLITool()
	tempDir := t.TempDir()
	var got CLICommand
	var schemaPath string

	inv, err := NewCLIInvoker(CLIInvokerConfig{
		Backend: CLIBackendCodex,
		Model:   "gpt-5.4-mini-xhigh",
		TempDir: tempDir,
		Runner: func(ctx context.Context, cmd CLICommand) (CLICommandResult, error) {
			got = cmd
			for i, arg := range cmd.Args {
				if arg == "--output-schema" && i+1 < len(cmd.Args) {
					schemaPath = cmd.Args[i+1]
				}
			}
			if schemaPath == "" {
				t.Fatalf("missing --output-schema in args: %v", cmd.Args)
			}
			schemaData, err := os.ReadFile(schemaPath)
			if err != nil {
				t.Fatalf("read schema: %v", err)
			}
			if !strings.Contains(string(schemaData), `"action"`) {
				t.Fatalf("schema missing action property: %s", schemaData)
			}
			if !strings.Contains(string(schemaData), `"additionalProperties": false`) {
				t.Fatalf("schema should close object properties: %s", schemaData)
			}
			return CLICommandResult{Stdout: []byte(`{"action":"add","subject":"CLI backend","body":["Adds Codex CLI output-schema support"]}`)}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewCLIInvoker: %v", err)
	}

	result, trace, err := inv.Invoke(context.Background(), "system", "user", tool, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.ToolCall == nil || result.ToolCall.Name != "generate_commit" {
		t.Fatalf("unexpected tool call: %+v", result.ToolCall)
	}
	if trace == nil || trace.Provider != "codex-cli" {
		t.Fatalf("unexpected trace: %+v", trace)
	}
	if got.Name != "codex" {
		t.Fatalf("command name = %q, want codex", got.Name)
	}
	if !containsSubsequence(got.Args, []string{"exec", "--output-schema"}) {
		t.Fatalf("unexpected codex args: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"--model", "gpt-5.4-mini-xhigh"}) {
		t.Fatalf("codex args missing model: %v", got.Args)
	}
	if got.Args[len(got.Args)-1] != "-" {
		t.Fatalf("codex prompt should be read from stdin, args: %v", got.Args)
	}
	if !strings.Contains(got.Stdin, "Return only a JSON object") {
		t.Fatalf("stdin missing JSON instruction: %q", got.Stdin)
	}
	if _, err := os.Stat(schemaPath); !os.IsNotExist(err) {
		t.Fatalf("schema file should be cleaned up, stat err: %v", err)
	}
}

func TestCLIInvokerClaudeUnwrapsJSONResult(t *testing.T) {
	tool := tools.Definition{
		Name:        "generate_pull_request",
		Description: "Generate PR",
		Parameters: tools.ObjectSchema(map[string]tools.Property{
			"title":   tools.StringProperty("title"),
			"summary": tools.StringProperty("summary"),
			"changes": tools.ArrayProperty("changes", tools.StringProperty("change")),
			"testing": tools.ArrayProperty("testing", tools.StringProperty("test")),
		}, "title", "summary", "changes", "testing"),
	}

	inv, err := NewCLIInvoker(CLIInvokerConfig{
		Backend: CLIBackendClaude,
		Model:   "sonnet",
		Runner: func(ctx context.Context, cmd CLICommand) (CLICommandResult, error) {
			if cmd.Name != "claude" {
				t.Fatalf("command name = %q, want claude", cmd.Name)
			}
			if !containsSubsequence(cmd.Args, []string{"--print", "--input-format", "text", "--output-format", "json"}) {
				t.Fatalf("unexpected claude args: %v", cmd.Args)
			}
			if !containsSubsequence(cmd.Args, []string{"--model", "sonnet"}) {
				t.Fatalf("claude args missing model: %v", cmd.Args)
			}
			if !strings.Contains(strings.Join(cmd.Args, " "), "--json-schema") {
				t.Fatalf("claude args missing schema: %v", cmd.Args)
			}
			wrapped := map[string]string{
				"type":   "result",
				"result": `{"title":"Add CLI backends","summary":"Adds CLI-backed one-shot generation.","changes":["Adds Claude support"],"testing":["go test ./pkg/oneshot"]}`,
			}
			data, _ := json.Marshal(wrapped)
			return CLICommandResult{Stdout: data}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewCLIInvoker: %v", err)
	}

	result, _, err := inv.Invoke(context.Background(), "system", "user", tool, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var payload struct {
		Title string `json:"title"`
	}
	if err := result.ToolCall.Unmarshal(&payload); err != nil {
		t.Fatalf("unmarshal tool call: %v", err)
	}
	if payload.Title != "Add CLI backends" {
		t.Fatalf("title = %q", payload.Title)
	}
}

func TestNewCLIInvokerRejectsUnsupportedBackend(t *testing.T) {
	_, err := NewCLIInvoker(CLIInvokerConfig{Backend: "api"})
	if err == nil {
		t.Fatal("expected unsupported backend error")
	}
}

func TestParseCLIJSONExtractsPreambleObject(t *testing.T) {
	raw, err := parseCLIJSON([]byte("thinking...\n```json\n{\"action\":\"add\"}\n```"))
	if err != nil {
		t.Fatalf("parseCLIJSON: %v", err)
	}
	if string(raw) != `{"action":"add"}` {
		t.Fatalf("raw = %s", raw)
	}
}

func TestMarshalCLISchemaClosesObjects(t *testing.T) {
	data, err := marshalCLISchema(testCLITool().Parameters)
	if err != nil {
		t.Fatalf("marshalCLISchema: %v", err)
	}
	if !strings.Contains(string(data), `"additionalProperties": false`) {
		t.Fatalf("schema should include additionalProperties false: %s", data)
	}
}

func TestMarshalCLISchemaRequiresNullableOptionalProperties(t *testing.T) {
	tool := tools.Definition{
		Name:        "generate_commit",
		Description: "Generate commit",
		Parameters: tools.ObjectSchema(map[string]tools.Property{
			"action": tools.StringProperty("action"),
			"scope":  tools.StringProperty("scope"),
		}, "action"),
	}

	data, err := marshalCLISchema(tool.Parameters)
	if err != nil {
		t.Fatalf("marshalCLISchema: %v", err)
	}

	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if !schemaRequiredContains(schema, "action") || !schemaRequiredContains(schema, "scope") {
		t.Fatalf("required should include all properties: %s", data)
	}

	props := schema["properties"].(map[string]any)
	scope := props["scope"].(map[string]any)
	if !schemaTypeIncludes(scope["type"], "null") {
		t.Fatalf("optional property should allow null: %s", data)
	}

	action := props["action"].(map[string]any)
	if schemaTypeIncludes(action["type"], "null") {
		t.Fatalf("required property should not allow null: %s", data)
	}
}

func testCLITool() tools.Definition {
	return tools.Definition{
		Name:        "generate_commit",
		Description: "Generate commit",
		Parameters: tools.ObjectSchema(map[string]tools.Property{
			"action":  tools.StringProperty("action"),
			"subject": tools.StringProperty("subject"),
			"body":    tools.ArrayProperty("body", tools.StringProperty("bullet")),
		}, "action", "subject", "body"),
	}
}

func containsSubsequence(values, want []string) bool {
	if len(want) == 0 {
		return true
	}
	next := 0
	for _, value := range values {
		if value == want[next] {
			next++
			if next == len(want) {
				return true
			}
		}
	}
	return false
}

func schemaRequiredContains(schema map[string]any, want string) bool {
	required, ok := schema["required"].([]any)
	if !ok {
		return false
	}
	for _, value := range required {
		if value == want {
			return true
		}
	}
	return false
}

func schemaTypeIncludes(value any, want string) bool {
	switch typ := value.(type) {
	case string:
		return typ == want
	case []any:
		for _, item := range typ {
			if item == want {
				return true
			}
		}
	}
	return false
}
