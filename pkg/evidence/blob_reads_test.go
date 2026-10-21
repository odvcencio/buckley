package evidence

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"runtime"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestBlobStore_Read_CompleteFrameVariants(t *testing.T) {
	encode := func(body []byte) []byte {
		t.Helper()
		encoded, err := compressZstdBytes(body)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	first, second := []byte("first frame\n"), bytes.Repeat([]byte("second frame\n"), 4096)
	var stream bytes.Buffer
	writer, err := zstd.NewWriter(&stream, zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(8<<20))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(second); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	skippable := []byte{0x50, 0x2a, 0x4d, 0x18, 3, 0, 0, 0, 's', 'k', 'p'}
	for _, tc := range []struct {
		name          string
		encoded, want []byte
	}{
		{"empty", encode(nil), []byte{}},
		{"known_size", encode(second), second},
		{"legacy_stream", stream.Bytes(), second},
		{"multiple_frames", append(encode(first), encode(second)...), append(bytes.Clone(first), second...)},
		{"skippable_prefix", append(skippable, encode(second)...), second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewBlobStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path, err := store.Write(ContentSHA256Hex(tc.want), tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, tc.encoded, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := store.Read(path)
			if err != nil || !bytes.Equal(got, tc.want) || got == nil {
				t.Fatalf("read lost complete body: got=%d want=%d err=%v", len(got), len(tc.want), err)
			}
			fragmented, err := decompressZstd(oneByteReader{bytes.NewReader(tc.encoded)})
			if err != nil || !bytes.Equal(fragmented, tc.want) {
				t.Fatalf("fragmented read changed data: %v", err)
			}
		})
	}
}

type oneByteReader struct{ reader io.Reader }

func (r oneByteReader) Read(p []byte) (int, error) { return r.reader.Read(p[:min(len(p), 1)]) }

func TestDecompressZstd_RejectsFailuresWithoutPartialBody(t *testing.T) {
	body := bytes.Repeat([]byte("retained evidence\n"), 4096)
	encoded, err := compressZstdBytes(body)
	if err != nil {
		t.Fatal(err)
	}
	badChecksum := bytes.Clone(encoded)
	badChecksum[len(badChecksum)-1] ^= 0xff
	for _, tc := range []struct {
		name   string
		reader io.Reader
		cause  error
	}{
		{"header_read_error", errorReader{}, io.ErrUnexpectedEOF},
		{"truncated_header", bytes.NewReader(encoded[:3]), nil},
		{"truncated_body", bytes.NewReader(encoded[:len(encoded)-3]), nil},
		{"checksum", bytes.NewReader(badChecksum), zstd.ErrCRCMismatch},
		{"late_read_error", io.MultiReader(bytes.NewReader(encoded), errorReader{}), io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decompressZstd(tc.reader)
			if err == nil || got != nil {
				t.Fatalf("accepted partial body: bytes=%d err=%v", len(got), err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("read cause lost: %v", err)
			}
		})
	}
}

func TestDecompressZstd_HugeDeclaredSizeDoesNotPreallocateIt(t *testing.T) {
	// A streaming frame with a 1 KiB window and a 1 TiB declared content size,
	// followed by an empty final block. It must fail with bounded scratch space.
	for _, tc := range []struct {
		name string
		size uint64
	}{{"64MiB", 64 << 20}, {"1TiB", 1 << 40}} {
		t.Run(tc.name, func(t *testing.T) {
			frame := []byte{0x28, 0xb5, 0x2f, 0xfd, 0xc0, 0x00}
			frame = binary.LittleEndian.AppendUint64(frame, tc.size)
			frame = append(frame, 0x01, 0x00, 0x00)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			got, err := decompressZstd(bytes.NewReader(frame))
			runtime.ReadMemStats(&after)
			allocated := after.TotalAlloc - before.TotalAlloc
			if err == nil || got != nil || allocated > 16<<20 {
				t.Fatalf("oversized hint: err=%v allocated=%d", err, allocated)
			}
			t.Logf("declared_bytes=%d allocated_bytes=%d rejected=true", tc.size, allocated)
		})
	}
}
