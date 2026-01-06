package tool

import (
	"encoding/json"

	"github.com/odvcencio/buckley/pkg/encoding/toon"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

var resultCodec = toon.New(true)

// SetResultEncoding toggles whether tool outputs use TOON or JSON encoding.
func SetResultEncoding(useToon bool) {
	resultCodec = toon.New(useToon)
}

// Tool represents a tool that can be called by the LLM
//
//go:generate mockgen -package=tool -destination=mock_tool_test.go github.com/odvcencio/buckley/pkg/tool Tool
type Tool interface {
	Name() string
	Description() string
	Parameters() builtin.ParameterSchema
	Execute(params map[string]any) (*builtin.Result, error)
}

// ToOpenAIFunction converts a tool to OpenAI function calling format
func ToOpenAIFunction(t Tool) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        t.Name(),
			"description": t.Description(),
			"parameters":  t.Parameters(),
		},
	}
}

// ToJSON converts a result to JSON
func ToJSON(r *builtin.Result) (string, error) {
	data, err := resultCodec.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// FromJSON parses a result from JSON
func FromJSON(jsonStr string) (*builtin.Result, error) {
	var result builtin.Result
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, err
	}
	return &result, nil
}
