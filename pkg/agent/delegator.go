package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	pkgcontext "m31labs.dev/buckley/pkg/context"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/modelusage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/transparency"
)

const maxDelegationOutputBytes = 4096

var errDelegationIncomplete = errors.New("delegate task incomplete")

type delegationChatCompletion func(context.Context, model.ChatRequest) (*model.ChatResponse, error)

// Delegator manages sub-agent execution
type Delegator struct {
	modelMgr       *model.Manager
	registry       *tool.Registry
	specs          map[string]*pkgcontext.SubAgentSpec
	chatCompletion delegationChatCompletion
}

// NewDelegator creates a new delegator instance
func NewDelegator(mgr *model.Manager, registry *tool.Registry, specs map[string]*pkgcontext.SubAgentSpec) *Delegator {
	delegator := &Delegator{
		modelMgr: mgr,
		registry: registry,
		specs:    specs,
	}
	if mgr != nil {
		delegator.chatCompletion = mgr.ChatCompletion
	}
	return delegator
}

// DelegationResult holds the result of a sub-agent execution
type DelegationResult struct {
	Output          string
	Success         bool
	Incomplete      bool
	Cost            float64
	CostUnknown     bool
	TokensUsed      int
	InputTokens     int
	OutputTokens    int
	ModelUsed       string
	FinishReason    string
	Usage           *transparency.TokenUsage
	ModelExecutions []model.ExecutionIdentity
	ErrorMessage    string
}

// Delegate executes a task using a sub-agent
func (d *Delegator) Delegate(ctx context.Context, agentName string, task string) (*DelegationResult, error) {
	spec, ok := d.specs[agentName]
	if !ok {
		return nil, fmt.Errorf("sub-agent not found: %s", agentName)
	}

	if d.modelMgr == nil || d.chatCompletion == nil {
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

	chatResp, err := d.chatCompletion(ctx, req)
	result := d.delegationResultFromResponse(modelID, chatResp)

	if err != nil {
		if result == nil {
			return &DelegationResult{
				ModelUsed:    modelID,
				Success:      false,
				Incomplete:   true,
				ErrorMessage: errDelegationIncomplete.Error(),
			}, errDelegationIncomplete
		}
		result.Success = false
		result.Incomplete = true
		result.ErrorMessage = errDelegationIncomplete.Error()
		return result, errDelegationIncomplete
	}

	if result == nil {
		return &DelegationResult{
			ModelUsed:    modelID,
			Success:      false,
			Incomplete:   true,
			ErrorMessage: errDelegationIncomplete.Error(),
		}, errDelegationIncomplete
	}
	if result.Output == "" || !isConclusiveDelegateFinish(result.FinishReason) {
		result.Success = false
		result.Incomplete = true
		result.ErrorMessage = errDelegationIncomplete.Error()
		return result, errDelegationIncomplete
	}

	result.Success = true
	return result, nil
}

func (d *Delegator) delegationResultFromResponse(modelID string, chatResp *model.ChatResponse) *DelegationResult {
	if chatResp == nil {
		return nil
	}
	result := &DelegationResult{
		ModelUsed:    modelID,
		TokensUsed:   chatResp.Usage.TotalTokens,
		InputTokens:  chatResp.Usage.PromptTokens,
		OutputTokens: chatResp.Usage.CompletionTokens,
		FinishReason: firstFinishReason(chatResp),
	}
	if usage := modelusage.FromResponse(chatResp); modelusage.HasEvidence(usage) {
		cloned := transparency.CloneTokenUsage(usage)
		result.Usage = &cloned
		if cost, ok := d.authoritativeDelegationCost(modelID, chatResp, cloned); ok {
			result.Cost = cost
		} else {
			result.CostUnknown = true
		}
	}
	if chatResp.ExecutionIdentity != nil {
		result.ModelExecutions = append(result.ModelExecutions, *chatResp.ExecutionIdentity)
	}
	if len(chatResp.Choices) == 0 {
		return result
	}
	content, err := model.ExtractTextContent(chatResp.Choices[0].Message.Content)
	if err != nil {
		return result
	}
	_, public := model.ExtractThinkingContent(content)
	result.Output = truncateUTF8(strings.TrimSpace(public), maxDelegationOutputBytes)
	return result
}

func (d *Delegator) authoritativeDelegationCost(modelID string, resp *model.ChatResponse, usage transparency.TokenUsage) (float64, bool) {
	if d == nil || d.modelMgr == nil || resp == nil || !modelusage.HasEvidence(usage) {
		return 0, false
	}
	info, err := d.modelMgr.GetModelInfo(modelID)
	if err != nil {
		return 0, false
	}
	pricing := transparency.ModelPricing{
		InputPerMillion:  info.Pricing.Prompt,
		OutputPerMillion: info.Pricing.Completion,
	}
	if transparency.CostUnknownForUsage(usage, pricing) {
		return 0, false
	}
	if resp.Usage.PromptTokens == 0 && resp.Usage.CompletionTokens == 0 {
		if resp.UsagePresent && info.PricingKnown && info.Pricing.Prompt == 0 && info.Pricing.Completion == 0 {
			return 0, true
		}
		return 0, false
	}
	cost, err := d.modelMgr.CalculateBoundedCost(modelID, resp.Usage)
	if err != nil {
		return 0, false
	}
	return cost, true
}

func firstFinishReason(resp *model.ChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(resp.Choices[0].FinishReason)
}

func isConclusiveDelegateFinish(reason string) bool {
	return strings.EqualFold(strings.TrimSpace(reason), "stop")
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
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
