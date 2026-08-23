package runledger

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/launchadmission"
	"m31labs.dev/buckley/pkg/storage"
)

const (
	LaunchEnvelopeSchema   = launchadmission.EnvelopeSchema
	MaxLaunchEnvelopeBytes = launchadmission.MaxEnvelopeBytes
)

var (
	ErrLaunchEnvelopeInvalid  = launchadmission.ErrInvalid
	ErrLaunchEnvelopeConflict = launchadmission.ErrConflict
)

type LaunchAdmissionJournal interface {
	launchadmission.Store
	GetLaunchEnvelope(context.Context, string, string) (launchadmission.Record, error)
	GetHistoricalLaunchEnvelope(context.Context, string, string) (launchadmission.Record, error)
}

var _ LaunchAdmissionJournal = (*SQLiteStore)(nil)

// EnsureLaunchAdmission is the sole production writer. The opaque input can
// only be created by launchadmission.Service after all trusted observations.
func (s *SQLiteStore) EnsureLaunchAdmission(ctx context.Context, sealed launchadmission.SealedEnvelope) (launchadmission.Record, bool, error) {
	if s == nil || s.db == nil {
		return launchadmission.Record{}, false, errors.New("runledger: launch admission journal is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := sealed.CanonicalBytes(); err != nil {
		return launchadmission.Record{}, false, err
	}
	var result launchadmission.Record
	created := false
	err := retryMailboxBusy(ctx, func() error {
		var ensureErr error
		result, created, ensureErr = s.ensureLaunchAdmissionOnce(ctx, sealed)
		return ensureErr
	})
	if err != nil {
		return launchadmission.Record{}, false, err
	}
	return result, created, nil
}

func (s *SQLiteStore) ensureLaunchAdmissionOnce(ctx context.Context, sealed launchadmission.SealedEnvelope) (launchadmission.Record, bool, error) {
	snapshot := sealed.Snapshot()
	sealedBytes, err := sealed.CanonicalBytes()
	if err != nil {
		return launchadmission.Record{}, false, err
	}
	return s.ensureLaunchEnvelopeProjectionOnce(ctx, snapshot, sealedBytes)
}

// ensureLaunchEnvelopeProjectionOnce is the package-private persistence seam
// used to exercise the adapter. The public writer accepts only an opaque seal.
func (s *SQLiteStore) ensureLaunchEnvelopeProjectionOnce(ctx context.Context, snapshot launchadmission.Envelope, sealedBytes []byte) (record launchadmission.Record, created bool, err error) {
	if err := validateLaunchIdentity(snapshot.SessionID, snapshot.RunID); err != nil {
		return launchadmission.Record{}, false, err
	}
	conn, err := acquireLaunchForeignKeyConn(ctx, s.db)
	if err != nil {
		return launchadmission.Record{}, false, err
	}
	began := false
	defer func() {
		cleanupErr := closeLaunchForeignKeyConn(conn, began)
		if cleanupErr != nil {
			record = launchadmission.Record{}
			created = false
			err = errors.Join(err, cleanupErr)
		}
	}()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return launchadmission.Record{}, false, errors.Join(fmt.Errorf("runledger: begin launch admission: %w", err), conn.discard())
	}
	began = true

	var contractSession string
	if err := conn.QueryRowContext(ctx, `
		SELECT contract.session_id
		FROM agent_run_contracts AS contract
		JOIN agent_runs AS run
			ON run.run_id = contract.run_id AND run.session_id = contract.session_id
		WHERE contract.run_id = ?
	`, snapshot.RunID).Scan(&contractSession); errors.Is(err, sql.ErrNoRows) {
		return launchadmission.Record{}, false, ErrNotFound
	} else if err != nil {
		return launchadmission.Record{}, false, fmt.Errorf("runledger: read launch run contract: %w", err)
	} else if contractSession != snapshot.SessionID {
		return launchadmission.Record{}, false, fmt.Errorf("%w: session ownership changed", ErrLaunchEnvelopeConflict)
	}
	now, err := launchSQLiteTime(ctx, conn)
	if err != nil {
		return launchadmission.Record{}, false, err
	}

	existing, err := readLaunchEnvelope(ctx, conn, snapshot.SessionID, snapshot.RunID)
	if err == nil {
		if err := existing.ValidateAt(now); err != nil {
			return launchadmission.Record{}, false, err
		}
		existingBytes, existingErr := json.Marshal(existing.Snapshot())
		if existingErr != nil || existing.Digest() != snapshot.EnvelopeDigest || !bytes.Equal(sealedBytes, existingBytes) {
			return launchadmission.Record{}, false, fmt.Errorf("%w: run %s", ErrLaunchEnvelopeConflict, snapshot.RunID)
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return launchadmission.Record{}, false, fmt.Errorf("runledger: commit launch admission replay: %w", err)
		}
		began = false
		return existing, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return launchadmission.Record{}, false, err
	}

	record, err = launchRecordAt(snapshot, now)
	if err != nil {
		return launchadmission.Record{}, false, err
	}
	encoded, err := record.CanonicalBytes()
	if err != nil {
		return launchadmission.Record{}, false, err
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO launch_envelopes (
			run_id, session_id, schema_version, profile_id, profile_version,
			profile_digest, envelope_digest, envelope_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, snapshot.RunID, snapshot.SessionID, snapshot.Schema, snapshot.Profile.ID, snapshot.Profile.Schema,
		snapshot.ProfileDigest, snapshot.EnvelopeDigest, string(encoded), sqliteTimestamp(record.CreatedAt())); err != nil {
		return launchadmission.Record{}, false, fmt.Errorf("runledger: insert launch admission: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return launchadmission.Record{}, false, fmt.Errorf("runledger: commit launch admission: %w", err)
	}
	began = false
	return record, true, nil
}

func launchRecordAt(snapshot launchadmission.Envelope, createdAt time.Time) (launchadmission.Record, error) {
	encoded, err := json.Marshal(struct {
		launchadmission.Envelope
		CreatedAt time.Time `json:"created_at"`
	}{Envelope: snapshot, CreatedAt: createdAt})
	if err != nil || len(encoded) > MaxLaunchEnvelopeBytes {
		return launchadmission.Record{}, fmt.Errorf("%w: encoded launch record", ErrLaunchEnvelopeInvalid)
	}
	return launchadmission.DecodeRecord(encoded)
}

func (s *SQLiteStore) GetLaunchEnvelope(ctx context.Context, sessionID, runID string) (launchadmission.Record, error) {
	if s == nil || s.db == nil {
		return launchadmission.Record{}, errors.New("runledger: launch admission journal is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateLaunchIdentity(sessionID, runID); err != nil {
		return launchadmission.Record{}, err
	}
	var result launchadmission.Record
	err := retryMailboxBusy(ctx, func() error {
		var readErr error
		result, readErr = s.getActiveLaunchEnvelopeOnce(ctx, sessionID, runID)
		return readErr
	})
	return result, err
}

func (s *SQLiteStore) getActiveLaunchEnvelopeOnce(ctx context.Context, sessionID, runID string) (record launchadmission.Record, err error) {
	conn, err := acquireLaunchForeignKeyConn(ctx, s.db)
	if err != nil {
		return launchadmission.Record{}, err
	}
	began := false
	defer func() {
		if closeErr := closeLaunchForeignKeyConn(conn, began); closeErr != nil {
			record = launchadmission.Record{}
			err = errors.Join(err, closeErr)
		}
	}()
	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		return launchadmission.Record{}, errors.Join(fmt.Errorf("runledger: begin active launch admission read: %w", err), conn.discard())
	}
	began = true
	now, err := launchSQLiteTime(ctx, conn)
	if err != nil {
		return launchadmission.Record{}, err
	}
	record, err = readLaunchEnvelope(ctx, conn, sessionID, runID)
	if err != nil {
		return launchadmission.Record{}, err
	}
	if err := record.ValidateAt(now); err != nil {
		return launchadmission.Record{}, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return launchadmission.Record{}, fmt.Errorf("runledger: commit active launch admission read: %w", err)
	}
	began = false
	return record, nil
}

// GetHistoricalLaunchEnvelope reads immutable evidence for audit without
// reactivating expired price evidence.
func (s *SQLiteStore) GetHistoricalLaunchEnvelope(ctx context.Context, sessionID, runID string) (record launchadmission.Record, err error) {
	if s == nil || s.db == nil {
		return launchadmission.Record{}, errors.New("runledger: launch admission journal is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateLaunchIdentity(sessionID, runID); err != nil {
		return launchadmission.Record{}, err
	}
	conn, err := acquireLaunchForeignKeyConn(ctx, s.db)
	if err != nil {
		return launchadmission.Record{}, err
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			record = launchadmission.Record{}
			err = errors.Join(err, closeErr)
		}
	}()
	return readLaunchEnvelope(ctx, conn, sessionID, runID)
}

type launchEnvelopeQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func launchSQLiteTime(ctx context.Context, queryer launchEnvelopeQueryer) (time.Time, error) {
	var raw string
	if err := queryer.QueryRowContext(ctx, `SELECT strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`).Scan(&raw); err != nil {
		return time.Time{}, fmt.Errorf("runledger: read launch sqlite time: %w", err)
	}
	return parseSQLiteTimestamp(raw), nil
}

func readLaunchEnvelope(ctx context.Context, queryer launchEnvelopeQueryer, sessionID, runID string) (launchadmission.Record, error) {
	var schema, profileID, profileVersion, profileDigest, envelopeDigest, encoded, createdRaw string
	err := queryer.QueryRowContext(ctx, `
		SELECT envelope.schema_version, envelope.profile_id, envelope.profile_version,
			envelope.profile_digest, envelope.envelope_digest, envelope.envelope_json,
			envelope.created_at
		FROM launch_envelopes AS envelope
		JOIN agent_run_contracts AS contract
			ON contract.run_id = envelope.run_id AND contract.session_id = envelope.session_id
		JOIN agent_runs AS run
			ON run.run_id = contract.run_id AND run.session_id = contract.session_id
		WHERE envelope.session_id = ? AND envelope.run_id = ?
	`, sessionID, runID).Scan(&schema, &profileID, &profileVersion, &profileDigest, &envelopeDigest, &encoded, &createdRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return launchadmission.Record{}, ErrNotFound
	}
	if err != nil {
		return launchadmission.Record{}, fmt.Errorf("runledger: read launch admission: %w", err)
	}
	record, err := launchadmission.DecodeRecord([]byte(encoded))
	if err != nil {
		return launchadmission.Record{}, err
	}
	snapshot := record.Snapshot()
	createdAt := parseSQLiteTimestamp(createdRaw)
	if schema != snapshot.Schema || profileID != snapshot.Profile.ID || profileVersion != snapshot.Profile.Schema || profileDigest != snapshot.ProfileDigest || envelopeDigest != snapshot.EnvelopeDigest || !createdAt.Equal(record.CreatedAt()) || snapshot.SessionID != sessionID || snapshot.RunID != runID {
		return launchadmission.Record{}, fmt.Errorf("%w: stored projection mismatch", ErrLaunchEnvelopeInvalid)
	}
	return record, nil
}

func validateLaunchIdentity(sessionID, runID string) error {
	if err := agentcoord.ValidateMonitorIdentifier("session_id", sessionID, true); err != nil {
		return fmt.Errorf("%w: session_id is invalid", ErrLaunchEnvelopeInvalid)
	}
	if err := agentcoord.ValidateMonitorIdentifier("run_id", runID, true); err != nil || reservedAgentIdentity(runID) {
		return fmt.Errorf("%w: run_id is invalid", ErrLaunchEnvelopeInvalid)
	}
	return nil
}

func createLaunchEnvelopesTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_run_contracts_identity
			ON agent_run_contracts(run_id, session_id);
		CREATE TABLE IF NOT EXISTS launch_envelopes (
			run_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			schema_version TEXT NOT NULL,
			profile_id TEXT NOT NULL,
			profile_version TEXT NOT NULL,
			profile_digest TEXT NOT NULL,
			envelope_digest TEXT NOT NULL,
			envelope_json TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			CHECK (schema_version = 'buckley.launch.envelope.v1'),
			CHECK (length(profile_id) BETWEEN 1 AND 32),
			CHECK (profile_version = 'buckley.launch.profile.v1'),
			CHECK (length(profile_digest) = 64),
			CHECK (length(envelope_digest) = 64),
			CHECK (length(envelope_json) BETWEEN 2 AND 65536),
			CHECK (json_valid(envelope_json)),
			UNIQUE (run_id, session_id),
			FOREIGN KEY(run_id, session_id)
				REFERENCES agent_run_contracts(run_id, session_id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_launch_envelopes_session
			ON launch_envelopes(session_id, run_id);
		CREATE TRIGGER IF NOT EXISTS trg_launch_envelopes_immutable
		BEFORE UPDATE ON launch_envelopes
		BEGIN
			SELECT RAISE(ABORT, 'launch envelope is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_envelopes_delete_immutable
		BEFORE DELETE ON launch_envelopes
		WHEN EXISTS (
			SELECT 1 FROM agent_run_contracts
			WHERE run_id = OLD.run_id AND session_id = OLD.session_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch envelope is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_contract_delete_immutable
		BEFORE DELETE ON agent_run_contracts
		WHEN EXISTS (
			SELECT 1 FROM launch_envelopes
			WHERE run_id = OLD.run_id AND session_id = OLD.session_id
		) AND EXISTS (
			SELECT 1 FROM agent_runs
			WHERE run_id = OLD.run_id AND session_id = OLD.session_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch run contract is immutable');
		END;
	`)
	if err != nil {
		return fmt.Errorf("create launch envelopes: %w", err)
	}
	return nil
}
