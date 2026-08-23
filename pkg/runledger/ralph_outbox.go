package runledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ralphOutboxDeliveryLease      = 30 * time.Second
	ralphOutboxRecoveryMaxBatches = 64
)

const (
	ralphDeliveryUntracked = "untracked"
	ralphDeliveryWaiting   = "waiting"
	ralphDeliveryClaimed   = "claimed"
	ralphDeliveryDelivered = "delivered"
)

func enqueueRalphOutboxTx(ctx context.Context, tx *sql.Tx, eventID string, enabled bool) error {
	if !enabled {
		return nil
	}
	now := sqliteTimestamp(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO run_event_ralph_outbox (
			event_id, state, attempt_count, created_at, updated_at
		) VALUES (?, 'pending', 0, ?, ?)
	`, eventID, now, now); err != nil {
		return fmt.Errorf("runledger: enqueue ralph event delivery: %w", err)
	}
	return nil
}

func (s *SQLiteStore) drainRalphOutbox(ctx context.Context, sink RalphSink, batchSize int) error {
	if s == nil || s.db == nil || sink == nil || batchSize <= 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	seen := make(map[string]struct{}, batchSize)
	var result error
	for batch := 0; batch < ralphOutboxRecoveryMaxBatches && ctx.Err() == nil; batch++ {
		eventIDs, err := s.listRecoverableRalphEvents(ctx, batchSize)
		if err != nil {
			return errors.Join(result, err)
		}
		if len(eventIDs) == 0 {
			break
		}
		progressed := false
		for _, eventID := range eventIDs {
			if _, attempted := seen[eventID]; attempted {
				continue
			}
			seen[eventID] = struct{}{}
			progressed = true
			if err := s.deliverRalphOutbox(ctx, eventID, sink); err != nil {
				result = errors.Join(result, err)
			}
			if ctx.Err() != nil {
				break
			}
		}
		if !progressed || len(eventIDs) < batchSize {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(result, err)
	}
	return result
}

func (s *SQLiteStore) listRecoverableRalphEvents(ctx context.Context, limit int) ([]string, error) {
	now := sqliteLeaseTimestamp(time.Now().UTC())
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_id
		FROM run_event_ralph_outbox
		WHERE state IN ('pending', 'failed')
		   OR (state = 'delivering' AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?)
		ORDER BY updated_at ASC, event_id ASC
		LIMIT ?
	`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("runledger: list recoverable ralph deliveries: %w", err)
	}
	eventIDs := make([]string, 0, limit)
	for rows.Next() {
		var eventID string
		if err := rows.Scan(&eventID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("runledger: scan recoverable ralph delivery: %w", err)
		}
		eventIDs = append(eventIDs, eventID)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("runledger: close recoverable ralph deliveries: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runledger: iterate recoverable ralph deliveries: %w", err)
	}
	return eventIDs, nil
}

func (s *SQLiteStore) deliverRalphOutbox(ctx context.Context, eventID string, sink RalphSink) error {
	if sink == nil {
		return nil
	}
	owner := "ralph_" + NewEventID()
	waitDeadline := time.Now().Add(mailboxBusyRetryWindow)
	for {
		decision := ""
		err := retryMailboxBusy(ctx, func() error {
			var claimErr error
			decision, claimErr = s.claimRalphOutbox(ctx, eventID, owner)
			return claimErr
		})
		if err != nil {
			return fmt.Errorf("%w: claim delivery: %v", ErrRalphDualWriteFailed, err)
		}
		switch decision {
		case ralphDeliveryUntracked, ralphDeliveryDelivered:
			return nil
		case ralphDeliveryClaimed:
			goto deliver
		case ralphDeliveryWaiting:
			if time.Now().After(waitDeadline) {
				return fmt.Errorf("%w: delivery remains owned by another writer", ErrRalphDualWriteFailed)
			}
			timer := time.NewTimer(5 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("%w: wait for delivery: %v", ErrRalphDualWriteFailed, ctx.Err())
			case <-timer.C:
			}
		default:
			return fmt.Errorf("%w: invalid outbox decision %q", ErrRalphDualWriteFailed, decision)
		}
	}

deliver:
	event, err := s.loadCanonicalRalphEvent(ctx, eventID)
	if err != nil {
		_ = s.settleRalphOutbox(eventID, owner, false)
		return fmt.Errorf("%w: load canonical event: %v", ErrRalphDualWriteFailed, err)
	}
	if err := sink.WriteEvent(ctx, event); err != nil {
		settleErr := s.settleRalphOutbox(eventID, owner, false)
		if settleErr != nil {
			return fmt.Errorf("%w: %v (record failure: %v)", ErrRalphDualWriteFailed, err, settleErr)
		}
		return fmt.Errorf("%w: %v", ErrRalphDualWriteFailed, err)
	}
	if err := s.settleRalphOutbox(eventID, owner, true); err != nil {
		return fmt.Errorf("%w: record delivery: %v", ErrRalphDualWriteFailed, err)
	}
	return nil
}

