package orchestrator

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MultimodalInput represents processed input from various sources
type MultimodalInput struct {
	Text        string       // Combined text content
	Images      []ImageData  // Processed images
	AudioPaths  []string     // Original audio file paths (for reference)
	VideoPaths  []string     // Original video file paths (for reference)
	Attachments []Attachment // Other file attachments
	Metadata    InputMeta    // Processing metadata
}

// ImageData holds processed image information
type ImageData struct {
	DataURL  string // base64 data URL for the image
	Path     string // Original file path
	MimeType string // MIME type
	Width    int    // Image width (if known)
	Height   int    // Image height (if known)
}

// Attachment represents a non-media file attachment
type Attachment struct {
	Path     string // File path
	Name     string // Display name
	MimeType string // MIME type
	Size     int64  // File size in bytes
}

// InputMeta contains metadata about input processing
type InputMeta struct {
	TranscribedAudio  bool     // Audio was transcribed
	ExtractedFrames   int      // Number of video frames extracted
	ProcessingErrors  []string // Non-fatal processing errors
	TranscriptionText string   // Just the transcription portion
}

// RawInput represents unprocessed user input
type RawInput struct {
	Text        string   // Raw text input
	AudioPaths  []string // Paths to audio files
	VideoPaths  []string // Paths to video files
	ImagePaths  []string // Paths to image files
	Attachments []string // Paths to other files
}

// InputProcessor handles multimodal input processing
type InputProcessor struct {
	transcriber Transcriber
	maxFrames   int  // Maximum video frames to extract
	enableVideo bool // Whether video processing is enabled
	workDir     string
}

// NewInputProcessor creates a new input processor
func NewInputProcessor(transcriber Transcriber) *InputProcessor {
	if transcriber == nil {
		transcriber = &NullTranscriber{}
	}
	return &InputProcessor{
		transcriber: transcriber,
		maxFrames:   5,     // Default: extract up to 5 frames from videos
		enableVideo: false, // Video processing requires ffmpeg, disabled by default
	}
}

// SetWorkDir sets the working directory for temporary files
func (p *InputProcessor) SetWorkDir(dir string) {
	p.workDir = dir
}

// EnableVideoProcessing enables video frame extraction (requires ffmpeg)
func (p *InputProcessor) EnableVideoProcessing(enabled bool) {
	p.enableVideo = enabled
}

// SetMaxFrames sets the maximum number of video frames to extract
func (p *InputProcessor) SetMaxFrames(max int) {
	if max > 0 {
		p.maxFrames = max
	}
}

// Process converts raw input into a unified multimodal input
func (p *InputProcessor) Process(ctx context.Context, raw RawInput) (*MultimodalInput, error) {
	result := &MultimodalInput{
		Text:     raw.Text,
		Metadata: InputMeta{},
	}

	// Process audio files - transcribe to text
	for _, audioPath := range raw.AudioPaths {
		if !fileExists(audioPath) {
			result.Metadata.ProcessingErrors = append(result.Metadata.ProcessingErrors,
				fmt.Sprintf("audio file not found: %s", audioPath))
			continue
		}

		result.AudioPaths = append(result.AudioPaths, audioPath)

		if p.transcriber.IsSupported(audioPath) {
			transcribed, err := p.transcriber.Transcribe(ctx, audioPath)
			if err != nil {
				result.Metadata.ProcessingErrors = append(result.Metadata.ProcessingErrors,
					fmt.Sprintf("transcription failed for %s: %v", audioPath, err))
			} else {
				result.Metadata.TranscribedAudio = true
				result.Metadata.TranscriptionText += transcribed + "\n"
			}
		} else {
			result.Metadata.ProcessingErrors = append(result.Metadata.ProcessingErrors,
				fmt.Sprintf("unsupported audio format: %s", audioPath))
		}
	}

	// Append transcription to text
	if result.Metadata.TranscriptionText != "" {
		if result.Text != "" {
			result.Text += "\n\n[Transcribed audio]\n"
		}
		result.Text += strings.TrimSpace(result.Metadata.TranscriptionText)
	}

	// Process images
	for _, imagePath := range raw.ImagePaths {
		if !fileExists(imagePath) {
			result.Metadata.ProcessingErrors = append(result.Metadata.ProcessingErrors,
				fmt.Sprintf("image file not found: %s", imagePath))
			continue
		}

		imgData, err := processImage(imagePath)
		if err != nil {
			result.Metadata.ProcessingErrors = append(result.Metadata.ProcessingErrors,
				fmt.Sprintf("image processing failed for %s: %v", imagePath, err))
			continue
		}
		result.Images = append(result.Images, *imgData)
	}

	// Process videos (if enabled)
	for _, videoPath := range raw.VideoPaths {
		if !fileExists(videoPath) {
			result.Metadata.ProcessingErrors = append(result.Metadata.ProcessingErrors,
				fmt.Sprintf("video file not found: %s", videoPath))
			continue
		}

		result.VideoPaths = append(result.VideoPaths, videoPath)
		videoTranscribed := false

		if p.enableVideo {
			frames, audioPath, err := p.extractVideoContent(ctx, videoPath)
			if err != nil {
				result.Metadata.ProcessingErrors = append(result.Metadata.ProcessingErrors,
					fmt.Sprintf("video processing failed for %s: %v", videoPath, err))
			} else {
				result.Images = append(result.Images, frames...)
				result.Metadata.ExtractedFrames += len(frames)

				// Transcribe audio track if present
				if audioPath != "" && p.transcriber.IsSupported(audioPath) {
					transcribed, err := p.transcriber.Transcribe(ctx, audioPath)
					if err == nil {
						videoTranscribed = true
						result.Metadata.TranscribedAudio = true
						if result.Text != "" {
							result.Text += "\n\n[Video audio transcription]\n"
						}
						result.Text += transcribed
					}
				}
			}

		}

		if !videoTranscribed && p.transcriber.IsSupported(videoPath) {
			transcribed, err := p.transcriber.Transcribe(ctx, videoPath)
			if err != nil {
				result.Metadata.ProcessingErrors = append(result.Metadata.ProcessingErrors,
					fmt.Sprintf("transcription failed for %s: %v", videoPath, err))
				continue
			}

			result.Metadata.TranscribedAudio = true
			if result.Text != "" {
				result.Text += "\n\n[Video audio transcription]\n"
			}
			result.Text += transcribed
		}
	}

	// Process attachments
	for _, attachPath := range raw.Attachments {
		if !fileExists(attachPath) {
			result.Metadata.ProcessingErrors = append(result.Metadata.ProcessingErrors,
				fmt.Sprintf("attachment not found: %s", attachPath))
			continue
		}

		info, err := os.Stat(attachPath)
		if err != nil {
			continue
		}

		result.Attachments = append(result.Attachments, Attachment{
			Path:     attachPath,
			Name:     filepath.Base(attachPath),
			MimeType: getMimeType(attachPath),
			Size:     info.Size(),
		})
	}

	return result, nil
}

