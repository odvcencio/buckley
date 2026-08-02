package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/mcp"
	"m31labs.dev/buckley/pkg/policy"
)

// fakeServerBin holds the path to pkg/mcp's testdata/fakeserver binary,
// built once by TestMain and reused across this package's MCP bridging
// tests. Reusing pkg/mcp's fake server (rather than a second copy under
// pkg/tool/testdata) keeps a single source of truth for the wire-level MCP
// test fixture.
var fakeServerBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "buckley-tool-mcp-fakeserver-")
	if err != nil {
		os.Exit(1)
	}
	bin := filepath.Join(dir, "fakeserver")
	build := exec.Command("go", "build", "-o", bin, "m31labs.dev/buckley/pkg/mcp/testdata/fakeserver")
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		os.Stderr.WriteString(string(out))
		os.RemoveAll(dir)
		os.Exit(1)
	}
	fakeServerBin = bin

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func connectedFakeManager(t *testing.T, mode string, extraArgs ...string) *mcp.Manager {
	t.Helper()
	args := append([]string{"-mode=" + mode}, extraArgs...)
	manager := mcp.NewManager()
	manager.AddServer(mcp.Config{
		Name:    "fake",
		Command: fakeServerBin,
		Args:    args,
		Timeout: 5 * time.Second,
	})
	if err := manager.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { manager.Close() })
	return manager
}

func TestMCPToolName_Sanitizes(t *testing.T) {
	got := mcpToolName("my server!", "read/file")
	if got != "mcp_my_server_read_file" {
		t.Fatalf("unexpected sanitized name: %q", got)
	}
}

func TestMCPToolName_EmptyPartsFallBackToUnknown(t *testing.T) {
	got := mcpToolName("", "***")
	if got != "mcp_unknown_unknown" {
		t.Fatalf("expected mcp_unknown_unknown, got %q", got)
	}
}

func TestTruncateBytes_NoTruncationNeeded(t *testing.T) {
	if got := truncateBytes("short", 200); got != "short" {
		t.Fatalf("expected unchanged string, got %q", got)
	}
}

func TestTruncateBytes_TruncatesWithEllipsis(t *testing.T) {
	long := strings.Repeat("a", 300)
	got := truncateBytes(long, 200)
	if len(got) > 200 {
		t.Fatalf("expected result within 200 bytes, got %d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected an ellipsis suffix, got %q", got)
	}
}

func TestMCPInputSchemaToParameters_Basic(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "file path"},
			"tags": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"mode": map[string]any{
				"type": "string",
				"enum": []any{"read", "write"},
			},
		},
		"required": []any{"path"},
	}

	params := mcpInputSchemaToParameters(schema)
	if params.Type != "object" {
		t.Fatalf("expected type object, got %q", params.Type)
	}
	if len(params.Required) != 1 || params.Required[0] != "path" {
		t.Fatalf("expected required=[path], got %v", params.Required)
	}
	pathProp, ok := params.Properties["path"]
	if !ok || pathProp.Description != "file path" {
		t.Fatalf("unexpected path property: %+v", pathProp)
	}
	tagsProp, ok := params.Properties["tags"]
	if !ok || tagsProp.Items == nil || tagsProp.Items.Type != "string" {
		t.Fatalf("unexpected tags property: %+v", tagsProp)
	}
	modeProp, ok := params.Properties["mode"]
	if !ok || len(modeProp.Enum) != 2 {
		t.Fatalf("unexpected mode property: %+v", modeProp)
	}
}

func TestMCPInputSchemaToParameters_NilSchema(t *testing.T) {
	params := mcpInputSchemaToParameters(nil)
	if params.Type != "object" || params.Properties == nil {
		t.Fatalf("expected a safe default object schema, got %+v", params)
	}
}

func TestMCPInputSchemaToParameters_NestedObject(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"filter": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"status": map[string]any{"type": "string"},
				},
				"required": []any{"status"},
			},
		},
	}
	params := mcpInputSchemaToParameters(schema)
	filter, ok := params.Properties["filter"]
	if !ok || filter.Type != "object" {
		t.Fatalf("expected nested object property, got %+v", filter)
	}
	if _, ok := filter.Properties["status"]; !ok {
		t.Fatalf("expected nested status property, got %+v", filter.Properties)
	}
	if len(filter.Required) != 1 || filter.Required[0] != "status" {
		t.Fatalf("expected nested required=[status], got %v", filter.Required)
	}
}

