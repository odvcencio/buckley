package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReclaimSQLite_ReclaimsDeletedSearchEntriesAndPreservesSearch(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.DB().SetMaxOpenConns(1)
	now := time.Now()
	for _, id := range []string{"expired", "retained"} {
		if err := store.CreateSession(&Session{ID: id, CreatedAt: now, LastActive: now, Status: SessionStatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	err = WithSQLiteWriteTransaction(context.Background(), store.DB(), func(conn *sql.Conn) error {
		body := strings.Repeat("obsolete retirement evidence ", 512)
		for i := 0; i < 512; i++ {
			if _, err := conn.ExecContext(context.Background(), `INSERT INTO messages(session_id,role,content,timestamp) VALUES('expired','user',?,?)`, body, sqliteTimestamp(now)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const survivingContent = "retainedneedle preserves searchable history"
	if _, err := store.DB().Exec(`INSERT INTO messages(session_id,role,content,timestamp) VALUES('retained','user',?,?)`, survivingContent, sqliteTimestamp(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`DELETE FROM sessions WHERE session_id='expired'`); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := store.DB().QueryRow(`SELECT SUM(length(block)) FROM messages_fts_data`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if err := ReclaimSQLite(context.Background(), store.DB(), false); err != nil {
			t.Fatal(err)
		}
		if err := store.DB().QueryRow(`SELECT SUM(length(block)) FROM messages_fts_data`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after < before/4 {
			break
		}
	}
	if before < 64*1024 || after >= before/4 {
		t.Fatalf("deleted search data not reclaimed: before=%d after=%d", before, after)
	}
	var content string
	if err := store.DB().QueryRow(`SELECT m.content FROM messages_fts JOIN messages m ON m.id=messages_fts.rowid WHERE messages_fts MATCH 'retainedneedle'`).Scan(&content); err != nil || content != survivingContent {
		t.Fatalf("retained search content=%q err=%v", content, err)
	}
	var deletedMatches int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'obsolete'`).Scan(&deletedMatches); err != nil || deletedMatches != 0 {
		t.Fatalf("deleted matches=%d err=%v", deletedMatches, err)
	}
	if _, err := store.DB().Exec(`INSERT INTO messages_fts(messages_fts,rank) VALUES('integrity-check',1)`); err != nil {
		t.Fatal(err)
	}
}

func TestWithSQLiteWriteTransaction_RollbackAndCancellationReleaseConnection(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "transaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.DB().SetMaxOpenConns(1)
	sentinel := errors.New("rollback")
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		err := WithSQLiteWriteTransaction(ctx, store.DB(), func(conn *sql.Conn) error {
			if _, err := conn.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('rolled-back','value')`); err != nil {
				return err
			}
			if cancelled {
				cancel()
				return ctx.Err()
			}
			return sentinel
		})
		cancel()
		if cancelled && !errors.Is(err, context.Canceled) || !cancelled && !errors.Is(err, sentinel) {
			t.Fatal(err)
		}
		var count int
		if err := store.DB().QueryRow(`SELECT COUNT(*) FROM settings WHERE key='rolled-back'`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("count=%d err=%v", count, err)
		}
	}
}

func TestReclaimSQLite_ReclaimsNewAndLegacyDatabases(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "incremental", true: "legacy vacuum"}[legacy], func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "space.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if !legacy {
				if err := EnableSQLiteIncrementalVacuum(db); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`CREATE TABLE data(id INTEGER PRIMARY KEY, body BLOB)`); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 8; i++ {
				if _, err := db.Exec(`INSERT INTO data(body) VALUES(zeroblob(262144))`); err != nil {
					t.Fatal(err)
				}
			}
			var before, after int
			if err := db.QueryRow(`PRAGMA page_count`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DELETE FROM data WHERE id > 1`); err != nil {
				t.Fatal(err)
			}
			if err := ReclaimSQLite(context.Background(), db, legacy); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`PRAGMA page_count`).Scan(&after); err != nil || after >= before/2 {
				t.Fatalf("pages before=%d after=%d err=%v", before, after, err)
			}
			var size, mode int
			if err := db.QueryRow(`SELECT length(body) FROM data WHERE id=1`).Scan(&size); err != nil || size != 262144 {
				t.Fatalf("surviving record size=%d err=%v", size, err)
			}
			if err := db.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil || mode != 2 {
				t.Fatalf("vacuum mode=%d err=%v", mode, err)
			}
		})
	}
}

func TestReclaimSQLite_DefersWALTruncationForReaderAndRestoresTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reader.db")
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.DB().SetMaxOpenConns(2)
	if _, err := store.DB().Exec(`CREATE TABLE data(value INTEGER); INSERT INTO data VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	reader, err := store.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Rollback() })
	var value int
	if err := reader.QueryRow(`SELECT COUNT(*) FROM data`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO data VALUES(2)`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ReclaimSQLite(ctx, store.DB(), false); err != nil {
		t.Fatalf("cleanup waited for reader: %v", err)
	}
	if err := store.DB().QueryRow(`PRAGMA busy_timeout`).Scan(&value); err != nil || value != 5000 {
		t.Fatalf("timeout=%d err=%v", value, err)
	}
	if err := reader.QueryRow(`SELECT COUNT(*) FROM data`).Scan(&value); err != nil || value != 1 {
		t.Fatalf("reader changed: count=%d err=%v", value, err)
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := ReclaimSQLite(context.Background(), store.DB(), false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path + "-wal")
	if err != nil || info.Size() != 0 {
		t.Fatalf("WAL not truncated: info=%v err=%v", info, err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM data`).Scan(&value); err != nil || value != 2 {
		t.Fatalf("writer data lost: count=%d err=%v", value, err)
	}
}
