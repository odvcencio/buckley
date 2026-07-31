package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const openAIContinuationVersion = 1

var _ ContinuationClient = (*OpenAIProvider)(nil)

type openAIContinuationState struct {
	Version int               `json:"version"`
	Items   []json.RawMessage `json:"items"`
}

type openAIResponsesRequest struct {
	Model                string            `json:"model"`
	Input                []json.RawMessage `json:"input"`
	Store                bool              `json:"store"`
	Tools                []map[string]any  `json:"tools,omitempty"`
	ToolChoice           any               `json:"tool_choice,omitempty"`
	ParallelToolCalls    *bool             `json:"parallel_tool_calls,omitempty"`
	Reasoning            map[string]any    `json:"reasoning,omitempty"`
	MaxOutputTokens      int               `json:"max_output_tokens,omitempty"`
	Temperature          float64           `json:"temperature,omitempty"`
	Text                 map[string]any    `json:"text,omitempty"`
	Metadata             map[string]string `json:"metadata,omitempty"`
	ServiceTier          string            `json:"service_tier,omitempty"`
	PromptCacheKey       string            `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string            `json:"prompt_cache_retention,omitempty"`
}

type openAIResponsesResponse struct {
	ID                string                   `json:"id"`
	Model             string                   `json:"model"`
	Status            string                   `json:"status"`
	Output            []json.RawMessage        `json:"output"`
	Usage             openAIResponsesUsage     `json:"usage"`
	Error             *ErrorDetail             `json:"error,omitempty"`
	IncompleteDetails *openAIIncompleteDetails `json:"incomplete_details,omitempty"`
}

type openAIResponsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type openAIIncompleteDetails struct {
	Reason string `json:"reason"`
}

type openAIResponseItem struct {
	ID               string                  `json:"id"`
	Type             string                  `json:"type"`
	Role             string                  `json:"role"`
	Status           string                  `json:"status"`
	CallID           string                  `json:"call_id"`
	Name             string                  `json:"name"`
	Arguments        string                  `json:"arguments"`
	EncryptedContent string                  `json:"encrypted_content"`
	Content          []openAIResponseContent `json:"content"`
	Summary          []openAIResponseSummary `json:"summary"`
}

type openAIResponseContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type openAIResponseSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// SupportsContinuation reports whether an OpenAI model can carry native
// reasoning state through the Responses API.
func (p *OpenAIProvider) SupportsContinuation(modelID string) bool {
	canonical := openAIContinuationModelID(modelID)
	info, ok := openAIModelIndex[canonical]
	if !ok {
		return false
	}
	for _, parameter := range info.SupportedParameters {
		if parameter == "reasoning" {
			return true
		}
	}
	return false
}

// ChatCompletionWithContinuation executes a stateless Responses API turn and
// returns the full canonical item window as opaque provider continuation state.
func (p *OpenAIProvider) ChatCompletionWithContinuation(ctx context.Context, continuationReq ContinuationRequest) (*ContinuationResponse, error) {
	req := continuationReq.Request
	canonicalModel := openAIContinuationModelID(req.Model)
	if !p.SupportsContinuation(canonicalModel) {
		return nil, fmt.Errorf("openai continuation is not supported for model %s", req.Model)
	}

	var priorItems []json.RawMessage
	if continuationReq.Continuation != nil {
		if !continuationReq.Continuation.Compatible(p.ID(), canonicalModel) {
			return nil, fmt.Errorf("openai continuation belongs to %s/%s, not %s/%s",
				continuationReq.Continuation.ProviderID,
				continuationReq.Continuation.ModelID,
				p.ID(),
				canonicalModel,
			)
		}
		state, err := decodeOpenAIContinuation(continuationReq.Continuation.State)
		if err != nil {
			return nil, err
		}
		priorItems = cloneRawMessages(state.Items)
	}

	newItems, err := openAIResponseInputItems(req.Messages)
	if err != nil {
		return nil, err
	}
	input := append(priorItems, newItems...)
	apiReq, err := openAIResponseRequest(req, input)
	if err != nil {
		return nil, err
	}
	apiResp, err := p.invokeResponse(ctx, apiReq)
	if err != nil {
		return nil, err
	}
	chatResp, err := translateOpenAIResponse(apiResp)
	if err != nil {
		return nil, err
	}

	state := openAIContinuationState{
		Version: openAIContinuationVersion,
		Items:   append(cloneRawMessages(input), cloneRawMessages(apiResp.Output)...),
	}
	opaque, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("marshaling openai continuation: %w", err)
	}
	return &ContinuationResponse{
		Response: chatResp,
		Continuation: &ProviderContinuation{
			ProviderID: p.ID(),
			ModelID:    canonicalModel,
			State:      opaque,
		},
	}, nil
}

func openAIContinuationModelID(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if strings.HasPrefix(modelID, "openai/") {
		return modelID
	}
	return "openai/" + modelID
}

func decodeOpenAIContinuation(raw json.RawMessage) (*openAIContinuationState, error) {
	var state openAIContinuationState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("decoding openai continuation: %w", err)
	}
	if state.Version != openAIContinuationVersion {
		return nil, fmt.Errorf("unsupported openai continuation version: %d", state.Version)
	}
	return &state, nil
}

func cloneRawMessages(items []json.RawMessage) []json.RawMessage {
	cloned := make([]json.RawMessage, len(items))
	for i := range items {
		cloned[i] = append(json.RawMessage(nil), items[i]...)
	}
	return cloned
}

func openAIResponseRequest(req ChatRequest, input []json.RawMessage) (openAIResponsesRequest, error) {
	tools, err := openAIResponseTools(req.Tools)
	if err != nil {
		return openAIResponsesRequest{}, err
	}
	apiReq := openAIResponsesRequest{
		Model:                normalizeModelForProvider(req.Model, "openai"),
		Input:                input,
		Store:                false,
		Tools:                tools,
		ParallelToolCalls:    req.ParallelToolCalls,
		Reasoning:            openAIResponseReasoning(req),
		MaxOutputTokens:      responseMaxOutputTokens(req),
		Temperature:          req.Temperature,
		Text:                 openAIResponseTextFormat(req.ResponseFormat),
		Metadata:             req.Metadata,
		ServiceTier:          req.ServiceTier,
		PromptCacheKey:       req.PromptCacheKey,
		PromptCacheRetention: req.PromptCacheRetention,
	}
	switch choice := strings.TrimSpace(req.ToolChoice); choice {
	case "":
	case "auto", "none", "required":
		apiReq.ToolChoice = choice
	default:
		apiReq.ToolChoice = map[string]any{"type": "function", "name": choice}
	}
	return apiReq, nil
}

func responseMaxOutputTokens(req ChatRequest) int {
	if req.MaxCompletionTokens > 0 {
		return req.MaxCompletionTokens
	}
	return req.MaxTokens
}

func openAIResponseReasoning(req ChatRequest) map[string]any {
	reasoning := make(map[string]any)
	if req.Reasoning != nil && strings.TrimSpace(req.Reasoning.Effort) != "" {
		reasoning["effort"] = strings.TrimSpace(req.Reasoning.Effort)
	}
	if req.IncludeReasoning != nil && *req.IncludeReasoning {
		reasoning["summary"] = "auto"
	}
	if len(reasoning) == 0 {
		return nil
	}
	return reasoning
}

func openAIResponseTextFormat(responseFormat map[string]any) map[string]any {
	if len(responseFormat) == 0 {
		return nil
	}
	formatType, _ := responseFormat["type"].(string)
	switch formatType {
	case "json_object":
		return map[string]any{"format": map[string]any{"type": "json_object"}}
	case "json_schema":
		schema, _ := responseFormat["json_schema"].(map[string]any)
		if len(schema) == 0 {
			return nil
		}
		format := make(map[string]any, len(schema)+1)
		format["type"] = "json_schema"
		for key, value := range schema {
			format[key] = value
		}
		return map[string]any{"format": format}
	default:
		return nil
	}
}

func openAIResponseTools(tools []map[string]any) ([]map[string]any, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	translated := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		toolType, _ := tool["type"].(string)
		if toolType != "function" {
			return nil, fmt.Errorf("openai responses tool type is not supported: %s", toolType)
		}
		if function, ok := tool["function"].(map[string]any); ok {
			flattened := make(map[string]any, len(function)+1)
			flattened["type"] = "function"
			for key, value := range function {
				flattened[key] = value
			}
			translated = append(translated, flattened)
			continue
		}
		translated = append(translated, cloneAnyMap(tool))
	}
	return translated, nil
}

func cloneAnyMap(input map[string]any) map[string]any {
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func openAIResponseInputItems(messages []Message) ([]json.RawMessage, error) {
	items := make([]json.RawMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "tool" {
			if strings.TrimSpace(msg.ToolCallID) == "" {
				return nil, fmt.Errorf("openai responses tool result is missing tool_call_id")
			}
			item, err := marshalOpenAIResponseItem(map[string]any{
				"type":    "function_call_output",
				"call_id": msg.ToolCallID,
				"output":  messageContentToText(msg.Content),
			})
			if err != nil {
				return nil, err
			}
			items = append(items, item)
			continue
		}
		if msg.Role != "system" && msg.Role != "developer" && msg.Role != "user" && msg.Role != "assistant" {
			return nil, fmt.Errorf("openai responses message role is not supported: %s", msg.Role)
		}

		if content := openAIResponseMessageContent(msg); content != nil {
			item, err := marshalOpenAIResponseItem(map[string]any{
				"type":    "message",
				"role":    msg.Role,
				"content": content,
			})
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		for _, call := range msg.ToolCalls {
			item, err := marshalOpenAIResponseItem(map[string]any{
				"type":      "function_call",
				"call_id":   call.ID,
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			})
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
	}
	return items, nil
}

func openAIResponseMessageContent(msg Message) any {
	switch content := msg.Content.(type) {
	case nil:
		return nil
	case string:
		if content == "" {
			return nil
		}
		return content
	case []ContentPart:
		parts := make([]map[string]any, 0, len(content))
		for _, part := range content {
			switch part.Type {
			case "text":
				if part.Text != "" {
					parts = append(parts, map[string]any{"type": "input_text", "text": part.Text})
				}
			case "image_url":
				if part.ImageURL != nil && part.ImageURL.URL != "" {
					image := map[string]any{"type": "input_image", "image_url": part.ImageURL.URL}
					if part.ImageURL.Detail != "" {
						image["detail"] = part.ImageURL.Detail
					}
					parts = append(parts, image)
				}
			}
		}
		if len(parts) == 0 {
			return nil
		}
		return parts
	default:
		text := messageContentToText(content)
		if text == "" {
			return nil
		}
		return text
	}
}

func marshalOpenAIResponseItem(item map[string]any) (json.RawMessage, error) {
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("marshaling openai response input item: %w", err)
	}
	return raw, nil
}

// invokeResponse executes the Responses API call through the shared
// resilient transport. Retrying a transient 429/5xx happens inside this
// call, before it ever returns to ContinuationCoordinator.Call -- so a
// single 429 here is invisible to the coordinator and never triggers its
// error-path Reset, which would otherwise destroy the persisted continuation
// state and force a full-context resend on the next turn.
func (p *OpenAIProvider) invokeResponse(ctx context.Context, req openAIResponsesRequest) (*openAIResponsesResponse, error) {
	data, err := p.transport.Do(ctx, p.httpClient, http.MethodPost, p.baseURL+"/responses", req, p.setAuthHeaders)
	if err != nil {
		return nil, err
	}

	var response openAIResponsesResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decoding openai responses response: %w", err)
	}
	if response.Error != nil {
		return nil, fmt.Errorf("openai responses request failed: %s", response.Error.Message)
	}
	return &response, nil
}

func translateOpenAIResponse(response *openAIResponsesResponse) (*ChatResponse, error) {
	if response == nil {
		return nil, fmt.Errorf("openai responses response is nil")
	}
	var (
		textParts        []string
		reasoningParts   []string
		reasoningDetails []ReasoningDetail
		toolCalls        []ToolCall
	)
	for _, raw := range response.Output {
		var item openAIResponseItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("decoding openai response item: %w", err)
		}
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				switch content.Type {
				case "output_text":
					if content.Text != "" {
						textParts = append(textParts, content.Text)
					}
				case "refusal":
					if content.Refusal != "" {
						textParts = append(textParts, content.Refusal)
					}
				}
			}
		case "function_call":
			toolCalls = append(toolCalls, ToolCall{
				ID:   item.CallID,
				Type: "function",
				Function: FunctionCall{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			})
		case "reasoning":
			for index, summary := range item.Summary {
				if summary.Text == "" {
					continue
				}
				reasoningParts = append(reasoningParts, summary.Text)
				reasoningDetails = append(reasoningDetails, ReasoningDetail{
					Type:     "reasoning.summary",
					ID:       item.ID,
					Index:    index,
					HasIndex: true,
					Summary:  summary.Text,
				})
			}
		}
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	} else if response.Status != "" && response.Status != "completed" {
		finishReason = response.Status
		if response.IncompleteDetails != nil && response.IncompleteDetails.Reason != "" {
			finishReason = response.IncompleteDetails.Reason
		}
	}
	usage := Usage{
		PromptTokens:     response.Usage.InputTokens,
		CompletionTokens: response.Usage.OutputTokens,
		TotalTokens:      response.Usage.TotalTokens,
		PromptTokensDetails: &PromptTokensDetails{
			CachedTokens: response.Usage.InputTokensDetails.CachedTokens,
		},
		CompletionTokenDetails: &CompletionTokenDetails{
			ReasoningTokens: response.Usage.OutputTokensDetails.ReasoningTokens,
		},
	}
	return &ChatResponse{
		ID:    response.ID,
		Model: response.Model,
		Choices: []Choice{{
			Index: 0,
			Message: Message{
				Role:             "assistant",
				Content:          strings.Join(textParts, ""),
				ToolCalls:        toolCalls,
				Reasoning:        strings.Join(reasoningParts, "\n"),
				ReasoningDetails: reasoningDetails,
			},
			FinishReason: finishReason,
		}},
		Usage: usage,
	}, nil
}
