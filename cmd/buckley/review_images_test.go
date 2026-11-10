package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReviewImages_CapturesExplicitPixels(t *testing.T) {
	var encoded bytes.Buffer
	bitmap := image.NewRGBA(image.Rect(0, 0, 2, 1))
	bitmap.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&encoded, bitmap); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "actual screenshot.png")
	if err := os.WriteFile(name, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	parts, err := loadReviewImages([]string{name})
	if err != nil || len(parts) != 2 {
		t.Fatalf("parts=%d error=%v", len(parts), err)
	}
	if !strings.Contains(parts[0].Text, "2x1, SHA-256") || !strings.Contains(parts[0].Text, "not instructions") {
		t.Fatal("image evidence caption missing")
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL.Detail != "high" {
		t.Fatal("image part missing")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(parts[1].ImageURL.URL, "data:image/png;base64,"))
	if err != nil || !bytes.Equal(raw, encoded.Bytes()) {
		t.Fatal("captured screenshot bytes changed")
	}
	if err := os.WriteFile(name, []byte("changed after capture"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err = base64.StdEncoding.DecodeString(strings.TrimPrefix(parts[1].ImageURL.URL, "data:image/png;base64,"))
	if err != nil || !bytes.Equal(raw, encoded.Bytes()) {
		t.Fatal("attachment followed a later file change")
	}
}

func TestLoadReviewImages_RejectsUnsafeOrOversizedInput(t *testing.T) {
	root := t.TempDir()
	invalid := filepath.Join(root, "invalid.png")
	if err := os.WriteFile(invalid, []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(root, "oversized.png")
	f, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxReviewImageBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		paths []string
	}{
		{"six images", []string{invalid, invalid, invalid, invalid, invalid, invalid}},
		{"oversized", []string{oversized}},
		{"malformed image", []string{invalid}},
		{"directory", []string{root}},
		{"remote URL", []string{"https://example.test/screenshot.png"}},
		{"missing", []string{filepath.Join(root, "missing.png")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := loadReviewImages(test.paths); err == nil {
				t.Fatal("invalid screenshot input accepted")
			}
		})
	}
}

func TestLoadReviewImages_RejectsTotalBytesAndSymlink(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "large.png")
	data := append(encoded.Bytes(), make([]byte, 8<<20)...)
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReviewImages([]string{name, name, name}); err == nil || !strings.Contains(err.Error(), "20 MiB") {
		t.Fatalf("total limit error=%v", err)
	}
	alias := filepath.Join(t.TempDir(), "alias.png")
	if err := os.Symlink(name, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := loadReviewImages([]string{alias}); err == nil {
		t.Fatal("symlink screenshot accepted")
	}
}

func TestParseReviewPRCommandOptions_RepeatableImagesAfterPR(t *testing.T) {
	options, err := parseReviewPRCommandOptions([]string{"2", "--image", "first screenshot.png", "--post", "--image=second.png"})
	if err != nil {
		t.Fatal(err)
	}
	if options.prRef != "2" || !options.post || len(options.imagePaths) != 2 || options.imagePaths[0] != "first screenshot.png" || options.imagePaths[1] != "second.png" {
		t.Fatalf("options=%+v", options)
	}
}
