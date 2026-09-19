package storage

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

func TestWithSessionExecWrite_ContextDeadlineBoundsBusyWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessionexec-busy-deadline.db")
	holder, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	contender, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = contender.Close() })
	contender.db.SetMaxOpenConns(1)
	contender.db.SetMaxIdleConns(1)

	conn, err := holder.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	locked := true
	t.Cleanup(func() {
		if locked {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	})

	var invoked atomic.Bool
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = contender.withSessionExecWrite(ctx, func(*sessionExecConn) error {
		invoked.Store(true)
		return nil
	})
	elapsed := time.Since(start)

	if invoked.Load() {
		t.Fatal("write callback ran while another connection held the writer lock")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("busy writer error = %v, want context deadline/cancel", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("busy writer ignored short context for %s; want bounded cancellation", elapsed)
	}
	if _, err := conn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatalf("release writer lock: %v", err)
	}
	locked = false
	var busyTimeout int64
	probeConn, err := contender.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := probeConn.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		_ = probeConn.Close()
		t.Fatalf("read restored busy timeout: %v", err)
	}
	if err := probeConn.Close(); err != nil {
		t.Fatalf("close probe connection: %v", err)
	}
	if busyTimeout != sqliteWALBusyTimeout.Milliseconds() {
		t.Fatalf("busy timeout after cancelled write = %d, want %d", busyTimeout, sqliteWALBusyTimeout.Milliseconds())
	}
	invoked.Store(false)
	if err := contender.withSessionExecWrite(context.Background(), func(db *sessionExecConn) error {
		invoked.Store(true)
		var value int
		return db.queryRow(`SELECT 1`).Scan(&value)
	}); err != nil {
		t.Fatalf("successful write after holder releases: %v", err)
	}
	if !invoked.Load() {
		t.Fatal("write callback did not run after holder released")
	}
}

func TestWithSessionExecWrite_DeadlineRestoresBusyTimeoutAfterCallback(t *testing.T) {
	tests := []struct {
		name    string
		run     func(*sessionExecConn) error
		wantErr error
	}{
		{
			name: "success",
			run: func(db *sessionExecConn) error {
				var value int
				return db.queryRow(`SELECT 1`).Scan(&value)
			},
		},
		{
			name:    "callback error rolls back",
			wantErr: errors.New("callback failed"),
			run: func(*sessionExecConn) error {
				return errors.New("callback failed")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := New(filepath.Join(t.TempDir(), "sessionexec-restore.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			store.db.SetMaxOpenConns(1)
			store.db.SetMaxIdleConns(1)

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err = store.withSessionExecWrite(ctx, test.run)
			cancel()
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("deadline write = %v, want success", err)
				}
			} else if err == nil || err.Error() != test.wantErr.Error() {
				t.Fatalf("deadline write error = %v, want %v", err, test.wantErr)
			}

			var busyTimeout int64
			conn, err := store.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
				_ = conn.Close()
				t.Fatalf("read restored busy timeout: %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Fatalf("close probe connection: %v", err)
			}
			if busyTimeout != sqliteWALBusyTimeout.Milliseconds() {
				t.Fatalf("busy timeout after deadline write = %d, want %d", busyTimeout, sqliteWALBusyTimeout.Milliseconds())
			}
			if err := store.withSessionExecWrite(context.Background(), func(*sessionExecConn) error { return nil }); err != nil {
				t.Fatalf("later write after restored deadline connection: %v", err)
			}
		})
	}
}

