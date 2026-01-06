package embeddings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Service provides text embedding capabilities
type ProviderKind string

const (
	ProviderOpenRouter ProviderKind = "openrouter"
	ProviderOpenAI     ProviderKind = "openai"
)

type ServiceOptions struct {
	APIKey   string
	Model    string
	Provider ProviderKind
	BaseURL  string
	CacheDir string
}

type Service struct {
	apiKey     string
	apiURL     string
	model      string
	provider   ProviderKind
	httpClient *http.Client
	cache      *Cache
}

// NewService creates a new embedding service backed by the configured provider.
func NewService(opts ServiceOptions) *Service {
	service := &Service{
		apiKey:   opts.APIKey,
		model:    opts.Model,
		provider: opts.Provider,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		cache: NewCache(opts.CacheDir),
	}

	switch opts.Provider {
	case ProviderOpenAI:
		service.apiURL = opts.BaseURL
		if service.apiURL == "" {
			service.apiURL = "https://api.openai.com/v1/embeddings"
		}
		if service.model == "" {
			service.model = "text-embedding-3-small"
		}
	case ProviderOpenRouter:
		service.apiURL = opts.BaseURL
		if service.apiURL == "" {
			service.apiURL = "https://openrouter.ai/api/v1/embeddings"
		}
		if service.model == "" {
			service.model = "openai/text-embedding-3-small"
		}
	default:
		service.provider = ProviderOpenRouter
		service.apiURL = "https://openrouter.ai/api/v1/embeddings"
		if service.model == "" {
			service.model = "openai/text-embedding-3-small"
		}
	}

	return service
}

// SetModel changes the embedding model
func (s *Service) SetModel(model string) {
	s.model = model
}

// Embed generates an embedding vector for the given text
func (s *Service) Embed(ctx context.Context, text string) ([]float64, error) {
	// Check cache first
	cacheKey := s.computeCacheKey(text)
	if cached, ok := s.cache.Get(cacheKey); ok {
		return cached, nil
	}

	// Call OpenAI API
	embedding, err := s.callEmbeddingAPI(ctx, text)
	if err != nil {
		return nil, err
	}

	// Cache result
	s.cache.Set(cacheKey, embedding)

	return embedding, nil
}

// EmbedBatch generates embeddings for multiple texts
func (s *Service) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	embeddings := make([][]float64, len(texts))

	for i, text := range texts {
		embedding, err := s.Embed(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("failed to embed text %d: %w", i, err)
		}
		embeddings[i] = embedding
	}

	return embeddings, nil
}

// callEmbeddingAPI calls the OpenRouter embeddings API
func (s *Service) callEmbeddingAPI(ctx context.Context, text string) ([]float64, error) {
	reqBody := map[string]any{
		"model": s.model,
		"input": text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	return result.Data[0].Embedding, nil
}

// computeCacheKey computes a cache key for the text
func (s *Service) computeCacheKey(text string) string {
	hash := sha256.Sum256([]byte(s.model + "|" + text))
	return fmt.Sprintf("%x", hash)[:32]
}

// ClearCache clears the embedding cache
func (s *Service) ClearCache() error {
	return s.cache.Clear()
}

// CosineSimilarity computes the cosine similarity between two vectors
func CosineSimilarity(a, b []float64) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("vectors must have same length")
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0, nil
	}

	return dotProduct / (sqrt(normA) * sqrt(normB)), nil
}

// sqrt computes square root using Newton's method
func sqrt(x float64) float64 {
	if x == 0 {
		return 0
	}

	// Use a simple approximation for efficiency
	z := x
	for i := 0; i < 10; i++ {
		z = (z + x/z) / 2
	}
	return z
}
