package runledger

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewWithDB_VerifiesThenRestoresForeignKeysOnExistingConnection(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "existing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if got := foreignKeysEnabled(t, conn); got != 0 {
		conn.Close()
		t.Fatalf("foreign_keys before NewWithDB = %d, want 0", got)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := NewWithDB(db); err != nil {
		t.Fatal(err)
	}
	conn, err = db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := foreignKeysEnabled(t, conn); got != 0 {
		t.Fatalf("foreign_keys after NewWithDB = %d, want restored 0", got)
	}
}

func TestLaunchForeignKeys_AreScopedAwayFromHeldPoolConnections(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "pool.db") + "?_pragma=foreign_keys(0)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)

	ctx := context.Background()
	held := make([]*sql.Conn, 0, 2)
	for i := 0; i < 2; i++ {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
		if got := foreignKeysEnabled(t, conn); got != 0 {
			t.Fatalf("unrelated held connection %d foreign_keys = %d, want 0", i, got)
		}
	}

	if _, err := NewWithDB(db); err != nil {
		for _, conn := range held {
			conn.Close()
		}
		t.Fatal(err)
	}
	launchConn, err := acquireLaunchForeignKeyConn(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer launchConn.Close()
	if got := foreignKeysEnabled(t, launchConn.Conn); got != 1 {
		t.Fatalf("launch connection foreign_keys = %d, want 1", got)
	}
	assertOrphanLaunchEnvelopeRejected(t, launchConn.Conn, "held")
	for i, conn := range held {
		if got := foreignKeysEnabled(t, conn); got != 0 {
			t.Fatalf("unrelated held connection %d changed foreign_keys = %d, want 0", i, got)
		}
	}
	for _, conn := range held {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}

	var count int
	if err := launchConn.QueryRowContext(ctx, `SELECT COUNT(*) FROM launch_envelopes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("orphan launch envelope count = %d, want 0", count)
	}
	if err := launchConn.Close(); err != nil {
		t.Fatal(err)
	}
	assertPoolForeignKeys(t, db, 0)
}

func TestLaunchForeignKeys_EnableEveryReplacementConnection(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "replacement.db") + "?_pragma=foreign_keys(0)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	if _, err := NewWithDB(db); err != nil {
		t.Fatal(err)
	}

	// Returning the current connection with no idle allowance closes it. Each
	// launch-boundary acquisition therefore verifies a newly created
	// replacement without changing unrelated SQLite pools.
	db.SetMaxIdleConns(0)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		conn, err := acquireLaunchForeignKeyConn(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		if got := foreignKeysEnabled(t, conn.Conn); got != 1 {
			conn.Close()
			t.Fatalf("replacement connection %d foreign_keys = %d, want 1", i, got)
		}
		assertOrphanLaunchEnvelopeRejected(t, conn.Conn, fmt.Sprintf("replacement-%d", i))
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLaunchAdmissionOperations_EnableTheirExactPoolConnection(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "operations.db")+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store, err := NewWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	ensureLaunchTestRun(t, store, "session-fk-operations", "run-fk-operations")
	envelope := launchTestEnvelope(t, "session-fk-operations", "run-fk-operations", "gsxmail", launchTestPrice(t, time.Now().UTC().Round(0).Add(-time.Second), time.Minute), "")

	disablePoolForeignKeys(t, db)
	if _, _, err := ensureLaunchTestEnvelope(context.Background(), store, envelope); err != nil {
		t.Fatalf("ensure launch envelope: %v", err)
	}
	assertPoolForeignKeys(t, db, 0)

	disablePoolForeignKeys(t, db)
	if _, err := store.GetLaunchEnvelope(context.Background(), envelope.SessionID, envelope.RunID); err != nil {
		t.Fatalf("get active launch envelope: %v", err)
	}
	assertPoolForeignKeys(t, db, 0)

	disablePoolForeignKeys(t, db)
	if _, err := store.GetHistoricalLaunchEnvelope(context.Background(), envelope.SessionID, envelope.RunID); err != nil {
		t.Fatalf("get historical launch envelope: %v", err)
	}
	assertPoolForeignKeys(t, db, 0)
}

func TestLaunchAdmissionStore_RejectsPreexistingOrphanRunContract(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "orphan-contract.db")+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store, err := NewWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO agent_run_contracts (
			run_id, session_id, input_digest, task_evidence_id, created_at
		) VALUES ('run-orphan-contract', 'session-orphan-contract', 'input-digest',
			'task-evidence', '2026-08-21T00:00:00Z')
	`); err != nil {
		t.Fatal(err)
	}
	envelope := launchTestEnvelope(t, "session-orphan-contract", "run-orphan-contract", "gsxmail", launchTestPrice(t, time.Now().UTC().Round(0).Add(-time.Second), time.Minute), "")
	if _, _, err := ensureLaunchTestEnvelope(context.Background(), store, envelope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("orphan contract admission error = %v, want not found", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM launch_envelopes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("launch envelopes after orphan contract = %d, want 0", count)
	}
}

func TestLaunchForeignKeys_PreserveOriginallyEnabledConnection(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "enabled.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	lease, err := acquireLaunchForeignKeyConn(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if got := foreignKeysEnabled(t, lease.Conn); got != 1 {
		t.Fatalf("foreign_keys during launch operation = %d, want 1", got)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	assertPoolForeignKeys(t, db, 1)
}

func TestLaunchForeignKeys_RestoreFailureDiscardsPhysicalConnection(t *testing.T) {
	stub := &foreignKeyRestoreFailureDriver{}
	driverName := fmt.Sprintf("runledger-fk-restore-%d", foreignKeyTestDriverID.Add(1))
	sql.Register(driverName, stub)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	lease, err := acquireLaunchForeignKeyConn(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if got := foreignKeysEnabled(t, lease.Conn); got != 1 {
		t.Fatalf("foreign_keys during launch operation = %d, want 1", got)
	}
	if err := lease.Close(); err == nil || !strings.Contains(err.Error(), "restore SQLite foreign-key setting") {
		t.Fatalf("restore error = %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := stub.opens.Load(); got != 2 {
		t.Fatalf("physical connection opens = %d, want 2 after discard", got)
	}
	if got := foreignKeysEnabled(t, conn); got != 0 {
		t.Fatalf("replacement foreign_keys = %d, want original 0", got)
	}
}

func TestLaunchForeignKeys_RollbackFailureDiscardsPhysicalConnection(t *testing.T) {
	stub := &foreignKeyRestoreFailureDriver{}
	driverName := fmt.Sprintf("runledger-fk-rollback-%d", foreignKeyTestDriverID.Add(1))
	sql.Register(driverName, stub)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	lease, err := acquireLaunchForeignKeyConn(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := closeLaunchForeignKeyConn(lease, true); err == nil || !strings.Contains(err.Error(), "rollback launch admission transaction") {
		t.Fatalf("rollback cleanup error = %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := stub.opens.Load(); got != 2 {
		t.Fatalf("physical connection opens = %d, want 2 after rollback discard", got)
	}
}

func TestLaunchAdmissionRead_RestoreFailureReturnsZeroResult(t *testing.T) {
	observed := time.Now().UTC().Round(0).Add(-time.Second)
	envelope := launchTestEnvelope(t, "session-restore-failure", "run-restore-failure", "gsxmail", launchTestPrice(t, observed, time.Minute), "")
	record, err := launchRecordAt(envelope, observed.Add(500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := record.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	stub := &foreignKeyRestoreFailureDriver{launchRow: []driver.Value{
		envelope.Schema, envelope.Profile.ID, envelope.Profile.Schema,
		envelope.ProfileDigest, envelope.EnvelopeDigest, string(encoded),
		sqliteTimestamp(record.CreatedAt()),
	}}
	driverName := fmt.Sprintf("runledger-fk-result-%d", foreignKeyTestDriverID.Add(1))
	sql.Register(driverName, stub)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := newSQLiteStore(db)
	got, err := store.GetHistoricalLaunchEnvelope(context.Background(), envelope.SessionID, envelope.RunID)
	if err == nil || !strings.Contains(err.Error(), "restore SQLite foreign-key setting") {
		t.Fatalf("historical cleanup error = %v", err)
	}
	if !got.CreatedAt().IsZero() || got.Digest() != "" {
		t.Fatalf("historical result survived cleanup failure: created=%v digest=%q", got.CreatedAt(), got.Digest())
	}
}

func TestLaunchAdmissionBeginFailureDiscardsPossiblyOpenTransaction(t *testing.T) {
	observed := time.Now().UTC().Round(0).Add(-time.Second)
	envelope := launchTestEnvelope(t, "session-begin-failure", "run-begin-failure", "gsxmail", launchTestPrice(t, observed, time.Minute), "")
	for _, test := range []struct {
		name string
		run  func(*SQLiteStore) error
	}{
		{name: "ensure", run: func(store *SQLiteStore) error {
			record, created, err := store.ensureLaunchEnvelopeProjectionOnce(context.Background(), envelope, nil)
			if !record.CreatedAt().IsZero() || created {
				t.Fatalf("ensure result survived begin failure: created_at=%v created=%v", record.CreatedAt(), created)
			}
			return err
		}},
		{name: "active read", run: func(store *SQLiteStore) error {
			record, err := store.getActiveLaunchEnvelopeOnce(context.Background(), envelope.SessionID, envelope.RunID)
			if !record.CreatedAt().IsZero() {
				t.Fatalf("active result survived begin failure: created_at=%v", record.CreatedAt())
			}
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &foreignKeyBeginFailureDriver{}
			driverName := fmt.Sprintf("runledger-fk-begin-%d", foreignKeyTestDriverID.Add(1))
			sql.Register(driverName, stub)
			db, err := sql.Open(driverName, "")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
			if err := test.run(newSQLiteStore(db)); !errors.Is(err, context.Canceled) {
				t.Fatalf("begin error = %v, want injected cancellation", err)
			}
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if got := stub.opens.Load(); got != 2 {
				t.Fatalf("physical connection opens = %d, want 2 after begin discard", got)
			}
		})
	}
}

func TestNewWithDB_ForeignKeyVerificationFailurePreventsMigrations(t *testing.T) {
	stub := &foreignKeyDisabledDriver{}
	driverName := fmt.Sprintf("runledger-fk-disabled-%d", foreignKeyTestDriverID.Add(1))
	sql.Register(driverName, stub)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := NewWithDB(db); err == nil || !strings.Contains(err.Error(), "foreign-key enforcement is unavailable") {
		t.Fatalf("NewWithDB error = %v, want foreign-key verification failure", err)
	}
	if got := stub.nonPragmaExecs.Load(); got != 0 {
		t.Fatalf("migration/write statements after verification failure = %d, want 0", got)
	}
	store := newSQLiteStore(db)
	if _, err := store.GetHistoricalLaunchEnvelope(context.Background(), "session-fk-disabled", "run-fk-disabled"); err == nil || !strings.Contains(err.Error(), "foreign-key enforcement is unavailable") {
		t.Fatalf("historical launch read error = %v, want foreign-key verification failure", err)
	}
	if _, err := store.GetLaunchEnvelope(context.Background(), "session-fk-disabled", "run-fk-disabled"); err == nil || !strings.Contains(err.Error(), "foreign-key enforcement is unavailable") {
		t.Fatalf("active launch read error = %v, want foreign-key verification failure", err)
	}
	if got := stub.nonPragmaExecs.Load(); got != 0 {
		t.Fatalf("v21 write statements after verification failure = %d, want 0", got)
	}
	if got := stub.nonPragmaQueries.Load(); got != 0 {
		t.Fatalf("v21 read statements after verification failure = %d, want 0", got)
	}
}

func foreignKeysEnabled(t *testing.T, conn *sql.Conn) int {
	t.Helper()
	var enabled int
	if err := conn.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	return enabled
}

func disablePoolForeignKeys(t *testing.T, db *sql.DB) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if got := foreignKeysEnabled(t, conn); got != 0 {
		conn.Close()
		t.Fatalf("foreign_keys after explicit disable = %d, want 0", got)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertPoolForeignKeys(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := foreignKeysEnabled(t, conn); got != want {
		t.Fatalf("pooled connection foreign_keys = %d, want %d", got, want)
	}
}

func assertOrphanLaunchEnvelopeRejected(t *testing.T, conn *sql.Conn, suffix string) {
	t.Helper()
	_, err := conn.ExecContext(context.Background(), `
		INSERT INTO launch_envelopes (
			run_id, session_id, schema_version, profile_id, profile_version,
			profile_digest, envelope_digest, envelope_json, created_at
		) VALUES (?, ?, 'buckley.launch.envelope.v1', 'gsxmail',
			'buckley.launch.profile.v1', ?, ?, '{}', '2026-08-21T00:00:00Z')
	`, "missing-run-"+suffix, "missing-session-"+suffix, strings.Repeat("a", 64), strings.Repeat("b", 64))
	if err == nil {
		t.Fatal("orphan launch envelope insert succeeded")
	}
}

var foreignKeyTestDriverID atomic.Uint64

type foreignKeyDisabledDriver struct {
	nonPragmaExecs   atomic.Int64
	nonPragmaQueries atomic.Int64
}

type foreignKeyRestoreFailureDriver struct {
	opens     atomic.Int64
	launchRow []driver.Value
}

type foreignKeyBeginFailureDriver struct {
	opens atomic.Int64
}

func (d *foreignKeyBeginFailureDriver) Open(string) (driver.Conn, error) {
	d.opens.Add(1)
	return &foreignKeyBeginFailureConn{}, nil
}

type foreignKeyBeginFailureConn struct{}

func (*foreignKeyBeginFailureConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (*foreignKeyBeginFailureConn) Close() error { return nil }

func (*foreignKeyBeginFailureConn) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

func (*foreignKeyBeginFailureConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	switch strings.ToUpper(strings.TrimSpace(query)) {
	case "PRAGMA FOREIGN_KEYS = ON":
		return driver.RowsAffected(0), nil
	case "BEGIN", "BEGIN IMMEDIATE":
		return nil, context.Canceled
	default:
		return nil, fmt.Errorf("unexpected exec")
	}
}

func (*foreignKeyBeginFailureConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.EqualFold(strings.TrimSpace(query), "PRAGMA foreign_keys") {
		return &singleIntegerRow{value: 1}, nil
	}
	return nil, fmt.Errorf("unexpected query")
}

func (d *foreignKeyRestoreFailureDriver) Open(string) (driver.Conn, error) {
	d.opens.Add(1)
	return &foreignKeyRestoreFailureConn{driver: d}, nil
}

type foreignKeyRestoreFailureConn struct {
	driver      *foreignKeyRestoreFailureDriver
	foreignKeys int
}

func (*foreignKeyRestoreFailureConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (*foreignKeyRestoreFailureConn) Close() error { return nil }

func (*foreignKeyRestoreFailureConn) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

func (c *foreignKeyRestoreFailureConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	switch strings.ToUpper(strings.TrimSpace(query)) {
	case "PRAGMA FOREIGN_KEYS = ON":
		c.foreignKeys = 1
		return driver.RowsAffected(0), nil
	case "PRAGMA FOREIGN_KEYS = OFF":
		return nil, errors.New("injected restore failure")
	case "ROLLBACK":
		return nil, errors.New("injected rollback failure")
	default:
		return nil, fmt.Errorf("unexpected exec")
	}
}

func (c *foreignKeyRestoreFailureConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.EqualFold(strings.TrimSpace(query), "PRAGMA foreign_keys") {
		return &singleIntegerRow{value: int64(c.foreignKeys)}, nil
	}
	if strings.Contains(query, "FROM launch_envelopes AS envelope") && c.driver != nil && c.driver.launchRow != nil {
		return &singleValueRow{values: append([]driver.Value(nil), c.driver.launchRow...)}, nil
	}
	return nil, fmt.Errorf("unexpected query")
}

type singleValueRow struct {
	values []driver.Value
	done   bool
}

func (r *singleValueRow) Columns() []string {
	columns := make([]string, len(r.values))
	for i := range columns {
		columns[i] = fmt.Sprintf("column_%d", i)
	}
	return columns
}

func (*singleValueRow) Close() error { return nil }

func (r *singleValueRow) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(dest, r.values)
	return nil
}

func (d *foreignKeyDisabledDriver) Open(string) (driver.Conn, error) {
	return &foreignKeyDisabledConn{driver: d}, nil
}

type foreignKeyDisabledConn struct {
	driver *foreignKeyDisabledDriver
}

func (*foreignKeyDisabledConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (*foreignKeyDisabledConn) Close() error { return nil }

func (*foreignKeyDisabledConn) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

func (c *foreignKeyDisabledConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	trimmed := strings.TrimSpace(query)
	if strings.EqualFold(trimmed, "PRAGMA foreign_keys = ON") || strings.EqualFold(trimmed, "PRAGMA foreign_keys = OFF") {
		return driver.RowsAffected(0), nil
	}
	c.driver.nonPragmaExecs.Add(1)
	return driver.RowsAffected(0), nil
}

func (c *foreignKeyDisabledConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.EqualFold(strings.TrimSpace(query), "PRAGMA foreign_keys") {
		return &singleIntegerRow{value: 0}, nil
	}
	c.driver.nonPragmaQueries.Add(1)
	return nil, fmt.Errorf("unexpected query")
}

type singleIntegerRow struct {
	value int64
	done  bool
}

func (*singleIntegerRow) Columns() []string { return []string{"foreign_keys"} }

func (*singleIntegerRow) Close() error { return nil }

func (r *singleIntegerRow) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.value
	return nil
}
