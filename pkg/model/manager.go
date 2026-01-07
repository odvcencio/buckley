package model

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
)

// Manager manages provider routing and model metadata.
type Manager struct {
	config         *config.Config
	providers      map[string]Provider
	providerOrder  []string
	catalog        map[string]ModelInfo
	providerModels map[string][]string
	modelProviders map[string]string
}

// NewManager creates a new model manager
func NewManager(cfg *config.Config) (*Manager, error) {
	providers, err := providerFactory(cfg)
	if err != nil {
		return nil, err
	}

	order := make([]string, 0, len(providers))
	for id := range providers {
		order = append(order, id)
	}
	sort.Strings(order)

	return &Manager{
		config:         cfg,
		providers:      providers,
		providerOrder:  order,
		catalog:        make(map[string]ModelInfo),
		providerModels: make(map[string][]string),
		modelProviders: make(map[string]string),
	}, nil
}

// Initialize fetches provider catalogs and validates configuration
func (m *Manager) Initialize() error {
	aggregated := make(map[string]ModelInfo)
	providerModels := make(map[string][]string)
	modelProviders := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errCh := make(chan error, len(m.providers))

	for _, provider := range m.providers {
		provider := provider
		wg.Add(1)
		go func() {
			defer wg.Done()
			cat, err := provider.FetchCatalog()
			if err != nil {
				errCh <- fmt.Errorf("%s catalog: %w", provider.ID(), err)
				return
			}
			mu.Lock()
			for _, info := range cat.Data {
				aggregated[info.ID] = info
				providerID := provider.ID()
				providerModels[providerID] = append(providerModels[providerID], info.ID)
				modelProviders[info.ID] = providerID
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}

	m.catalog = aggregated
	m.providerModels = providerModels
	m.modelProviders = modelProviders

	if err := m.ensureConfiguredModels(); err != nil {
		return err
	}
	return nil
}

// GetModelInfo returns information about a model
func (m *Manager) GetModelInfo(modelID string) (*ModelInfo, error) {
	if info, ok := m.catalog[modelID]; ok {
		return &info, nil
	}

	provider := m.providerForModel(modelID)
	if provider == nil {
		return nil, fmt.Errorf("no provider configured for model %s", modelID)
	}
	return provider.GetModelInfo(modelID)
}

// GetCatalog returns the merged model catalog
func (m *Manager) GetCatalog() *ModelCatalog {
	var models []ModelInfo
	for _, info := range m.catalog {
		models = append(models, info)
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
	return &ModelCatalog{Data: models}
}

// ProviderIDForModel returns the provider ID Buckley will use for a model.
func (m *Manager) ProviderIDForModel(modelID string) string {
	if m == nil {
		return ""
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return ""
	}
	if providerID, ok := m.modelProviders[modelID]; ok {
		return providerID
	}
	if provider := m.providerForModel(modelID); provider != nil {
		return provider.ID()
	}
	if strings.Contains(modelID, "/") {
		prefix := strings.SplitN(modelID, "/", 2)[0]
		if _, ok := m.providers[prefix]; ok {
			return prefix
		}
	}
	return ""
}

// SetRequestTimeout updates provider HTTP request timeouts when supported.
func (m *Manager) SetRequestTimeout(timeout time.Duration) {
	for _, provider := range m.providers {
		if configurer, ok := provider.(TimeoutConfigurer); ok {
			configurer.SetTimeout(timeout)
		}
	}
}

// ChatCompletion performs a chat completion routed to the proper provider
func (m *Manager) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	provider := m.providerForModel(req.Model)
	if provider == nil {
		return nil, fmt.Errorf("no provider configured for model %s", req.Model)
	}
	req.Model = normalizeModelForProvider(req.Model, provider.ID())
	resp, err := provider.ChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ChatCompletionStream performs a streaming chat completion
func (m *Manager) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	provider := m.providerForModel(req.Model)
	if provider == nil {
		errChan := make(chan error, 1)
		errChan <- fmt.Errorf("no provider configured for model %s", req.Model)
		close(errChan)
		return nil, errChan
	}
	req.Model = normalizeModelForProvider(req.Model, provider.ID())
	return provider.ChatCompletionStream(ctx, req)
}

func (m *Manager) providerForModel(modelID string) Provider {
	for prefix, providerID := range m.config.Providers.ModelRouting {
		if strings.HasPrefix(modelID, prefix) {
			if provider, ok := m.providers[providerID]; ok {
				return provider
			}
		}
	}

	if providerID := m.config.Models.DefaultProvider; providerID != "" {
		if provider, ok := m.providers[providerID]; ok {
			return provider
		}
	}

	for _, providerID := range m.providerOrder {
		if provider, ok := m.providers[providerID]; ok {
			return provider
		}
	}

	for _, provider := range m.providers {
		return provider
	}

	return nil
}

// GetPlanningModel returns the configured planning model
func (m *Manager) GetPlanningModel() string {
	return m.config.Models.Planning
}

// GetExecutionModel returns the configured execution model
func (m *Manager) GetExecutionModel() string {
	return m.config.Models.Execution
}

// GetReviewModel returns the configured review model
func (m *Manager) GetReviewModel() string {
	return m.config.Models.Review
}

// GetPricing returns pricing information for a model
func (m *Manager) GetPricing(modelID string) (*ModelPricing, error) {
	info, err := m.GetModelInfo(modelID)
	if err != nil {
		return nil, err
	}
	return &info.Pricing, nil
}

func (m *Manager) ensureConfiguredModels() error {
	if len(m.catalog) == 0 {
		return fmt.Errorf("no models discovered from configured providers")
	}

	var warnings []string

	if warning, err := m.ensureModel("planning"); err != nil {
		return fmt.Errorf("planning model: %w", err)
	} else if warning != "" {
		warnings = append(warnings, warning)
	}

	if warning, err := m.ensureModel("execution"); err != nil {
		return fmt.Errorf("execution model: %w", err)
	} else if warning != "" {
		warnings = append(warnings, warning)
	}

	if warning, err := m.ensureModel("review"); err != nil {
		return fmt.Errorf("review model: %w", err)
	} else if warning != "" {
		warnings = append(warnings, warning)
	}

	for _, warning := range warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
	}
	return nil
}

func (m *Manager) ensureModel(role string) (string, error) {
	var field *string
	switch role {
	case "planning":
		field = &m.config.Models.Planning
	case "execution":
		field = &m.config.Models.Execution
	case "review":
		field = &m.config.Models.Review
	default:
		return "", nil
	}

	if *field != "" && m.modelAvailable(*field) {
		return "", nil
	}

	fallback, ok := m.selectFallbackModel()
	if !ok {
		if *field == "" {
			return "", fmt.Errorf("no available models to configure %s role", role)
		}
		return "", fmt.Errorf("%s model %q not found and no fallback models available", role, *field)
	}

	previous := *field
	*field = fallback

	if previous == "" {
		return fmt.Sprintf("%s model not configured; defaulting to %s", role, fallback), nil
	}
	return fmt.Sprintf("%s model %q not found; defaulting to %s", role, previous, fallback), nil
}

func (m *Manager) selectFallbackModel() (string, bool) {
	var providersToTry []string
	if m.config.Models.DefaultProvider != "" {
		providersToTry = append(providersToTry, m.config.Models.DefaultProvider)
	}
	providersToTry = append(providersToTry, m.providerOrder...)

	seen := make(map[string]bool)
	for _, providerID := range providersToTry {
		if providerID == "" || seen[providerID] {
			continue
		}
		seen[providerID] = true

		if modelID, ok := m.firstModelForProvider(providerID); ok {
			return modelID, true
		}
	}

	for _, models := range m.providerModels {
		if len(models) > 0 {
			return models[0], true
		}
	}

	for modelID := range m.catalog {
		return modelID, true
	}

	return "", false
}

func (m *Manager) firstModelForProvider(providerID string) (string, bool) {
	models := m.providerModels[providerID]
	if len(models) == 0 {
		return "", false
	}
	return models[0], true
}

func (m *Manager) modelAvailable(modelID string) bool {
	_, ok := m.catalog[modelID]
	return ok
}

// GetContextLength returns the context length for a model
func (m *Manager) GetContextLength(modelID string) (int, error) {
	info, err := m.GetModelInfo(modelID)
	if err != nil {
		return 0, err
	}
	return info.ContextLength, nil
}

// CalculateCost calculates the cost of an API call
func (m *Manager) CalculateCost(modelID string, usage Usage) (float64, error) {
	return m.CalculateCostFromTokens(modelID, usage.PromptTokens, usage.CompletionTokens)
}

// CalculateCostFromTokens calculates cost from token counts
func (m *Manager) CalculateCostFromTokens(modelID string, promptTokens, completionTokens int) (float64, error) {
	pricing, err := m.GetPricing(modelID)
	if err != nil {
		return 0, err
	}

	promptCost := (float64(promptTokens) / 1_000_000) * pricing.Prompt
	completionCost := (float64(completionTokens) / 1_000_000) * pricing.Completion

	return promptCost + completionCost, nil
}

// SupportsVision checks if a model supports vision/image inputs
func (m *Manager) SupportsVision(modelID string) bool {
	info, err := m.GetModelInfo(modelID)
	if err != nil {
		return false
	}

	// Check if modality includes image support
	modality := info.Architecture.Modality
	return modality == "text+image" || modality == "multimodal" ||
		modality == "text+image->text" || modality == "image+text->text"
}

// SupportsReasoning checks if a model supports reasoning parameter
func (m *Manager) SupportsReasoning(modelID string) bool {
	info, err := m.GetModelInfo(modelID)
	if err != nil {
		return false
	}

	// Check supported_parameters from catalog
	for _, param := range info.SupportedParameters {
		if param == "reasoning" {
			return true
		}
	}
	return false
}

// SupportsTools checks if a model supports function/tool calling
func (m *Manager) SupportsTools(modelID string) bool {
	info, err := m.GetModelInfo(modelID)
	if err != nil {
		return false
	}

	// Check supported_parameters for "tools" or "functions"
	for _, param := range info.SupportedParameters {
		if param == "tools" || param == "functions" {
			return true
		}
	}
	return false
}

// GetVisionFallbackModel returns a fallback model for vision tasks
func (m *Manager) GetVisionFallbackModel() string {
	// Get configured vision fallback models
	fallbacks := m.config.Models.VisionFallback

	// If no fallbacks configured, use defaults
	if len(fallbacks) == 0 {
		fallbacks = []string{
			"openai/gpt-5-nano",
			"google/gemini-2.5-flash-lite-preview-09-2025",
		}
	}

	// Return first available model
	for _, modelID := range fallbacks {
		if m.modelAvailable(modelID) {
			return modelID
		}
	}

	// Return first preference even if not validated
	return fallbacks[0]
}

// DescribeImage uses a vision model to describe an image
func (m *Manager) DescribeImage(ctx context.Context, imageURL string) (string, error) {
	visionModel := m.GetVisionFallbackModel()

	// Create multimodal message
	req := ChatRequest{
		Model: visionModel,
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentPart{
					{
						Type: "text",
						Text: "Describe this image in great detail. Include all visible elements, text, UI components, colors, layouts, and any other visual information that would be important for understanding the image completely. Be precise and comprehensive.",
					},
					{
						Type: "image_url",
						ImageURL: &ImageURL{
							URL:    imageURL,
							Detail: "high",
						},
					},
				},
			},
		},
		Temperature: 0.3, // Lower temperature for more consistent descriptions
		MaxTokens:   2000,
	}

	resp, err := m.ChatCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("vision fallback failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from vision model")
	}

	// Extract text content from response
	return ExtractTextContent(resp.Choices[0].Message.Content)
}

