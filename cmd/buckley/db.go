package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func runDBCommand(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = strings.TrimSpace(args[0])
	}
	switch sub {
	case "backup":
		return runDBBackup(args[1:])
	case "restore":
		return runDBRestore(args[1:])
	default:
		return fmt.Errorf("usage: buckley db <backup|restore> [flags]")
	}
}

func runDBBackup(args []string) error {
	fs := flag.NewFlagSet("db backup", flag.ContinueOnError)
	out := fs.String("out", "", "Output path for the backup .db file (required)")
	dbPathFlag := fs.String("db", "", "Source DB path (defaults to BUCKLEY_DB_PATH/BUCKLEY_DATA_DIR)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*out) == "" {
		return fmt.Errorf("usage: buckley db backup --out <path>")
	}

	dbPath := strings.TrimSpace(*dbPathFlag)
	if dbPath == "" {
		resolved, err := resolveDBPath()
		if err != nil {
			return err
		}
		dbPath = resolved
	}

	outPath, err := expandHomePath(*out)
	if err != nil {
		return err
	}
	outPath, err = filepath.Abs(outPath)
	if err != nil {
		return err
	}

	if _, err := os.Stat(outPath); err == nil {
		return fmt.Errorf("backup destination already exists: %s", outPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat backup destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}

	tmpPath := outPath + ".tmp"
	_ = os.Remove(tmpPath)

	if err := vacuumInto(dbPath, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("finalize backup: %w", err)
	}

	fmt.Printf("✅ Backed up %s -> %s\n", dbPath, outPath)
	return nil
}

func runDBRestore(args []string) error {
	fs := flag.NewFlagSet("db restore", flag.ContinueOnError)
	in := fs.String("in", "", "Input path to a .db backup file (required)")
	dbPathFlag := fs.String("db", "", "Destination DB path (defaults to BUCKLEY_DB_PATH/BUCKLEY_DATA_DIR)")
	force := fs.Bool("force", false, "Overwrite existing DB (required when destination exists)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*in) == "" {
		return fmt.Errorf("usage: buckley db restore --in <path> [--force]")
	}

	inPath, err := expandHomePath(*in)
	if err != nil {
		return err
	}
	inPath, err = filepath.Abs(inPath)
	if err != nil {
		return err
	}
	if info, err := os.Stat(inPath); err != nil {
		return fmt.Errorf("stat input: %w", err)
	} else if info.IsDir() {
		return fmt.Errorf("input must be a file: %s", inPath)
	}

	dbPath := strings.TrimSpace(*dbPathFlag)
	if dbPath == "" {
		resolved, err := resolveDBPath()
		if err != nil {
			return err
		}
		dbPath = resolved
	}
	dbPath, err = expandHomePath(dbPath)
	if err != nil {
		return err
	}
	dbPath, err = filepath.Abs(dbPath)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return fmt.Errorf("create db directory: %w", err)
	}

	if _, err := os.Stat(dbPath); err == nil {
		if !*force {
			return fmt.Errorf("destination exists: %s (re-run with --force after stopping Buckley)", dbPath)
		}
		backupPath := fmt.Sprintf("%s.bak.%s", dbPath, time.Now().UTC().Format("20060102T150405Z"))
		if err := os.Rename(dbPath, backupPath); err != nil {
			return fmt.Errorf("backup existing db: %w", err)
		}
		fmt.Printf("Moved existing DB to %s\n", backupPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat destination: %w", err)
	}

	tmpPath := dbPath + ".restore.tmp"
	_ = os.Remove(tmpPath)
	if err := copyFile(inPath, tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dbPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("finalize restore: %w", err)
	}

	// Clean up WAL files from any previous instance (safe when Buckley is stopped).
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	fmt.Printf("✅ Restored %s -> %s\n", inPath, dbPath)
	fmt.Println("Next: run `buckley migrate` before starting the server if you upgraded versions.")
	return nil
}

func vacuumInto(dbPath string, outPath string) error {
	dbPath = strings.TrimSpace(dbPath)
	outPath = strings.TrimSpace(outPath)
	if dbPath == "" {
		return fmt.Errorf("db path cannot be empty")
	}
	if outPath == "" {
		return fmt.Errorf("output path cannot be empty")
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("set busy timeout: %w", err)
	}

	stmt := fmt.Sprintf("VACUUM INTO '%s'", escapeSQLiteString(outPath))
	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("VACUUM INTO failed: %w", err)
	}
	return nil
}

func escapeSQLiteString(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func copyFile(src, dst string, perm os.FileMode) error {
	src = strings.TrimSpace(src)
	dst = strings.TrimSpace(dst)
	if src == "" || dst == "" {
		return fmt.Errorf("copy paths cannot be empty")
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("open destination: %w", err)
	}
	defer func() {
		_ = out.Close()
	}()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	return out.Close()
}
