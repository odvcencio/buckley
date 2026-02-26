package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestIsAudioFile(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"audio.mp3", true},
		{"audio.MP3", true},
		{"voice.wav", true},
		{"podcast.m4a", true},
		{"recording.ogg", true},
		{"song.flac", true},
		{"video.mp4", true}, // mp4 can be audio
		{"image.png", false},
		{"document.pdf", false},
		{"code.go", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := IsAudioFile(tt.path)
			if result != tt.expected {
				t.Errorf("IsAudioFile(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestDefaultTranscriptionConfig(t *testing.T) {
	cfg := DefaultTranscriptionConfig()

	if cfg.Provider != "api" {
		t.Errorf("Expected provider 'api', got %s", cfg.Provider)
	}
	if cfg.WhisperModel != "whisper-1" {
		t.Errorf("Expected model 'whisper-1', got %s", cfg.WhisperModel)
	}
	if cfg.Timeout != 60 {
		t.Errorf("Expected timeout 60, got %d", cfg.Timeout)
	}
}

func TestNewWhisperTranscriber(t *testing.T) {
	cfg := &TranscriptionConfig{
		Provider:     "api",
		WhisperModel: "whisper-1",
		APIEndpoint:  "https://api.openai.com/v1/audio/transcriptions",
		Timeout:      30,
	}

	transcriber := NewWhisperTranscriber(cfg, "test-key")

	if transcriber.Provider() != "whisper-api" {
		t.Errorf("Expected provider 'whisper-api', got %s", transcriber.Provider())
	}

	if !transcriber.IsSupported("test.mp3") {
		t.Error("Expected mp3 to be supported")
	}

	if transcriber.IsSupported("test.txt") {
		t.Error("Expected txt to not be supported")
	}
}

func TestWhisperTranscriber_TranscribeNoAPIKey(t *testing.T) {
	cfg := DefaultTranscriptionConfig()
	transcriber := NewWhisperTranscriber(cfg, "")

	ctx := context.Background()
	_, err := transcriber.Transcribe(ctx, "test.mp3")

	if err == nil {
		t.Error("Expected error for missing API key")
	}
	if err.Error() != "openai api key not configured for transcription" {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestWhisperTranscriber_TranscribeUnsupportedFormat(t *testing.T) {
	cfg := DefaultTranscriptionConfig()
	transcriber := NewWhisperTranscriber(cfg, "test-key")

	ctx := context.Background()
	_, err := transcriber.Transcribe(ctx, "test.txt")

	if err == nil {
		t.Error("Expected error for unsupported format")
	}
}

func TestWhisperTranscriber_TranscribeMockServer(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		if !hasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("Expected Authorization header with Bearer token")
		}
		if !hasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Error("Expected multipart/form-data content type")
		}

		// Return mock transcription
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("This is the transcribed text"))
	}))
	defer server.Close()

	// Create temp audio file
	tmpDir := t.TempDir()
	audioPath := filepath.Join(tmpDir, "test.mp3")
	if err := os.WriteFile(audioPath, []byte("fake audio data"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Test transcription
	cfg := &TranscriptionConfig{
		Provider:     "api",
		WhisperModel: "whisper-1",
		APIEndpoint:  server.URL,
		Timeout:      30,
	}
	transcriber := NewWhisperTranscriber(cfg, "test-api-key")

	ctx := context.Background()
	result, err := transcriber.Transcribe(ctx, audioPath)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "This is the transcribed text" {
		t.Errorf("Expected 'This is the transcribed text', got %q", result)
	}
}

func TestNullTranscriber(t *testing.T) {
	transcriber := &NullTranscriber{}

	if transcriber.Provider() != "null" {
		t.Errorf("Expected provider 'null', got %s", transcriber.Provider())
	}

	if transcriber.IsSupported("any.mp3") {
		t.Error("NullTranscriber should not support any format")
	}

	ctx := context.Background()
	_, err := transcriber.Transcribe(ctx, "test.mp3")
	if err == nil {
		t.Error("Expected error from NullTranscriber")
	}
}

func TestHybridTranscriber(t *testing.T) {
	// Create mock transcribers
	primary := &mockTranscriber{
		provider:  "primary",
		supported: true,
		result:    "primary result",
		err:       nil,
	}
	fallback := &mockTranscriber{
		provider:  "fallback",
		supported: true,
		result:    "fallback result",
		err:       nil,
	}

	hybrid := NewHybridTranscriber(primary, fallback)

	if hybrid.Provider() != "hybrid" {
		t.Errorf("Expected provider 'hybrid', got %s", hybrid.Provider())
	}

	// Test primary success
	ctx := context.Background()
	result, err := hybrid.Transcribe(ctx, "test.mp3")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "primary result" {
		t.Errorf("Expected 'primary result', got %q", result)
	}

	// Test fallback when primary fails
	primary.err = context.DeadlineExceeded
	result, err = hybrid.Transcribe(ctx, "test.mp3")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "fallback result" {
		t.Errorf("Expected 'fallback result', got %q", result)
	}
}

// mockTranscriber for testing
type mockTranscriber struct {
	provider  string
	supported bool
	result    string
	err       error
}

func (m *mockTranscriber) Provider() string {
	return m.provider
}

func (m *mockTranscriber) IsSupported(path string) bool {
	return m.supported
}

func (m *mockTranscriber) Transcribe(ctx context.Context, path string) (string, error) {
	return m.result, m.err
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
