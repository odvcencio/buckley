package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestWriteExclusiveOutput_CreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "response.patch")
	if err := writeExclusiveOutput(path, []byte("patch text")); err != nil {
		t.Fatalf("writeExclusiveOutput() error = %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "patch text" {
		t.Fatalf("content = %q, want %q", content, "patch text")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

func TestWriteExclusiveOutput_ExistingFileIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "response.patch")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := writeExclusiveOutput(path, []byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "output already exists") {
		t.Fatalf("writeExclusiveOutput() error = %v, want existing-output error", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "existing" {
		t.Fatalf("content = %q, want existing content", content)
	}
}

func TestWriteExclusiveOutput_ExistingSymlinkIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "response.patch")
	if err := os.WriteFile(target, []byte("target content"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("Symlink() is unavailable: %v", err)
	}

	err := writeExclusiveOutput(path, []byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "output already exists") {
		t.Fatalf("writeExclusiveOutput() error = %v, want existing-output error", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "target content" {
		t.Fatalf("target content = %q, want original content", content)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat() error = %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("output path is no longer a symlink")
	}
}

func TestOxAlphaPatchText_NilResponse(t *testing.T) {
	content, err := oxAlphaPatchText(nil)
	if err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Fatalf("oxAlphaPatchText(nil) = %q, %v; want nil-response error", content, err)
	}
}

func TestOxAlphaPatchText_RecoversReasoningDetails(t *testing.T) {
	response := &model.ChatResponse{Choices: []model.Choice{{
		Message: model.Message{ReasoningDetails: []model.ReasoningDetail{
			{Text: "first"},
			{Summary: "second"},
		}},
	}}}

	content, err := oxAlphaPatchText(response)
	if err != nil {
		t.Fatalf("oxAlphaPatchText() error = %v", err)
	}
	if content != "first\nsecond\n" {
		t.Fatalf("content = %q, want recovered reasoning details", content)
	}
}