// ParseInputText extracts file references from text input
func ParseInputText(text string) RawInput {
	raw := RawInput{}
	lines := strings.Split(text, "\n")
	var textLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check for various file types
		if isImageFile(trimmed) && fileExists(trimmed) {
			raw.ImagePaths = append(raw.ImagePaths, trimmed)
			continue
		}
		if isVideoFile(trimmed) && fileExists(trimmed) {
			raw.VideoPaths = append(raw.VideoPaths, trimmed)
			continue
		}
		if IsAudioFile(trimmed) && fileExists(trimmed) {
			raw.AudioPaths = append(raw.AudioPaths, trimmed)
			continue
		}
		if isRegularFile(trimmed) {
			raw.Attachments = append(raw.Attachments, trimmed)
			continue
		}

		textLines = append(textLines, line)
	}

	raw.Text = strings.TrimSpace(strings.Join(textLines, "\n"))
	return raw
}

// Helper functions

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

func isImageFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	imageExts := []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp"}
	for _, e := range imageExts {
		if ext == e {
			return true
		}
	}
	return false
}

func isVideoFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	videoExts := []string{".mp4", ".avi", ".mov", ".mkv", ".webm", ".m4v"}
	for _, e := range videoExts {
		if ext == e {
			return true
		}
	}
	return false
}

func getMimeType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	mimeTypes := map[string]string{
		".txt":  "text/plain",
		".json": "application/json",
		".yaml": "application/x-yaml",
		".yml":  "application/x-yaml",
		".md":   "text/markdown",
		".go":   "text/x-go",
		".py":   "text/x-python",
		".js":   "text/javascript",
		".ts":   "text/typescript",
		".html": "text/html",
		".css":  "text/css",
		".xml":  "application/xml",
		".pdf":  "application/pdf",
	}
	if mime, ok := mimeTypes[ext]; ok {
		return mime
	}
	return "application/octet-stream"
}

func processImage(imagePath string) (*ImageData, error) {
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read image: %w", err)
	}

	ext := strings.ToLower(filepath.Ext(imagePath))
	mimeType := getImageMimeType(ext)

	encoded := base64.StdEncoding.EncodeToString(data)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, encoded)

	return &ImageData{
		DataURL:  dataURL,
		Path:     imagePath,
		MimeType: mimeType,
	}, nil
}

func getImageMimeType(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return "application/octet-stream"
	}
}

// extractVideoContent extracts frames and audio from a video file.
// Not yet implemented - requires ffmpeg integration.
func (p *InputProcessor) extractVideoContent(ctx context.Context, videoPath string) ([]ImageData, string, error) {
	return nil, "", fmt.Errorf("video frame extraction not yet implemented (requires ffmpeg)")
}

// HasContent returns true if the input has any meaningful content
func (m *MultimodalInput) HasContent() bool {
	return m.Text != "" || len(m.Images) > 0 || len(m.AudioPaths) > 0 || len(m.VideoPaths) > 0 || len(m.Attachments) > 0
}

// Summary returns a brief summary of the input content
func (m *MultimodalInput) Summary() string {
	parts := []string{}

	if m.Text != "" {
		words := len(strings.Fields(m.Text))
		parts = append(parts, fmt.Sprintf("%d words", words))
	}

	if len(m.Images) > 0 {
		parts = append(parts, fmt.Sprintf("%d images", len(m.Images)))
	}

	if len(m.AudioPaths) > 0 {
		parts = append(parts, fmt.Sprintf("%d audio files", len(m.AudioPaths)))
	}

	if len(m.VideoPaths) > 0 {
		parts = append(parts, fmt.Sprintf("%d videos", len(m.VideoPaths)))
	}

	if m.Metadata.TranscribedAudio {
		parts = append(parts, "audio transcription")
	}

	if len(m.Attachments) > 0 {
		parts = append(parts, fmt.Sprintf("%d attachments", len(m.Attachments)))
	}

	if len(parts) == 0 {
		return "empty input"
	}

	return strings.Join(parts, ", ")
}
