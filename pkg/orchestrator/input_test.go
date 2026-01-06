package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNewInputProcessor(t *testing.T) {
	// With nil transcriber
	proc := NewInputProcessor(nil)
	if proc == nil {
		t.Fatal("Expected non-nil processor")
	}
	if proc.transcriber == nil {
		t.Error("Expected NullTranscriber to be set")
	}
	if proc.maxFrames != 5 {
		t.Errorf("Expected maxFrames 5, got %d", proc.maxFrames)
	}

	// With custom transcriber
	mockT := &mockTranscriber{provider: "test"}
	proc = NewInputProcessor(mockT)
	if proc.transcriber.Provider() != "test" {
		t.Error("Expected custom transcriber")
	}
}

func TestInputProcessor_SetWorkDir(t *testing.T) {
	proc := NewInputProcessor(nil)
	proc.SetWorkDir("/tmp/test")
	if proc.workDir != "/tmp/test" {
		t.Errorf("Expected workDir '/tmp/test', got %s", proc.workDir)
	}
}

func TestInputProcessor_SetMaxFrames(t *testing.T) {
	proc := NewInputProcessor(nil)

	proc.SetMaxFrames(10)
	if proc.maxFrames != 10 {
		t.Errorf("Expected maxFrames 10, got %d", proc.maxFrames)
	}

	// Negative should not change
	proc.SetMaxFrames(-5)
	if proc.maxFrames != 10 {
		t.Errorf("Expected maxFrames to remain 10, got %d", proc.maxFrames)
	}

	proc.SetMaxFrames(0)
	if proc.maxFrames != 10 {
		t.Errorf("Expected maxFrames to remain 10, got %d", proc.maxFrames)
	}
}

func TestInputProcessor_Process_TextOnly(t *testing.T) {
	proc := NewInputProcessor(nil)
	ctx := context.Background()

	raw := RawInput{
		Text: "Hello, world!",
	}

	result, err := proc.Process(ctx, raw)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Text != "Hello, world!" {
		t.Errorf("Expected text 'Hello, world!', got %q", result.Text)
	}
	if len(result.Images) != 0 {
		t.Errorf("Expected 0 images, got %d", len(result.Images))
	}
	if result.Metadata.TranscribedAudio {
		t.Error("Expected TranscribedAudio to be false")
	}
}

func TestInputProcessor_Process_WithImages(t *testing.T) {
	// Create temp image file
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "test.png")
	// Create a minimal valid PNG (1x1 transparent pixel)
	pngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41, // IDAT chunk
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, // IEND chunk
		0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(imgPath, pngData, 0644); err != nil {
		t.Fatalf("Failed to create test image: %v", err)
	}

	proc := NewInputProcessor(nil)
	ctx := context.Background()

	raw := RawInput{
		Text:       "Here's an image",
		ImagePaths: []string{imgPath},
	}

	result, err := proc.Process(ctx, raw)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(result.Images) != 1 {
		t.Fatalf("Expected 1 image, got %d", len(result.Images))
	}

	img := result.Images[0]
	if img.Path != imgPath {
		t.Errorf("Expected path %s, got %s", imgPath, img.Path)
	}
	if img.MimeType != "image/png" {
		t.Errorf("Expected mime type 'image/png', got %s", img.MimeType)
	}
	if !hasDataURLPrefix(img.DataURL, "data:image/png;base64,") {
		t.Error("Expected PNG data URL")
	}
}

func TestInputProcessor_Process_WithAudio(t *testing.T) {
	// Create temp audio file
	tmpDir := t.TempDir()
	audioPath := filepath.Join(tmpDir, "test.mp3")
	if err := os.WriteFile(audioPath, []byte("fake audio"), 0644); err != nil {
		t.Fatalf("Failed to create test audio: %v", err)
	}

	// Mock transcriber that returns transcription
	mockT := &mockTranscriber{
		provider:  "mock",
		supported: true,
		result:    "This is transcribed text",
		err:       nil,
	}

	proc := NewInputProcessor(mockT)
	ctx := context.Background()

	raw := RawInput{
		Text:       "Original text",
		AudioPaths: []string{audioPath},
	}

	result, err := proc.Process(ctx, raw)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !result.Metadata.TranscribedAudio {
		t.Error("Expected TranscribedAudio to be true")
	}

	// Text should contain both original and transcription
	if !containsStr(result.Text, "Original text") {
		t.Error("Expected original text to be preserved")
	}
	if !containsStr(result.Text, "This is transcribed text") {
		t.Error("Expected transcription to be added")
	}
}