func TestFlattenMCPContent_TextOnly(t *testing.T) {
	text, blocks := flattenMCPContent([]sdkmcp.Content{
		&sdkmcp.TextContent{Text: "line one"},
		&sdkmcp.TextContent{Text: "line two"},
	})
	if text != "line one\nline two" {
		t.Fatalf("unexpected joined text: %q", text)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 block summaries, got %d", len(blocks))
	}
}

func TestFlattenMCPContent_ImageAndResourceSummarized(t *testing.T) {
	text, blocks := flattenMCPContent([]sdkmcp.Content{
		&sdkmcp.TextContent{Text: "caption"},
		&sdkmcp.ImageContent{Data: []byte{1, 2, 3, 4}, MIMEType: "image/png"},
		&sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{URI: "file:///tmp/x.txt"}},
	})
	if text != "caption" {
		t.Fatalf("expected only the text block in output, got %q", text)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 block summaries, got %d: %v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[1], "image/png") {
		t.Fatalf("expected image summary to name the mime type, got %q", blocks[1])
	}
	if !strings.Contains(blocks[2], "file:///tmp/x.txt") {
		t.Fatalf("expected resource summary to name the uri, got %q", blocks[2])
	}
	// Neither block embeds raw bytes/base64 payloads.
	for _, b := range blocks {
		if strings.Contains(b, string([]byte{1, 2, 3, 4})) {
			t.Fatalf("image summary must not embed raw bytes: %q", b)
		}
	}
}

func TestMCPTool_ExecuteWithContext_CallsThroughManager(t *testing.T) {
	manager := connectedFakeManager(t, "normal", "-tools=0")
	tools := manager.AllTools()
	var echoDef *sdkmcp.Tool
	for _, tws := range tools {
		if tws.Tool.Name == "echo" {
			echoDef = tws.Tool
		}
	}
	if echoDef == nil {
		t.Fatal("expected the fake server's echo tool to be listed")
	}

	bt := newMCPTool(manager, "fake", echoDef, 0)
	if bt.Name() != "mcp_fake_echo" {
		t.Fatalf("expected mcp_fake_echo, got %q", bt.Name())
	}

	res, err := bt.Execute(map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}
	if res.Data["output"] != "hello" {
		t.Fatalf("expected output=hello, got %+v", res.Data)
	}
}

func TestMCPTool_Execute_ServerReportedError(t *testing.T) {
	manager := connectedFakeManager(t, "normal", "-tools=0")
	_, tl, found := manager.FindTool("fail")
	if !found {
		t.Fatal("expected the fake server's fail tool to be listed")
	}
	bt := newMCPTool(manager, "fake", tl, 0)

	res, err := bt.Execute(nil)
	if err != nil {
		t.Fatalf("Execute should not return a transport error: %v", err)
	}
	if res.Success {
		t.Fatal("expected Success=false for a server-reported tool error")
	}
}

func TestMCPTool_Metadata_ConservativeByDefault(t *testing.T) {
	def := &sdkmcp.Tool{Name: "anything", Description: "d", InputSchema: map[string]any{"type": "object"}}
	bt := newMCPTool(mcp.NewManager(), "srv", def, 0)
	meta := bt.Metadata()
	if meta.Category != CategoryExternal {
		t.Fatalf("expected CategoryExternal, got %q", meta.Category)
	}
	if meta.Impact != ImpactDestructive {
		t.Fatalf("expected ImpactDestructive (most conservative), got %q", meta.Impact)
	}
	// GetMetadata must resolve through the RichTool interface, not the
	// name-substring fallback.
	if got := GetMetadata(bt); got.Category != CategoryExternal {
		t.Fatalf("expected GetMetadata to use mcpTool.Metadata(), got %+v", got)
	}
}

func TestMCPTool_Description_TruncatedTo200Bytes(t *testing.T) {
	def := &sdkmcp.Tool{
		Name:        "verbose",
		Description: strings.Repeat("x", 500),
		InputSchema: map[string]any{"type": "object"},
	}
	bt := newMCPTool(mcp.NewManager(), "srv", def, 0)
	if len(bt.Description()) > mcpDescriptionBudgetBytes {
		t.Fatalf("expected description within %d bytes, got %d", mcpDescriptionBudgetBytes, len(bt.Description()))
	}
}

