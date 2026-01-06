package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const ollamaBaseURL = "http://localhost:11434"

// OllamaProvider implements Provider for local Ollama instances.
type OllamaProvider struct {
	baseURL    string
	httpClient *http.Client
}

// NewOllamaProvider builds an Ollama provider.
func NewOllamaProvider(baseURL string, networkLogsEnabled bool) *OllamaProvider {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = ollamaBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	transport := NewLoggingTransportWithEnabled(nil, networkLogsEnabled)
	return &OllamaProvider{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:   defaultTimeout,
			Transport: transport,
		},
	}
}

// ID returns provider identifier.
func (p *OllamaProvider) ID() string {
	return "ollama"
}

// FetchCatalog returns the list of models known to Ollama.
func (p *OllamaProvider) FetchCatalog() (*ModelCatalog, error) {
	resp, err := p.httpClient.Get(p.baseURL + "/api/tags")
	if err != nil {
		return nil, fmt.Errorf("ollama list models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama list models failed (%d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode ollama catalog: %w", err)
	}

	models := make([]ModelInfo, 0, len(result.Models))
	for _, model := range result.Models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			continue
		}
		models = append(models, ModelInfo{
			ID:            "ollama/" + name,
			Name:          name,
			ContextLength: 8192,
			Pricing: ModelPricing{
				Prompt:     0,
				Completion: 0,
			},
			Architecture: Architecture{
				Modality: "text",
			},
			SupportedParameters: []string{"tools", "functions"},
		})
	}

	return &ModelCatalog{Data: models}, nil
}

// GetModelInfo returns model metadata for a given ID.
func (p *OllamaProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
	catalog, err := p.FetchCatalog()
	if err != nil {
		return nil, err
	}
	for _, info := range catalog.Data {
		if info.ID == modelID {
			return &info, nil
		}
	}
	return nil, fmt.Errorf("ollama model not found: %s", modelID)
}

// ChatCompletion executes a non-streaming chat request.
func (p *OllamaProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	ollamaReq, err := p.buildRequest(req, false)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama chat failed (%d): %s", resp.StatusCode, string(body))
	}

	var chatResp ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decode ollama response: %w", err)
	}

	msg := p.toModelMessage(chatResp.Message)
	usage := usageFromOllama(chatResp.PromptEvalCount, chatResp.EvalCount)

	return &ChatResponse{
		ID:    "",
		Model: chatResp.Model,
		Choices: []Choice{
			{
				Index:        0,
				Message:      msg,
				FinishReason: finishReason(chatResp.DoneReason),
			},
		},
		Usage: usage,
	}, nil
}

// ChatCompletionStream streams responses from Ollama.
func (p *OllamaProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	chunkChan := make(chan StreamChunk, 10)
	errChan := make(chan error, 1)

	ollamaReq, err := p.buildRequest(req, true)
	if err != nil {
		errChan <- err
		close(chunkChan)
		close(errChan)
		return chunkChan, errChan
	}

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		errChan <- fmt.Errorf("marshal ollama request: %w", err)
		close(chunkChan)
		close(errChan)
		return chunkChan, errChan
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		errChan <- err
		close(chunkChan)
		close(errChan)
		return chunkChan, errChan
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		errChan <- err
		close(chunkChan)
		close(errChan)
		return chunkChan, errChan
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		errChan <- fmt.Errorf("ollama stream failed (%d): %s", resp.StatusCode, string(body))
		close(chunkChan)
		close(errChan)
		return chunkChan, errChan
	}

	go func() {
		defer resp.Body.Close()
		defer close(chunkChan)
		defer close(errChan)

		reader := bufio.NewReader(resp.Body)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line, err := reader.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					return
				}
				errChan <- err
				return
			}

			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}

			var chunkResp ollamaChatResponse
			if err := json.Unmarshal(line, &chunkResp); err != nil {
				continue
			}

			delta := MessageDelta{
				Role:    chunkResp.Message.Role,
				Content: chunkResp.Message.Content,
			}
			if len(chunkResp.Message.ToolCalls) > 0 {
				delta.ToolCalls = toToolCallDeltas(chunkResp.Message.ToolCalls)
			}

			var finish *string
			if chunkResp.Done {
				reason := finishReason(chunkResp.DoneReason)
				finish = &reason
			}

			if delta.Role == "" && delta.Content == "" && len(delta.ToolCalls) == 0 && finish == nil {
				continue
			}

			stream := StreamChunk{
				Model: chunkResp.Model,
				Choices: []StreamChoice{
					{
						Index:        0,
						Delta:        delta,
						FinishReason: finish,
					},
				},
			}

			if chunkResp.Done {
				usage := usageFromOllama(chunkResp.PromptEvalCount, chunkResp.EvalCount)
				stream.Usage = &usage
			}

			chunkChan <- stream
		}
	}()

	return chunkChan, errChan
}

