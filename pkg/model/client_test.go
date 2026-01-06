package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestNewClient tests client initialization
func TestNewClient(t *testing.T) {
	tests := []struct {
		name        string
		apiKey      string
		baseURL     string
		expectedURL string
	}{
		{
			name:        "with_custom_base_url",
			apiKey:      "test-key",
			baseURL:     "https://custom.api.com",
			expectedURL: "https://custom.api.com",
		},
		{
			name:        "with_empty_base_url_uses_default",
			apiKey:      "test-key",
			baseURL:     "",
			expectedURL: defaultBaseURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.apiKey, tt.baseURL)

			if client == nil {
				t.Fatal("NewClient returned nil")
			}
			if client.apiKey != tt.apiKey {
				t.Errorf("apiKey = %q, want %q", client.apiKey, tt.apiKey)
			}
			if client.baseURL != tt.expectedURL {
				t.Errorf("baseURL = %q, want %q", client.baseURL, tt.expectedURL)
			}
			if client.httpClient == nil {
				t.Error("httpClient is nil")
			}
		})
	}
}

// TestClient_FetchCatalog tests catalog fetching
func TestClient_FetchCatalog(t *testing.T) {
	tests := []struct {
		name           string
		response       string
		statusCode     int
		expectError    bool
		validateResult func(*testing.T, *ModelCatalog)
	}{
		{
			name:       "successful_fetch",
			statusCode: http.StatusOK,
			response: `{
				"data": [
					{
						"id": "test/model-1",
						"name": "Test Model 1",
						"context_length": 4096,
						"pricing": {
							"prompt": "0.001",
							"completion": "0.002"
						}
					}
				]
			}`,
			expectError: false,
			validateResult: func(t *testing.T, catalog *ModelCatalog) {
				if len(catalog.Data) != 1 {
					t.Errorf("expected 1 model, got %d", len(catalog.Data))
				}
				if catalog.Data[0].ID != "test/model-1" {
					t.Errorf("model ID = %q, want %q", catalog.Data[0].ID, "test/model-1")
				}
			},
		},
		{
			name:        "server_error",
			statusCode:  http.StatusInternalServerError,
			response:    `{"error": "internal error"}`,
			expectError: true,
		},
		{
			name:        "invalid_json",
			statusCode:  http.StatusOK,
			response:    `{invalid json}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/models" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClient("test-key", server.URL)
			catalog, err := client.FetchCatalog()

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.validateResult != nil {
				tt.validateResult(t, catalog)
			}
		})
	}
}

// TestClient_FetchCatalog_Caching tests catalog caching behavior
func TestClient_FetchCatalog_Caching(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data": []}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL)

	// First call
	_, err := client.FetchCatalog()
	if err != nil {
		t.Fatalf("first fetch failed: %v", err)
	}

	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}

	// Second call (should use cache)
	_, err = client.FetchCatalog()
	if err != nil {
		t.Fatalf("second fetch failed: %v", err)
	}

	if callCount != 1 {
		t.Errorf("expected cached result (1 call), got %d calls", callCount)
	}

	// Invalidate cache by setting old age
	client.catalogAge = time.Now().Add(-25 * time.Hour)

	// Third call (should fetch again)
	_, err = client.FetchCatalog()
	if err != nil {
		t.Fatalf("third fetch failed: %v", err)
	}

	if callCount != 2 {
		t.Errorf("expected 2 calls after cache invalidation, got %d", callCount)
	}
}

// TestClient_GetModelInfo tests model info retrieval
func TestClient_GetModelInfo(t *testing.T) {
	tests := []struct {
		name        string
		modelID     string
		catalogData string
		expectError bool
	}{
		{
			name:    "model_found",
			modelID: "test/model-1",
			catalogData: `{
				"data": [
					{
						"id": "test/model-1",
						"name": "Test Model 1",
						"context_length": 4096
					},
					{
						"id": "test/model-2",
						"name": "Test Model 2",
						"context_length": 8192
					}
				]
			}`,
			expectError: false,
		},
		{
			name:    "model_not_found",
			modelID: "nonexistent/model",
			catalogData: `{
				"data": [
					{
						"id": "test/model-1",
						"name": "Test Model 1",
						"context_length": 4096
					}
				]
			}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(tt.catalogData))
			}))
			defer server.Close()

			client := NewClient("test-key", server.URL)
			info, err := client.GetModelInfo(tt.modelID)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if info.ID != tt.modelID {
				t.Errorf("model ID = %q, want %q", info.ID, tt.modelID)
			}
		})
	}
}

