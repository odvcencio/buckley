package image

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_Generate_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}

		var req imageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != DefaultModel {
			t.Errorf("model = %q", req.Model)
		}

		resp := imageResponse{
			ID:    "gen-1",
			Model: DefaultModel,
			Choices: []imageChoice{{
				Index: 0,
				Message: responseMsg{
					Role: "assistant",
					Content: mustMarshal(t, []contentPart{
						{Type: "text", Text: "A cat in a hat"},
						{Type: "image_url", ImageURL: &imageURL{URL: "data:image/png;base64,AAAA"}},
					}),
				},
				FinishReason: "stop",
			}},
			Usage: imageUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	result, err := c.Generate(context.Background(), "a cat in a hat", "", nil, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.Text != "A cat in a hat" {
		t.Errorf("text = %q", result.Text)
	}
	if len(result.ImageData) == 0 {
		t.Error("expected image data")
	}
}

func TestClient_Generate_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "rate limited"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	_, err := c.Generate(context.Background(), "test", "", nil, "")
	if err == nil {
		t.Error("expected error for 429 response")
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
