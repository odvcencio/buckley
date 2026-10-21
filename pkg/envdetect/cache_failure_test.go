package envdetect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetector_CacheWriteFailurePreservesDetection(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.26.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".buckley"), []byte("cache directory blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	profile, err := NewDetector(root).Detect()
	if err != nil || profile == nil || len(profile.Languages) != 1 || profile.Languages[0].Name != "go" {
		t.Fatalf("optional cache failure broke detection: %+v %v", profile, err)
	}
}
