package evidence

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlobStore_WriteReadRoundTrip(t *testing.T) {
	root := t.TempDir()
	blobs, err := NewBlobStore(root)
	if err != nil {
		t.Fatalf("NewBlobStore() error = %v", err)
	}

	content := bytes.Repeat([]byte("evidence blob content "), 1000)
	sha := ContentSHA256Hex(content)

	path, err := blobs.Write(sha, content)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	wantSuffix := filepath.Join("sha256", sha[0:2], sha[2:4], sha+".zst")
	if !strings.HasSuffix(path, wantSuffix) {
		t.Fatalf("blob path = %q, want suffix %q", path, wantSuffix)
	}

	got, err := blobs.Read(path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("round-tripped content mismatch: got %d bytes, want %d bytes", len(got), len(content))
	}
}

func TestBlobStore_WritePermissions(t *testing.T) {
	root := t.TempDir()
	blobs, err := NewBlobStore(root)
	if err != nil {
		t.Fatalf("NewBlobStore() error = %v", err)
	}

	content := []byte("permission check")
	sha := ContentSHA256Hex(content)
	path, err := blobs.Write(sha, content)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("blob file perm = %v, want 0600", perm)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat() dir error = %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("blob dir perm = %v, want 0700", perm)
	}
}

func TestBlobStore_WriteIdempotent(t *testing.T) {
	root := t.TempDir()
	blobs, err := NewBlobStore(root)
	if err != nil {
		t.Fatalf("NewBlobStore() error = %v", err)
	}

	content := []byte("idempotent write")
	sha := ContentSHA256Hex(content)

	first, err := blobs.Write(sha, content)
	if err != nil {
		t.Fatalf("first Write() error = %v", err)
	}
	second, err := blobs.Write(sha, content)
	if err != nil {
		t.Fatalf("second Write() error = %v", err)
	}
	if first != second {
		t.Fatalf("expected identical path on rewrite: %q != %q", first, second)
	}
}

// TestBlobStore_WriteLeavesNoTempFiles verifies the write-temp/fsync/rename
// sequence never leaves a partially written file at the final path: after
// Write returns, the only file present is the final blob, and no ".tmp"
// sibling remains. This is the crash-safety property from section 13.2.
func TestBlobStore_WriteLeavesNoTempFiles(t *testing.T) {
	root := t.TempDir()
	blobs, err := NewBlobStore(root)
	if err != nil {
		t.Fatalf("NewBlobStore() error = %v", err)
	}

	content := bytes.Repeat([]byte("z"), 4096)
	sha := ContentSHA256Hex(content)
	path, err := blobs.Write(sha, content)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one file in blob dir after write, got %d: %v", len(entries), entries)
	}
	if entries[0].Name() != filepath.Base(path) {
		t.Fatalf("unexpected file left behind: %q", entries[0].Name())
	}
}

func TestBlobStore_DeleteMissingIsNotError(t *testing.T) {
	root := t.TempDir()
	blobs, err := NewBlobStore(root)
	if err != nil {
		t.Fatalf("NewBlobStore() error = %v", err)
	}
	if err := blobs.Delete(filepath.Join(root, "sha256", "ab", "cd", "missing.zst")); err != nil {
		t.Fatalf("Delete() on missing file error = %v, want nil", err)
	}
}

func TestBlobStore_Walk(t *testing.T) {
	root := t.TempDir()
	blobs, err := NewBlobStore(root)
	if err != nil {
		t.Fatalf("NewBlobStore() error = %v", err)
	}

	var written []string
	for i := 0; i < 3; i++ {
		content := []byte{byte(i), byte(i + 1), byte(i + 2)}
		sha := ContentSHA256Hex(content)
		path, err := blobs.Write(sha, content)
		if err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		written = append(written, path)
	}

	var walked []string
	if err := blobs.Walk(func(path string) error {
		walked = append(walked, path)
		return nil
	}); err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(walked) != len(written) {
		t.Fatalf("Walk() found %d files, want %d", len(walked), len(written))
	}
}

func TestPathForHashRejectsTraversalAndMalformedHashes(t *testing.T) {
	b, err := NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"....", "..", "ab/cd", "ABCDEF" + strings.Repeat("0", 58), strings.Repeat("0", 63), strings.Repeat("0", 65), "../" + strings.Repeat("a", 61)} {
		if _, err := b.PathForHash(bad); err == nil {
			t.Fatalf("PathForHash accepted malformed hash %q", bad)
		}
	}
}

func TestWriteRejectsMismatchedContentHash(t *testing.T) {
	b, err := NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wrong := strings.Repeat("a", 64)
	if _, err := b.Write(wrong, []byte("content that does not hash to that")); err == nil {
		t.Fatal("Write accepted content whose hash does not match the declared hash")
	}
}

func TestReadAndDeleteRejectPathsOutsideRoot(t *testing.T) {
	b, err := NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "victim.zst")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{outside, filepath.Join(b.Root(), "..", "victim.zst"), "/etc/hostname"} {
		if _, err := b.Read(bad); err == nil {
			t.Fatalf("Read accepted path outside root: %q", bad)
		}
		if err := b.Delete(bad); err == nil {
			t.Fatalf("Delete accepted path outside root: %q", bad)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file was touched: %v", err)
	}
}

func TestReadRejectsFinalComponentSymlink(t *testing.T) {
	b, err := NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.zst")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(b.Root(), "sha256", "aa", "bb")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "blob.zst")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := b.Read(link); err == nil {
		t.Fatal("Read followed a final-component symlink outside the root")
	}
}
