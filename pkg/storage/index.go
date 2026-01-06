package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// FileRecord represents indexed file metadata.
type FileRecord struct {
	Path      string
	Checksum  string
	Language  string
	SizeBytes int64
	Summary   string
	UpdatedAt time.Time
}

// SymbolRecord represents a code symbol in a file.
type SymbolRecord struct {
	FilePath  string
	Name      string
	Kind      string
	Signature string
	StartLine int
	EndLine   int
}

// ImportRecord represents an import discovered in a file.
type ImportRecord struct {
	FilePath   string
	ImportPath string
}

// UpsertFileRecord stores or updates metadata for a file.
func (s *Store) UpsertFileRecord(ctx context.Context, rec *FileRecord) error {
	if s.db == nil {
		return fmt.Errorf("store not initialized")
	}

	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = time.Now()
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO fs_files (path, checksum, language, size_bytes, summary, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			checksum=excluded.checksum,
			language=excluded.language,
			size_bytes=excluded.size_bytes,
			summary=excluded.summary,
			updated_at=excluded.updated_at
	`, rec.Path, rec.Checksum, rec.Language, rec.SizeBytes, rec.Summary, rec.UpdatedAt)
	return err
}

// ReplaceSymbols replaces all symbols for a given file.
func (s *Store) ReplaceSymbols(ctx context.Context, filePath string, symbols []SymbolRecord) error {
	if s.db == nil {
		return fmt.Errorf("store not initialized")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM fs_symbols WHERE file_path = ?`, filePath); err != nil {
		return err
	}

	if len(symbols) > 0 {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO fs_symbols (file_path, name, kind, signature, start_line, end_line)
			VALUES (?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, sym := range symbols {
			if _, err := stmt.ExecContext(ctx, filePath, sym.Name, sym.Kind, sym.Signature, sym.StartLine, sym.EndLine); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// ReplaceImports replaces all imports for a given file.
func (s *Store) ReplaceImports(ctx context.Context, filePath string, imports []ImportRecord) error {
	if s.db == nil {
		return fmt.Errorf("store not initialized")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM fs_imports WHERE file_path = ?`, filePath); err != nil {
		return err
	}

	if len(imports) > 0 {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO fs_imports (file_path, import_path)
			VALUES (?, ?)
		`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, imp := range imports {
			if _, err := stmt.ExecContext(ctx, filePath, imp.ImportPath); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// SearchFiles performs a basic LIKE search across file metadata.
func (s *Store) SearchFiles(ctx context.Context, query, pathGlob string, limit int) ([]FileRecord, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	stmt := "SELECT path, checksum, language, size_bytes, summary, updated_at FROM fs_files"
	where := []string{}
	args := []any{}

	if pathGlob != "" {
		where = append(where, "path LIKE ? ESCAPE '\\'")
		args = append(args, globToLike(pathGlob))
	}
	if query != "" {
		where = append(where, "(path LIKE ? ESCAPE '\\' OR summary LIKE ? ESCAPE '\\')")
		pat := "%" + query + "%"
		args = append(args, pat, pat)
	}
	if len(where) > 0 {
		stmt += " WHERE " + strings.Join(where, " AND ")
	}
	stmt += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []FileRecord
	for rows.Next() {
		var rec FileRecord
		if err := rows.Scan(&rec.Path, &rec.Checksum, &rec.Language, &rec.SizeBytes, &rec.Summary, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

// SearchSymbols finds symbols by name/path.
func (s *Store) SearchSymbols(ctx context.Context, symbol, pathGlob string, limit int) ([]SymbolRecord, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	stmt := "SELECT file_path, name, kind, signature, start_line, end_line FROM fs_symbols"
	where := []string{}
	args := []any{}

	if symbol != "" {
		where = append(where, "name LIKE ? ESCAPE '\\'")
		args = append(args, "%"+symbol+"%")
	}
	if pathGlob != "" {
		where = append(where, "file_path LIKE ? ESCAPE '\\'")
		args = append(args, globToLike(pathGlob))
	}
	if len(where) > 0 {
		stmt += " WHERE " + strings.Join(where, " AND ")
	}
	stmt += " ORDER BY file_path LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []SymbolRecord
	for rows.Next() {
		var rec SymbolRecord
		if err := rows.Scan(&rec.FilePath, &rec.Name, &rec.Kind, &rec.Signature, &rec.StartLine, &rec.EndLine); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

func globToLike(glob string) string {
	glob = strings.ReplaceAll(glob, "\\", "/")
	glob = strings.ReplaceAll(glob, "\\", "\\\\")
	glob = strings.ReplaceAll(glob, "%", "\\%")
	glob = strings.ReplaceAll(glob, "_", "\\_")
	glob = strings.ReplaceAll(glob, "*", "%")
	return glob
}

// GetAllFileChecksums returns a map of file path to checksum for all indexed files.
func (s *Store) GetAllFileChecksums(ctx context.Context) (map[string]string, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store not initialized")
	}

	rows, err := s.db.QueryContext(ctx, `SELECT path, checksum FROM fs_files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	checksums := make(map[string]string)
	for rows.Next() {
		var path, checksum string
		if err := rows.Scan(&path, &checksum); err != nil {
			return nil, err
		}
		checksums[path] = checksum
	}
	return checksums, rows.Err()
}

// DeleteFileRecord removes a file and its associated symbols/imports from the index.
func (s *Store) DeleteFileRecord(ctx context.Context, filePath string) error {
	if s.db == nil {
		return fmt.Errorf("store not initialized")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM fs_symbols WHERE file_path = ?`, filePath); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fs_imports WHERE file_path = ?`, filePath); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fs_files WHERE path = ?`, filePath); err != nil {
		return err
	}

	return tx.Commit()
}
