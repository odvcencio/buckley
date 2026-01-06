// Package image provides image input handling for AI assistants.
// It supports reading images from files, clipboard, and screenshots.
package image

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// SupportedFormats lists all supported image formats
var SupportedFormats = []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp"}

// Image represents an image with its data and metadata
type Image struct {
	Data     []byte
	MimeType string
	Source   string // file path, "clipboard", "screenshot", or URL
	Width    int
	Height   int
}

// Base64 returns the base64-encoded image data
func (i *Image) Base64() string {
	return base64.StdEncoding.EncodeToString(i.Data)
}

// DataURI returns the image as a data URI
func (i *Image) DataURI() string {
	return fmt.Sprintf("data:%s;base64,%s", i.MimeType, i.Base64())
}

// ToAPIFormat returns the image in API-compatible format
func (i *Image) ToAPIFormat() map[string]any {
	return map[string]any{
		"type": "image",
		"source": map[string]any{
			"type":       "base64",
			"media_type": i.MimeType,
			"data":       i.Base64(),
		},
	}
}

// FromFile reads an image from a file path
func FromFile(path string) (*Image, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	mimeType := detectMimeType(absPath, data)
	if mimeType == "" {
		return nil, fmt.Errorf("unsupported image format: %s", filepath.Ext(absPath))
	}

	return &Image{
		Data:     data,
		MimeType: mimeType,
		Source:   absPath,
	}, nil
}

// FromURL fetches an image from a URL
func FromURL(url string) (*Image, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP error: %s", resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = detectMimeType("", data)
	}

	if !isImageMimeType(mimeType) {
		return nil, fmt.Errorf("not an image: %s", mimeType)
	}

	return &Image{
		Data:     data,
		MimeType: mimeType,
		Source:   url,
	}, nil
}

// FromClipboard reads an image from the system clipboard
func FromClipboard() (*Image, error) {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		// macOS: use pbpaste with -Prefer png
		cmd = exec.Command("osascript", "-e", `
			set theImage to the clipboard as «class PNGf»
			return theImage
		`)
	case "linux":
		// Linux: try xclip first, then xsel
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--output")
		} else {
			return nil, fmt.Errorf("no clipboard tool available (install xclip or xsel)")
		}
	case "windows":
		// Windows: use PowerShell
		cmd = exec.Command("powershell", "-command",
			"[System.Windows.Forms.Clipboard]::GetImage().Save([System.Console]::OpenStandardOutput(), [System.Drawing.Imaging.ImageFormat]::Png)")
	default:
		return nil, fmt.Errorf("clipboard not supported on %s", runtime.GOOS)
	}

	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to read clipboard: %w", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("clipboard is empty or does not contain an image")
	}

	mimeType := detectMimeType("", data)
	if mimeType == "" {
		mimeType = "image/png" // Default for clipboard
	}

	return &Image{
		Data:     data,
		MimeType: mimeType,
		Source:   "clipboard",
	}, nil
}

// TakeScreenshot captures the screen or a region
func TakeScreenshot(region string) (*Image, error) {
	var cmd *exec.Cmd
	var outputPath string

	// Create temp file for screenshot
	tmpFile, err := os.CreateTemp("", "screenshot-*.png")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	outputPath = tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(outputPath)

	switch runtime.GOOS {
	case "darwin":
		args := []string{"-c", "-t", "png", outputPath}
		if region != "" {
			// Interactive selection
			args = []string{"-i", "-c", "-t", "png", outputPath}
		}
		cmd = exec.Command("screencapture", args...)

	case "linux":
		// Try various screenshot tools
		if _, err := exec.LookPath("gnome-screenshot"); err == nil {
			args := []string{"-f", outputPath}
			if region != "" {
				args = append([]string{"-a"}, args...)
			}
			cmd = exec.Command("gnome-screenshot", args...)
		} else if _, err := exec.LookPath("scrot"); err == nil {
			args := []string{outputPath}
			if region != "" {
				args = append([]string{"-s"}, args...)
			}
			cmd = exec.Command("scrot", args...)
		} else if _, err := exec.LookPath("import"); err == nil {
			// ImageMagick import
			args := []string{"-window", "root", outputPath}
			if region != "" {
				args = []string{outputPath} // Interactive
			}
			cmd = exec.Command("import", args...)
		} else {
			return nil, fmt.Errorf("no screenshot tool available (install gnome-screenshot, scrot, or imagemagick)")
		}

	case "windows":
		// Use PowerShell to take screenshot
		script := fmt.Sprintf(`
			Add-Type -AssemblyName System.Windows.Forms
			$screen = [System.Windows.Forms.Screen]::PrimaryScreen
			$bitmap = New-Object System.Drawing.Bitmap($screen.Bounds.Width, $screen.Bounds.Height)
			$graphics = [System.Drawing.Graphics]::FromImage($bitmap)
			$graphics.CopyFromScreen($screen.Bounds.Location, [System.Drawing.Point]::Empty, $screen.Bounds.Size)
			$bitmap.Save('%s', [System.Drawing.Imaging.ImageFormat]::Png)
		`, outputPath)
		cmd = exec.Command("powershell", "-command", script)

	default:
		return nil, fmt.Errorf("screenshots not supported on %s", runtime.GOOS)
	}

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("screenshot failed: %w", err)
	}

	// Read the captured image
	return FromFile(outputPath)
}

