package evidence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"m31labs.dev/buckley/pkg/storage"
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
	// OtherTTL expires other known kinds after their owning history is
	// removed. Zero leaves their retention to the caller.
	OtherTTL time.Duration
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

var durableKinds = []Kind{KindCheckpoint, KindReview, KindCommitProposal, KindVerificationReceipt}

var otherKinds = []Kind{KindSource, KindProjection, KindContextBundle, KindContextManifest,
	KindDiff, KindSubagentTask, KindSubagentReport, KindSubagentMessage, KindMemoryRecall}

// Sweep deletes unpinned objects older than their class's TTL and returns
// the IDs it removed. User and run pins always protect their objects. Automatic verification
// receipt pins expire with the receipt after DurableTTL.
func (s *SQLiteStore) Sweep(ctx context.Context, policy RetentionPolicy, now time.Time) ([]string, error) {
	var removed []string
	err := s.sweep(ctx, policy, now, func(id string) { removed = append(removed, id) })
	return removed, err
}

// SweepCount keeps cleanup memory bounded even for a large retention backlog.
func (s *SQLiteStore) SweepCount(ctx context.Context, policy RetentionPolicy, now time.Time) (int, error) {
	removed := 0
	err := s.sweep(ctx, policy, now, func(string) { removed++ })
	return removed, err
}

func (s *SQLiteStore) sweep(ctx context.Context, policy RetentionPolicy, now time.Time, removed func(string)) error {
	guards, err := s.retainedReferenceGuards(ctx)
	if err != nil {
		return err
	}
	if policy.DurableTTL > 0 {
		for {
			ids, err := s.expireVerificationReceipts(ctx, now.Add(-policy.DurableTTL), guards)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			for _, id := range ids {
				removed(id)
			}
		}
	}
	for _, class := range []struct {
		kinds []Kind
		ttl   time.Duration
	}{
		{ephemeralKinds, policy.EphemeralTTL}, {durableKinds, policy.DurableTTL}, {otherKinds, policy.OtherTTL},
	} {
		if class.ttl <= 0 {
			continue
		}
		for {
			ids, err := s.expiredUnpinnedIDs(ctx, class.kinds, now.Add(-class.ttl), guards)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			deleted, err := s.deleteUnpinnedObjects(ctx, ids, guards)
			if err != nil {
				return err
			}
			for _, id := range deleted {
				removed(id)
			}
		}
	}

	return nil
}

func (s *SQLiteStore) expiredUnpinnedIDs(ctx context.Context, kinds []Kind, cutoff time.Time, guards string) ([]string, error) {
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
	`+guards+` ORDER BY created_at, evidence_id LIMIT 256`, placeholders)

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

func (s *SQLiteStore) deleteUnpinnedObjects(ctx context.Context, ids []string, guards string) ([]string, error) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i], args[i] = "?", id
	}
	var deleted []string
	err := storage.WithSQLiteWriteTransaction(ctx, s.db, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `DELETE FROM evidence_objects
			WHERE evidence_id IN (`+strings.Join(placeholders, ",")+`) AND NOT EXISTS (
				SELECT 1 FROM evidence_pins WHERE evidence_id = evidence_objects.evidence_id)
			`+guards+` RETURNING evidence_id`, args...)
		if err != nil {
			return fmt.Errorf("evidence: delete expired objects: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			deleted = append(deleted, id)
		}
		// Files are removed after the row transaction commits. Orphan cleanup
		// rechecks references under the same writer lock used by Put; rollback
		// and a new writer can therefore never leave a row without its blob.
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return deleted, nil
}

// Ledger references predating lifetime pins still protect their evidence. These
// tables are optional: an evidence store can be used without a run ledger.
func (s *SQLiteStore) retainedReferenceGuards(ctx context.Context) (string, error) {
	guard := ""
	for _, reference := range []struct{ table, predicate string }{
		{"execution_steps", "output_evidence_id = evidence_objects.evidence_id"},
		{"context_receipts", "bundle_evidence_id = evidence_objects.evidence_id OR manifest_evidence_id = evidence_objects.evidence_id"},
		{"task_checkpoints", "markdown_evidence_id = evidence_objects.evidence_id"},
		{"agent_run_contracts", "task_evidence_id = evidence_objects.evidence_id"},
		{"agent_mailbox", "content_ref = evidence_objects.evidence_id"},
		{"run_events", ""},
	} {
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`, reference.table).Scan(&exists); err != nil {
			return "", err
		}
		if exists {
			if reference.table == "run_events" {
				// An uncorrelated list parses retained event bodies once per
				// batch instead of once for every candidate evidence object.
				guard += ` AND evidence_id NOT IN (
					SELECT refs.value FROM run_events CROSS JOIN json_each(json_extract(payload_json,
						'$.request_evidence_id', '$.response_evidence_id', '$.output_evidence_id', '$.evidence_id')) AS refs
					WHERE refs.value IS NOT NULL
					UNION ALL
					SELECT refs.value FROM run_events CROSS JOIN json_each(evidence_ids_json) AS refs
					WHERE refs.value IS NOT NULL)`
			} else {
				guard += " AND NOT EXISTS (SELECT 1 FROM " + reference.table + " WHERE " + reference.predicate + ")"
			}
		}
	}
	return guard, nil
}

