package reviewledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/klauspost/compress/zstd"
	"m31labs.dev/buckley/pkg/orchestrator"
	_ "modernc.org/sqlite"
)

type BackfillCounts struct {
	Blobs   int64 `json:"blobs"`
	Records int   `json:"records"`
}

// Backfill opens all source files and the SQLite database read-only. Existing
// hashes are accepted, so a interrupted backfill can safely run again.
func (l *Ledger) Backfill(ctx context.Context, evidenceRoot, database, recordsDir string) (BackfillCounts, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	keys, err := l.objects.List(ctx, l.key("blobs/sha256/"))
	if err != nil {
		return BackfillCounts{}, err
	}
	existing := make(map[string]bool, len(keys))
	for _, key := range keys {
		existing[key] = true
	}
	type blob struct {
		hash string
		body []byte
	}
	jobs := make(chan blob, 8)
	var count atomic.Int64
	var firstErr error
	var once sync.Once
	var wg sync.WaitGroup
	fail := func(err error) { once.Do(func() { firstErr = err; cancel() }) }
	for range 8 {
		wg.Go(func() {
			for item := range jobs {
				if err := l.UploadBlob(ctx, item.hash, item.body); err != nil {
					fail(err)
					return
				}
				count.Add(1)
			}
		})
	}
	seen := map[string]bool{}
	send := func(hash string, body []byte) error {
		key, err := BlobKey(hash)
		if err != nil {
			return err
		}
		if Hash(body) != hash {
			return fmt.Errorf("backfill blob hash mismatch: %s", hash)
		}
		if seen[hash] {
			return nil
		}
		seen[hash] = true
		if existing[l.key(key)] {
			count.Add(1)
			return nil
		}
		select {
		case jobs <- blob{hash, body}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		close(jobs)
		wg.Wait()
		return BackfillCounts{}, err
	}
	defer decoder.Close()
	err = filepath.WalkDir(filepath.Join(evidenceRoot, "sha256"), func(filename string, entry fs.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".zst") {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse evidence symlink: %s", filename)
		}
		hash := strings.TrimSuffix(entry.Name(), ".zst")
		if _, err := BlobKey(hash); err != nil {
			return err
		}
		compressed, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		body, err := decoder.DecodeAll(compressed, nil)
		if err != nil {
			return err
		}
		return send(hash, body)
	})
	if err == nil && database != "" {
		err = backfillInline(ctx, database, send)
	}
	if err != nil {
		fail(err)
	}
	close(jobs)
	wg.Wait()
	counts := BackfillCounts{Blobs: count.Load()}
	if firstErr != nil {
		return counts, firstErr
	}
	if database != "" {
		legacy, err := l.backfillReviewRuns(ctx, database)
		counts.Records += legacy
		counts.Blobs += int64(legacy)
		if err != nil {
			return counts, err
		}
	}
	if recordsDir != "" {
		err = filepath.WalkDir(recordsDir, func(filename string, entry fs.DirEntry, walkErr error) error {
			if os.IsNotExist(walkErr) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("refuse record symlink: %s", filename)
			}
			data, err := os.ReadFile(filename)
			if err != nil {
				return err
			}
			// Import either a retry envelope (with all bodies) or a saved manifest.
			var item pending
			if err := json.Unmarshal(data, &item); err != nil {
				return err
			}
			if item.Record.ReviewID != "" {
				if err := l.upload(ctx, item); err != nil {
					return err
				}
			} else {
				var record orchestrator.ReviewRecord
				if err := json.Unmarshal(data, &record); err != nil {
					return err
				}
				key, err := ManifestKey(record)
				if err != nil {
					return err
				}
				// Confirm every reference exists before publishing a standalone manifest.
				hashes := append([]string(nil), record.Evidence...)
				for _, command := range record.Verification {
					hashes = append(hashes, command.LogHash)
				}
				for _, hash := range hashes {
					key, _ := BlobKey(hash)
					if _, err := l.objects.Read(ctx, l.key(key)); err != nil {
						return fmt.Errorf("missing backfill evidence %s: %w", hash, err)
					}
				}
				body, err := json.Marshal(record)
				if err != nil {
					return err
				}
				err = l.objects.Create(ctx, l.key(key), body, "application/json")
				if err == ErrExists {
					existing, readErr := l.objects.Read(ctx, l.key(key))
					if readErr != nil {
						return readErr
					}
					if string(existing) != string(body) {
						return fmt.Errorf("immutable backfill manifest conflict")
					}
					err = nil
				}
				if err != nil {
					return err
				}
			}
			counts.Records++
			return nil
		})
	}
	return counts, err
}

func backfillInline(ctx context.Context, filename string, send func(string, []byte) error) error {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return err
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT content_sha256, inline_body FROM evidence_objects WHERE storage_kind = 'inline'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		var body []byte
		if err := rows.Scan(&hash, &body); err != nil {
			return err
		}
		if err := send(hash, body); err != nil {
			return err
		}
	}
	return rows.Err()
}