// detectMimeType determines the MIME type from extension or magic bytes
func detectMimeType(path string, data []byte) string {
	// Try extension first
	if path != "" {
		ext := strings.ToLower(filepath.Ext(path))
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
		case ".svg":
			return "image/svg+xml"
		}
	}

	// Try magic bytes
	if len(data) >= 8 {
		// PNG
		if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
			return "image/png"
		}
		// JPEG
		if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
			return "image/jpeg"
		}
		// GIF
		if data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 {
			return "image/gif"
		}
		// WebP
		if data[0] == 0x52 && data[1] == 0x49 && data[2] == 0x46 && data[3] == 0x46 &&
			data[8] == 0x57 && data[9] == 0x45 && data[10] == 0x42 && data[11] == 0x50 {
			return "image/webp"
		}
		// BMP
		if data[0] == 0x42 && data[1] == 0x4D {
			return "image/bmp"
		}
	}

	return ""
}

// isImageMimeType checks if a MIME type is an image type
func isImageMimeType(mimeType string) bool {
	return strings.HasPrefix(mimeType, "image/")
}

// IsSupportedFormat checks if a file extension is supported
func IsSupportedFormat(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, format := range SupportedFormats {
		if ext == format {
			return true
		}
	}
	return false
}

// ResizeImage resizes an image to fit within max dimensions
// Returns nil if no resizing needed or if resizing fails
func ResizeImage(img *Image, maxWidth, maxHeight int) (*Image, error) {
	// For now, return as-is since proper image resizing requires
	// image decoding which adds dependencies
	// In production, use image/png, image/jpeg packages
	return img, nil
}

// CompressImage compresses an image to reduce size
func CompressImage(img *Image, quality int) (*Image, error) {
	// For now, return as-is
	// In production, use image encoding with quality settings
	return img, nil
}

// ImageInput handles image input from various sources
type ImageInput struct {
	MaxSize      int64 // Max file size in bytes
	MaxDimension int   // Max width/height in pixels
}

// NewImageInput creates a new image input handler
func NewImageInput() *ImageInput {
	return &ImageInput{
		MaxSize:      20 * 1024 * 1024, // 20MB default
		MaxDimension: 4096,             // 4K default
	}
}

// Load loads an image from a source (file path, URL, "clipboard", or "screenshot")
func (ii *ImageInput) Load(source string) (*Image, error) {
	source = strings.TrimSpace(source)

	switch {
	case source == "clipboard":
		return FromClipboard()

	case source == "screenshot" || strings.HasPrefix(source, "screenshot:"):
		region := ""
		if strings.HasPrefix(source, "screenshot:") {
			region = strings.TrimPrefix(source, "screenshot:")
		}
		return TakeScreenshot(region)

	case strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://"):
		return FromURL(source)

	default:
		return FromFile(source)
	}
}

// Validate checks if an image is valid for API use
func (ii *ImageInput) Validate(img *Image) error {
	if img == nil {
		return fmt.Errorf("image is nil")
	}

	if len(img.Data) == 0 {
		return fmt.Errorf("image data is empty")
	}

	if ii.MaxSize > 0 && int64(len(img.Data)) > ii.MaxSize {
		return fmt.Errorf("image too large: %d bytes (max %d)", len(img.Data), ii.MaxSize)
	}

	if img.MimeType == "" {
		return fmt.Errorf("unknown image format")
	}

	return nil
}
