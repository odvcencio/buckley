package tool

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/mcp"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// CategoryExternal groups tools bridged in from external MCP servers. It is
// declared here (not in metadata.go) because every MCP tool belongs to it
// by construction; no other tool package needs the value.
const CategoryExternal Category = "external"

const (
	// mcpDescriptionBudgetBytes caps a bridged tool's description so a
	// verbose MCP server cannot blow Buckley's overall tool schema budget.
	mcpDescriptionBudgetBytes = 200
	// mcpDefaultCallTimeout bounds a single tools/call when the server's
	// config does not set an explicit timeout.
	mcpDefaultCallTimeout = 60 * time.Second
)

// mcpTool adapts a single MCP server tool into a Buckley Tool. MCP servers
// are untrusted external processes: every bridged tool carries
// CategoryExternal and ImpactDestructive (the most conservative Impact
// value in this package), regardless of what the server's own tool
// metadata implies, so downstream approval and permission logic treats it
// with the same caution as an arbitrary shell command.
type mcpTool struct {
	manager     *mcp.Manager
	server      string
	def         *sdkmcp.Tool
	name        string
	description string
	timeout     time.Duration
}

// newMCPTool builds a Buckley Tool wrapper around a single MCP tool
// definition. It does not register the tool; call Registry.Register with
// the result, or use RegisterMCPTools to bridge an entire manager.
func newMCPTool(manager *mcp.Manager, server string, def *sdkmcp.Tool, timeout time.Duration) *mcpTool {
	if timeout <= 0 {
		timeout = mcpDefaultCallTimeout
	}
	return &mcpTool{
		manager:     manager,
		server:      server,
		def:         def,
		name:        mcpToolName(server, def.Name),
		description: truncateBytes(strings.TrimSpace(def.Description), mcpDescriptionBudgetBytes),
		timeout:     timeout,
	}
}

func (t *mcpTool) Name() string        { return t.name }
func (t *mcpTool) Description() string { return t.description }

// Metadata implements RichTool.
func (t *mcpTool) Metadata() ToolMetadata {
	return ToolMetadata{
		Category: CategoryExternal,
		Impact:   ImpactDestructive,
		Cost:     CostExpensive,
		Intent:   fmt.Sprintf("Calling MCP tool %q on server %q", t.def.Name, t.server),
		Summary:  "MCP tool call completed",
	}
}

func (t *mcpTool) Parameters() builtin.ParameterSchema {
	return mcpInputSchemaToParameters(t.def.InputSchema)
}

