package image

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegration_FullFlow(t *testing.T) {
	// Create a real PNG-like payload (PNG magic bytes)
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	b64 := base64.StdEncoding.EncodeToString(pngData)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req imageRequest
		json.NewDecoder(r.Body).Decode(&req)

		// Verify response_modalities was sent
		if len(req.ResponseModalities) != 2 ||
			req.ResponseModalities[0] != "TEXT" ||
			req.ResponseModalities[1] != "IMAGE" {
			t.Errorf("response_modalities = %v", req.ResponseModalities)
		}

		resp := imageResponse{
			ID:    "gen-int",
			Model: req.Model,
			Choices: []imageChoice{{
				Message: responseMsg{
					Role: "assistant",
					Content: mustMarshal(t, []contentPart{
						{Type: "text", Text: "Generated a cat in a hat"},
						{Type: "image_url", ImageURL: &imageURL{
							URL: "data:image/png;base64," + b64,
						}},
					}),
				},
				FinishReason: "stop",
			}},
			Usage: imageUsage{PromptTokens: 50, CompletionTokens: 100, TotalTokens: 150},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "cat.png")

	runner := NewRunner(RunnerConfig{
		BaseURL: srv.URL,
		APIKey:  "test-key",
	})

	result, err := runner.Run(context.Background(), RunOptions{
		Prompt:     "a cat in a hat",
		OutputPath: outPath,
		Size:       "1024x1024",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Text != "Generated a cat in a hat" {
		t.Errorf("text = %q", result.Text)
	}

	// Verify file was written with PNG magic bytes
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) != len(pngData) {
		t.Errorf("file size = %d, want %d", len(data), len(pngData))
	}
	if data[0] != 0x89 || data[1] != 0x50 {
		t.Error("file does not start with PNG magic bytes")
	}
}

func TestIntegration_EditFlow(t *testing.T) {
	var requestBody imageRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&requestBody)

		pngData := []byte{0x89, 0x50, 0x4E, 0x47}
		b64 := base64.StdEncoding.EncodeToString(pngData)
		resp := imageResponse{
			ID:    "gen-edit",
			Model: DefaultModel,
			Choices: []imageChoice{{
				Message: responseMsg{
					Role: "assistant",
					Content: mustMarshal(t, []contentPart{
						{Type: "text", Text: "Background removed"},
						{Type: "image_url", ImageURL: &imageURL{URL: "data:image/png;base64," + b64}},
					}),
				},
				FinishReason: "stop",
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()

	// Write a fake input image
	inputPath := filepath.Join(tmpDir, "input.jpg")
	os.WriteFile(inputPath, []byte("fake-jpeg-data"), 0644)

	outPath := filepath.Join(tmpDir, "output.png")
	runner := NewRunner(RunnerConfig{BaseURL: srv.URL, APIKey: "test-key"})

	_, err := runner.Run(context.Background(), RunOptions{
		Prompt:     "remove the background",
		OutputPath: outPath,
		InputPath:  inputPath,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify the request included the input image
	raw, _ := json.Marshal(requestBody.Messages[0].Content)
	var parts []contentPart
	json.Unmarshal(raw, &parts)

	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	if !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Error("input image not sent as data URI")
	}

	// Verify output was written
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("output file not created: %v", err)
	}
}
