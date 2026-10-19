package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"strings"

	"modernc.org/sqlite"
)

const storageWALBudgetError = "storage WAL budget exceeded"

func init() {
	sqlite.MustRegisterScalarFunction("buckley_wal_bytes", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		path, _ := args[0].(string)
		return sqliteWALBytes(path)
	})
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
		ctx := context.Background()
		limit, err := sqliteDriverInt(ctx, conn, `SELECT limit_bytes FROM storage_budget WHERE id = 1`)
		if err != nil && strings.Contains(err.Error(), "no such table: storage_budget") {
			return nil
		}
		if err != nil {
			return err
		}
		return applySQLiteAllocation(ctx, conn, limit)
	})
}

func sqliteWALBytes(path string) (int64, error) {
	if path == "" {
		return 0, nil
	}
	info, err := os.Stat(path + "-wal")
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func sqliteDriverInt(ctx context.Context, conn sqlite.ExecQuerierContext, query string) (int64, error) {
	rows, err := conn.QueryContext(ctx, query, nil)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	values := make([]driver.Value, len(rows.Columns()))
	if err := rows.Next(values); err != nil {
		return 0, err
	}
	value, ok := values[0].(int64)
	if !ok {
		return 0, fmt.Errorf("SQLite allocation pragma returned %T", values[0])
	}
	return value, nil
}

func applySQLiteAllocation(ctx context.Context, conn sqlite.ExecQuerierContext, limit int64) error {
	pageSize, err := sqliteDriverInt(ctx, conn, "PRAGMA page_size")
	if err != nil {
		return err
	}
	maxPages, checkpointPages, journalBytes := int64(4294967294), int64(1000), int64(-1)
	if limit > 0 {
		maxPages = max(int64(1), limit/4/pageSize)
		journalBytes = min(int64(16<<20), limit/8)
		checkpointPages = max(int64(1), min(int64(1000), journalBytes/2/pageSize))
	}
	for _, pragma := range []string{
		fmt.Sprintf("PRAGMA max_page_count = %d", maxPages),
		fmt.Sprintf("PRAGMA wal_autocheckpoint = %d", checkpointPages),
		fmt.Sprintf("PRAGMA journal_size_limit = %d", journalBytes),
		"PRAGMA recursive_triggers = ON",
	} {
		if _, err := conn.ExecContext(ctx, pragma, nil); err != nil {
			return fmt.Errorf("configure SQLite allocation: %w", err)
		}
	}
	return nil
}

func configureSQLiteAllocation(ctx context.Context, db *sql.DB, limit int64) error {
	stats := db.Stats()
	count := max(1, stats.OpenConnections)
	if stats.InUse > 0 && stats.MaxOpenConnections > 0 {
		count = stats.MaxOpenConnections
	}
	connections := make([]*sql.Conn, 0, count)
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for range count {
		conn, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		connections = append(connections, conn)
		if err := conn.Raw(func(raw any) error {
			return applySQLiteAllocation(ctx, raw.(sqlite.ExecQuerierContext), limit)
		}); err != nil {
			return err
		}
	}
	return nil
}

// RecoverStorageBudgetWAL attempts a nonblocking checkpoint before admitting new
// work. A long-lived reader can defer recovery; writes then fail closed.
func RecoverStorageBudgetWAL(ctx context.Context, db *sql.DB) error {
	budget, err := readStorageBudget(ctx, db)
	if err != nil || budget.LimitBytes == 0 {
		return err
	}
	var path string
	if err := db.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil {
		return err
	}
	bytes, err := sqliteWALBytes(path)
	if err != nil || bytes <= min(int64(16<<20), budget.LimitBytes/8) {
		return err
	}
	if err := truncateSQLiteWAL(ctx, db); err != nil {
		return err
	}
	bytes, err = sqliteWALBytes(path)
	if err != nil {
		return err
	}
	if bytes > min(int64(16<<20), budget.LimitBytes/8) {
		return fmt.Errorf("%w: %s; a retained reader must release its snapshot", ErrStorageBudget, storageWALBudgetError)
	}
	return nil
}
