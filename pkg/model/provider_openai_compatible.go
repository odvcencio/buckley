package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

// OpenAICompatibleProvider connects to an OpenAI-compatible chat API.
type OpenAICompatibleProvider struct {
	providerID                           string
	modelPrefix                          string
	liteLLMInfo                          bool
	baseURL                              string
	apiKey                               string
	httpClient                           *http.Client
	transport                            *ProviderTransport
	modelCacheMu                         sync.RWMutex
	modelCache                           []ModelInfo
	cacheTTL                             time.Duration
	cacheTime                            time.Time
	observedParamsMu                     sync.RWMutex
	observedParams                       map[string]map[string]struct{}
	staticModels                         []string
	staticParams                         map[string][]string
	staticContext                        map[string]int
	streamIdle                           time.Duration
	streamFirstContent                   time.Duration
	streamFirstContentMaxReasoningChunks int
}

// LiteLLMProvider is the deprecated name for OpenAICompatibleProvider.
type LiteLLMProvider = OpenAICompatibleProvider

// NewOpenAICompatibleProvider builds the canonical generic provider.
func NewOpenAICompatibleProvider(cfg config.OpenAICompatibleConfig, networkLogsEnabled bool) *OpenAICompatibleProvider {
	return newOpenAICompatibleProvider("openai_compatible", false, cfg, networkLogsEnabled)
}

// NewLiteLLMProvider builds the deprecated LiteLLM-compatible provider.
func NewLiteLLMProvider(cfg config.LiteLLMConfig, networkLogsEnabled bool) *OpenAICompatibleProvider {
	return newOpenAICompatibleProvider("litellm", true, cfg, networkLogsEnabled)
}

func newOpenAICompatibleProvider(providerID string, liteLLMInfo bool, cfg config.OpenAICompatibleConfig, networkLogsEnabled bool) *OpenAICompatibleProvider {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" && liteLLMInfo {
		baseURL = "http://localhost:4000"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	transport := NewLoggingTransportWithEnabled(nil, networkLogsEnabled)
	return &OpenAICompatibleProvider{
		providerID:                           providerID,
		modelPrefix:                          providerID + "/",
		liteLLMInfo:                          liteLLMInfo,
		baseURL:                              baseURL,
		apiKey:                               strings.TrimSpace(cfg.APIKey),
		httpClient:                           &http.Client{Timeout: defaultTimeout, Transport: transport},
		transport:                            NewProviderTransport(ProviderTransportOptions{}),
		cacheTTL:                             5 * time.Minute,
		observedParams:                       make(map[string]map[string]struct{}),
		staticModels:                         cfg.Models,
		staticParams:                         cfg.SupportedParameters,
		staticContext:                        cfg.ContextLengths,
		streamIdle:                           cfg.StreamIdleTimeout,
		streamFirstContent:                   cfg.StreamFirstContentTimeout,
		streamFirstContentMaxReasoningChunks: cfg.StreamFirstContentMaxReasoningChunks,
	}
}

// ID returns provider identifier.
func (p *OpenAICompatibleProvider) ID() string {
	return p.providerID
}

// FetchCatalog returns model metadata from the compatible API.
func (p *OpenAICompatibleProvider) FetchCatalog() (*ModelCatalog, error) {
	p.modelCacheMu.RLock()
	if time.Since(p.cacheTime) < p.cacheTTL && len(p.modelCache) > 0 {
		models := append([]ModelInfo(nil), p.modelCache...)
		p.modelCacheMu.RUnlock()
		return &ModelCatalog{Data: p.mergeObservedCatalogParameters(models)}, nil
	}
	p.modelCacheMu.RUnlock()

	var (
		models []ModelInfo
		err    error
	)
	if p.liteLLMInfo {
		models, err = p.fetchModelInfo()
		if err != nil {
			models, err = p.fetchModels()
		}
	} else {
		models, err = p.fetchModels()
	}
	if err != nil {
		if len(p.staticModels) == 0 {
			return nil, fmt.Errorf("%s list models: %w", p.providerID, err)
		}
		models = p.buildStaticModels()
	}

	if len(models) == 0 && len(p.staticModels) > 0 {
		models = p.buildStaticModels()
	}

	p.modelCacheMu.Lock()
	p.modelCache = models
	p.cacheTime = time.Now()
	p.modelCacheMu.Unlock()
	return &ModelCatalog{Data: models}, nil
}