func TestInputProcessor_Process_MissingFiles(t *testing.T) {
	proc := NewInputProcessor(nil)
	ctx := context.Background()

	raw := RawInput{
		Text:       "Test",
		ImagePaths: []string{"/nonexistent/image.png"},
		AudioPaths: []string{"/nonexistent/audio.mp3"},
	}

	result, err := proc.Process(ctx, raw)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Should have processing errors but not fail
	if len(result.Metadata.ProcessingErrors) != 2 {
		t.Errorf("Expected 2 processing errors, got %d", len(result.Metadata.ProcessingErrors))
	}
}

func TestParseInputText(t *testing.T) {
	// Create temp files
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "test.png")
	audioPath := filepath.Join(tmpDir, "audio.mp3")
	os.WriteFile(imgPath, []byte("fake"), 0644)
	os.WriteFile(audioPath, []byte("fake"), 0644)

	input := "Hello world\n" + imgPath + "\nMore text\n" + audioPath + "\nFinal text"
	raw := ParseInputText(input)

	if len(raw.ImagePaths) != 1 {
		t.Errorf("Expected 1 image path, got %d", len(raw.ImagePaths))
	}
	if len(raw.AudioPaths) != 1 {
		t.Errorf("Expected 1 audio path, got %d", len(raw.AudioPaths))
	}
	if len(raw.Attachments) != 0 {
		t.Errorf("Expected 0 attachments, got %d", len(raw.Attachments))
	}
	if !containsStr(raw.Text, "Hello world") {
		t.Error("Expected 'Hello world' in text")
	}
	if !containsStr(raw.Text, "More text") {
		t.Error("Expected 'More text' in text")
	}
	if !containsStr(raw.Text, "Final text") {
		t.Error("Expected 'Final text' in text")
	}
	// File paths should be stripped
	if containsStr(raw.Text, imgPath) {
		t.Error("Image path should be stripped from text")
	}
}

func TestParseInputText_VideoAndAttachment(t *testing.T) {
	tmpDir := t.TempDir()

	videoPath := filepath.Join(tmpDir, "video.mp4")
	attachPath := filepath.Join(tmpDir, "doc.pdf")

	if err := os.WriteFile(videoPath, []byte("fake video"), 0644); err != nil {
		t.Fatalf("Failed to create test video: %v", err)
	}
	if err := os.WriteFile(attachPath, []byte("fake pdf"), 0644); err != nil {
		t.Fatalf("Failed to create test attachment: %v", err)
	}

	input := "Hello\n" + videoPath + "\n" + attachPath + "\nBye"
	raw := ParseInputText(input)

	if len(raw.VideoPaths) != 1 {
		t.Errorf("Expected 1 video path, got %d", len(raw.VideoPaths))
	}
	if len(raw.AudioPaths) != 0 {
		t.Errorf("Expected 0 audio paths, got %d", len(raw.AudioPaths))
	}
	if len(raw.Attachments) != 1 {
		t.Errorf("Expected 1 attachment, got %d", len(raw.Attachments))
	}

	if containsStr(raw.Text, videoPath) {
		t.Error("Video path should be stripped from text")
	}
	if containsStr(raw.Text, attachPath) {
		t.Error("Attachment path should be stripped from text")
	}
	if !containsStr(raw.Text, "Hello") || !containsStr(raw.Text, "Bye") {
		t.Errorf("Expected text to preserve non-file lines, got %q", raw.Text)
	}
}

