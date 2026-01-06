package model

import (
	"context"
	"errors"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
	"go.uber.org/mock/gomock"
)

func TestManagerGetModelInfo_FromCatalog(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	manager := &Manager{
		config: &config.Config{},
		catalog: map[string]ModelInfo{
			"test/model-1": {
				ID:            "test/model-1",
				Name:          "Test Model 1",
				ContextLength: 4096,
				Pricing:       ModelPricing{Prompt: 0.001, Completion: 0.002},
			},
		},
	}

	info, err := manager.GetModelInfo("test/model-1")
	if err != nil {
		t.Fatalf("GetModelInfo() error = %v", err)
	}

	if info.ID != "test/model-1" {
		t.Errorf("Model ID = %s, want test/model-1", info.ID)
	}

	if info.Name != "Test Model 1" {
		t.Errorf("Model Name = %s, want Test Model 1", info.Name)
	}

	if info.ContextLength != 4096 {
		t.Errorf("ContextLength = %d, want 4096", info.ContextLength)
	}
}

func TestManagerGetModelInfo_FromProvider(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	provider := NewMockProvider(ctrl)
	provider.EXPECT().ID().Return("testprovider").AnyTimes()
	provider.EXPECT().GetModelInfo("test/model-2").Return(&ModelInfo{
		ID:            "test/model-2",
		Name:          "Test Model 2",
		ContextLength: 8192,
	}, nil)

	manager := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{
				DefaultProvider: "testprovider",
			},
		},
		providers: map[string]Provider{
			"testprovider": provider,
		},
		catalog: map[string]ModelInfo{},
	}

	info, err := manager.GetModelInfo("test/model-2")
	if err != nil {
		t.Fatalf("GetModelInfo() error = %v", err)
	}

	if info.ID != "test/model-2" {
		t.Errorf("Model ID = %s, want test/model-2", info.ID)
	}

	if info.ContextLength != 8192 {
		t.Errorf("ContextLength = %d, want 8192", info.ContextLength)
	}
}

func TestManagerGetModelInfo_NoProvider(t *testing.T) {
	manager := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{
				DefaultProvider: "",
			},
		},
		providers: map[string]Provider{},
		catalog:   map[string]ModelInfo{},
	}

	_, err := manager.GetModelInfo("unknown/model")
	if err == nil {
		t.Error("Expected error for unknown model with no providers, got nil")
	}
}

func TestManagerGetCatalog(t *testing.T) {
	manager := &Manager{
		config: &config.Config{},
		catalog: map[string]ModelInfo{
			"provider-a/model-1": {ID: "provider-a/model-1", Name: "Model 1"},
			"provider-b/model-2": {ID: "provider-b/model-2", Name: "Model 2"},
			"provider-a/model-3": {ID: "provider-a/model-3", Name: "Model 3"},
		},
	}

	catalog := manager.GetCatalog()

	if len(catalog.Data) != 3 {
		t.Errorf("Catalog has %d models, want 3", len(catalog.Data))
	}

	// Verify models are sorted by ID
	if catalog.Data[0].ID != "provider-a/model-1" {
		t.Errorf("First model ID = %s, want provider-a/model-1", catalog.Data[0].ID)
	}

	if catalog.Data[1].ID != "provider-a/model-3" {
		t.Errorf("Second model ID = %s, want provider-a/model-3", catalog.Data[1].ID)
	}

	if catalog.Data[2].ID != "provider-b/model-2" {
		t.Errorf("Third model ID = %s, want provider-b/model-2", catalog.Data[2].ID)
	}
}

func TestManagerGetCatalog_Empty(t *testing.T) {
	manager := &Manager{
		config:  &config.Config{},
		catalog: map[string]ModelInfo{},
	}

	catalog := manager.GetCatalog()

	if len(catalog.Data) != 0 {
		t.Errorf("Empty catalog has %d models, want 0", len(catalog.Data))
	}
}

