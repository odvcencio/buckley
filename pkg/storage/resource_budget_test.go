package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func newBudgetDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "budget.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(1000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(3)
	t.Cleanup(func() { _ = db.Close() })
	if err := EnableSQLiteWAL(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE entries(id INTEGER PRIMARY KEY, content BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return db, path
}

func TestConfigureStorageBudget_AtomicAcrossConnectionsAndReplace(t *testing.T) {
	db, _ := newBudgetDB(t)
	ctx := context.Background()
	if err := ConfigureStorageBudget(ctx, db, 16<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO entries VALUES(1, 'first')`); err != nil {
		t.Fatal(err)
	}
	before, err := ReadStorageBudget(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO entries VALUES(1, 'other')`); err != nil {
		t.Fatal(err)
	}
	after, _ := ReadStorageBudget(ctx, db)
	if before != after {
		t.Fatalf("replace leaked accounting: before=%+v after=%+v", before, after)
	}
	if _, err := db.Exec(`UPDATE storage_budget SET limit_bytes = used_bytes + 1200`); err != nil {
		t.Fatal(err)
	}
	if err := truncateSQLiteWAL(ctx, db); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO entries VALUES(2, 'fits')`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`INSERT INTO entries VALUES(3, 'must fail')`)
	if !IsStorageBudgetError(err) {
		t.Fatalf("quota error = %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadStorageBudget(ctx, db)
	if got.UsedBytes != before.UsedBytes {
		t.Fatalf("rollback leaked bytes: %+v", got)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rows=%d err=%v", count, err)
	}
	if _, err := db.Exec(`DELETE FROM entries`); err != nil {
		t.Fatal(err)
	}
	got, _ = ReadStorageBudget(ctx, db)
	if got.UsedBytes != 0 {
		t.Fatalf("deleted bytes = %+v", got)
	}
}

func TestConfigureStorageBudget_NativeAllocationIncludesUnaccountedIndexes(t *testing.T) {
	db, path := newBudgetDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`CREATE TABLE payload_fts_shadow(body BLOB)`); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureStorageBudget(ctx, db, 16<<20); err != nil {
		t.Fatal(err)
	}
	var failure error
	for range 100 {
		_, failure = db.Exec(`INSERT INTO payload_fts_shadow VALUES(zeroblob(131072))`)
		if failure != nil {
			break
		}
	}
	if !IsStorageBudgetError(failure) {
		t.Fatalf("native allocation ceiling error = %v", failure)
	}
	var pages, size int64
	if err := db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&size); err != nil {
		t.Fatal(err)
	}
	if pages*size > 4<<20 {
		t.Fatalf("database allocated %d bytes", pages*size)
	}
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var capPages int64
	if err := other.QueryRow(`PRAGMA max_page_count`).Scan(&capPages); err != nil || capPages*size != 4<<20 {
		t.Fatalf("reopen cap=%d err=%v", capPages, err)
	}
	if err := ConfigureStorageBudget(ctx, db, 32<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO payload_fts_shadow VALUES(zeroblob(131072))`); err != nil {
		t.Fatalf("raised quota did not recover: %v", err)
	}
}

