package tools

import "encoding/json"

// Definition describes a tool that a model can call.
// This is the contract between Buckley and the model.
type Definition struct {
	// Name is the tool identifier (e.g., "generate_commit")
	Name string `json:"name"`

	// Description explains what the tool does (shown to the model)
	Description string `json:"description"`

	// Parameters defines the JSON schema for tool arguments
	Parameters Schema `json:"parameters"`
}

// ToOpenAIFormat converts the definition to OpenAI function calling format.
// This is used for models that support OpenAI-style tool use.
func (d Definition) ToOpenAIFormat() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        d.Name,
			"description": d.Description,
			"parameters":  d.Parameters,
		},
	}
}

// ToAnthropicFormat converts the definition to Anthropic tool format.
// This is used for Claude and other Anthropic models.
func (d Definition) ToAnthropicFormat() map[string]any {
	return map[string]any{
		"name":         d.Name,
		"description":  d.Description,
		"input_schema": d.Parameters,
	}
}

// ToolCall represents a tool invocation from a model response.
type ToolCall struct {
	// ID is the unique identifier for this tool call (from the model)
	ID string `json:"id"`

	// Name is the tool that was called
	Name string `json:"name"`

	// Arguments is the raw JSON arguments from the model
	Arguments json.RawMessage `json:"arguments"`
}

// Unmarshal decodes the tool call arguments into the given type.
func (tc ToolCall) Unmarshal(v any) error {
	return json.Unmarshal(tc.Arguments, v)
}

// ToolResult represents the result of executing a tool.
type ToolResult struct {
	// CallID matches the ToolCall.ID this is responding to
	CallID string `json:"tool_call_id"`

	// Content is the result content (usually JSON or text)
	Content string `json:"content"`

	// IsError indicates if the tool execution failed
	IsError bool `json:"is_error,omitempty"`
}

// NewToolResult creates a successful tool result.
func NewToolResult(callID string, content any) (ToolResult, error) {
	var contentStr string
	switch v := content.(type) {
	case string:
		contentStr = v
	case []byte:
		contentStr = string(v)
	default:
		data, err := json.Marshal(content)
		if err != nil {
			return ToolResult{}, err
		}
		contentStr = string(data)
	}
	return ToolResult{
		CallID:  callID,
		Content: contentStr,
	}, nil
}

// NewToolError creates an error tool result.
func NewToolError(callID string, err error) ToolResult {
	return ToolResult{
		CallID:  callID,
		Content: err.Error(),
		IsError: true,
	}
}
