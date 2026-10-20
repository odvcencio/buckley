package storage

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	DefaultSymbolIndexRecords = 100000
	DefaultSymbolIndexBytes   = 64 << 20
)

// SymbolDefinition contains a bounded indexed signature and its original byte count.
type SymbolDefinition struct {
	SymbolRecord
	SignatureBytes int
}

// FindSymbolDefinitions reads an exact-name snapshot without sorting a full result set in SQLite.
// A bare name also matches the leaf of a qualified name. Paths are literal file/directory scopes.
func (s *Store) FindSymbolDefinitions(ctx context.Context, name string, kinds, paths []string, limit int) ([]SymbolDefinition, int, error) {
	if s.db == nil {
		return nil, 0, fmt.Errorf("store not initialized")
	}
	if name == "" || len(name) > 1024 {
		return nil, 0, fmt.Errorf("symbol name must contain 1 to 1024 bytes")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if len(paths) == 0 || len(paths) > 2 || len(kinds) == 0 || len(kinds) > 16 {
		return nil, 0, fmt.Errorf("symbol query requires bounded kind and path scopes")
	}
	where := []string{"(name = ? COLLATE BINARY"}
	args := []any{name}
	if !strings.Contains(name, ".") {
		where[0] += " OR substr(name, -length(?)) = ? COLLATE BINARY"
		suffix := "." + name
		args = append(args, suffix, suffix)
	}
	where[0] += ")"
	marks := make([]string, len(kinds))
	for i, kind := range kinds {
		marks[i] = "?"
		args = append(args, kind)
	}
	where = append(where, "lower(kind) IN ("+strings.Join(marks, ",")+")")
	scopes := []string{}
	for _, path := range paths {
		if len(path) > 8192 {
			return nil, 0, fmt.Errorf("symbol path scope exceeds metadata budget")
		}
		path = strings.TrimSuffix(path, "/")
		if path == "." {
			scopes = append(scopes, "(substr(file_path,1,1) <> '/' AND substr(file_path,2,1) <> ':')")
			continue
		}
		prefix := path + "/"
		scopes = append(scopes, "(file_path = ? OR substr(file_path,1,length(?)) = ?)")
		args = append(args, path, prefix, prefix)
	}
	where = append(where, "("+strings.Join(scopes, " OR ")+")")
	query := `SELECT substr(file_path,1,8193), substr(name,1,8193), kind, substr(coalesce(signature,''),1,256), coalesce(start_line,0), coalesce(end_line,0), length(CAST(coalesce(signature,'') AS BLOB)), length(CAST(file_path AS BLOB)), length(CAST(name AS BLOB)) FROM fs_symbols WHERE ` + strings.Join(where, " AND ")
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	selected := make([]SymbolDefinition, 0, limit)
	total := 0
	processed := 0
	bytes := int64(0)
	for rows.Next() {
		var rec SymbolDefinition
		var pathBytes, nameBytes int
		if err := rows.Scan(&rec.FilePath, &rec.Name, &rec.Kind, &rec.Signature, &rec.StartLine, &rec.EndLine, &rec.SignatureBytes, &pathBytes, &nameBytes); err != nil {
			return nil, 0, err
		}
		processed++
		if processed > DefaultSymbolIndexRecords {
			return nil, 0, fmt.Errorf("symbol index record budget exceeded; narrow the path")
		}
		if pathBytes > 8192 || nameBytes > 8192 || rec.StartLine < 1 || !utf8.ValidString(rec.FilePath) || !utf8.ValidString(rec.Name) || !utf8.ValidString(rec.Signature) {
			return nil, 0, fmt.Errorf("invalid or oversized indexed symbol metadata; rebuild the index or use live search")
		}
		bytes += int64(len(rec.FilePath) + len(rec.Name) + len(rec.Signature) + len(rec.Kind))
		if bytes > DefaultSymbolIndexBytes {
			return nil, 0, fmt.Errorf("symbol index metadata byte budget exceeded; narrow the path")
		}
		canonical := path.Clean(strings.ReplaceAll(rec.FilePath, "\\", "/"))
		inside := false
		for _, scope := range paths {
			scope = path.Clean(scope)
			if scope == "." {
				inside = !strings.HasPrefix(canonical, "/") && !strings.HasPrefix(canonical, "../") && canonical != ".." && !(len(canonical) > 1 && canonical[1] == ':')
			} else {
				inside = canonical == scope || strings.HasPrefix(canonical, strings.TrimSuffix(scope, "/")+"/")
			}
			if inside {
				break
			}
		}
		if !inside {
			continue
		}
		rec.FilePath = canonical
		total++
		at := sort.Search(len(selected), func(i int) bool {
			other := selected[i]
			if rec.FilePath != other.FilePath {
				return rec.FilePath < other.FilePath
			}
			if rec.StartLine != other.StartLine {
				return rec.StartLine < other.StartLine
			}
			if rec.Name != other.Name {
				return rec.Name < other.Name
			}
			if rec.Kind != other.Kind {
				return rec.Kind < other.Kind
			}
			return rec.Signature < other.Signature
		})
		if at == limit {
			continue
		}
		if len(selected) < limit {
			selected = append(selected, SymbolDefinition{})
		}
		copy(selected[at+1:], selected[at:len(selected)-1])
		selected[at] = rec
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return selected, total, nil
}