// ExtractTextContent extracts text from a message content field
func ExtractTextContent(content any) (string, error) {
	switch v := content.(type) {
	case string:
		return v, nil
	case []any:
		// Try to extract text from content parts
		var textParts []string
		for _, part := range v {
			if partMap, ok := part.(map[string]any); ok {
				if text, ok := partMap["text"].(string); ok && text != "" {
					textParts = append(textParts, text)
				}
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n"), nil
		}
	}

	return "", fmt.Errorf("unexpected content format")
}

// ExtractTextContentOrEmpty extracts text from content, returning empty string on error
func ExtractTextContentOrEmpty(content any) string {
	text, _ := ExtractTextContent(content)
	return text
}

// thinkTagPattern matches <think>...</think> blocks in model output.
// Kimi K2 and other reasoning models embed thinking in these tags.
var thinkTagPattern = regexp.MustCompile(`(?s)<think>(.*?)</think>`)

// ExtractThinkingContent separates thinking/reasoning from the main content.
// Returns (thinking, content) where thinking is the content of <think> tags
// and content is the remaining text with thinking tags removed.
func ExtractThinkingContent(text string) (thinking string, content string) {
	matches := thinkTagPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return "", text
	}

	// Collect all thinking blocks
	var thinkingParts []string
	for _, match := range matches {
		if len(match) > 1 {
			thinkingParts = append(thinkingParts, strings.TrimSpace(match[1]))
		}
	}

	// Remove thinking tags from content
	content = thinkTagPattern.ReplaceAllString(text, "")
	content = strings.TrimSpace(content)

	thinking = strings.Join(thinkingParts, "\n\n")
	return thinking, content
}
