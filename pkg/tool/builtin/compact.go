package builtin

import (
	"context"

	"github.com/odvcencio/buckley/pkg/conversation"
)

// CompactContextTool triggers background conversation compaction.
type CompactContextTool struct {
	compactor *conversation.CompactionManager
}

// NewCompactContextTool creates a compact_context tool.
func NewCompactContextTool(compactor *conversation.CompactionManager) *CompactContextTool {
	return &CompactContextTool{compactor: compactor}
}

func (t *CompactContextTool) Name() string {
	return "compact_context"
}

func (t *CompactContextTool) Description() string {
	return "Summarize older conversation context to free up token budget. Use when conversation is long or before complex multi-step work."
}

func (t *CompactContextTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type:       "object",
		Properties: map[string]PropertySchema{},
	}
}

func (t *CompactContextTool) Execute(_ map[string]any) (*Result, error) {
	if t == nil || t.compactor == nil {
		return &Result{
			Success: false,
			Error:   "compaction manager unavailable",
		}, nil
	}

	t.compactor.CompactAsync(context.Background())

	return &Result{
		Success: true,
		Data: map[string]any{
			"status": "compaction_started",
		},
	}, nil
}
