package oneshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tools"
)

func TestCLIInvokerCodex_UsesSingleCompleteOutputSchemaFile(t *testing.T) {
	tool := testCLITool()
	tempDir := t.TempDir()
	var got CLICommand
	var schemaPath string
	wantSchema, err := marshalCLISchema(tool.Parameters)
	if err != nil {
		t.Fatalf("marshal expected schema: %v", err)
	}

	inv, err := NewCLIInvoker(CLIInvokerConfig{
		Backend:         CLIBackendCodex,
		Model:           "gpt-5.4-mini",
		ReasoningEffort: "xhigh",
		TempDir:         tempDir,
		Runner: func(ctx context.Context, cmd CLICommand) (CLICommandResult, error) {
			got = cmd
			schemaPaths := codexOutputSchemaPaths(cmd.Args)
			if len(schemaPaths) != 1 {
				t.Fatalf("output schema paths = %v, want exactly one", schemaPaths)
			}
			schemaPath = schemaPaths[0]
			schemaData, err := os.ReadFile(schemaPath)
			if err != nil {
				t.Fatalf("read schema: %v", err)
			}
			if !bytes.Equal(schemaData, wantSchema) {
				t.Fatalf("schema file differs from complete marshaled schema\ngot:\n%s\nwant:\n%s", schemaData, wantSchema)
			}

			var schema map[string]any
			if err := json.Unmarshal(schemaData, &schema); err != nil {
				t.Fatalf("unmarshal schema: %v", err)
			}
			if schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Fatalf("schema should be a closed object: %s", schemaData)
			}
			properties, ok := schema["properties"].(map[string]any)
			if !ok || len(properties) != len(tool.Parameters.Properties) {
				t.Fatalf("schema properties = %#v, want all %d properties", schema["properties"], len(tool.Parameters.Properties))
			}
			for name := range tool.Parameters.Properties {
				if _, ok := properties[name]; !ok {
					t.Fatalf("schema missing property %q: %s", name, schemaData)
				}
				if !schemaRequiredContains(schema, name) {
					t.Fatalf("schema missing required property %q: %s", name, schemaData)
				}
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
	if !containsSubsequence(got.Args, []string{"--model", "gpt-5.4-mini"}) {
		t.Fatalf("codex args missing model: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"-c", `model_reasoning_effort="xhigh"`}) {
		t.Fatalf("codex args missing reasoning effort: %v", got.Args)
	}
	if got.Args[len(got.Args)-1] != "-" {
		t.Fatalf("codex prompt should be read from stdin, args: %v", got.Args)
	}
	for _, want := range []string{"system", "user", "Return only a JSON object", "`generate_commit`"} {
		if !strings.Contains(got.Stdin, want) {
			t.Fatalf("stdin missing %q: %q", want, got.Stdin)
		}
	}
	for _, forbidden := range []string{"JSON schema:", `"properties"`, `"action"`, `"subject"`, `"body"`} {
		if strings.Contains(got.Stdin, forbidden) {
			t.Fatalf("stdin duplicated schema fragment %q: %q", forbidden, got.Stdin)
		}
	}
	if _, err := os.Stat(schemaPath); !os.IsNotExist(err) {
		t.Fatalf("schema file should be cleaned up, stat err: %v", err)
	}
}

func TestCLIInvokerCodex_RejectsReservedOutputSchemaExtraArgsBeforeEffects(t *testing.T) {
	tests := []struct {
		name      string
		extraArgs []string
	}{
		{name: "separate", extraArgs: []string{"--output-schema", "attacker-schema.json"}},
		{name: "equals", extraArgs: []string{"--output-schema=attacker-schema.json"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			runnerCalls := 0
			inv, err := NewCLIInvoker(CLIInvokerConfig{
				Backend:   CLIBackendCodex,
				ExtraArgs: tt.extraArgs,
				TempDir:   tempDir,
				Runner: func(ctx context.Context, cmd CLICommand) (CLICommandResult, error) {
					runnerCalls++
					return CLICommandResult{Stdout: []byte(`{"action":"add"}`)}, nil
				},
			})
			if err != nil {
				t.Fatalf("NewCLIInvoker: %v", err)
			}

			result, trace, err := inv.Invoke(context.Background(), "system", "user evidence", testCLITool(), nil)
			if err == nil || !strings.Contains(err.Error(), "conflicts with reserved --output-schema") {
				t.Fatalf("Invoke error = %v, want reserved output schema rejection", err)
			}
			if result != nil {
				t.Fatalf("result = %+v, want nil", result)
			}
			if trace == nil || trace.Error == "" || len(trace.ToolCalls) != 0 {
				t.Fatalf("failure trace should record the rejection without a tool call: %+v", trace)
			}
			if runnerCalls != 0 {
				t.Fatalf("runner calls = %d, want zero", runnerCalls)
			}
			entries, err := os.ReadDir(tempDir)
			if err != nil {
				t.Fatalf("read temp dir: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("temp artifacts = %v, want none", entries)
			}
		})
	}
}

func TestCodexOutputSchemaPaths_CountsSeparateAndEqualsForms(t *testing.T) {
	paths := codexOutputSchemaPaths([]string{
		"exec",
		"--output-schema", "first.json",
		"--output-schema=second.json",
	})
	if len(paths) != 2 || paths[0] != "first.json" || paths[1] != "second.json" {
		t.Fatalf("paths = %v, want both output schema spellings", paths)
	}
}

func TestCLIInvokerCodex_LargeSchemaDoesNotGrowStdin(t *testing.T) {
	const propertyCount = 512
	const toolName = "schema_size_invariant"

	smallTool := tools.Definition{
		Name: toolName,
		Parameters: tools.ObjectSchema(map[string]tools.Property{
			"field_0000": tools.StringProperty("small field"),
		}, "field_0000"),
	}
	largeProperties := make(map[string]tools.Property, propertyCount)
	largeRequired := make([]string, 0, propertyCount)
	largeOutput := make(map[string]string, propertyCount)
	for i := 0; i < propertyCount; i++ {
		name := fmt.Sprintf("field_%04d", i)
		largeProperties[name] = tools.StringProperty(strings.Repeat("large schema description ", 4))
		largeRequired = append(largeRequired, name)
		largeOutput[name] = "value"
	}
	largeTool := tools.Definition{
		Name:       toolName,
		Parameters: tools.ObjectSchema(largeProperties, largeRequired...),
	}
	largeStdout, err := json.Marshal(largeOutput)
	if err != nil {
		t.Fatalf("marshal large output: %v", err)
	}

	type measurement struct {
		stdin       string
		schemaBytes int
	}
	invoke := func(tool tools.Definition, stdout []byte) measurement {
		t.Helper()
		var got measurement
		inv, err := NewCLIInvoker(CLIInvokerConfig{
			Backend: CLIBackendCodex,
			TempDir: t.TempDir(),
			Runner: func(ctx context.Context, cmd CLICommand) (CLICommandResult, error) {
				paths := codexOutputSchemaPaths(cmd.Args)
				if len(paths) != 1 {
					t.Fatalf("output schema paths = %v, want exactly one", paths)
				}
				schemaData, err := os.ReadFile(paths[0])
				if err != nil {
					t.Fatalf("read schema: %v", err)
				}
				got = measurement{stdin: cmd.Stdin, schemaBytes: len(schemaData)}
				return CLICommandResult{Stdout: stdout}, nil
			},
		})
		if err != nil {
			t.Fatalf("NewCLIInvoker: %v", err)
		}
		result, _, err := inv.Invoke(context.Background(), "fixed system", "fixed user evidence", tool, nil)
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		if result == nil || result.ToolCall == nil {
			t.Fatalf("missing tool call: %+v", result)
		}
		return got
	}

	small := invoke(smallTool, []byte(`{"field_0000":"value"}`))
	large := invoke(largeTool, largeStdout)
	if small.stdin != large.stdin {
		t.Fatalf("Codex stdin changed with schema size: small=%d bytes large=%d bytes", len(small.stdin), len(large.stdin))
	}
	if large.schemaBytes <= small.schemaBytes {
		t.Fatalf("large schema file = %d bytes, want greater than small schema file %d", large.schemaBytes, small.schemaBytes)
	}
	t.Logf("Codex stdin=%d bytes; schema file grew from %d to %d bytes", len(small.stdin), small.schemaBytes, large.schemaBytes)
}

func TestCLIInvokerCodex_ValidOutputRoundTrips(t *testing.T) {
	inv, err := NewCLIInvoker(CLIInvokerConfig{
		Backend: CLIBackendCodex,
		TempDir: t.TempDir(),
		Runner: func(ctx context.Context, cmd CLICommand) (CLICommandResult, error) {
			return CLICommandResult{Stdout: []byte(`{"action":"add","subject":"Round trip","body":["Preserved"]}`)}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewCLIInvoker: %v", err)
	}

	result, _, err := inv.Invoke(context.Background(), "system", "user evidence", testCLITool(), nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var payload struct {
		Action  string   `json:"action"`
		Subject string   `json:"subject"`
		Body    []string `json:"body"`
	}
	if result == nil || result.ToolCall == nil {
		t.Fatalf("missing tool call: %+v", result)
	}
	if err := result.ToolCall.Unmarshal(&payload); err != nil {
		t.Fatalf("unmarshal tool call: %v", err)
	}
	if payload.Action != "add" || payload.Subject != "Round trip" || len(payload.Body) != 1 || payload.Body[0] != "Preserved" {
		t.Fatalf("round-trip payload = %+v", payload)
	}
}

func TestCLIInvokerCodex_MalformedOutputFailsClosed(t *testing.T) {
	inv, err := NewCLIInvoker(CLIInvokerConfig{
		Backend: CLIBackendCodex,
		TempDir: t.TempDir(),
		Runner: func(ctx context.Context, cmd CLICommand) (CLICommandResult, error) {
			return CLICommandResult{Stdout: []byte(`{"action":`)}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewCLIInvoker: %v", err)
	}

	result, trace, err := inv.Invoke(context.Background(), "system", "user evidence", testCLITool(), nil)
	if err == nil {
		t.Fatal("expected malformed output error")
	}
	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	if trace == nil || trace.Error == "" || len(trace.ToolCalls) != 0 {
		t.Fatalf("failure trace should record the error without a tool call: %+v", trace)
	}
	if !strings.Contains(err.Error(), "parse codex CLI JSON output") {
		t.Fatalf("error = %q, want parse failure", err)
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

func codexOutputSchemaPaths(args []string) []string {
	var paths []string
	for i, arg := range args {
		if strings.HasPrefix(arg, "--output-schema=") {
			paths = append(paths, strings.TrimPrefix(arg, "--output-schema="))
			continue
		}
		if arg != "--output-schema" {
			continue
		}
		if i+1 >= len(args) {
			paths = append(paths, "")
			continue
		}
		paths = append(paths, args[i+1])
	}
	return paths
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
func TestParseCLIJSONClaudeEventArrayPrefersStructuredOutput(t *testing.T) {
	stdout := []byte(`[{"type":"system","message":{"ignore":true}},{"type":"result","result":"{\"title\":\"fallback\"}","structured_output":{"title":"preferred"}}]`)
	raw, err := parseCLIJSON(stdout)
	if err != nil {
		t.Fatalf("parseCLIJSON: %v", err)
	}
	var payload struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Title != "preferred" {
		t.Fatalf("title = %q, want preferred structured output", payload.Title)
	}
}

func TestParseCLIJSONClaudeEventArrayFallsBackToResult(t *testing.T) {
	stdout := []byte(`[{"type":"system"},{"type":"result","result":"{\"action\":\"commit\"}"}]`)
	raw, err := parseCLIJSON(stdout)
	if err != nil {
		t.Fatalf("parseCLIJSON: %v", err)
	}
	var payload struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Action != "commit" {
		t.Fatalf("action = %q, want commit", payload.Action)
	}
}

func TestParseCLIJSONClaudeEventArrayRejectsEnvelopeObjects(t *testing.T) {
	_, err := parseCLIJSON([]byte(`[{"type":"system"},{"type":"assistant","message":{"role":"assistant"}}]`))
	if err == nil {
		t.Fatal("expected event array without a result payload to fail")
	}
	if !strings.Contains(err.Error(), "no structured result object") {
		t.Fatalf("error = %q", err)
	}
}
