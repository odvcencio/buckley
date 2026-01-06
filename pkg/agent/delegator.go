package agent

import (
	"context"
	"fmt"
	"strings"

	pkgcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

// Delegator manages sub-agent execution
type Delegator struct {
	modelMgr *model.Manager
	registry *tool.Registry
	specs    map[string]*pkgcontext.SubAgentSpec
}

// NewDelegator creates a new delegator instance
func NewDelegator(mgr *model.Manager, registry *tool.Registry, specs map[string]*pkgcontext.SubAgentSpec) *Delegator {
	return &Delegator{
		modelMgr: mgr,
		registry: registry,
		specs:    specs,
	}
}

// DelegationResult holds the result of a sub-agent execution
type DelegationResult struct {
	Output       string
	Success      bool
	Cost         float64
	TokensUsed   int
	ModelUsed    string
	ErrorMessage string
}

// Delegate executes a task using a sub-agent
func (d *Delegator) Delegate(ctx context.Context, agentName string, task string) (*DelegationResult, error) {
	spec, ok := d.specs[agentName]
	if !ok {
		return nil, fmt.Errorf("sub-agent not found: %s", agentName)
	}

	if d.modelMgr == nil {
		return nil, fmt.Errorf("model manager unavailable for delegation")
	}

	// Filter tools to those allowed for this agent
	_ = d.filterTools(spec.Tools)

	// Get model for this agent (use spec model or fallback)
	modelID := spec.Model
	if modelID == "" {
		modelID = d.modelMgr.GetExecutionModel()
	}

	systemPrompt := "You are a Buckley sub-agent. Answer concisely and focus on the requested task."
	if strings.TrimSpace(spec.Instructions) != "" {
		systemPrompt = spec.Instructions
	}

	req := model.ChatRequest{
		Model: modelID,
		Messages: []model.Message{
			{
				Role:    "system",
				Content: systemPrompt,
			},
			{
				Role:    "user",
				Content: task,
			},
		},
		Temperature: 0.3,
		Tools:       d.buildToolDefinitions(d.filterTools(spec.Tools)),
		ToolChoice:  "auto",
	}

	chatResp, err := d.modelMgr.ChatCompletion(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("delegate call failed: %w", err)
	}

	content, err := model.ExtractTextContent(chatResp.Choices[0].Message.Content)
	if err != nil {
		return nil, fmt.Errorf("delegate response parse failed: %w", err)
	}

	// Execute with the model
	result := &DelegationResult{
		ModelUsed: modelID,
		Success:   true,
		Output:    content,
		TokensUsed: func() int {
			if chatResp != nil {
				return chatResp.Usage.TotalTokens
			}
			return 0
		}(),
	}

	return result, nil
}

// filterTools creates a filtered registry with only allowed tools
func (d *Delegator) filterTools(allowedTools []string) *tool.Registry {
	// If no tools specified, allow all
	if len(allowedTools) == 0 {
		return d.registry
	}

	// Create an empty registry (without built-in tools)
	filtered := tool.NewEmptyRegistry()

	// Create a set of allowed tool names for O(1) lookup
	allowed := make(map[string]bool, len(allowedTools))
	for _, name := range allowedTools {
		allowed[name] = true
	}

	// Copy only allowed tools from the original registry
	for _, t := range d.registry.List() {
		if allowed[t.Name()] {
			filtered.Register(t)
		}
	}

	return filtered
}

// buildToolDefinitions converts tools to OpenAI function format
func (d *Delegator) buildToolDefinitions(registry *tool.Registry) []map[string]any {
	tools := []map[string]any{}

	for _, t := range registry.List() {
		tools = append(tools, tool.ToOpenAIFunction(t))
	}

	return tools
}

// ListAgents returns all available sub-agents
func (d *Delegator) ListAgents() []string {
	agents := []string{}
	for name := range d.specs {
		agents = append(agents, name)
	}
	return agents
}

// GetSpec returns the specification for a sub-agent
func (d *Delegator) GetSpec(agentName string) (*pkgcontext.SubAgentSpec, bool) {
	spec, ok := d.specs[agentName]
	return spec, ok
}
