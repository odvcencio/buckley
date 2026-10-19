package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sqlite3 "modernc.org/sqlite/lib"
)

const storageBudgetError = "storage byte budget exceeded"

var ErrStorageBudget = errors.New(storageBudgetError)

// StorageBudget accounts for durable rows and owned blobs. SQLite allocation and
// WAL limits also bound physical overhead. Existing oversized files are preserved.
type StorageBudget struct {
	LimitBytes int64 `json:"limitBytes"`
	UsedBytes  int64 `json:"usedBytes"`
}

func IsStorageBudgetError(err error) bool {
	var code sqliteCodeError
	return err != nil && (errors.Is(err, ErrStorageBudget) || strings.Contains(err.Error(), storageBudgetError) ||
		strings.Contains(err.Error(), storageWALBudgetError) || (errors.As(err, &code) && code.Code()&0xff == sqlite3.SQLITE_FULL))
}

// ConfigureStorageBudget installs transactional accounting on the composed
// database after its adapters have migrated. It never removes retained history.
// Existing databases above the ceiling may shrink, but cannot add payload.
func ConfigureStorageBudget(ctx context.Context, db *sql.DB, limit int64) (resultErr error) {
	if limit < 0 {
		return fmt.Errorf("storage budget must be nonnegative")
	}
	previous, err := readStorageBudget(ctx, db)
	if err != nil {
		return err
	}
	if limit > 0 && previous.LimitBytes == limit {
		missing, err := storageBudgetNeedsRefresh(ctx, db)
		if err != nil || !missing {
			return err
		}
	}
	defer func() {
		if resultErr == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if current, err := readStorageBudget(cleanup, db); err == nil {
			previous = current
		}
		resultErr = errors.Join(resultErr, configureSQLiteAllocation(cleanup, db, previous.LimitBytes))
	}()
	// Lift an old allocation ceiling before installing a larger policy. Applying
	// it again after commit covers connections opened during configuration.
	if err := configureSQLiteAllocation(ctx, db, 0); err != nil {
		return err
	}
	err = WithSQLiteWriteTransaction(ctx, db, func(conn *sql.Conn) error {
		if limit == 0 {
			rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'trigger' AND substr(name, 1, 15) = 'storage_budget_'`)
			if err != nil {
				return err
			}
			var names []string
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					rows.Close()
					return err
				}
				names = append(names, name)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, name := range names {
				if _, err := conn.ExecContext(ctx, `DROP TRIGGER `+quoteSQLiteIdentifier(name)); err != nil {
					return err
				}
			}
			_, err = conn.ExecContext(ctx, `DROP TABLE IF EXISTS storage_budget`)
			return err
		}
		if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS storage_budget (
			id INTEGER PRIMARY KEY CHECK(id = 1), limit_bytes INTEGER NOT NULL,
			used_bytes INTEGER NOT NULL CHECK(used_bytes >= 0));
			INSERT INTO storage_budget VALUES (1, 0, 0) ON CONFLICT(id) DO UPDATE SET limit_bytes = 0`); err != nil {
			return err
		}
		rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master
			WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '%_fts%'
			AND name <> 'storage_budget' AND name NOT LIKE '%schema_migrations' ORDER BY name`)
		if err != nil {
			return err
		}
		var tables []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			tables = append(tables, name)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		var used int64
		for _, table := range tables {
			cost, err := storageRowCost(ctx, conn, table, "")
			if err != nil {
				return err
			}
			var bytes int64
			if err := conn.QueryRowContext(ctx, `SELECT COALESCE(SUM(`+cost+`), 0) FROM `+quoteSQLiteIdentifier(table)).Scan(&bytes); err != nil {
				return fmt.Errorf("account %s: %w", table, err)
			}
			used += bytes
			newCost := qualifyStorageCost(cost, "NEW.")
			oldCost := qualifyStorageCost(cost, "OLD.")
			for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
				name := quoteSQLiteIdentifier("storage_budget_" + table + "_" + strings.ToLower(op))
				if _, err := conn.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+name); err != nil {
					return err
				}
				delta := newCost
				if op == "UPDATE" {
					delta = "(" + newCost + ") - (" + oldCost + ")"
				} else if op == "DELETE" {
					delta = "-(" + oldCost + ")"
				}
				stmt := `CREATE TRIGGER ` + name + ` AFTER ` + op + ` ON ` + quoteSQLiteIdentifier(table) + ` BEGIN
					SELECT CASE WHEN EXISTS (SELECT 1 FROM storage_budget WHERE id = 1 AND limit_bytes > 0
						AND buckley_wal_bytes((SELECT file FROM pragma_database_list WHERE name = 'main')) > MIN(16777216, limit_bytes / 8))
					THEN RAISE(ABORT, '` + storageWALBudgetError + `') END;
					SELECT CASE WHEN (` + delta + `) > 0 AND EXISTS (
						SELECT 1 FROM storage_budget WHERE id = 1 AND limit_bytes > 0
						AND used_bytes > limit_bytes - (` + delta + `))
					THEN RAISE(ABORT, '` + storageBudgetError + `') END;
					UPDATE storage_budget SET used_bytes = MAX(0, used_bytes + (` + delta + `)) WHERE id = 1;
				END`
				if _, err := conn.ExecContext(ctx, stmt); err != nil {
					return fmt.Errorf("install %s budget: %w", table, err)
				}
			}
		}
		_, err = conn.ExecContext(ctx, `UPDATE storage_budget SET limit_bytes = ?, used_bytes = ? WHERE id = 1`, limit, used)
		return err
	})
	if err != nil {
		return err
	}
	if err := configureSQLiteAllocation(ctx, db, limit); err != nil {
		return err
	}
	return truncateSQLiteWAL(ctx, db)
}

// Identifiers are quoted before this expression is constructed. Qualifying just
// the identifiers leaves the same cost formula for backfill and all three triggers.
func qualifyStorageCost(cost, prefix string) string {
	return strings.ReplaceAll(cost, `COALESCE("`, `COALESCE(`+prefix+`"`)
}

func quoteSQLiteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func storageRowCost(ctx context.Context, conn *sql.Conn, table, prefix string) (string, error) {
	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(`+quoteSQLiteIdentifier(table)+`)`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var terms []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
			return "", err
		}
		column := `COALESCE(` + prefix + quoteSQLiteIdentifier(name) + `, '')`
		if table == "evidence_objects" && name == "stored_byte_count" {
			// Four times the physical blob bytes leaves space for the database,
			// WAL and atomic blob writes. Legacy rows use their raw size.
			terms = append(terms, `2 * CASE WHEN `+column+` > 0 THEN `+column+` ELSE COALESCE(`+prefix+`"byte_count", 0) END`)
		} else if strings.Contains(strings.ToUpper(typ), "INT") || strings.Contains(strings.ToUpper(typ), "REAL") || strings.Contains(strings.ToUpper(typ), "BOOL") {
			terms = append(terms, `CASE WHEN typeof(`+column+`) IN ('integer', 'real') THEN 8 ELSE MAX(8, length(CAST(`+column+` AS BLOB))) END`)
		} else {
			terms = append(terms, `length(CAST(`+column+` AS BLOB))`)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "1024 + 2 * (" + strings.Join(terms, " + ") + ")", nil
}

// RefreshStorageBudget covers tables introduced by a newly composed adapter.
// Already covered databases need only a schema lookup, not a history scan.
func RefreshStorageBudget(ctx context.Context, db *sql.DB) error {
	budget, err := readStorageBudget(ctx, db)
	if err != nil || budget.LimitBytes == 0 {
		return err
	}
	missing, err := storageBudgetNeedsRefresh(ctx, db)
	if err != nil || !missing {
		return err
	}
	return ConfigureStorageBudget(ctx, db, budget.LimitBytes)
}

func storageBudgetNeedsRefresh(ctx context.Context, db budgetReader) (bool, error) {
	var missing bool
	err := db.QueryRowContext(ctx, `WITH operations(op) AS (VALUES ('insert'), ('update'), ('delete'))
		SELECT EXISTS (SELECT 1 FROM sqlite_master AS tables CROSS JOIN operations
		WHERE tables.type = 'table' AND tables.name NOT LIKE 'sqlite_%' AND tables.name NOT LIKE '%_fts%'
		AND tables.name <> 'storage_budget' AND tables.name NOT LIKE '%schema_migrations'
		AND NOT EXISTS (SELECT 1 FROM sqlite_master AS triggers WHERE triggers.type = 'trigger'
			AND triggers.name = 'storage_budget_' || tables.name || '_' || operations.op
			AND NOT EXISTS (SELECT 1 FROM pragma_table_info(tables.name) AS columns
				WHERE instr(triggers.sql, '"' || replace(columns.name, '"', '""') || '"') = 0)))`).Scan(&missing)
	return missing, err
}

func ReadStorageBudget(ctx context.Context, db *sql.DB) (StorageBudget, error) {
	return readStorageBudget(ctx, db)
}

type budgetReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readStorageBudget(ctx context.Context, db budgetReader) (StorageBudget, error) {
	var budget StorageBudget
	err := db.QueryRowContext(ctx, `SELECT limit_bytes, used_bytes FROM storage_budget WHERE id = 1`).Scan(&budget.LimitBytes, &budget.UsedBytes)
	if errors.Is(err, sql.ErrNoRows) || (err != nil && strings.Contains(err.Error(), "no such table: storage_budget")) {
		return StorageBudget{}, nil
	}
	return budget, err
}

// CheckStorageBudget checks a pending evidence write before creating its blob.
// Callers hold the writer lock; triggers enforce the final transaction's ceiling.
func CheckStorageBudget(ctx context.Context, db budgetReader, additionalBytes int64) error {
	budget, err := readStorageBudget(ctx, db)
	if err != nil {
		return err
	}
	if budget.LimitBytes == 0 {
		return nil
	}
	if additionalBytes > budget.LimitBytes || budget.UsedBytes > budget.LimitBytes-additionalBytes {
		return fmt.Errorf("%w: %d of %d bytes used; %d bytes required; raise resources.storage_bytes or prune completed history", ErrStorageBudget, budget.UsedBytes, budget.LimitBytes, additionalBytes)
	}
	return nil
}