func TestWithSessionExecWrite_RestoreFailureAfterCommitDoesNotRetry(t *testing.T) {
	state := &sessionExecWriteDriverState{restoreErr: errors.New("restore failed")}
	store := newSessionExecWriteDriverStore(t, state)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var calls atomic.Int32
	if err := store.withSessionExecWrite(ctx, func(*sessionExecConn) error {
		calls.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("committed write with restore failure = %v, want success", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("callback calls = %d, want 1", got)
	}
	if state.begins.Load() != 1 || state.commits.Load() != 1 || state.rollbacks.Load() != 0 {
		t.Fatalf("transaction counts begin=%d commit=%d rollback=%d, want 1/1/0",
			state.begins.Load(), state.commits.Load(), state.rollbacks.Load())
	}
	if err := store.withSessionExecWrite(context.Background(), func(*sessionExecConn) error { return nil }); err != nil {
		t.Fatalf("later write after discarded restore-failed connection: %v", err)
	}
	if got := state.connects.Load(); got < 2 {
		t.Fatalf("restore-failed connection was reused; connects=%d, want at least 2", got)
	}
}

func TestWithSessionExecWrite_RestoreFailureAfterCallbackErrorDoesNotMaskOrRetry(t *testing.T) {
	state := &sessionExecWriteDriverState{restoreErr: errors.New("restore failed")}
	store := newSessionExecWriteDriverStore(t, state)
	sentinel := errors.New("callback failed")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var calls atomic.Int32
	err := store.withSessionExecWrite(ctx, func(*sessionExecConn) error {
		calls.Add(1)
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("callback error = %v, want sentinel", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("callback calls = %d, want 1", got)
	}
	if state.begins.Load() != 1 || state.commits.Load() != 0 || state.rollbacks.Load() != 1 {
		t.Fatalf("transaction counts begin=%d commit=%d rollback=%d, want 1/0/1",
			state.begins.Load(), state.commits.Load(), state.rollbacks.Load())
	}
	if err := store.withSessionExecWrite(context.Background(), func(*sessionExecConn) error { return nil }); err != nil {
		t.Fatalf("later write after discarded rollback restore-failed connection: %v", err)
	}
	if got := state.connects.Load(); got < 2 {
		t.Fatalf("restore-failed rollback connection was reused; connects=%d, want at least 2", got)
	}
}

func TestWithSessionExecWrite_ConfigureBusyTimeoutErrorDiscardsConnection(t *testing.T) {
	state := &sessionExecWriteDriverState{configureErr: errors.New("configure failed")}
	store := newSessionExecWriteDriverStore(t, state)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var calls atomic.Int32
	err := store.withSessionExecWrite(ctx, func(*sessionExecConn) error {
		calls.Add(1)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "configure session command busy timeout") {
		t.Fatalf("configure error = %v, want contextual configure error", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("callback calls after configure error = %d, want 0", calls.Load())
	}
	state.configureErr = nil
	if err := store.withSessionExecWrite(context.Background(), func(*sessionExecConn) error { return nil }); err != nil {
		t.Fatalf("later write after configure-failed connection discarded: %v", err)
	}
	if got := state.connects.Load(); got < 2 {
		t.Fatalf("configure-failed connection was reused; connects=%d, want at least 2", got)
	}
}

func newSessionExecWriteDriverStore(t *testing.T, state *sessionExecWriteDriverState) *Store {
	t.Helper()
	db := sql.OpenDB(sessionExecWriteTestConnector{state: state})
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return &Store{db: db}
}

type sessionExecWriteDriverState struct {
	configureErr error
	restoreErr   error
	connects     atomic.Int32
	begins       atomic.Int32
	commits      atomic.Int32
	rollbacks    atomic.Int32
}

type sessionExecWriteTestConnector struct {
	state *sessionExecWriteDriverState
}

func (c sessionExecWriteTestConnector) Connect(context.Context) (driver.Conn, error) {
	c.state.connects.Add(1)
	return &sessionExecWriteTestConn{state: c.state}, nil
}

func (c sessionExecWriteTestConnector) Driver() driver.Driver {
	return sessionExecWriteTestDriver{}
}

type sessionExecWriteTestDriver struct{}

func (sessionExecWriteTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type sessionExecWriteTestConn struct {
	state      *sessionExecWriteDriverState
	configured atomic.Bool
}

func (c *sessionExecWriteTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}

func (c *sessionExecWriteTestConn) Close() error {
	return nil
}

func (c *sessionExecWriteTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("legacy begin not supported")
}

func (c *sessionExecWriteTestConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	normalized := strings.ToUpper(strings.TrimSpace(query))
	switch {
	case strings.HasPrefix(normalized, "PRAGMA BUSY_TIMEOUT"):
		if !c.configured.Load() {
			c.configured.Store(true)
			if c.state.configureErr != nil {
				return nil, c.state.configureErr
			}
			return driver.RowsAffected(0), nil
		}
		if c.state.restoreErr != nil {
			return nil, c.state.restoreErr
		}
		return driver.RowsAffected(0), nil
	case normalized == "BEGIN IMMEDIATE":
		c.state.begins.Add(1)
		return driver.RowsAffected(0), nil
	case normalized == "COMMIT":
		c.state.commits.Add(1)
		return driver.RowsAffected(0), nil
	case normalized == "ROLLBACK":
		c.state.rollbacks.Add(1)
		return driver.RowsAffected(0), nil
	default:
		return nil, fmt.Errorf("unexpected exec query %q", query)
	}
}

func (c *sessionExecWriteTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return sessionExecWriteEmptyRows{}, nil
}

type sessionExecWriteEmptyRows struct{}

func (sessionExecWriteEmptyRows) Columns() []string {
	return nil
}

func (sessionExecWriteEmptyRows) Close() error {
	return nil
}

func (sessionExecWriteEmptyRows) Next([]driver.Value) error {
	return io.EOF
}