// SetTimeout updates the Ollama client timeout (0 disables timeout).
func (p *OllamaProvider) SetTimeout(timeout time.Duration) {
	if p.httpClient != nil {
		p.httpClient.Timeout = timeout
	}
}

type ollamaChatRequest struct {
	Model    string           `json:"model"`
	Messages []ollamaMessage  `json:"messages"`
	Stream   bool             `json:"stream,omitempty"`
	Tools    []map[string]any `json:"tools,omitempty"`
	Options  map[string]any   `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

type ollamaToolCall struct {
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type ollamaChatResponse struct {
	Model           string        `json:"model"`
	Message         ollamaMessage `json:"message"`
	Done            bool          `json:"done"`
	DoneReason      string        `json:"done_reason"`
	PromptEvalCount int           `json:"prompt_eval_count"`
	EvalCount       int           `json:"eval_count"`
}

func (p *OllamaProvider) buildRequest(req ChatRequest, stream bool) (*ollamaChatRequest, error) {
	messages := make([]ollamaMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		converted, err := toOllamaMessage(msg)
		if err != nil {
			return nil, err
		}
		messages = append(messages, converted)
	}

	options := map[string]any{}
	if req.Temperature != 0 {
		options["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		options["num_predict"] = req.MaxTokens
	}
	if len(options) == 0 {
		options = nil
	}

	ollamaReq := &ollamaChatRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   stream,
		Tools:    nil,
		Options:  options,
	}
	if len(req.Tools) > 0 {
		ollamaReq.Tools = req.Tools
	}
	return ollamaReq, nil
}

func toOllamaMessage(msg Message) (ollamaMessage, error) {
	content := ""
	if msg.Content != nil {
		content = messageContentToText(msg.Content)
	}
	out := ollamaMessage{
		Role:       msg.Role,
		Content:    content,
		ToolCallID: msg.ToolCallID,
		Name:       msg.Name,
	}
	if len(msg.ToolCalls) > 0 {
		out.ToolCalls = make([]ollamaToolCall, 0, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			raw := rawArguments(call.Function.Arguments)
			out.ToolCalls = append(out.ToolCalls, ollamaToolCall{
				ID:   call.ID,
				Type: call.Type,
				Function: ollamaToolFunction{
					Name:      call.Function.Name,
					Arguments: raw,
				},
			})
		}
	}
	return out, nil
}

func (p *OllamaProvider) toModelMessage(msg ollamaMessage) Message {
	out := Message{
		Role:       msg.Role,
		Content:    msg.Content,
		ToolCallID: msg.ToolCallID,
		Name:       msg.Name,
	}
	if len(msg.ToolCalls) > 0 {
		out.ToolCalls = make([]ToolCall, 0, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			arguments := string(bytes.TrimSpace(call.Function.Arguments))
			if arguments == "" {
				arguments = "{}"
			}
			callType := call.Type
			if callType == "" {
				callType = "function"
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:   call.ID,
				Type: callType,
				Function: FunctionCall{
					Name:      call.Function.Name,
					Arguments: arguments,
				},
			})
		}
	}
	return out
}

func toToolCallDeltas(calls []ollamaToolCall) []ToolCallDelta {
	deltas := make([]ToolCallDelta, 0, len(calls))
	for i, call := range calls {
		args := string(bytes.TrimSpace(call.Function.Arguments))
		if args == "" {
			args = "{}"
		}
		callType := call.Type
		if callType == "" {
			callType = "function"
		}
		deltas = append(deltas, ToolCallDelta{
			Index: i,
			ID:    call.ID,
			Type:  callType,
			Function: &FunctionCallDelta{
				Name:      call.Function.Name,
				Arguments: args,
			},
		})
	}
	return deltas
}

func rawArguments(args string) json.RawMessage {
	args = strings.TrimSpace(args)
	if args == "" {
		return json.RawMessage([]byte(`{}`))
	}
	if !json.Valid([]byte(args)) {
		escaped, _ := json.Marshal(args)
		return json.RawMessage(escaped)
	}
	return json.RawMessage([]byte(args))
}

func usageFromOllama(promptTokens, completionTokens int) Usage {
	total := promptTokens + completionTokens
	return Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      total,
	}
}

func finishReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "stop"
	}
	return reason
}