func TestRegisterMCPTools_BridgesAllServerTools(t *testing.T) {
	manager := connectedFakeManager(t, "normal", "-tools=1")
	r := NewEmptyRegistry()

	cfg := config.MCPConfig{MaxTools: config.DefaultMCPMaxTools}
	names := RegisterMCPTools(r, manager, cfg)

	if len(names) != 4 { // echo, fail, mixed_content, synthetic_0
		t.Fatalf("expected 4 bridged tools, got %d: %v", len(names), names)
	}
	bridged, ok := r.Get("mcp_fake_echo")
	if !ok {
		t.Fatalf("expected mcp_fake_echo to be registered, have: %v", names)
	}
	if kind := r.ToolKind("mcp_fake_echo"); kind != "execute" {
		t.Fatalf("expected ACP kind execute, got %q", kind)
	}
	if bridged.Description() == "" {
		t.Fatal("expected a non-empty description")
	}
}

func TestRegisterMCPTools_MaxToolsCap(t *testing.T) {
	// The fake server always exposes echo, fail, and mixed_content, plus
	// N synthetic tools: cap at 2 total to force truncation.
	manager := connectedFakeManager(t, "normal", "-tools=5")
	r := NewEmptyRegistry()

	names := RegisterMCPTools(r, manager, config.MCPConfig{MaxTools: 2})

	if len(names) != 2 {
		t.Fatalf("expected exactly 2 bridged tools under the cap, got %d: %v", len(names), names)
	}
}

func TestRegisterMCPTools_NilInputsAreNoop(t *testing.T) {
	if got := RegisterMCPTools(nil, mcp.NewManager(), config.MCPConfig{}); got != nil {
		t.Fatalf("expected nil result for a nil registry, got %v", got)
	}
	if got := RegisterMCPTools(NewEmptyRegistry(), nil, config.MCPConfig{}); got != nil {
		t.Fatalf("expected nil result for a nil manager, got %v", got)
	}
}

// TestMCPTool_SubjectToPermissionDenyRule proves a bridged mcp_* tool is
// evaluated by the same glob-permission middleware as any other file tool:
// registering it through RegisterMCPTools and Registry.Use is sufficient,
// with no MCP-specific permission code required.
func TestMCPTool_SubjectToPermissionDenyRule(t *testing.T) {
	manager := mcp.NewManager() // no server connection needed: deny short-circuits before Execute
	r := NewEmptyRegistry()

	def := &sdkmcp.Tool{
		Name:        "read_file",
		Description: "Reads a file from disk via an external MCP filesystem server.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []any{"path"},
		},
	}
	bt := newMCPTool(manager, "fsserver", def, 0)
	r.Register(bt)
	r.SetToolKind(bt.Name(), "execute")

	if bt.Name() != "mcp_fsserver_read_file" {
		t.Fatalf("unexpected tool name: %q", bt.Name())
	}

	gate := &PermissionGate{
		Layers: []policy.PermissionLayer{
			{
				Name: "project",
				Rules: []policy.PermissionRule{
					{Tool: "*", ArgPattern: "**/*.env", Action: policy.PermissionDeny},
				},
			},
		},
		Posture: "interactive",
	}
	r.Use(NewPermissionMiddleware(gate))

	res, err := r.Execute(bt.Name(), map[string]any{"path": "/workspace/.env"})
	if err != nil {
		t.Fatalf("Execute returned a transport error instead of a denial result: %v", err)
	}
	if res == nil || res.Success {
		t.Fatalf("expected the mcp_* tool call to be denied, got %+v", res)
	}
	if !strings.Contains(res.Error, "permission denied") {
		t.Fatalf("expected a permission-denied error, got %q", res.Error)
	}

	// A path that does not match the deny rule is unaffected by the
	// permission layer (it still fails, but for lack of a connected
	// server, proving the deny rule -- not some blanket MCP block -- was
	// what stopped the first call).
	res2, err := r.Execute(bt.Name(), map[string]any{"path": "/workspace/notes.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res2 == nil || strings.Contains(res2.Error, "permission denied") {
		t.Fatalf("expected a non-permission failure (no server connected), got %+v", res2)
	}
}