// TestClient_ChatCompletion tests non-streaming chat completion
func TestClient_ChatCompletion(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		response    string
		expectError bool
	}{
		{
			name:       "successful_completion",
			statusCode: http.StatusOK,
			response: `{
				"id": "test-id",
				"model": "test/model",
				"choices": [{
					"message": {
						"role": "assistant",
						"content": "Hello, how can I help you?"
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 10,
					"completion_tokens": 20,
					"total_tokens": 30
				}
			}`,
			expectError: false,
		},
		{
			name:        "server_error",
			statusCode:  http.StatusInternalServerError,
			response:    `{"error": {"message": "internal error"}}`,
			expectError: true,
		},
		{
			name:        "rate_limit_error",
			statusCode:  http.StatusTooManyRequests,
			response:    `{"error": {"message": "rate limit exceeded"}}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if !strings.Contains(r.URL.Path, "/chat/completions") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				// Verify headers
				if r.Header.Get("Authorization") == "" {
					t.Error("missing Authorization header")
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
				}

				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClient("test-key", server.URL)
			req := ChatRequest{
				Model: "test/model",
				Messages: []Message{
					{Role: "user", Content: "Hello"},
				},
			}

			ctx := context.Background()
			resp, err := client.ChatCompletion(ctx, req)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if resp.ID != "test-id" {
				t.Errorf("response ID = %q, want %q", resp.ID, "test-id")
			}
			if len(resp.Choices) != 1 {
				t.Errorf("choices count = %d, want 1", len(resp.Choices))
			}
		})
	}
}

// TestClient_ChatCompletionStream tests streaming chat completion
func TestClient_ChatCompletionStream(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		chunks       []string
		expectError  bool
		expectChunks int
	}{
		{
			name:       "successful_stream",
			statusCode: http.StatusOK,
			chunks: []string{
				`data: {"id":"test-1","choices":[{"delta":{"role":"assistant","content":"Hello"}}]}`,
				`data: {"id":"test-1","choices":[{"delta":{"content":" there"}}]}`,
				`data: [DONE]`,
			},
			expectError:  false,
			expectChunks: 2,
		},
		{
			name:         "server_error",
			statusCode:   http.StatusInternalServerError,
			chunks:       []string{},
			expectError:  true,
			expectChunks: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use done channel to synchronize server and client
			done := make(chan struct{})

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.statusCode != http.StatusOK {
					w.WriteHeader(tt.statusCode)
					w.Write([]byte(`{"error": {"message": "error"}}`))
					return
				}

				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)

				for i, chunk := range tt.chunks {
					w.Write([]byte(chunk + "\n\n"))
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
					// Small delay between chunks to ensure client reads them
					// This is especially important for the last chunk before [DONE]
					if i < len(tt.chunks)-1 {
						time.Sleep(10 * time.Millisecond)
					}
				}

				// Wait for client to finish reading before closing connection
				select {
				case <-done:
				case <-time.After(1 * time.Second):
					// Timeout to prevent test hang if client fails
				}
			}))
			defer server.Close()

			client := NewClient("test-key", server.URL)
			req := ChatRequest{
				Model:    "test/model",
				Messages: []Message{{Role: "user", Content: "Hello"}},
				Stream:   true,
			}

			ctx := context.Background()
			chunkChan, errChan := client.ChatCompletionStream(ctx, req)

			var receivedChunks int
			var err error

		receiveLoop:
			for {
				select {
				case chunk, ok := <-chunkChan:
					if !ok {
						break receiveLoop
					}
					receivedChunks++
					if chunk.ID == "" {
						t.Error("chunk ID is empty")
					}
				case e := <-errChan:
					err = e
					// Drain chunk channel
					for range chunkChan {
					}
					break receiveLoop
				}
			}

			// Signal that client is done reading
			close(done)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if receivedChunks != tt.expectChunks {
				t.Errorf("received %d chunks, want %d", receivedChunks, tt.expectChunks)
			}
		})
	}
}

// TestClient_ChatCompletion_ContextCancellation tests context cancellation
func TestClient_ChatCompletion_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delay response to allow cancellation
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ChatResponse{})
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL)
	req := ChatRequest{
		Model:    "test/model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := client.ChatCompletion(ctx, req)
	if err == nil {
		t.Error("expected context cancellation error, got nil")
	}

	if !strings.Contains(err.Error(), "context") && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("expected context/deadline error, got: %v", err)
	}
}

// TestExtractTextContent tests text content extraction
func TestExtractTextContent(t *testing.T) {
	tests := []struct {
		name    string
		content any
		want    string
		wantErr bool
	}{
		{
			name:    "string_content",
			content: "Hello, world!",
			want:    "Hello, world!",
			wantErr: false,
		},
		{
			name: "multimodal_with_text",
			content: []any{
				map[string]any{"type": "text", "text": "Part 1"},
				map[string]any{"type": "text", "text": "Part 2"},
			},
			want:    "Part 1\nPart 2",
			wantErr: false,
		},
		{
			name:    "empty_string",
			content: "",
			want:    "",
			wantErr: false,
		},
		{
			name:    "nil_content",
			content: nil,
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractTextContent(tt.content)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClient_calculateRetryDelay tests retry delay calculation
func TestClient_calculateRetryDelay(t *testing.T) {
	client := NewClient("test-key", "")

	tests := []struct {
		name        string
		attempt     int
		lastErr     error
		expectedMin time.Duration
		expectedMax time.Duration
	}{
		{
			name:        "first_retry",
			attempt:     1,
			lastErr:     nil,
			expectedMin: 1 * time.Second,
			expectedMax: 1 * time.Second,
		},
		{
			name:        "second_retry",
			attempt:     2,
			lastErr:     nil,
			expectedMin: 2 * time.Second,
			expectedMax: 2 * time.Second,
		},
		{
			name:        "third_retry",
			attempt:     3,
			lastErr:     nil,
			expectedMin: 4 * time.Second,
			expectedMax: 4 * time.Second,
		},
		{
			name:        "large_attempt_capped",
			attempt:     10,
			lastErr:     nil,
			expectedMin: 30 * time.Second,
			expectedMax: 30 * time.Second,
		},
		{
			name:    "with_retry_after_header",
			attempt: 1,
			lastErr: &APIError{
				StatusCode: 429,
				RetryAfter: 5 * time.Second,
			},
			expectedMin: 5 * time.Second,
			expectedMax: 5 * time.Second,
		},
		{
			name:    "retry_after_exceeds_max",
			attempt: 1,
			lastErr: &APIError{
				StatusCode: 429,
				RetryAfter: 60 * time.Second,
			},
			expectedMin: 30 * time.Second,
			expectedMax: 30 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay := client.calculateRetryDelay(tt.attempt, tt.lastErr)
			if delay < tt.expectedMin || delay > tt.expectedMax {
				t.Errorf("calculateRetryDelay() = %v, want between %v and %v",
					delay, tt.expectedMin, tt.expectedMax)
			}
		})
	}
}

// TestParseRetryAfter tests parsing of Retry-After headers
func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected time.Duration
	}{
		{
			name:     "empty_header",
			header:   "",
			expected: 0,
		},
		{
			name:     "seconds_format",
			header:   "30",
			expected: 30 * time.Second,
		},
		{
			name:     "zero_seconds",
			header:   "0",
			expected: 0,
		},
		{
			name:     "invalid_format",
			header:   "invalid",
			expected: 0,
		},
		{
			name:     "large_seconds",
			header:   "120",
			expected: 120 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetryAfter(tt.header)
			if got != tt.expected {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.header, got, tt.expected)
			}
		})
	}
}

// TestClient_ValidateAPIKey tests API key validation
func TestClient_ValidateAPIKey(t *testing.T) {
	tests := []struct {
		name            string
		catalogResponse string
		chatResponse    string
		catalogStatus   int
		chatStatus      int
		expectError     bool
	}{
		{
			name:            "valid_key",
			catalogStatus:   http.StatusOK,
			catalogResponse: `{"data": [{"id": "openai/gpt-3.5-turbo"}]}`,
			chatStatus:      http.StatusOK,
			chatResponse: `{
				"id": "test",
				"model": "openai/gpt-3.5-turbo",
				"choices": [{"message": {"role": "assistant", "content": "Hi"}, "finish_reason": "stop"}],
				"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
			}`,
			expectError: false,
		},
		{
			name:            "invalid_key_catalog_fails",
			catalogStatus:   http.StatusUnauthorized,
			catalogResponse: `{"error": {"message": "invalid API key"}}`,
			expectError:     true,
		},
		{
			name:            "invalid_key_chat_fails",
			catalogStatus:   http.StatusOK,
			catalogResponse: `{"data": [{"id": "openai/gpt-3.5-turbo"}]}`,
			chatStatus:      http.StatusUnauthorized,
			chatResponse:    `{"error": {"message": "invalid API key"}}`,
			expectError:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalogCalled := false
			chatCalled := false

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/models") {
					catalogCalled = true
					w.WriteHeader(tt.catalogStatus)
					w.Write([]byte(tt.catalogResponse))
					return
				}

				if strings.Contains(r.URL.Path, "/chat/completions") {
					chatCalled = true
					w.WriteHeader(tt.chatStatus)
					w.Write([]byte(tt.chatResponse))
					return
				}

				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()

			client := NewClient("test-key", server.URL)
			err := client.ValidateAPIKey()

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !catalogCalled {
				t.Error("catalog endpoint not called")
			}

			// Chat may not be called if catalog fails
			if tt.catalogStatus == http.StatusOK && !chatCalled {
				t.Error("chat endpoint not called")
			}
		})
	}
}

// TestClient_Retry tests retry logic
func TestClient_ChatCompletion_Retry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			// Fail first attempt with retryable error
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error": {"message": "rate limit"}}`))
			return
		}
		// Succeed on second attempt
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "test",
			"model": "test/model",
			"choices": [{"message": {"role": "assistant", "content": "success"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL)
	req := ChatRequest{
		Model:    "test/model",
		Messages: []Message{{Role: "user", Content: "test"}},
	}

	ctx := context.Background()
	resp, err := client.ChatCompletion(ctx, req)

	if err != nil {
		t.Fatalf("expected success after retry, got error: %v", err)
	}

	if resp.ID != "test" {
		t.Errorf("response ID = %q, want test", resp.ID)
	}

	if attempts < 2 {
		t.Errorf("expected at least 2 attempts, got %d", attempts)
	}
}
