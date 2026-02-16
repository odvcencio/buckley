package image

import (
	"encoding/json"
	"testing"
)

func TestParseImageResponse_TextAndImage(t *testing.T) {
	raw := `{
		"id": "gen-123",
		"model": "google/gemini-3-pro-image-preview",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Here is your cat"},
					{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgo="}}
				]
			},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}
	}`
	var resp imageResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	text, imgData, err := resp.extractContent()
	if err != nil {
		t.Fatalf("extractContent: %v", err)
	}
	if text != "Here is your cat" {
		t.Errorf("text = %q, want %q", text, "Here is your cat")
	}
	if len(imgData) == 0 {
		t.Error("expected non-empty image data")
	}
}

func TestParseImageResponse_NoImage(t *testing.T) {
	raw := `{
		"id": "gen-456",
		"model": "google/gemini-3-pro-image-preview",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "I cannot generate that image"
			},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`
	var resp imageResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	_, _, err := resp.extractContent()
	if err == nil {
		t.Error("expected error for response with no image")
	}
}

func TestBuildRequest_TextOnly(t *testing.T) {
	req := buildRequest("a cat in a hat", "", nil, "google/gemini-3-pro-image-preview")
	if req.Model != "google/gemini-3-pro-image-preview" {
		t.Errorf("model = %q", req.Model)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(req.Messages))
	}
	if len(req.ResponseModalities) != 2 {
		t.Errorf("response_modalities len = %d, want 2", len(req.ResponseModalities))
	}
}

func TestBuildRequest_WithSize(t *testing.T) {
	req := buildRequest("a landscape", "1920x1080", nil, "google/gemini-3-pro-image-preview")
	msg := req.Messages[0]
	parts, ok := msg.Content.([]contentPart)
	if !ok {
		t.Fatal("expected []contentPart")
	}
	if len(parts) != 1 {
		t.Fatalf("parts len = %d, want 1", len(parts))
	}
	if parts[0].Text == "" {
		t.Error("expected non-empty text")
	}
}

func TestBuildRequest_WithInputImage(t *testing.T) {
	fakeImage := []byte("fake-png-data")
	req := buildRequest("remove background", "", fakeImage, "google/gemini-3-pro-image-preview")
	msg := req.Messages[0]
	parts, ok := msg.Content.([]contentPart)
	if !ok {
		t.Fatal("expected []contentPart")
	}
	if len(parts) != 2 {
		t.Fatalf("parts len = %d, want 2 (text + image)", len(parts))
	}
	if parts[1].ImageURL == nil {
		t.Error("expected image_url part")
	}
}