func TestManagerChatCompletion_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	req := ChatRequest{
		Model: "testprovider/model",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}

	expectedResponse := &ChatResponse{
		ID:    "chat-123",
		Model: "model",
		Choices: []Choice{
			{
				Index:   0,
				Message: Message{Role: "assistant", Content: "Hi there!"},
			},
		},
	}

	provider := NewMockProvider(ctrl)
	provider.EXPECT().ID().Return("testprovider").AnyTimes()
	provider.EXPECT().ChatCompletion(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, r ChatRequest) (*ChatResponse, error) {
			// Verify model normalization happened
			if r.Model != "model" {
				t.Errorf("Provider received model = %s, want model (normalized)", r.Model)
			}
			return expectedResponse, nil
		},
	)

	manager := &Manager{
		config: &config.Config{
			Providers: config.ProviderConfig{
				ModelRouting: map[string]string{
					"testprovider/": "testprovider",
				},
			},
		},
		providers: map[string]Provider{
			"testprovider": provider,
		},
	}

	resp, err := manager.ChatCompletion(ctx, req)
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	if resp.ID != "chat-123" {
		t.Errorf("Response ID = %s, want chat-123", resp.ID)
	}

	if len(resp.Choices) == 0 {
		t.Fatal("Response has no choices")
	}

	if content, ok := resp.Choices[0].Message.Content.(string); !ok || content != "Hi there!" {
		t.Errorf("Response content = %v, want 'Hi there!'", resp.Choices[0].Message.Content)
	}
}

func TestManagerChatCompletion_NoProvider(t *testing.T) {
	manager := &Manager{
		config: &config.Config{
			Providers: config.ProviderConfig{
				ModelRouting: map[string]string{},
			},
		},
		providers: map[string]Provider{},
	}

	ctx := context.Background()
	req := ChatRequest{
		Model: "unknown/model",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}

	_, err := manager.ChatCompletion(ctx, req)
	if err == nil {
		t.Error("Expected error for model with no provider, got nil")
	}
}

func TestManagerChatCompletion_ProviderError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	req := ChatRequest{
		Model: "testprovider/model",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}

	expectedError := errors.New("API rate limit exceeded")

	provider := NewMockProvider(ctrl)
	provider.EXPECT().ID().Return("testprovider").AnyTimes()
	provider.EXPECT().ChatCompletion(ctx, gomock.Any()).Return(nil, expectedError)

	manager := &Manager{
		config: &config.Config{
			Providers: config.ProviderConfig{
				ModelRouting: map[string]string{
					"testprovider/": "testprovider",
				},
			},
		},
		providers: map[string]Provider{
			"testprovider": provider,
		},
	}

	_, err := manager.ChatCompletion(ctx, req)
	if err == nil {
		t.Error("Expected provider error, got nil")
	}

	if err != expectedError {
		t.Errorf("Error = %v, want %v", err, expectedError)
	}
}