// GetModelInfo returns cached model metadata when available.
func (p *OpenAICompatibleProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
	catalog, err := p.FetchCatalog()
	if err != nil {
		return nil, err
	}
	for _, info := range catalog.Data {
		if info.ID == modelID {
			return &info, nil
		}
	}
	return nil, fmt.Errorf("%s model not found: %s", p.providerID, modelID)
}

// ChatCompletion executes a non-streaming request.
func (p *OpenAICompatibleProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	req.Model = strings.TrimPrefix(req.Model, p.modelPrefix)
	req.Stream = false
	return p.invoke(ctx, req)
}

// ChatCompletionStream streams responses from the compatible API.
func (p *OpenAICompatibleProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	req.Model = strings.TrimPrefix(req.Model, p.modelPrefix)
	req.Stream = true
	chunkChan := make(chan StreamChunk, 10)
	errChan := make(chan error, 1)

	go func() {
		defer close(chunkChan)
		defer close(errChan)
		if err := p.invokeStream(ctx, req, chunkChan); err != nil {
			errChan <- err
		}
	}()

	return chunkChan, errChan
}

// SetTimeout updates the client timeout (0 disables timeout).
func (p *OpenAICompatibleProvider) SetTimeout(timeout time.Duration) {
	if p.httpClient != nil {
		p.httpClient.Timeout = timeout
	}
}

