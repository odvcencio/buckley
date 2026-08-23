package runledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// CreateTaskCheckpointIfLatest atomically appends checkpoint only when the
// named parent is still the latest checkpoint for the same task, session, and
// run. The zero expectation (empty ID, version zero) creates a root only when
// the task has no checkpoint. A false applied result is a compare-and-save
// conflict, not a storage failure.
func (s *SQLiteStore) CreateTaskCheckpointIfLatest(
	ctx context.Context,
	checkpoint TaskCheckpoint,
	expectedCheckpointID string,
	expectedVersion int,
) (saved TaskCheckpoint, applied bool, err error) {
	if checkpoint.TaskID == "" {
		return TaskCheckpoint{}, false, fmt.Errorf("runledger: task_id is required")
	}
	if checkpoint.SessionID == "" {
		return TaskCheckpoint{}, false, fmt.Errorf("runledger: session_id is required for conditional checkpoint")
	}
	if checkpoint.RunID == "" {
		return TaskCheckpoint{}, false, fmt.Errorf("runledger: run_id is required for conditional checkpoint")
	}
	if checkpoint.MarkdownEvidenceID == "" {
		return TaskCheckpoint{}, false, fmt.Errorf("runledger: markdown_evidence_id is required")
	}
	rootExpectation := expectedCheckpointID == "" && expectedVersion == 0
	if !rootExpectation && (expectedCheckpointID == "" || expectedVersion <= 0) {
		return TaskCheckpoint{}, false, fmt.Errorf("runledger: expected checkpoint ID and positive version must be supplied together")
	}
	if checkpoint.ParentCheckpointID != "" && checkpoint.ParentCheckpointID != expectedCheckpointID {
		return TaskCheckpoint{}, false, fmt.Errorf("runledger: checkpoint parent does not match expectation")
	}
	nextVersion := expectedVersion + 1
	if checkpoint.Version != 0 && checkpoint.Version != nextVersion {
		return TaskCheckpoint{}, false, fmt.Errorf("runledger: checkpoint version %d does not follow expected version %d", checkpoint.Version, expectedVersion)
	}
	exists, err := evidenceRowExists(ctx, s.db, checkpoint.MarkdownEvidenceID)
	if err != nil {
		return TaskCheckpoint{}, false, err
	}
	if !exists {
		return TaskCheckpoint{}, false, fmt.Errorf("%w: %s", ErrEvidenceNotFound, checkpoint.MarkdownEvidenceID)
	}
	if checkpoint.CheckpointID == "" {
		checkpoint.CheckpointID = "cp_" + ulid.Make().String()
	}
	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = time.Now().UTC()
	}
	checkpoint.ParentCheckpointID = expectedCheckpointID
	checkpoint.Version = nextVersion

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO task_checkpoints (
			checkpoint_id, parent_checkpoint_id, task_id, session_id, run_id, version,
			status, snapshot_id, reason, state_json, markdown_evidence_id, created_at
		)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE (
			? = '' AND ? = 0 AND NOT EXISTS (
				SELECT 1 FROM task_checkpoints WHERE task_id = ?
			)
		) OR EXISTS (
			SELECT 1
			FROM task_checkpoints AS current
			WHERE current.task_id = ?
			  AND current.checkpoint_id = ?
			  AND current.version = ?
			  AND current.session_id = ?
			  AND COALESCE(current.run_id, '') = ?
			  AND NOT EXISTS (
				SELECT 1 FROM task_checkpoints AS newer
				WHERE newer.task_id = current.task_id AND newer.version > current.version
			  )
		)
	`, checkpoint.CheckpointID, nullableStr(checkpoint.ParentCheckpointID), checkpoint.TaskID, checkpoint.SessionID,
		nullableStr(checkpoint.RunID), checkpoint.Version, checkpoint.Status, nullableStr(checkpoint.SnapshotID),
		checkpoint.Reason, checkpoint.StateJSON, checkpoint.MarkdownEvidenceID, sqliteTimestamp(checkpoint.CreatedAt),
		expectedCheckpointID, expectedVersion, checkpoint.TaskID,
		checkpoint.TaskID, expectedCheckpointID, expectedVersion, checkpoint.SessionID, checkpoint.RunID)
	if err != nil {
		// A concurrent winner can surface as a uniqueness or busy error on
		// some SQLite schedules. One bounded read classifies that race as a
		// deterministic CAS miss; unchanged state remains a storage failure.
		if latest, latestErr := s.LatestTaskCheckpoint(ctx, checkpoint.TaskID); latestErr == nil {
			if rootExpectation || latest.CheckpointID != expectedCheckpointID || latest.Version != expectedVersion {
				return TaskCheckpoint{}, false, nil
			}
		} else if rootExpectation && errors.Is(latestErr, ErrNotFound) {
			return TaskCheckpoint{}, false, fmt.Errorf("runledger: conditional checkpoint insert: %w", err)
		}
		return TaskCheckpoint{}, false, fmt.Errorf("runledger: conditional checkpoint insert: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return TaskCheckpoint{}, false, fmt.Errorf("runledger: conditional checkpoint rows affected: %w", err)
	}
	if rows == 0 {
		return TaskCheckpoint{}, false, nil
	}
	if rows != 1 {
		return TaskCheckpoint{}, false, fmt.Errorf("runledger: conditional checkpoint inserted %d rows", rows)
	}
	return checkpoint, true, nil
}