func TestManagerChatCompletionStream_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	req := ChatRequest{
		Model: "testprovider/model",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
		Stream: true,
	}

	chunkChan := make(chan StreamChunk, 2)
	errChan := make(chan error)

	chunkChan <- StreamChunk{
		Choices: []StreamChoice{
			{Index: 0, Delta: MessageDelta{Content: "Hello"}},
		},
	}
	chunkChan <- StreamChunk{
		Choices: []StreamChoice{
			{Index: 0, Delta: MessageDelta{Content: " world"}},
		},
	}
	close(chunkChan)
	close(errChan)

	provider := NewMockProvider(ctrl)
	provider.EXPECT().ID().Return("testprovider").AnyTimes()
	provider.EXPECT().ChatCompletionStream(ctx, gomock.Any()).Return(chunkChan, errChan)

	manager := &Manager{
		config: &config.Config{
			Providers: config.ProviderConfig{
				ModelRouting: map[string]string{
					"testprovider/": "testprovider",
				},
			},
		},
		providers: map[string]Provider{
			"testprovider": provider,
		},
	}

	resultChunks, resultErrs := manager.ChatCompletionStream(ctx, req)

	chunks := []StreamChunk{}
	for chunk := range resultChunks {
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 2 {
		t.Errorf("Received %d chunks, want 2", len(chunks))
	}

	if len(chunks[0].Choices) == 0 || chunks[0].Choices[0].Delta.Content != "Hello" {
		t.Errorf("First chunk delta content = %v, want Hello", chunks[0].Choices)
	}

	// Verify no errors
	for err := range resultErrs {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestManagerChatCompletionStream_NoProvider(t *testing.T) {
	manager := &Manager{
		config: &config.Config{
			Providers: config.ProviderConfig{
				ModelRouting: map[string]string{},
			},
		},
		providers: map[string]Provider{},
	}

	ctx := context.Background()
	req := ChatRequest{
		Model: "unknown/model",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
		Stream: true,
	}

	chunkChan, errChan := manager.ChatCompletionStream(ctx, req)

	if chunkChan != nil {
		t.Error("Expected nil chunk channel for missing provider")
	}

	// Should receive error
	err := <-errChan
	if err == nil {
		t.Error("Expected error for missing provider, got nil")
	}
}

func TestManagerInitialize_CatalogAggregation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	provider1 := NewMockProvider(ctrl)
	provider1.EXPECT().ID().Return("provider1").AnyTimes()
	provider1.EXPECT().FetchCatalog().Return(&ModelCatalog{
		Data: []ModelInfo{
			{ID: "provider1/model-a", Name: "Model A"},
			{ID: "provider1/model-b", Name: "Model B"},
		},
	}, nil)

	provider2 := NewMockProvider(ctrl)
	provider2.EXPECT().ID().Return("provider2").AnyTimes()
	provider2.EXPECT().FetchCatalog().Return(&ModelCatalog{
		Data: []ModelInfo{
			{ID: "provider2/model-c", Name: "Model C"},
		},
	}, nil)

	manager := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{
				Planning:  "provider1/model-a",
				Execution: "provider1/model-b",
				Review:    "provider2/model-c",
			},
		},
		providers: map[string]Provider{
			"provider1": provider1,
			"provider2": provider2,
		},
		catalog: make(map[string]ModelInfo),
	}

	err := manager.Initialize()
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if len(manager.catalog) != 3 {
		t.Errorf("Catalog has %d models, want 3", len(manager.catalog))
	}

	if _, ok := manager.catalog["provider1/model-a"]; !ok {
		t.Error("Catalog missing provider1/model-a")
	}

	if _, ok := manager.catalog["provider1/model-b"]; !ok {
		t.Error("Catalog missing provider1/model-b")
	}

	if _, ok := manager.catalog["provider2/model-c"]; !ok {
		t.Error("Catalog missing provider2/model-c")
	}
}

func TestManagerInitialize_ProviderError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	provider := NewMockProvider(ctrl)
	provider.EXPECT().ID().Return("failingprovider").AnyTimes()
	provider.EXPECT().FetchCatalog().Return(nil, errors.New("network error"))

	manager := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{
				Planning:  "test/model",
				Execution: "test/model",
				Review:    "test/model",
			},
		},
		providers: map[string]Provider{
			"failingprovider": provider,
		},
		catalog: make(map[string]ModelInfo),
	}

	err := manager.Initialize()
	if err == nil {
		t.Error("Expected error from failing provider, got nil")
	}
}

func TestManagerInitialize_InvalidConfiguredModel(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	provider := NewMockProvider(ctrl)
	provider.EXPECT().ID().Return("provider").AnyTimes()
	provider.EXPECT().FetchCatalog().Return(&ModelCatalog{
		Data: []ModelInfo{
			{ID: "provider/valid-model", Name: "Valid Model"},
		},
	}, nil)

	manager := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{
				Planning:  "provider/valid-model",
				Execution: "provider/invalid-model", // This model doesn't exist
				Review:    "provider/valid-model",
			},
		},
		providers: map[string]Provider{
			"provider": provider,
		},
		catalog: make(map[string]ModelInfo),
	}

	err := manager.Initialize()
	if err != nil {
		t.Fatalf("Initialize returned unexpected error: %v", err)
	}

	if manager.config.Models.Execution != "provider/valid-model" {
		t.Fatalf("expected execution model to fallback to provider/valid-model, got %s", manager.config.Models.Execution)
	}
}
