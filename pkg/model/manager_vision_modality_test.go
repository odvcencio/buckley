package model

import "testing"

func TestModalityAcceptsImageInput(t *testing.T) {
	tests := []struct {
		name     string
		modality string
		want     bool
	}{
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"text only", "text", false},
		{"audio only", "audio", false},
		{"compact image+text", "image+text", true},
		{"compact text+image", "text+image", true},
		{"compact composite", "text+image+audio", true},
		{"multimodal", "multimodal", true},
		{"multimodal case-insensitive", "MultiModal", true},
		{"arrow image first", "image+text->text", true},
		{"arrow image last", "text+image->text", true},
		{"arrow composite input", "text+image+audio->text", true},
		{"arrow case-insensitive", "Text+Image->Text", true},
		{"arrow whitespace around token", "text + image -> text", true},
		{"output-only text->image", "text->image", false},
		{"output-only image after arrow", "text+audio->image", false},
		{"unknown token", "video", false},
		{"image substring not exact", "imagegen", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modalityAcceptsImageInput(tt.modality); got != tt.want {
				t.Fatalf("modalityAcceptsImageInput(%q) = %v, want %v", tt.modality, got, tt.want)
			}
		})
	}
}
