package evidence

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RetentionPolicy configures how long unpinned evidence is kept before
// Sweep deletes it (section 13.5).
type RetentionPolicy struct {
	// EphemeralTTL bounds unreferenced ephemeral tool evidence (tool
	// results, command output, test output). Default: 30 days.
	EphemeralTTL time.Duration
	// DurableTTL bounds checkpoints, review receipts, and commit
	// proposals. Default: 180 days.
	DurableTTL time.Duration
}

// DefaultRetentionPolicy returns the defaults from section 13.5.
//
// Two of the spec's retention classes are intentionally not enforced by
// Sweep: "active task evidence" (retained while its task is active, plus 30
// days) and "evidence linked to a commit receipt" (pinned until explicit
// cleanup). Neither is decidable from within the evidence package, which
// has no notion of task activity or receipt linkage; those signals live in
// pkg/runledger and pkg/taskstate. The intended integration is that those
// packages call Pin/Release on the objects they reference, and Sweep only
// ever considers unpinned objects, so pinning is the enforcement mechanism
// for those two classes rather than a TTL.
func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		EphemeralTTL: 30 * 24 * time.Hour,
		DurableTTL:   180 * 24 * time.Hour,
	}
}

var ephemeralKinds = []Kind{
	KindModelRequest,
	KindModelResponse,
	KindToolRequest,
	KindToolResult,
	KindCommandOutput,
	KindTestOutput,
}

var durableKinds = []Kind{KindCheckpoint, KindReview, KindCommitProposal}

// Sweep deletes unpinned objects older than their class's TTL and returns
// the IDs it removed. It never deletes an object with an active pin
// (section 13.5: "User-pinned evidence: indefinite").
func (s *SQLiteStore) Sweep(ctx context.Context, policy RetentionPolicy, now time.Time) ([]string, error) {
	var removed []string

	ephemeralCutoff := now.Add(-policy.EphemeralTTL)
	ids, err := s.expiredUnpinnedIDs(ctx, ephemeralKinds, ephemeralCutoff)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := s.deleteObject(ctx, id); err != nil {
			return removed, err
		}
		removed = append(removed, id)
	}

	durableCutoff := now.Add(-policy.DurableTTL)
	ids, err = s.expiredUnpinnedIDs(ctx, durableKinds, durableCutoff)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := s.deleteObject(ctx, id); err != nil {
			return removed, err
		}
		removed = append(removed, id)
	}

	return removed, nil
}

func (s *SQLiteStore) expiredUnpinnedIDs(ctx context.Context, kinds []Kind, cutoff time.Time) ([]string, error) {
	placeholders := ""
	args := []any{}
	for i, k := range kinds {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, string(k))
	}
	args = append(args, sqliteTimestamp(cutoff))

	query := fmt.Sprintf(`
		SELECT evidence_id FROM evidence_objects
		WHERE kind IN (%s)
		  AND created_at < ?
		  AND evidence_id NOT IN (SELECT evidence_id FROM evidence_pins)
	`, placeholders)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("evidence: find expired objects: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("evidence: scan expired id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// deleteObject removes id's row and, if no other object shares its blob
// file (blob files are content-addressed by sha256 alone, independent of
// kind, so two different kinds of identical bytes can share one blob),
// deletes the blob file too.
func (s *SQLiteStore) deleteObject(ctx context.Context, id string) error {
	obj, err := s.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}

	if _, err := s.db.ExecContext(ctx, `DELETE FROM evidence_objects WHERE evidence_id = ?`, id); err != nil {
		return fmt.Errorf("evidence: delete object %s: %w", id, err)
	}

	if obj.Storage != StorageBlob || obj.BlobPath == "" {
		return nil
	}

	var refCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence_objects WHERE blob_path = ?`, obj.BlobPath).Scan(&refCount); err != nil {
		return fmt.Errorf("evidence: check blob refcount: %w", err)
	}
	if refCount > 0 {
		return nil
	}
	return s.blobs.Delete(obj.BlobPath)
}

// CleanupOrphanBlobs deletes blob files with no corresponding
// evidence_objects row. It is safe to interrupt and resume: each file is
// evaluated and removed independently, so a partial run leaves no
// inconsistent state to repair before continuing (section 13.2: "Orphan
// cleanup MUST be safe and resumable").
func (s *SQLiteStore) CleanupOrphanBlobs(ctx context.Context) ([]string, error) {
	referenced := make(map[string]bool)
	rows, err := s.db.QueryContext(ctx, `SELECT blob_path FROM evidence_objects WHERE blob_path IS NOT NULL AND blob_path != ''`)
	if err != nil {
		return nil, fmt.Errorf("evidence: list referenced blobs: %w", err)
	}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return nil, fmt.Errorf("evidence: scan referenced blob: %w", err)
		}
		referenced[path] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var removed []string
	walkErr := s.blobs.Walk(func(path string) error {
		if referenced[path] {
			return nil
		}
		if err := s.blobs.Delete(path); err != nil {
			return err
		}
		removed = append(removed, path)
		return nil
	})
	if walkErr != nil {
		return removed, fmt.Errorf("evidence: walk blob store: %w", walkErr)
	}
	return removed, nil
}
