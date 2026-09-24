package reviewledger

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	"github.com/klauspost/compress/zstd"
)

var evidenceIDPattern = regexp.MustCompile(`ev_[A-Z2-7]{26}`)

// ReferencedEvidence resolves code-mode evidence IDs before local retention can
// remove their bodies. The database and blob files are opened read-only.
func ReferencedEvidence(ctx context.Context, database string, bodies map[string][]byte) (map[string][]byte, error) {
	ids := map[string]bool{}
	for _, body := range bodies {
		for _, id := range evidenceIDPattern.FindAllString(string(body), -1) {
			ids[id] = true
		}
	}
	found := map[string][]byte{}
	if len(ids) == 0 {
		return found, nil
	}
	absolute, err := filepath.Abs(database)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}).String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	for id := range ids {
		var hash, storage string
		var inline []byte
		var blob sql.NullString
		err := db.QueryRowContext(ctx, `SELECT content_sha256,storage_kind,inline_body,blob_path FROM evidence_objects WHERE evidence_id=?`, id).Scan(&hash, &storage, &inline, &blob)
		if err != nil {
			return found, fmt.Errorf("read referenced evidence %s: %w", id, err)
		}
		body := inline
		if storage != "inline" {
			if !blob.Valid {
				return found, fmt.Errorf("missing blob path for %s", id)
			}
			compressed, err := os.ReadFile(blob.String)
			if err != nil {
				return found, err
			}
			body, err = decoder.DecodeAll(compressed, nil)
			if err != nil {
				return found, err
			}
		}
		if Hash(body) != hash {
			return found, fmt.Errorf("referenced evidence hash mismatch: %s", id)
		}
		found[hash] = body
	}
	return found, nil
}