func (p *OpenAICompatibleProvider) fetchModelInfo() ([]ModelInfo, error) {
	req, err := http.NewRequest("GET", p.baseURL+"/model/info", nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("model info returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			ModelName string `json:"model_name"`
			ModelInfo struct {
				MaxTokens               int     `json:"max_tokens"`
				MaxInputTokens          int     `json:"max_input_tokens"`
				MaxOutputTokens         int     `json:"max_output_tokens"`
				InputCostPerToken       float64 `json:"input_cost_per_token"`
				OutputCostPerToken      float64 `json:"output_cost_per_token"`
				Mode                    string  `json:"mode"`
				SupportsFunctionCalling bool    `json:"supports_function_calling"`
				SupportsVision          bool    `json:"supports_vision"`
			} `json:"model_info"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ModelInfo.Mode != "" && m.ModelInfo.Mode != "chat" {
			continue
		}

		contextLength := m.ModelInfo.MaxInputTokens
		if contextLength <= 0 {
			contextLength = m.ModelInfo.MaxTokens
		}

		info := ModelInfo{
			ID:                  m.ModelName,
			Name:                m.ModelName,
			ContextLength:       contextLength,
			MaxCompletionTokens: max(m.ModelInfo.MaxOutputTokens, 0),
			Pricing: ModelPricing{
				Prompt:     m.ModelInfo.InputCostPerToken * 1_000_000,
				Completion: m.ModelInfo.OutputCostPerToken * 1_000_000,
			},
		}
		if m.ModelInfo.SupportsFunctionCalling {
			info.SupportedParameters = []string{"tools", "functions"}
		}
		if m.ModelInfo.SupportsVision {
			info.Architecture = Architecture{Modality: "text+image"}
		}

		if normalized, ok := p.normalizeFetchedModelInfo(openAICompatibleFetchedModel{ModelInfo: info}); ok {
			models = append(models, normalized)
		}
	}

	return models, nil
}

func (p *OpenAICompatibleProvider) fetchModels() ([]ModelInfo, error) {
	req, err := http.NewRequest("GET", p.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("models returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data []openAICompatibleFetchedModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]ModelInfo, 0, len(result.Data))
	for _, fetched := range result.Data {
		info, ok := p.normalizeFetchedModelInfo(fetched)
		if !ok {
			continue
		}
		models = append(models, info)
	}
	return models, nil
}

type openAICompatibleFetchedModel struct {
	ModelInfo
	MaxModelLen int `json:"max_model_len"`
}

func (m *openAICompatibleFetchedModel) UnmarshalJSON(data []byte) error {
	var info ModelInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return err
	}
	var extra struct {
		MaxModelLen int `json:"max_model_len"`
	}
	if err := json.Unmarshal(data, &extra); err != nil {
		return err
	}
	m.ModelInfo = info
	m.MaxModelLen = extra.MaxModelLen
	return nil
}

func (p *OpenAICompatibleProvider) normalizeFetchedModelInfo(fetched openAICompatibleFetchedModel) (ModelInfo, bool) {
	info := fetched.ModelInfo
	rawID := strings.TrimSpace(info.ID)
	if rawID == "" {
		return ModelInfo{}, false
	}

	id := rawID
	if !strings.HasPrefix(id, p.modelPrefix) {
		id = p.modelPrefix + id
	}

	name := strings.TrimSpace(info.Name)
	if name == "" {
		name = rawID
	}
	info.ID = id
	info.Name = strings.TrimPrefix(name, p.modelPrefix)

	contextLength := info.ContextLength
	if contextLength <= 0 {
		contextLength = fetched.MaxModelLen
	}
	if contextLength <= 0 {
		contextLength = 8192
	}
	info.ContextLength = p.configuredContextLength(info.ID, contextLength)

	if info.Architecture.Modality == "" {
		info.Architecture.Modality = "text"
	}
	info.SupportedParameters = p.mergeSupportedParameters(info.ID, info.SupportedParameters)
	return info, true
}

func (p *OpenAICompatibleProvider) buildStaticModels() []ModelInfo {
	models := make([]ModelInfo, 0, len(p.staticModels))
	for _, raw := range p.staticModels {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id := raw
		if !strings.HasPrefix(id, p.modelPrefix) {
			id = p.modelPrefix + id
		}
		name := strings.TrimPrefix(id, p.modelPrefix)
		info := ModelInfo{
			ID:            id,
			Name:          name,
			ContextLength: p.configuredContextLength(id, 8192),
			Architecture:  Architecture{Modality: "text"},
		}
		info.SupportedParameters = p.mergeSupportedParameters(info.ID, nil)
		models = append(models, info)
	}
	return models
}

func (p *OpenAICompatibleProvider) configuredContextLength(modelID string, fallback int) int {
	configured := p.staticContext[modelID]
	if configured <= 0 {
		configured = p.staticContext[strings.TrimPrefix(modelID, p.modelPrefix)]
	}
	if configured > 0 {
		return configured
	}
	return fallback
}

func (p *OpenAICompatibleProvider) mergeSupportedParameters(modelID string, discovered []string) []string {
	parameters := append([]string(nil), discovered...)
	configured := p.staticParams[modelID]
	if len(configured) == 0 {
		configured = p.staticParams[strings.TrimPrefix(modelID, p.modelPrefix)]
	}
	for _, parameter := range configured {
		parameter = strings.TrimSpace(parameter)
		if parameter != "" && !containsString(parameters, parameter) {
			parameters = append(parameters, parameter)
		}
	}
	for _, parameter := range p.observedParameters(modelID) {
		if !containsString(parameters, parameter) {
			parameters = append(parameters, parameter)
		}
	}
	return parameters
}

func (p *OpenAICompatibleProvider) canonicalModelID(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" || strings.HasPrefix(modelID, p.modelPrefix) {
		return modelID
	}
	return p.modelPrefix + modelID
}

func (p *OpenAICompatibleProvider) observedParameters(modelID string) []string {
	canonical := p.canonicalModelID(modelID)
	p.observedParamsMu.RLock()
	observed := p.observedParams[canonical]
	parameters := make([]string, 0, len(observed))
	for parameter := range observed {
		parameters = append(parameters, parameter)
	}
	p.observedParamsMu.RUnlock()
	sort.Strings(parameters)
	return parameters
}

// observeSupportedParameter records exact output-wire evidence for one model.
// It does not infer related input controls such as reasoning_effort.
func (p *OpenAICompatibleProvider) observeSupportedParameter(modelID, parameter string) {
	canonical := p.canonicalModelID(modelID)
	parameter = strings.TrimSpace(parameter)
	if canonical == "" || parameter == "" {
		return
	}
	p.observedParamsMu.Lock()
	observed := p.observedParams[canonical]
	if observed == nil {
		observed = make(map[string]struct{})
		p.observedParams[canonical] = observed
	}
	if _, exists := observed[parameter]; exists {
		p.observedParamsMu.Unlock()
		return
	}
	observed[parameter] = struct{}{}
	p.observedParamsMu.Unlock()
}

func (p *OpenAICompatibleProvider) mergeObservedCatalogParameters(models []ModelInfo) []ModelInfo {
	for i := range models {
		models[i].SupportedParameters = p.mergeSupportedParameters(models[i].ID, models[i].SupportedParameters)
	}
	return models
}

func (p *OpenAICompatibleProvider) setAuthHeaders(req *http.Request) {
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
}

func (p *OpenAICompatibleProvider) invoke(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	data, err := p.transport.Do(ctx, p.httpClient, "POST", p.baseURL+"/chat/completions", p.compatiblePayload(req), p.setAuthHeaders)
	if err != nil {
		return nil, err
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(data, &chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	for _, choice := range chatResp.Choices {
		if choice.Message.ReasoningContent {
			p.observeSupportedParameter(req.Model, "reasoning_content")
			break
		}
	}
	chatResp.AttemptEvidence = nil
	chatResp.ExecutionIdentity = observedExecutionIdentity(chatResp.ID, chatResp.Model, nil)
	return &chatResp, nil
}

func (p *OpenAICompatibleProvider) invokeStream(ctx context.Context, req ChatRequest, chunkChan chan<- StreamChunk) error {
	payload := p.compatiblePayload(req)
	observeReasoningContent := !p.supportsParameter(req.Model, "reasoning_content")
	if p.streamIdle <= 0 && p.streamFirstContent <= 0 && p.streamFirstContentMaxReasoningChunks <= 0 && !observeReasoningContent {
		return p.transport.Stream(ctx, p.httpClient, "POST", p.baseURL+"/chat/completions", payload, p.setAuthHeaders, chunkChan)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	providerChunkStream := make(chan StreamChunk, 10)
	providerErrStream := make(chan error, 1)
	go func(providerChunks chan<- StreamChunk, errChan chan<- error) {
		errChan <- p.transport.Stream(streamCtx, p.httpClient, "POST", p.baseURL+"/chat/completions", payload, p.setAuthHeaders, providerChunks)
		close(providerChunks)
		close(errChan)
	}(providerChunkStream, providerErrStream)
	var providerChunks <-chan StreamChunk = providerChunkStream
	var errChan <-chan error = providerErrStream

	var (
		idleTimer          *time.Timer
		idleTimerC         <-chan time.Time
		firstContentTimer  *time.Timer
		firstContentTimerC <-chan time.Time
	)
	if p.streamIdle > 0 {
		idleTimer = time.NewTimer(p.streamIdle)
		idleTimerC = idleTimer.C
		defer idleTimer.Stop()
	}
	if p.streamFirstContent > 0 {
		firstContentTimer = time.NewTimer(p.streamFirstContent)
		firstContentTimerC = firstContentTimer.C
		defer firstContentTimer.Stop()
	}
	firstContentPending := p.streamFirstContent > 0 || p.streamFirstContentMaxReasoningChunks > 0
	firstContentReasoningChunks := 0
	var terminalErr error
	for providerChunks != nil || errChan != nil {
		select {
		case chunk, ok := <-providerChunks:
			if !ok {
				providerChunks = nil
				continue
			}
			if observeReasoningContent && streamChunkHasNativeReasoningContent(chunk) {
				p.observeSupportedParameter(req.Model, "reasoning_content")
				observeReasoningContent = false
			}
			if firstContentPending {
				if streamChunkHasUsableInitialResponse(chunk) {
					if firstContentTimer != nil && !stopTimer(firstContentTimer) {
						return p.streamFirstContentTimeoutError()
					}
					firstContentPending = false
					firstContentTimer = nil
					firstContentTimerC = nil
				} else if p.streamFirstContentMaxReasoningChunks > 0 && streamChunkHasReasoning(chunk) {
					firstContentReasoningChunks++
					if firstContentReasoningChunks > p.streamFirstContentMaxReasoningChunks {
						return p.streamFirstContentReasoningChunkLimitError(firstContentReasoningChunks)
					}
				}
			}
			if idleTimer != nil && streamChunkHasMeaningfulDelta(chunk) {
				resetTimer(idleTimer, p.streamIdle)
			}
			select {
			case chunkChan <- chunk:
			case <-firstContentTimerC:
				return p.streamFirstContentTimeoutError()
			case <-idleTimerC:
				return fmt.Errorf("%s stream idle timeout after %s", p.providerID, p.streamIdle)
			case <-ctx.Done():
				return ctx.Err()
			}
		case err, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			if err != nil {
				terminalErr = err
			}
			errChan = nil
		case <-firstContentTimerC:
			return p.streamFirstContentTimeoutError()
		case <-idleTimerC:
			return fmt.Errorf("%s stream idle timeout after %s", p.providerID, p.streamIdle)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return terminalErr
}

func (p *OpenAICompatibleProvider) streamFirstContentTimeoutError() error {
	return fmt.Errorf("%s stream first content timeout after %s", p.providerID, p.streamFirstContent)
}

func (p *OpenAICompatibleProvider) streamFirstContentReasoningChunkLimitError(observed int) error {
	return fmt.Errorf("%s stream first content reasoning chunk limit exceeded (%d > %d)", p.providerID, observed, p.streamFirstContentMaxReasoningChunks)
}

type openAICompatibleReasoningEffortPayload struct {
	ChatRequest
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type openAICompatibleWirePayload struct {
	ChatRequest
	Messages        []openAICompatibleWireMessage `json:"messages"`
	ReasoningEffort string                        `json:"reasoning_effort,omitempty"`
}

type openAICompatibleWireMessage struct {
	Role             string            `json:"role"`
	Content          any               `json:"content,omitempty"`
	ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	Name             string            `json:"name,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ReasoningDetails []ReasoningDetail `json:"reasoning_details,omitempty"`
}

func (p *OpenAICompatibleProvider) compatiblePayload(req ChatRequest) any {
	req.Reasoning = NormalizeReasoningConfig(req.Reasoning)
	reasoningEffort := ""
	if req.Reasoning != nil && p.supportsParameter(req.Model, "reasoning_effort") {
		if req.Reasoning.MaxTokens == 0 && req.Reasoning.Exclude == nil && (req.Reasoning.Enabled == nil || *req.Reasoning.Enabled) {
			reasoningEffort = strings.ToLower(strings.TrimSpace(req.Reasoning.Effort))
			if reasoningEffort != "" {
				req.Reasoning = nil
			}
		}
	}

	if p.supportsParameter(req.Model, "reasoning_content") {
		return openAICompatibleWirePayload{
			ChatRequest:     req,
			Messages:        openAICompatibleWireMessages(req.Messages),
			ReasoningEffort: reasoningEffort,
		}
	}
	if reasoningEffort == "" {
		return req
	}
	return openAICompatibleReasoningEffortPayload{
		ChatRequest:     req,
		ReasoningEffort: reasoningEffort,
	}
}

func openAICompatibleWireMessages(messages []Message) []openAICompatibleWireMessage {
	if messages == nil {
		return nil
	}
	out := make([]openAICompatibleWireMessage, 0, len(messages))
	for _, msg := range messages {
		wire := openAICompatibleWireMessage{
			Role:             msg.Role,
			Content:          msg.Content,
			ToolCalls:        msg.ToolCalls,
			ToolCallID:       msg.ToolCallID,
			Name:             msg.Name,
			ReasoningDetails: msg.ReasoningDetails,
		}
		if msg.Role == "assistant" {
			wire.ReasoningContent = msg.Reasoning
		}
		out = append(out, wire)
	}
	return out
}

func (p *OpenAICompatibleProvider) supportsParameter(modelID, parameter string) bool {
	parameter = strings.TrimSpace(parameter)
	if parameter == "" {
		return false
	}
	modelID = strings.TrimSpace(modelID)
	canonical := modelID
	if !strings.HasPrefix(canonical, p.modelPrefix) {
		canonical = p.modelPrefix + canonical
	}
	if containsString(p.mergeSupportedParameters(canonical, nil), parameter) {
		return true
	}
	p.modelCacheMu.RLock()
	defer p.modelCacheMu.RUnlock()
	for _, info := range p.modelCache {
		if info.ID == canonical && containsString(info.SupportedParameters, parameter) {
			return true
		}
	}
	return false
}

func streamChunkHasMeaningfulDelta(chunk StreamChunk) bool {
	for _, choice := range chunk.Choices {
		delta := choice.Delta
		if delta.Content != "" || delta.Reasoning != "" || len(delta.ReasoningDetails) > 0 || len(delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func streamChunkHasUsableInitialResponse(chunk StreamChunk) bool {
	for _, choice := range chunk.Choices {
		delta := choice.Delta
		if delta.Content != "" || len(delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func streamChunkHasReasoning(chunk StreamChunk) bool {
	for _, choice := range chunk.Choices {
		delta := choice.Delta
		if delta.Reasoning != "" || len(delta.ReasoningDetails) > 0 {
			return true
		}
	}
	return false
}

func streamChunkHasNativeReasoningContent(chunk StreamChunk) bool {
	for _, choice := range chunk.Choices {
		if choice.Delta.ReasoningContent {
			return true
		}
	}
	return false
}

func stopTimer(timer *time.Timer) bool {
	if timer == nil || timer.Stop() {
		return true
	}
	select {
	case <-timer.C:
		return false
	default:
		return true
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}
