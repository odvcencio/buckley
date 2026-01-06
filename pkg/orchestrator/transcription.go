package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Transcriber defines the interface for audio-to-text conversion
type Transcriber interface {
	// Transcribe converts an audio file to text
	Transcribe(ctx context.Context, audioPath string) (string, error)

	// IsSupported returns true if the audio format is supported
	IsSupported(audioPath string) bool

	// Provider returns the transcriber provider name
	Provider() string
}

// TranscriptionConfig holds transcription settings
type TranscriptionConfig struct {
	Provider     string `yaml:"provider"`      // api, system, hybrid
	WhisperModel string `yaml:"whisper_model"` // whisper-1
	APIEndpoint  string `yaml:"api_endpoint"`  // defaults to OpenAI
	Timeout      int    `yaml:"timeout"`       // seconds
}

// DefaultTranscriptionConfig returns sensible defaults
func DefaultTranscriptionConfig() *TranscriptionConfig {
	return &TranscriptionConfig{
		Provider:     "api",
		WhisperModel: "whisper-1",
		APIEndpoint:  "https://api.openai.com/v1/audio/transcriptions",
		Timeout:      60,
	}
}

// Supported audio extensions
var audioExtensions = []string{".mp3", ".mp4", ".mpeg", ".mpga", ".m4a", ".wav", ".webm", ".ogg", ".flac"}

// IsAudioFile checks if a file is a supported audio format
func IsAudioFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, audioExt := range audioExtensions {
		if ext == audioExt {
			return true
		}
	}
	return false
}

// WhisperTranscriber implements Transcriber using OpenAI's Whisper API
type WhisperTranscriber struct {
	config     *TranscriptionConfig
	apiKey     string
	httpClient *http.Client
}

// NewWhisperTranscriber creates a new Whisper API transcriber
func NewWhisperTranscriber(config *TranscriptionConfig, apiKey string) *WhisperTranscriber {
	if config == nil {
		config = DefaultTranscriptionConfig()
	}

	timeout := time.Duration(config.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	return &WhisperTranscriber{
		config: config,
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// Provider returns "whisper-api"
func (w *WhisperTranscriber) Provider() string {
	return "whisper-api"
}

// IsSupported checks if the audio format is supported
func (w *WhisperTranscriber) IsSupported(audioPath string) bool {
	return IsAudioFile(audioPath)
}

// Transcribe converts audio to text using Whisper API
func (w *WhisperTranscriber) Transcribe(ctx context.Context, audioPath string) (string, error) {
	if !w.IsSupported(audioPath) {
		return "", fmt.Errorf("unsupported audio format: %s", filepath.Ext(audioPath))
	}

	if w.apiKey == "" {
		return "", fmt.Errorf("OpenAI API key not configured for transcription")
	}

	// Open the audio file
	file, err := os.Open(audioPath)
	if err != nil {
		return "", fmt.Errorf("failed to open audio file: %w", err)
	}
	defer file.Close()

	// Create multipart form
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add file field
	part, err := writer.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("failed to copy file data: %w", err)
	}

	// Add model field
	if err := writer.WriteField("model", w.config.WhisperModel); err != nil {
		return "", fmt.Errorf("failed to write model field: %w", err)
	}

	// Add response format
	if err := writer.WriteField("response_format", "text"); err != nil {
		return "", fmt.Errorf("failed to write response_format field: %w", err)
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", w.config.APIEndpoint, &buf)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+w.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send request
	resp, err := w.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcription request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
			return "", fmt.Errorf("transcription failed: %s", errResp.Error.Message)
		}
		return "", fmt.Errorf("transcription failed with status %d: %s", resp.StatusCode, string(body))
	}

	return strings.TrimSpace(string(body)), nil
}

// NullTranscriber is a no-op transcriber for when transcription is disabled
type NullTranscriber struct{}

// Provider returns "null"
func (n *NullTranscriber) Provider() string {
	return "null"
}

// IsSupported always returns false
func (n *NullTranscriber) IsSupported(audioPath string) bool {
	return false
}

// Transcribe returns an error indicating transcription is disabled
func (n *NullTranscriber) Transcribe(ctx context.Context, audioPath string) (string, error) {
	return "", fmt.Errorf("transcription is not configured")
}

// HybridTranscriber tries system STT first, then falls back to API
type HybridTranscriber struct {
	primary  Transcriber
	fallback Transcriber
}

// NewHybridTranscriber creates a transcriber that tries multiple providers
func NewHybridTranscriber(primary, fallback Transcriber) *HybridTranscriber {
	return &HybridTranscriber{
		primary:  primary,
		fallback: fallback,
	}
}

// Provider returns "hybrid"
func (h *HybridTranscriber) Provider() string {
	return "hybrid"
}

// IsSupported returns true if either transcriber supports the format
func (h *HybridTranscriber) IsSupported(audioPath string) bool {
	if h.primary != nil && h.primary.IsSupported(audioPath) {
		return true
	}
	if h.fallback != nil && h.fallback.IsSupported(audioPath) {
		return true
	}
	return false
}

// Transcribe tries primary first, then fallback
func (h *HybridTranscriber) Transcribe(ctx context.Context, audioPath string) (string, error) {
	var firstErr error

	if h.primary != nil && h.primary.IsSupported(audioPath) {
		result, err := h.primary.Transcribe(ctx, audioPath)
		if err == nil {
			return result, nil
		}
		firstErr = err
	}

	if h.fallback != nil && h.fallback.IsSupported(audioPath) {
		result, err := h.fallback.Transcribe(ctx, audioPath)
		if err == nil {
			return result, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}

	if firstErr != nil {
		return "", fmt.Errorf("all transcription providers failed: %w", firstErr)
	}

	return "", fmt.Errorf("no transcription provider supports this audio format")
}
