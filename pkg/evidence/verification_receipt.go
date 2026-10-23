package evidence

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/storage"
)

const verificationReceiptPin = "verification-receipt"

// PinVerificationReceipt atomically retains a verification receipt and its log.
// These automatic pins expire with the receipt; user and run pins are independent.
func (s *SQLiteStore) PinVerificationReceipt(ctx context.Context, receiptID, logID string) error {
	return storage.WithSQLiteWriteTransaction(ctx, s.db, func(conn *sql.Conn) error {
		for _, pin := range []struct{ id, reason string }{{receiptID, verificationReceiptPin}, {logID, verificationReceiptPin + ":" + receiptID}} {
			if _, err := conn.ExecContext(ctx, `INSERT INTO evidence_pins(evidence_id,reason,created_at) VALUES(?,?,?) ON CONFLICT(evidence_id,reason) DO NOTHING`, pin.id, pin.reason, sqliteTimestamp(time.Now().UTC())); err != nil {
				return fmt.Errorf("evidence: pin verification receipt: %w", err)
			}
		}
		return nil
	})
}

func (s *SQLiteStore) expireVerificationReceipts(ctx context.Context, cutoff time.Time, guards string) ([]string, error) {
	var ids []string
	err := storage.WithSQLiteWriteTransaction(ctx, s.db, func(conn *sql.Conn) error {
		ids = nil
		rows, err := conn.QueryContext(ctx, `SELECT evidence_id FROM evidence_objects WHERE kind=? AND created_at<?
   AND NOT EXISTS(SELECT 1 FROM evidence_pins WHERE evidence_id=evidence_objects.evidence_id AND reason<>?)`+guards+` ORDER BY created_at,evidence_id LIMIT 256`, string(KindVerificationReceipt), sqliteTimestamp(cutoff), verificationReceiptPin)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		placeholders := make([]string, len(ids))
		args := make([]any, len(ids))
		reasons := make([]any, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
			reasons[i] = verificationReceiptPin + ":" + id
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM evidence_objects WHERE evidence_id IN (`+strings.Join(placeholders, ",")+`)`, args...); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `DELETE FROM evidence_pins WHERE reason IN (`+strings.Join(placeholders, ",")+`)`, reasons...)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("evidence: expire verification receipts: %w", err)
	}
	return ids, nil
}
