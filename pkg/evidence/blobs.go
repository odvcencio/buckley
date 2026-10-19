package evidence

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// EncodeAll is concurrency-safe; a single reusable worker bounds scratch
// memory across stores instead of allocating GOMAXPROCS workers per blob.
var blobEncoder = sync.OnceValues(func() (*zstd.Encoder, error) {
	return zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(1<<20))
})

// BlobStore persists evidence bodies larger than InlineThreshold as
// zstd-compressed files under a content-addressed directory layout:
//
//	<root>/sha256/ab/cd/<full-hash>.zst
//
// SQLite stores add a database namespace beneath sha256. Legacy shared paths
// remain readable; orphan cleanup leaves that unowned namespace untouched.
//
// Writes are crash-safe: content is written to a temporary file, fsynced,
// and atomically renamed into place. A blob file only becomes visible at its
// final path once it is fully durable.
type BlobStore struct {
	root string
	// SQLite stores sharing a data directory own separate writable namespaces.
	// Reads still accept legacy paths beneath root; cleanup visits only ours.
	namespace string
}

// NewBlobStore creates a BlobStore rooted at root, creating the directory
// (mode 0700) if it does not already exist.
func NewBlobStore(root string) (*BlobStore, error) {
	if root == "" {
		return nil, fmt.Errorf("evidence: blob store root cannot be empty")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("evidence: create blob root: %w", err)
	}
	return &BlobStore{root: root}, nil
}

// Root returns the blob store's root directory.
func (b *BlobStore) Root() string {
	return b.root
}

// PathForHash returns the on-disk path for a blob with the given lowercase
// hex sha256 hash, without checking whether the file exists. The hash must
// be exactly 64 lowercase hex characters; anything else is rejected so a
// caller-controlled value can never introduce path separators or traversal
// components.
func (b *BlobStore) PathForHash(sha256Hex string) (string, error) {
	if !validSHA256Hex(sha256Hex) {
		return "", fmt.Errorf("evidence: invalid content hash %q", sha256Hex)
	}
	return filepath.Join(b.writeRoot(), sha256Hex[0:2], sha256Hex[2:4], sha256Hex+".zst"), nil
}

func (b *BlobStore) writeRoot() string {
	return filepath.Join(b.root, "sha256", b.namespace)
}

func validSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Write compresses content with zstd and durably writes it to the
// content-addressed path for sha256Hex, using write-temp, fsync, atomic
// rename. Write is idempotent: if the blob already exists, it returns the
// existing path without rewriting it.
func (b *BlobStore) Write(sha256Hex string, content []byte) (string, error) {
	path, _, err := b.write(sha256Hex, content, nil)
	return path, err
}

func (b *BlobStore) write(sha256Hex string, content []byte, admit func(int64) error) (string, int64, error) {
	finalPath, err := b.PathForHash(sha256Hex)
	if err != nil {
		return "", 0, err
	}
	if actual := ContentSHA256Hex(content); actual != sha256Hex {
		return "", 0, fmt.Errorf("evidence: content hash mismatch: declared %s, actual %s", sha256Hex, actual)
	}

	if info, err := os.Stat(finalPath); err == nil {
		if admit != nil {
			if err := admit(info.Size()); err != nil {
				return "", 0, err
			}
		}
		return finalPath, info.Size(), nil
	} else if !os.IsNotExist(err) {
		return "", 0, fmt.Errorf("evidence: stat blob: %w", err)
	}

	compressed, err := compressZstd(content)
	if err != nil {
		return "", 0, fmt.Errorf("evidence: compress blob: %w", err)
	}
	if admit != nil {
		if err := admit(int64(len(compressed))); err != nil {
			return "", 0, err
		}
	}
	dir := filepath.Dir(finalPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, fmt.Errorf("evidence: create blob dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".blob-*.tmp")
	if err != nil {
		return "", 0, fmt.Errorf("evidence: create temp blob: %w", err)
	}
	tmpPath := tmp.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", 0, fmt.Errorf("evidence: chmod temp blob: %w", err)
	}
	if _, err := tmp.Write(compressed); err != nil {
		_ = tmp.Close()
		return "", 0, fmt.Errorf("evidence: write temp blob: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", 0, fmt.Errorf("evidence: fsync temp blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("evidence: close temp blob: %w", err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", 0, fmt.Errorf("evidence: rename blob into place: %w", err)
	}
	cleanupTmp = false

	return finalPath, int64(len(compressed)), nil
}

// confinePath resolves path and rejects anything that escapes the blob
// store's root, including relative traversal and symlinked parents, so the
// public path-based operations keep the same confinement guarantee as
// PathForHash.
func (b *BlobStore) confinePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("evidence: resolve blob path: %w", err)
	}
	rootAbs, err := filepath.Abs(b.root)
	if err != nil {
		return "", fmt.Errorf("evidence: resolve blob root: %w", err)
	}
	if resolvedRoot, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = resolvedRoot
	}
	if resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		abs = filepath.Join(resolvedDir, filepath.Base(abs))
	}
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("evidence: blob path %q is outside the blob root", path)
	}
	if fi, err := os.Lstat(abs); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("evidence: blob path %q is a symlink; refusing to follow", path)
	}
	return abs, nil
}

// Read decompresses and returns the content stored at path. The path must
// resolve inside the blob root.
func (b *BlobStore) Read(path string) ([]byte, error) {
	confined, err := b.confinePath(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(confined)
	if err != nil {
		return nil, fmt.Errorf("evidence: read blob: %w", err)
	}
	return decompressZstd(raw)
}

// Delete removes the blob file at path. Deleting a file that does not exist
// is not an error, so cleanup passes are safely resumable. The path must
// resolve inside the blob root.
func (b *BlobStore) Delete(path string) error {
	confined, err := b.confinePath(path)
	if err != nil {
		return err
	}
	if err := os.Remove(confined); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("evidence: delete blob: %w", err)
	}
	return nil
}

// Walk invokes fn for every blob file currently on disk. It is used by
// orphan cleanup to find blobs with no corresponding evidence_objects row.
func (b *BlobStore) Walk(fn func(path string) error) error {
	return b.walk(fn, nil)
}

func (b *BlobStore) walk(fn func(string) error, afterDir func(string) error) error {
	var visit func(string) error
	visit = func(dir string) error {
		file, err := os.Open(dir)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		defer file.Close()
		for {
			entries, readErr := file.ReadDir(128)
			if readErr != nil && readErr != io.EOF {
				return readErr
			}
			for _, entry := range entries {
				path := filepath.Join(dir, entry.Name())
				if entry.IsDir() {
					if err := visit(path); err != nil {
						return err
					}
				} else if filepath.Ext(path) == ".zst" {
					if err := fn(path); err != nil {
						return err
					}
				}
			}
			if readErr == io.EOF {
				break
			}
		}
		if err := file.Close(); err != nil {
			return err
		}
		if afterDir != nil && dir != b.writeRoot() {
			return afterDir(dir)
		}
		return nil
	}
	return visit(b.writeRoot())
}

func compressZstd(content []byte) ([]byte, error) {
	enc, err := blobEncoder()
	if err != nil {
		return nil, err
	}
	return enc.EncodeAll(content, make([]byte, 0, len(content))), nil
}

func decompressZstd(compressed []byte) ([]byte, error) {
	dec, err := zstd.NewReader(bytes.NewReader(compressed), zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	out, err := io.ReadAll(dec)
	if err != nil {
		return nil, err
	}
	return out, nil
}