func TestConfigureStorageBudget_HeldReaderBoundsWALAndRecovers(t *testing.T) {
	db, path := newBudgetDB(t)
	ctx := context.Background()
	if err := ConfigureStorageBudget(ctx, db, 16<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO entries VALUES(1, ?)`, strings.Repeat("a", 64<<10)); err != nil {
		t.Fatal(err)
	}
	if err := truncateSQLiteWAL(ctx, db); err != nil {
		t.Fatal(err)
	}
	reader, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var original string
	if err := reader.QueryRow(`SELECT content FROM entries WHERE id=1`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	var failure error
	for i := range 1000 {
		_, failure = db.Exec(`UPDATE entries SET content = ? WHERE id = 1`, fmt.Sprintf("%04d", i)+strings.Repeat("b", 64<<10-4))
		if failure != nil {
			break
		}
	}
	if !IsStorageBudgetError(failure) {
		t.Fatalf("WAL ceiling error = %v", failure)
	}
	wal, err := sqliteWALBytes(path)
	if err != nil {
		t.Fatal(err)
	}
	if wal > 3<<20 {
		t.Fatalf("WAL grew to %d bytes", wal)
	}
	if err := RecoverStorageBudgetWAL(ctx, db); !errors.Is(err, ErrStorageBudget) {
		t.Fatalf("held-reader recovery = %v", err)
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := RecoverStorageBudgetWAL(ctx, db); err != nil {
		t.Fatalf("released-reader recovery = %v", err)
	}
	if _, err := db.Exec(`UPDATE entries SET content = 'resumed' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureStorageBudget_OverBudgetHistoryReadableAndDisable(t *testing.T) {
	db, _ := newBudgetDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO entries VALUES(1, zeroblob(200000))`); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureStorageBudget(ctx, db, 100000); err != nil {
		t.Fatal(err)
	}
	if err := truncateSQLiteWAL(ctx, db); err != nil {
		t.Fatal(err)
	}
	var bytes int
	if err := db.QueryRow(`SELECT length(content) FROM entries`).Scan(&bytes); err != nil || bytes != 200000 {
		t.Fatalf("existing history lost: bytes=%d err=%v", bytes, err)
	}
	if _, err := db.Exec(`INSERT INTO entries VALUES(2, 'rejected')`); !IsStorageBudgetError(err) {
		t.Fatalf("above-quota insert = %v", err)
	}
	if _, err := db.Exec(`UPDATE entries SET content = 'small' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureStorageBudget(ctx, db, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO entries VALUES(2, zeroblob(300000))`); err != nil {
		t.Fatal(err)
	}
	budget, err := ReadStorageBudget(ctx, db)
	if err != nil || budget != (StorageBudget{}) {
		t.Fatalf("disabled = %+v err=%v", budget, err)
	}
}

func TestConfigureStorageBudget_UnchangedPolicyKeepsAccountingAndSchema(t *testing.T) {
	db, _ := newBudgetDB(t)
	ctx := context.Background()
	if err := ConfigureStorageBudget(ctx, db, 16<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO entries VALUES(1, 'retained history')`); err != nil {
		t.Fatal(err)
	}
	before, err := ReadStorageBudget(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	var versionBefore, versionAfter int
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&versionBefore); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureStorageBudget(ctx, db, 16<<20); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&versionAfter); err != nil {
		t.Fatal(err)
	}
	after, err := ReadStorageBudget(ctx, db)
	if err != nil || after != before || versionBefore != versionAfter {
		t.Fatalf("unchanged policy rewrote accounting: before=%+v after=%+v schema=%d/%d err=%v", before, after, versionBefore, versionAfter, err)
	}
}

func TestRefreshStorageBudget_CoversSchemaChanges(t *testing.T) {
	for _, scenario := range []string{"new table", "new column", "missing update trigger", "missing delete trigger"} {
		t.Run(scenario, func(t *testing.T) {
			db, _ := newBudgetDB(t)
			ctx := context.Background()
			if err := ConfigureStorageBudget(ctx, db, 16<<20); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO entries VALUES(1, 'retained')`); err != nil {
				t.Fatal(err)
			}
			before, err := ReadStorageBudget(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			statement := map[string]string{
				"new table":              `CREATE TABLE added(id INTEGER PRIMARY KEY, content BLOB)`,
				"new column":             `ALTER TABLE entries ADD COLUMN extra TEXT NOT NULL DEFAULT 'new data'`,
				"missing update trigger": `DROP TRIGGER storage_budget_entries_update`,
				"missing delete trigger": `DROP TRIGGER storage_budget_entries_delete`,
			}[scenario]
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if err := RefreshStorageBudget(ctx, db); err != nil {
				t.Fatal(err)
			}
			if scenario == "new column" {
				after, err := ReadStorageBudget(ctx, db)
				if err != nil || after.UsedBytes != before.UsedBytes+2*int64(len("new data")) {
					t.Fatalf("new column backfill: before=%+v after=%+v err=%v", before, after, err)
				}
				if _, err := db.Exec(`UPDATE entries SET extra='updated data' WHERE id=1`); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "new table" {
				if _, err := db.Exec(`INSERT INTO added VALUES(1,'new data')`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`DELETE FROM added`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`UPDATE entries SET content='updated'; DELETE FROM entries`); err != nil {
				t.Fatal(err)
			}
			after, err := ReadStorageBudget(ctx, db)
			if err != nil || after.UsedBytes != 0 {
				t.Fatalf("changed schema lost accounting: %+v err=%v", after, err)
			}
		})
	}
}