func (s *SQLiteStore) claimRalphOutbox(ctx context.Context, eventID, owner string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("runledger: begin ralph delivery claim: %w", err)
	}
	defer tx.Rollback()
	now, err := sqliteDBTime(ctx, tx)
	if err != nil {
		return "", err
	}
	var state string
	var leaseRaw sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT state, lease_expires_at
		FROM run_event_ralph_outbox WHERE event_id = ?
	`, strings.TrimSpace(eventID)).Scan(&state, &leaseRaw)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("runledger: commit absent ralph delivery: %w", err)
		}
		return ralphDeliveryUntracked, nil
	}
	if err != nil {
		return "", fmt.Errorf("runledger: inspect ralph delivery: %w", err)
	}
	if state == "delivered" {
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("runledger: commit existing ralph delivery: %w", err)
		}
		return ralphDeliveryDelivered, nil
	}
	if state == "delivering" && parseSQLiteTimestamp(leaseRaw.String).After(now) {
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("runledger: commit in-flight ralph delivery: %w", err)
		}
		return ralphDeliveryWaiting, nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE run_event_ralph_outbox
		SET state = 'delivering', delivery_owner = ?, lease_expires_at = ?,
			attempt_count = attempt_count + 1, updated_at = ?
		WHERE event_id = ? AND state <> 'delivered'
	`, owner, sqliteLeaseTimestamp(now.Add(ralphOutboxDeliveryLease)), sqliteTimestamp(now), eventID)
	if err != nil {
		return "", fmt.Errorf("runledger: claim ralph delivery: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		if err != nil {
			return "", fmt.Errorf("runledger: inspect ralph delivery claim: %w", err)
		}
		return "", fmt.Errorf("runledger: ralph delivery claim lost compare-and-swap")
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("runledger: commit ralph delivery claim: %w", err)
	}
	return ralphDeliveryClaimed, nil
}

func (s *SQLiteStore) settleRalphOutbox(eventID, owner string, delivered bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	state := "failed"
	var deliveredAt any
	if delivered {
		state = "delivered"
		deliveredAt = sqliteTimestamp(time.Now().UTC())
	}
	return retryMailboxBusy(ctx, func() error {
		result, err := s.db.ExecContext(ctx, `
			UPDATE run_event_ralph_outbox
			SET state = ?, delivery_owner = NULL, lease_expires_at = NULL,
				updated_at = ?, delivered_at = ?
			WHERE event_id = ? AND state = 'delivering' AND delivery_owner = ?
		`, state, sqliteTimestamp(time.Now().UTC()), deliveredAt, eventID, owner)
		if err != nil {
			return fmt.Errorf("runledger: settle ralph delivery: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("runledger: inspect settled ralph delivery: %w", err)
		}
		if rows != 1 {
			return fmt.Errorf("runledger: ralph delivery ownership changed")
		}
		return nil
	})
}

func (s *SQLiteStore) loadCanonicalRalphEvent(ctx context.Context, eventID string) (Event, error) {
	var (
		event           Event
		timestampRaw    string
		taskID          sql.NullString
		agentID         sql.NullString
		modelID         sql.NullString
		providerID      sql.NullString
		backend         sql.NullString
		snapshotID      sql.NullString
		payloadJSON     sql.NullString
		evidenceIDsJSON sql.NullString
		receiptIDsJSON  sql.NullString
		parentRunID     sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT re.event_id, re.run_id, re.sequence, re.event_type, re.timestamp,
			re.task_id, re.agent_id, re.model_id, re.provider_id, re.backend,
			re.snapshot_id, re.payload_json, re.evidence_ids_json,
			re.receipt_ids_json, re.redaction_version, ar.session_id, ar.parent_run_id
		FROM run_events re JOIN agent_runs ar ON ar.run_id = re.run_id
		WHERE re.event_id = ?
	`, strings.TrimSpace(eventID)).Scan(
		&event.ID, &event.RunID, &event.Sequence, &event.Type, &timestampRaw,
		&taskID, &agentID, &modelID, &providerID, &backend, &snapshotID,
		&payloadJSON, &evidenceIDsJSON, &receiptIDsJSON, &event.Redaction,
		&event.SessionID, &parentRunID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	if err != nil {
		return Event{}, fmt.Errorf("runledger: load ralph event: %w", err)
	}
	event.SchemaVersion = SchemaVersion
	event.Timestamp = parseSQLiteTimestamp(timestampRaw)
	event.ParentRunID = parentRunID.String
	event.TaskID = taskID.String
	event.AgentID = agentID.String
	event.ModelID = modelID.String
	event.ProviderID = providerID.String
	event.Backend = backend.String
	event.SnapshotID = snapshotID.String
	event.Payload, err = unmarshalJSONMap(payloadJSON.String)
	if err != nil {
		return Event{}, fmt.Errorf("runledger: decode ralph event payload: %w", err)
	}
	event.EvidenceIDs, err = unmarshalJSONStrings(evidenceIDsJSON.String)
	if err != nil {
		return Event{}, fmt.Errorf("runledger: decode ralph event evidence: %w", err)
	}
	event.ReceiptIDs, err = unmarshalJSONStrings(receiptIDsJSON.String)
	if err != nil {
		return Event{}, fmt.Errorf("runledger: decode ralph event receipts: %w", err)
	}
	return event, nil
}