// CleanupOrphanBlobs deletes blob files with no corresponding
// evidence_objects row. It is safe to interrupt and resume: each file is
// evaluated and removed independently, so a partial run leaves no
// inconsistent state to repair before continuing (section 13.2: "Orphan
// cleanup MUST be safe and resumable").
func (s *SQLiteStore) CleanupOrphanBlobs(ctx context.Context) ([]string, error) {
	var removed []string
	err := s.cleanupOrphanBlobs(ctx, func(path string) { removed = append(removed, path) })
	return removed, err
}

// CleanupOrphanBlobCount reclaims orphan files without retaining every path.
func (s *SQLiteStore) CleanupOrphanBlobCount(ctx context.Context) (int, error) {
	removed := 0
	err := s.cleanupOrphanBlobs(ctx, func(string) { removed++ })
	return removed, err
}

func (s *SQLiteStore) cleanupOrphanBlobs(ctx context.Context, removed func(string)) error {
	walkErr := s.blobs.walk(func(path string) error {
		return storage.WithSQLiteWriteTransaction(ctx, s.db, func(conn *sql.Conn) error {
			var referenced bool
			if err := conn.QueryRowContext(ctx, `SELECT EXISTS(
				SELECT 1 FROM evidence_objects WHERE blob_path = ?)`, path).Scan(&referenced); err != nil {
				return err
			}
			if referenced {
				return nil
			}
			if err := s.blobs.Delete(path); err != nil {
				return err
			}
			removed(path)
			return nil
		})
	}, func(dir string) error {
		return storage.WithSQLiteWriteTransaction(ctx, s.db, func(_ *sql.Conn) error {
			err := os.Remove(dir)
			if os.IsNotExist(err) || errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
				return nil
			}
			return err
		})
	})
	if walkErr != nil {
		return fmt.Errorf("evidence: walk blob store: %w", walkErr)
	}
	return nil
}

// CleanupStaleTemps removes interrupted blob writes after a grace period.
func (s *SQLiteStore) CleanupStaleTemps(ctx context.Context, cutoff time.Time) (int, error) {
	removed := 0
	err := filepath.WalkDir(s.blobs.writeRoot(), func(path string, entry os.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".blob-") || !strings.HasSuffix(entry.Name(), ".tmp") {
			return nil
		}
		return storage.WithSQLiteWriteTransaction(ctx, s.db, func(_ *sql.Conn) error {
			info, err := entry.Info()
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if !info.ModTime().Before(cutoff) {
				return nil
			}
			if err := s.blobs.Delete(path); err != nil {
				return err
			}
			removed++
			return nil
		})
	})
	return removed, err
}
