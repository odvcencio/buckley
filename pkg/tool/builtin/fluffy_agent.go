package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

// FluffyAgentTool provides interaction with fluffy-ui apps via agent socket.
type FluffyAgentTool struct{}

func (t *FluffyAgentTool) Name() string {
	return "fluffy_agent"
}

func (t *FluffyAgentTool) Description() string {
	return `Interact with a fluffy-ui application via its agent socket.

Actions:
- snapshot: Get the current UI state as an accessibility tree
- type: Type text into the focused widget
- key: Send a key press (enter, tab, escape, up, down, left, right, etc.)
- click: Click at coordinates (x, y)

The socket address should be in format "unix:/path/to/socket" or "tcp:host:port".
This tool enables AI agents to observe and control fluffy-ui applications programmatically.`
}

func (t *FluffyAgentTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"socket": {
				Type:        "string",
				Description: "Agent socket address (unix:/path or tcp:host:port)",
			},
			"action": {
				Type:        "string",
				Description: "Action to perform: snapshot, type, key, click",
				Enum:        []string{"snapshot", "type", "key", "click"},
			},
			"text": {
				Type:        "string",
				Description: "Text to type (for 'type' action)",
			},
			"key": {
				Type:        "string",
				Description: "Key name (for 'key' action): enter, tab, escape, up, down, left, right, backspace, delete, home, end, pageup, pagedown, f1-f12, or single character",
			},
			"x": {
				Type:        "integer",
				Description: "X coordinate (for 'click' action)",
			},
			"y": {
				Type:        "integer",
				Description: "Y coordinate (for 'click' action)",
			},
			"include_text": {
				Type:        "boolean",
				Description: "Include raw screen text in snapshot (default false)",
				Default:     false,
			},
		},
		Required: []string{"socket", "action"},
	}
}

type agentRequest struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Key         string `json:"key,omitempty"`
	X           int    `json:"x,omitempty"`
	Y           int    `json:"y,omitempty"`
	Button      string `json:"button,omitempty"`
	Action      string `json:"action,omitempty"`
	IncludeText bool   `json:"include_text,omitempty"`
}

type agentResponse struct {
	OK           bool            `json:"ok"`
	Error        string          `json:"error,omitempty"`
	Message      string          `json:"message,omitempty"`
	Snapshot     json.RawMessage `json:"snapshot,omitempty"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

func (t *FluffyAgentTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *FluffyAgentTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	socketAddr, _ := params["socket"].(string)
	action, _ := params["action"].(string)

	if socketAddr == "" {
		return nil, fmt.Errorf("socket address is required")
	}
	if action == "" {
		return nil, fmt.Errorf("action is required")
	}

	// Connect to socket
	conn, err := dialAgent(socketAddr)
	if err != nil {
		return nil, fmt.Errorf("connect to agent: %w", err)
	}
	defer conn.Close()

	// Set deadline
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(10 * time.Second)
	}
	conn.SetDeadline(deadline)

	// Send hello
	helloResp, err := sendAgentRequest(conn, agentRequest{Type: "hello"})
	if err != nil {
		return nil, fmt.Errorf("agent hello: %w", err)
	}
	if !helloResp.OK {
		return nil, fmt.Errorf("agent hello failed: %s - %s", helloResp.Error, helloResp.Message)
	}

	// Execute action
	var req agentRequest
	switch action {
	case "snapshot":
		includeText, _ := params["include_text"].(bool)
		req = agentRequest{Type: "snapshot", IncludeText: includeText}

	case "type":
		text, _ := params["text"].(string)
		if text == "" {
			return nil, fmt.Errorf("text is required for type action")
		}
		req = agentRequest{Type: "text", Text: text}

	case "key":
		key, _ := params["key"].(string)
		if key == "" {
			return nil, fmt.Errorf("key is required for key action")
		}
		req = agentRequest{Type: "key", Key: key}

	case "click":
		x, _ := params["x"].(float64)
		y, _ := params["y"].(float64)
		req = agentRequest{
			Type:   "mouse",
			X:      int(x),
			Y:      int(y),
			Button: "left",
			Action: "click",
		}

	default:
		return nil, fmt.Errorf("unknown action: %s", action)
	}

	resp, err := sendAgentRequest(conn, req)
	if err != nil {
		return nil, fmt.Errorf("agent %s: %w", action, err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("agent %s failed: %s - %s", action, resp.Error, resp.Message)
	}

	// Format result
	var output string
	if action == "snapshot" && len(resp.Snapshot) > 0 {
		output = formatSnapshot(resp.Snapshot)
	} else {
		output = fmt.Sprintf("Action '%s' completed successfully", action)
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"output": output,
		},
	}, nil
}

func dialAgent(addr string) (net.Conn, error) {
	if strings.HasPrefix(addr, "unix:") {
		return net.Dial("unix", strings.TrimPrefix(addr, "unix:"))
	}
	if strings.HasPrefix(addr, "tcp:") {
		return net.Dial("tcp", strings.TrimPrefix(addr, "tcp:"))
	}
	// Try as unix socket path
	return net.Dial("unix", addr)
}

func sendAgentRequest(conn net.Conn, req agentRequest) (*agentResponse, error) {
	enc := json.NewEncoder(conn)
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}
		return nil, fmt.Errorf("connection closed")
	}

	var resp agentResponse
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &resp, nil
}

type snapshot struct {
	Timestamp  string       `json:"timestamp"`
	Width      int          `json:"width"`
	Height     int          `json:"height"`
	LayerCount int          `json:"layer_count"`
	Text       string       `json:"text,omitempty"`
	Widgets    []widgetInfo `json:"widgets"`
	FocusedID  string       `json:"focused_id,omitempty"`
}

type widgetInfo struct {
	ID          string       `json:"id"`
	Type        string       `json:"type"`
	Label       string       `json:"label,omitempty"`
	Description string       `json:"description,omitempty"`
	Value       string       `json:"value,omitempty"`
	Focused     bool         `json:"focused,omitempty"`
	Focusable   bool         `json:"focusable,omitempty"`
	Children    []widgetInfo `json:"children,omitempty"`
}

func formatSnapshot(raw json.RawMessage) string {
	var snap snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return string(raw)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Screen: %dx%d, %d layers\n", snap.Width, snap.Height, snap.LayerCount))
	if snap.FocusedID != "" {
		sb.WriteString(fmt.Sprintf("Focused: %s\n", snap.FocusedID))
	}
	sb.WriteString("\nWidget Tree:\n")
	formatWidgets(&sb, snap.Widgets, 0)

	if snap.Text != "" {
		sb.WriteString("\n--- Screen Text ---\n")
		sb.WriteString(snap.Text)
	}

	return sb.String()
}

func formatWidgets(sb *strings.Builder, widgets []widgetInfo, indent int) {
	prefix := strings.Repeat("  ", indent)
	for _, w := range widgets {
		focused := ""
		if w.Focused {
			focused = " [FOCUSED]"
		}
		sb.WriteString(fmt.Sprintf("%s- [%s] %s%s\n", prefix, w.Type, w.Label, focused))
		if w.Description != "" {
			sb.WriteString(fmt.Sprintf("%s    desc: %s\n", prefix, w.Description))
		}
		if w.Value != "" {
			sb.WriteString(fmt.Sprintf("%s    value: %s\n", prefix, w.Value))
		}
		if len(w.Children) > 0 {
			formatWidgets(sb, w.Children, indent+1)
		}
	}
}
