package image

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImage_Base64(t *testing.T) {
	img := &Image{
		Data:     []byte("test data"),
		MimeType: "image/png",
	}

	b64 := img.Base64()
	if b64 == "" {
		t.Error("Base64() returned empty string")
	}

	// Verify it's valid base64
	if b64 != "dGVzdCBkYXRh" {
		t.Errorf("Base64() = %v, want dGVzdCBkYXRh", b64)
	}
}

func TestImage_DataURI(t *testing.T) {
	img := &Image{
		Data:     []byte("test"),
		MimeType: "image/png",
	}

	uri := img.DataURI()
	if uri == "" {
		t.Error("DataURI() returned empty string")
	}

	expected := "data:image/png;base64,dGVzdA=="
	if uri != expected {
		t.Errorf("DataURI() = %v, want %v", uri, expected)
	}
}

func TestImage_ToAPIFormat(t *testing.T) {
	img := &Image{
		Data:     []byte("test"),
		MimeType: "image/png",
	}

	format := img.ToAPIFormat()

	if format["type"] != "image" {
		t.Errorf("type = %v, want image", format["type"])
	}

	source, ok := format["source"].(map[string]any)
	if !ok {
		t.Fatal("source not found or wrong type")
	}

	if source["type"] != "base64" {
		t.Errorf("source.type = %v, want base64", source["type"])
	}

	if source["media_type"] != "image/png" {
		t.Errorf("source.media_type = %v, want image/png", source["media_type"])
	}
}

func TestFromFile(t *testing.T) {
	// Create a temp PNG file with valid magic bytes
	tmpDir, err := os.MkdirTemp("", "image-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// PNG magic bytes + minimal header
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	pngPath := filepath.Join(tmpDir, "test.png")
	if err := os.WriteFile(pngPath, pngData, 0644); err != nil {
		t.Fatal(err)
	}

	img, err := FromFile(pngPath)
	if err != nil {
		t.Fatalf("FromFile() error = %v", err)
	}

	if img.MimeType != "image/png" {
		t.Errorf("MimeType = %v, want image/png", img.MimeType)
	}

	if img.Source != pngPath {
		t.Errorf("Source = %v, want %v", img.Source, pngPath)
	}
}

func TestFromFile_NotFound(t *testing.T) {
	_, err := FromFile("/nonexistent/path/image.png")
	if err == nil {
		t.Error("FromFile() should return error for nonexistent file")
	}
}

func TestFromFile_UnsupportedFormat(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "image-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a text file with wrong extension
	txtPath := filepath.Join(tmpDir, "test.xyz")
	if err := os.WriteFile(txtPath, []byte("not an image"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err = FromFile(txtPath)
	if err == nil {
		t.Error("FromFile() should return error for unsupported format")
	}
}

func TestDetectMimeType_ByExtension(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"image.png", "image/png"},
		{"image.jpg", "image/jpeg"},
		{"image.jpeg", "image/jpeg"},
		{"image.gif", "image/gif"},
		{"image.webp", "image/webp"},
		{"image.bmp", "image/bmp"},
		{"image.PNG", "image/png"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := detectMimeType(tt.path, nil)
			if got != tt.want {
				t.Errorf("detectMimeType(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestDetectMimeType_ByMagicBytes(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{
			name: "PNG",
			data: []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A},
			want: "image/png",
		},
		{
			name: "JPEG",
			data: []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46},
			want: "image/jpeg",
		},
		{
			name: "GIF",
			data: []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x00, 0x00},
			want: "image/gif",
		},
		{
			name: "BMP",
			data: []byte{0x42, 0x4D, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
			want: "image/bmp",
		},
		{
			name: "WebP",
			data: []byte{0x52, 0x49, 0x46, 0x46, 0x00, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50},
			want: "image/webp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectMimeType("", tt.data)
			if got != tt.want {
				t.Errorf("detectMimeType(data) = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSupportedFormat(t *testing.T) {
	supported := []string{
		"image.png",
		"image.jpg",
		"image.jpeg",
		"image.gif",
		"image.webp",
		"image.bmp",
		"IMAGE.PNG",
	}

	for _, path := range supported {
		if !IsSupportedFormat(path) {
			t.Errorf("IsSupportedFormat(%q) = false, want true", path)
		}
	}

	unsupported := []string{
		"image.txt",
		"image.pdf",
		"image.svg",
		"image.ico",
	}

	for _, path := range unsupported {
		if IsSupportedFormat(path) {
			t.Errorf("IsSupportedFormat(%q) = true, want false", path)
		}
	}
}

func TestIsImageMimeType(t *testing.T) {
	images := []string{
		"image/png",
		"image/jpeg",
		"image/gif",
		"image/webp",
	}

	for _, mime := range images {
		if !isImageMimeType(mime) {
			t.Errorf("isImageMimeType(%q) = false, want true", mime)
		}
	}

	notImages := []string{
		"text/plain",
		"application/json",
		"video/mp4",
	}

	for _, mime := range notImages {
		if isImageMimeType(mime) {
			t.Errorf("isImageMimeType(%q) = true, want false", mime)
		}
	}
}

func TestImageInput_New(t *testing.T) {
	ii := NewImageInput()

	if ii.MaxSize != 20*1024*1024 {
		t.Errorf("MaxSize = %v, want 20MB", ii.MaxSize)
	}

	if ii.MaxDimension != 4096 {
		t.Errorf("MaxDimension = %v, want 4096", ii.MaxDimension)
	}
}

func TestImageInput_Validate(t *testing.T) {
	ii := NewImageInput()

	// Valid image
	img := &Image{
		Data:     []byte("test data"),
		MimeType: "image/png",
	}
	if err := ii.Validate(img); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}

	// Nil image
	if err := ii.Validate(nil); err == nil {
		t.Error("Validate(nil) should return error")
	}

	// Empty data
	emptyImg := &Image{MimeType: "image/png"}
	if err := ii.Validate(emptyImg); err == nil {
		t.Error("Validate(empty) should return error")
	}

	// No mime type
	noMimeImg := &Image{Data: []byte("data")}
	if err := ii.Validate(noMimeImg); err == nil {
		t.Error("Validate(no mime) should return error")
	}

	// Too large
	ii.MaxSize = 10
	largeImg := &Image{
		Data:     make([]byte, 100),
		MimeType: "image/png",
	}
	if err := ii.Validate(largeImg); err == nil {
		t.Error("Validate(too large) should return error")
	}
}

func TestImageInput_Load_File(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "image-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a test PNG
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	pngPath := filepath.Join(tmpDir, "test.png")
	if err := os.WriteFile(pngPath, pngData, 0644); err != nil {
		t.Fatal(err)
	}

	ii := NewImageInput()
	img, err := ii.Load(pngPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if img.Source != pngPath {
		t.Errorf("Source = %v, want %v", img.Source, pngPath)
	}
}

func TestSupportedFormats(t *testing.T) {
	expected := []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp"}

	if len(SupportedFormats) != len(expected) {
		t.Errorf("SupportedFormats has %d items, want %d", len(SupportedFormats), len(expected))
	}

	for _, format := range expected {
		found := false
		for _, sf := range SupportedFormats {
			if sf == format {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("SupportedFormats missing %s", format)
		}
	}
}
