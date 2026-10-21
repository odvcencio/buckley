package evidence

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestSQLiteStore_PutDuplicate_PreservesBodyMetadataAndPins(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
	}{{"inline", 12}, {"blob", InlineThreshold + 4096}} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			body := bytes.Repeat([]byte("x"), tc.size)
			first, err := store.Put(ctx, Object{Kind: KindToolResult, MediaType: "text/plain", InlineBody: body, Sensitivity: SensitivityWorkspace, EstimatedTokens: 17, Metadata: map[string]any{MetaSessionID: "original"}})
			if err != nil {
				t.Fatal(err)
			}
			second, err := store.Put(ctx, Object{Kind: first.Kind, MediaType: "application/octet-stream", InlineBody: body, Sensitivity: SensitivityPublic, Metadata: map[string]any{MetaSessionID: "reused"}})
			if err != nil {
				t.Fatal(err)
			}
			if second.ID != first.ID || second.MediaType != first.MediaType || second.Sensitivity != first.Sensitivity || second.EstimatedTokens != first.EstimatedTokens || !second.CreatedAt.Equal(first.CreatedAt) || second.Metadata[MetaSessionID] != "original" || !bytes.Equal(second.InlineBody, body) {
				t.Fatal("dedup changed original metadata or body")
			}
			var pins, objects int
			if err := store.DB().QueryRow(`SELECT COUNT(*) FROM evidence_pins WHERE evidence_id=?`, first.ID).Scan(&pins); err != nil {
				t.Fatal(err)
			}
			if err := store.DB().QueryRow(`SELECT COUNT(*) FROM evidence_objects`).Scan(&objects); err != nil {
				t.Fatal(err)
			}
			if pins != 2 || objects != 1 {
				t.Fatalf("pins=%d objects=%d", pins, objects)
			}
			// Returned bytes may be caller-owned, as on an initial Put. Mutating
			// them must never change the persisted object used by resume/replay.
			second.InlineBody[0] = 'y'
			got, err := store.Get(ctx, first.ID)
			if err != nil || got.InlineBody[0] != 'x' || got.ContentSHA256 != first.ContentSHA256 {
				t.Fatalf("persisted body changed: %v", err)
			}
		})
	}
}

func TestSQLiteStore_PutDuplicate_RejectsCorruptionWithoutPinning(t *testing.T) {
	for _, scenario := range []string{"inline_body", "byte_count", "hash", "short_body", "long_body", "checksum", "missing", "directory"} {
		t.Run(scenario, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			body := bytes.Repeat([]byte("x"), InlineThreshold+4096)
			if scenario == "inline_body" {
				body = []byte("original")
			}
			first, err := store.Put(ctx, Object{Kind: KindToolResult, InlineBody: body, Metadata: map[string]any{MetaSessionID: "original"}})
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "inline_body":
				_, err = store.DB().Exec(`UPDATE evidence_objects SET inline_body=? WHERE evidence_id=?`, []byte("modified"), first.ID)
			case "byte_count":
				_, err = store.DB().Exec(`UPDATE evidence_objects SET byte_count=byte_count+1 WHERE evidence_id=?`, first.ID)
			case "missing", "directory":
				err = os.Remove(first.BlobPath)
				if err == nil && scenario == "directory" {
					err = os.Mkdir(first.BlobPath, 0700)
				}
			default:
				changed := bytes.Repeat([]byte("y"), len(body))
				if scenario == "short_body" {
					changed = body[:len(body)-1]
				}
				if scenario == "long_body" {
					changed = append(bytes.Clone(body), 'x')
				}
				if scenario == "checksum" {
					changed = body
				}
				var encoded []byte
				encoded, err = compressZstd(changed)
				if scenario == "checksum" {
					encoded[len(encoded)-1] ^= 0xff
				}
				if err == nil {
					err = os.WriteFile(first.BlobPath, encoded, 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Put(ctx, Object{Kind: first.Kind, InlineBody: body, Metadata: map[string]any{MetaSessionID: "new"}})
			if scenario == "missing" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing blob cause lost: %v", err)
				}
			} else if !errors.Is(err, ErrIntegrity) {
				t.Fatalf("corrupt evidence reused: %v", err)
			}
			var pins int
			if err := store.DB().QueryRow(`SELECT COUNT(*) FROM evidence_pins WHERE evidence_id=?`, first.ID).Scan(&pins); err != nil {
				t.Fatal(err)
			}
			if pins != 1 {
				t.Fatalf("failed verification added a pin: %d", pins)
			}
		})
	}
}

func TestBlobStore_Verify_AcceptsLegacyStreamFrameAndRejectsLargeWindow(t *testing.T) {
	store, err := NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("legacy body\n"), 4096)
	path, err := store.PathForHash(ContentSHA256Hex(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := zstd.NewWriter(file, zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(8<<20))
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_, writeErr := writer.Write(body)
	closeErr := writer.Close()
	fileErr := file.Close()
	if err := errors.Join(writeErr, closeErr, fileErr); err != nil {
		t.Fatal(err)
	}
	if err := store.verify(context.Background(), path, body); err != nil {
		t.Fatalf("legacy stream rejected: %v", err)
	}
	// A valid empty streaming frame declaring a 128 MiB window. Verification
	// must reject the window before allocating it, regardless of output size.
	frame := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x88, 0x01, 0x00, 0x00}
	if err := os.WriteFile(path, frame, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.verify(context.Background(), path, nil); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("oversized window accepted: %v", err)
	}
}

type cancelReader struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (r cancelReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.cancel()
	return n, err
}

func TestVerifyBody_CancellationAndReadErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := bytes.Repeat([]byte("x"), 65536)
	err := verifyBody(ctx, cancelReader{bytes.NewReader(body), cancel}, body)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	store, err := NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.verify(ctx, "missing", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read touched storage: %v", err)
	}
	broken := io.MultiReader(bytes.NewReader(body), errorReader{})
	if err := verifyBody(context.Background(), broken, body); !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, ErrIntegrity) {
		t.Fatalf("late read error lost: %v", err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
