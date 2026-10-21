package evidence

import (
	"bytes"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func compressZstdBytes(content []byte) ([]byte, error) {
	compressed, err := compressZstd(content)
	if err != nil {
		return nil, err
	}
	return bytes.Join(compressed.chunks, nil), nil
}

func TestCompressZstd_BoundedPagesAndIndependentFrames(t *testing.T) {
	noise := make([]byte, 2<<20)
	_, _ = rand.New(rand.NewSource(42)).Read(noise)
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"empty", nil},
		{"small", []byte("retained evidence")},
		{"compressible", bytes.Repeat([]byte("x"), 2<<20)},
		{"incompressible", noise},
		{"mixed", append(bytes.Repeat([]byte("x"), 1<<20), noise...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compressed, err := compressZstd(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			var actual, capacity int64
			for _, chunk := range compressed.chunks {
				actual += int64(len(chunk))
				capacity += int64(cap(chunk))
				if cap(chunk) > blobCompressionPageBytes {
					t.Fatal("oversized compression page")
				}
			}
			if compressed.size != actual || capacity > actual+blobCompressionPageBytes {
				t.Fatalf("page accounting: size=%d actual=%d capacity=%d", compressed.size, actual, capacity)
			}
			// Reusing the encoder must not mutate an earlier frame's pages.
			before := bytes.Join(compressed.chunks, nil)
			if _, err := compressZstd(bytes.Repeat([]byte("next frame"), 65536)); err != nil {
				t.Fatal(err)
			}
			encoded := bytes.Join(compressed.chunks, nil)
			if !bytes.Equal(before, encoded) {
				t.Fatal("encoder retained or changed returned pages")
			}
			decoded, err := decompressZstd(bytes.NewReader(encoded))
			if err != nil || !bytes.Equal(decoded, tc.body) {
				t.Fatalf("frame changed payload: %v", err)
			}
		})
	}
}

func TestBlobStore_Write_AdmissionBeforeFilesystemMutation(t *testing.T) {
	store, err := NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 2<<20)
	_, _ = rand.New(rand.NewSource(42)).Read(body)
	sha := ContentSHA256Hex(body)
	path, err := store.PathForHash(sha)
	if err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("admission rejected")
	var admittedBytes int64
	_, _, err = store.write(sha, body, func(size int64) error { admittedBytes = size; return rejected })
	if !errors.Is(err, rejected) || admittedBytes <= 0 {
		t.Fatalf("admission cause or size lost: %d %v", admittedBytes, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("rejection created shard directory: %v", err)
	}
	stored, actual, err := store.write(sha, body, func(size int64) error {
		if size != admittedBytes {
			t.Fatalf("encoding changed after reset: %d != %d", size, admittedBytes)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(stored)
	if err != nil || info.Size() != actual || actual != admittedBytes {
		t.Fatalf("stored accounting changed: %d %d %v", actual, admittedBytes, err)
	}
}