func (t *mcpTool) Execute(params map[string]any) (*builtin.Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

// ExecuteWithContext implements the optional ContextTool interface so a
// caller-supplied context (carrying cancellation/deadlines) reaches the MCP
// call, capped by the tool's own timeout.
func (t *mcpTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*builtin.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	res, err := t.manager.CallTool(ctx, t.server, t.def.Name, params)
	if err != nil {
		return &builtin.Result{
			Success: false,
			Error:   fmt.Sprintf("mcp tool call failed: %v", err),
		}, nil
	}

	text, blocks := flattenMCPContent(res.Content)
	if res.IsError {
		msg := strings.TrimSpace(text)
		if msg == "" {
			msg = "mcp server reported a tool error"
		}
		return &builtin.Result{Success: false, Error: msg}, nil
	}

	return &builtin.Result{
		Success: true,
		Data: map[string]any{
			"output": text,
			"server": t.server,
			"tool":   t.def.Name,
			"blocks": blocks,
		},
	}, nil
}

// mcpToolNameSanitizer replaces any character that is not safe in a
// Buckley tool identifier with an underscore.
var mcpToolNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

// mcpToolName builds the bridged tool name "mcp_<server>_<tool>". Server
// and tool names come from an external, untrusted process, so both are
// sanitized to a safe identifier before joining.
func mcpToolName(server, tool string) string {
	return fmt.Sprintf("mcp_%s_%s", sanitizeMCPIdent(server), sanitizeMCPIdent(tool))
}

func sanitizeMCPIdent(s string) string {
	s = strings.Trim(mcpToolNameSanitizer.ReplaceAllString(strings.TrimSpace(s), "_"), "_")
	if s == "" {
		return "unknown"
	}
	return s
}

// truncateBytes shortens s to at most max bytes, appending "..." when
// truncated and never splitting a UTF-8 rune.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const suffix = "..."
	if max <= len(suffix) {
		return s[:max]
	}
	cut := max - len(suffix)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// flattenMCPContent joins every text content block (in order) into a
// single string for Data["output"], and returns a parallel slice of short
// placeholders describing every block (including non-text ones), for
// Data["blocks"]. Image, audio, and resource content are never inlined:
// only a bounded summary is returned, since embedding base64 payloads or
// raw resource bytes in a tool result would blow the schema/output budget.
func flattenMCPContent(content []sdkmcp.Content) (text string, blocks []string) {
	var texts []string
	for _, block := range content {
		switch b := block.(type) {
		case *sdkmcp.TextContent:
			texts = append(texts, b.Text)
			blocks = append(blocks, fmt.Sprintf("[text: %d bytes]", len(b.Text)))
		case *sdkmcp.ImageContent:
			blocks = append(blocks, fmt.Sprintf("[image: %s, %d bytes]", orUnknownMIME(b.MIMEType), len(b.Data)))
		case *sdkmcp.AudioContent:
			blocks = append(blocks, fmt.Sprintf("[audio: %s, %d bytes]", orUnknownMIME(b.MIMEType), len(b.Data)))
		case *sdkmcp.ResourceLink:
			blocks = append(blocks, fmt.Sprintf("[resource link: %s]", b.URI))
		case *sdkmcp.EmbeddedResource:
			uri := ""
			if b.Resource != nil {
				uri = b.Resource.URI
			}
			blocks = append(blocks, fmt.Sprintf("[embedded resource: %s]", uri))
		default:
			blocks = append(blocks, "[unrecognized content block]")
		}
	}
	return strings.Join(texts, "\n"), blocks
}

func orUnknownMIME(mimeType string) string {
	if strings.TrimSpace(mimeType) == "" {
		return "unknown mime type"
	}
	return mimeType
}

// mcpInputSchemaToParameters translates an MCP tool's inputSchema (a JSON
// Schema object, decoded client-side as map[string]any) into Buckley's
// ParameterSchema, recursing through nested object and array properties.
func mcpInputSchemaToParameters(schema any) builtin.ParameterSchema {
	root, _ := schema.(map[string]any)
	out := builtin.ParameterSchema{
		Type:       schemaTypeOrDefault(root, "object"),
		Properties: make(map[string]builtin.PropertySchema),
	}
	if root == nil {
		return out
	}
	if props, ok := root["properties"].(map[string]any); ok {
		for name, raw := range props {
			if propMap, ok := raw.(map[string]any); ok {
				out.Properties[name] = mcpPropertySchema(propMap)
			}
		}
	}
	out.Required = schemaStringList(root["required"])
	if ap, ok := root["additionalProperties"]; ok {
		out.AdditionalProperties = ap
	}
	return out
}

func mcpPropertySchema(m map[string]any) builtin.PropertySchema {
	prop := builtin.PropertySchema{
		Type:        schemaTypeOrDefault(m, ""),
		Description: schemaStringField(m, "description"),
		Required:    schemaStringList(m["required"]),
	}
	if def, ok := m["default"]; ok {
		prop.Default = def
	}
	if items, ok := m["items"].(map[string]any); ok {
		sub := mcpPropertySchema(items)
		prop.Items = &sub
	}
	prop.Enum = schemaStringList(m["enum"])
	if nested, ok := m["properties"].(map[string]any); ok {
		prop.Properties = make(map[string]builtin.PropertySchema, len(nested))
		for name, raw := range nested {
			if nm, ok := raw.(map[string]any); ok {
				prop.Properties[name] = mcpPropertySchema(nm)
			}
		}
	}
	if ap, ok := m["additionalProperties"]; ok {
		prop.AdditionalProperties = ap
	}
	return prop
}

func schemaTypeOrDefault(m map[string]any, def string) string {
	if m == nil {
		return def
	}
	if s, ok := m["type"].(string); ok {
		return s
	}
	return def
}

func schemaStringField(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func schemaStringList(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// mcpServerTimeouts indexes cfg's per-server call timeouts by server name,
// for servers that set one explicitly.
func mcpServerTimeouts(cfg config.MCPConfig) map[string]time.Duration {
	out := make(map[string]time.Duration, len(cfg.Servers))
	for _, srv := range cfg.Servers {
		if srv.Timeout > 0 {
			out[strings.TrimSpace(srv.Name)] = srv.Timeout
		}
	}
	return out
}

// RegisterMCPTools bridges every tool exposed by manager's connected
// servers into r as a Buckley Tool named "mcp_<server>_<tool>", using the
// registry's existing Register/SetToolKind surface (no registry_setup.go
// changes are needed for a caller to add a dynamic tool source). Each
// server's tool list is capped at cfg.MaxToolsOrDefault(); a server
// offering more has the excess dropped, with a log line naming the server
// and the drop count. Returns the names of every tool registered.
func RegisterMCPTools(r *Registry, manager *mcp.Manager, cfg config.MCPConfig) []string {
	if r == nil || manager == nil {
		return nil
	}
	maxTools := cfg.MaxToolsOrDefault()
	timeouts := mcpServerTimeouts(cfg)

	byServer := make(map[string][]*sdkmcp.Tool)
	var serverOrder []string
	for _, tws := range manager.AllTools() {
		if _, seen := byServer[tws.Server]; !seen {
			serverOrder = append(serverOrder, tws.Server)
		}
		byServer[tws.Server] = append(byServer[tws.Server], tws.Tool)
	}

	var registered []string
	for _, server := range serverOrder {
		tools := byServer[server]
		if len(tools) > maxTools {
			log.Printf("mcp: server %q offers %d tools, exceeding mcp.max_tools=%d; dropping %d tool(s)",
				server, len(tools), maxTools, len(tools)-maxTools)
			tools = tools[:maxTools]
		}
		timeout := timeouts[server]
		for _, def := range tools {
			bt := newMCPTool(manager, server, def, timeout)
			r.Register(bt)
			r.SetToolKind(bt.Name(), "execute")
			registered = append(registered, bt.Name())
		}
	}
	return registered
}
