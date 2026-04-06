package image

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRunner_Run_WritesFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := imageResponse{
			ID:    "gen-1",
			Model: DefaultModel,
			Choices: []imageChoice{{
				Message: responseMsg{
					Role: "assistant",
					Content: mustMarshal(t, []contentPart{
						{Type: "text", Text: "Here is your image"},
						{Type: "image_url", ImageURL: &imageURL{URL: "data:image/png;base64,iVBORw0KGgo="}},
					}),
				},
				FinishReason: "stop",
			}},
			Usage: imageUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	outPath := filepath.Join(t.TempDir(), "out.png")
	runner := NewRunner(RunnerConfig{
		BaseURL: srv.URL,
		APIKey:  "test-key",
	})
	result, err := runner.Run(context.Background(), RunOptions{
		Prompt:     "a cat",
		OutputPath: outPath,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Text != "Here is your image" {
		t.Errorf("text = %q", result.Text)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	if len(data) == 0 {
		t.Error("output file is empty")
	}
}

func TestRunner_Run_WithInputImage(t *testing.T) {
	var receivedParts []contentPart
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req imageRequest
		json.NewDecoder(r.Body).Decode(&req)
		raw, _ := json.Marshal(req.Messages[0].Content)
		json.Unmarshal(raw, &receivedParts)

		resp := imageResponse{
			ID:    "gen-2",
			Model: DefaultModel,
			Choices: []imageChoice{{
				Message: responseMsg{
					Role: "assistant",
					Content: mustMarshal(t, []contentPart{
						{Type: "text", Text: "Edited"},
						{Type: "image_url", ImageURL: &imageURL{URL: "data:image/png;base64,iVBORw0KGgo="}},
					}),
				},
				FinishReason: "stop",
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	inputPath := filepath.Join(t.TempDir(), "input.png")
	os.WriteFile(inputPath, []byte("fake-png"), 0644)

	outPath := filepath.Join(t.TempDir(), "out.png")
	runner := NewRunner(RunnerConfig{
		BaseURL: srv.URL,
		APIKey:  "test-key",
	})
	_, err := runner.Run(context.Background(), RunOptions{
		Prompt:     "remove background",
		OutputPath: outPath,
		InputPath:  inputPath,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(receivedParts) != 2 {
		t.Fatalf("sent %d parts, want 2", len(receivedParts))
	}
	if receivedParts[1].Type != "image_url" {
		t.Errorf("part[1].Type = %q, want image_url", receivedParts[1].Type)
	}
}

func TestRunner_Run_MissingOutputPath(t *testing.T) {
	runner := NewRunner(RunnerConfig{APIKey: "test"})
	_, err := runner.Run(context.Background(), RunOptions{
		Prompt: "a cat",
	})
	if err == nil {
		t.Error("expected error for missing output path")
	}
}