func TestMultimodalInput_HasContent(t *testing.T) {
	tests := []struct {
		name     string
		input    MultimodalInput
		expected bool
	}{
		{
			name:     "empty",
			input:    MultimodalInput{},
			expected: false,
		},
		{
			name:     "text only",
			input:    MultimodalInput{Text: "hello"},
			expected: true,
		},
		{
			name:     "images only",
			input:    MultimodalInput{Images: []ImageData{{Path: "test.png"}}},
			expected: true,
		},
		{
			name:     "attachments only",
			input:    MultimodalInput{Attachments: []Attachment{{Path: "file.txt"}}},
			expected: true,
		},
		{
			name:     "audio only",
			input:    MultimodalInput{AudioPaths: []string{"audio.mp3"}},
			expected: true,
		},
		{
			name:     "video only",
			input:    MultimodalInput{VideoPaths: []string{"video.mp4"}},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.input.HasContent()
			if result != tt.expected {
				t.Errorf("HasContent() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestMultimodalInput_Summary(t *testing.T) {
	tests := []struct {
		name     string
		input    MultimodalInput
		contains []string
	}{
		{
			name:     "empty",
			input:    MultimodalInput{},
			contains: []string{"empty input"},
		},
		{
			name:     "text only",
			input:    MultimodalInput{Text: "hello world test"},
			contains: []string{"3 words"},
		},
		{
			name: "images",
			input: MultimodalInput{
				Images: []ImageData{{}, {}},
			},
			contains: []string{"2 images"},
		},
		{
			name: "audio transcription",
			input: MultimodalInput{
				Metadata: InputMeta{TranscribedAudio: true},
			},
			contains: []string{"audio transcription"},
		},
		{
			name: "audio files",
			input: MultimodalInput{
				AudioPaths: []string{"audio.mp3"},
			},
			contains: []string{"1 audio files"},
		},
		{
			name: "videos",
			input: MultimodalInput{
				VideoPaths: []string{"video.mp4", "video2.mp4"},
			},
			contains: []string{"2 videos"},
		},
		{
			name: "mixed",
			input: MultimodalInput{
				Text:     "hello world",
				Images:   []ImageData{{}},
				Metadata: InputMeta{TranscribedAudio: true},
			},
			contains: []string{"2 words", "1 images", "audio transcription"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := tt.input.Summary()
			for _, s := range tt.contains {
				if !containsStr(summary, s) {
					t.Errorf("Summary() = %q, expected to contain %q", summary, s)
				}
			}
		})
	}
}

func TestIsImageFile(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"test.png", true},
		{"test.PNG", true},
		{"test.jpg", true},
		{"test.jpeg", true},
		{"test.gif", true},
		{"test.webp", true},
		{"test.bmp", true},
		{"test.mp3", false},
		{"test.txt", false},
		{"test.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := isImageFile(tt.path)
			if result != tt.expected {
				t.Errorf("isImageFile(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestIsVideoFile(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"video.mp4", true},
		{"video.MP4", true},
		{"video.avi", true},
		{"video.mov", true},
		{"video.mkv", true},
		{"video.webm", true},
		{"video.m4v", true},
		{"audio.mp3", false},
		{"image.png", false},
		{"doc.pdf", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := isVideoFile(tt.path)
			if result != tt.expected {
				t.Errorf("isVideoFile(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestGetMimeType(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"test.txt", "text/plain"},
		{"test.json", "application/json"},
		{"test.yaml", "application/x-yaml"},
		{"test.yml", "application/x-yaml"},
		{"test.md", "text/markdown"},
		{"test.go", "text/x-go"},
		{"test.py", "text/x-python"},
		{"test.unknown", "application/octet-stream"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := getMimeType(tt.path)
			if result != tt.expected {
				t.Errorf("getMimeType(%q) = %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}

func TestInputProcessor_Process_Video_TranscribesWhenSupported(t *testing.T) {
	tmpDir := t.TempDir()
	videoPath := filepath.Join(tmpDir, "video.mp4")
	if err := os.WriteFile(videoPath, []byte("fake video"), 0644); err != nil {
		t.Fatalf("Failed to create test video: %v", err)
	}

	mockT := &mockTranscriber{
		provider:  "mock",
		supported: true,
		result:    "video transcript",
		err:       nil,
	}

	proc := NewInputProcessor(mockT)
	ctx := context.Background()

	raw := RawInput{
		Text:       "Original text",
		VideoPaths: []string{videoPath},
	}

	result, err := proc.Process(ctx, raw)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(result.VideoPaths) != 1 {
		t.Fatalf("Expected 1 video path, got %d", len(result.VideoPaths))
	}
	if !result.Metadata.TranscribedAudio {
		t.Error("Expected TranscribedAudio to be true")
	}
	if !containsStr(result.Text, "video transcript") {
		t.Errorf("Expected video transcription to be added, got %q", result.Text)
	}
}

func TestInputProcessor_Process_Video_UnsupportedStillHasContent(t *testing.T) {
	tmpDir := t.TempDir()
	videoPath := filepath.Join(tmpDir, "video.mp4")
	if err := os.WriteFile(videoPath, []byte("fake video"), 0644); err != nil {
		t.Fatalf("Failed to create test video: %v", err)
	}

	proc := NewInputProcessor(nil)
	ctx := context.Background()

	raw := RawInput{
		VideoPaths: []string{videoPath},
	}

	result, err := proc.Process(ctx, raw)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.HasContent() {
		t.Error("Expected HasContent to be true when video paths exist")
	}
	if !containsStr(result.Summary(), "1 videos") {
		t.Errorf("Expected summary to include video count, got %q", result.Summary())
	}
}

// Helper functions

func hasDataURLPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
