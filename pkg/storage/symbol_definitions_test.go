package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func newSymbolDefinitionStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func putSymbolDefinition(t *testing.T, s *Store, path, name, kind, signature string, line int) {
	t.Helper()
	ctx := context.Background()
	if err := s.UpsertFileRecord(ctx, &FileRecord{Path: path}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceSymbols(ctx, path, []SymbolRecord{{Name: name, Kind: kind, Signature: signature, StartLine: line, EndLine: line}}); err != nil {
		t.Fatal(err)
	}
}

func TestFindSymbolDefinitions_ExactNamesAndScopes(t *testing.T) {
	s := newSymbolDefinitionStore(t)
	for _, row := range []struct{ path, name, kind string }{{"pkg/a.go", "Target", "function"}, {"pkg_nested/a.go", "Target", "function"}, {"pkg/b.go", "Service.Target", "method"}, {"pkg/c.go", "TargetExtra", "function"}, {"pkg/d.go", "target", "function"}, {"pkg/e.go", "Target", "class"}, {"../outside.go", "Target", "function"}, {"pkg/../other.go", "Target", "function"}, {"pkg/pct.go", "Ta_get%", "function"}} {
		putSymbolDefinition(t, s, row.path, row.name, row.kind, "signature", 2)
	}
	for _, tc := range []struct {
		name         string
		paths, kinds []string
		count        int
	}{{"Target", []string{"pkg"}, []string{"function", "method"}, 2}, {"Target", []string{"pkg/a.go"}, []string{"function"}, 1}, {"Service.Target", []string{"."}, []string{"method"}, 1}, {"target", []string{"."}, []string{"function"}, 1}, {"Ta_get%", []string{"pkg"}, []string{"function"}, 1}, {"Target", []string{"."}, []string{"function", "method"}, 4}} {
		rows, total, err := s.FindSymbolDefinitions(context.Background(), tc.name, tc.kinds, tc.paths, 20)
		if err != nil || total != tc.count || len(rows) != tc.count {
			t.Fatalf("case=%+v total=%d rows=%+v err=%v", tc, total, rows, err)
		}
	}
}
func TestFindSymbolDefinitions_BoundedSignaturesAndCounts(t *testing.T) {
	s := newSymbolDefinitionStore(t)
	signature := strings.Repeat("λ", 50000)
	for i := 24; i >= 0; i-- {
		putSymbolDefinition(t, s, fmt.Sprintf("pkg/f%03d.go", i), "Target", "function", signature, 3)
	}
	rows, total, err := s.FindSymbolDefinitions(context.Background(), "Target", []string{"function"}, []string{"pkg"}, 20)
	if err != nil || total != 25 || len(rows) != 20 {
		t.Fatalf("total=%d rows=%d err=%v", total, len(rows), err)
	}
	for i, row := range rows {
		if row.FilePath != fmt.Sprintf("pkg/f%03d.go", i) || row.SignatureBytes != len(signature) || len(row.Signature) > 1024 || row.Signature != strings.Repeat("λ", 256) {
			t.Fatalf("bad bounded row %d: bytes=%d signature=%d", i, row.SignatureBytes, len(row.Signature))
		}
	}
}
func TestFindSymbolDefinitions_CancellationAndMetadata(t *testing.T) {
	s := newSymbolDefinitionStore(t)
	putSymbolDefinition(t, s, "a.go", "Target", "function", "func Target()", 0)
	_, _, err := s.FindSymbolDefinitions(context.Background(), "Target", []string{"function"}, []string{"."}, 20)
	if err == nil {
		t.Fatal("accepted invalid definition location")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = s.FindSymbolDefinitions(ctx, "Target", []string{"function"}, []string{"."}, 20)
	if err == nil {
		t.Fatal("ignored cancellation")
	}
	for _, name := range []string{"", strings.Repeat("x", 1025)} {
		if _, _, err := s.FindSymbolDefinitions(context.Background(), name, []string{"function"}, []string{"."}, 20); err == nil {
			t.Fatal("accepted invalid name")
		}
	}
}
func TestFindSymbolDefinitions_RecordBudget(t *testing.T) {
	s := newSymbolDefinitionStore(t)
	ctx := context.Background()
	if err := s.UpsertFileRecord(ctx, &FileRecord{Path: "a.go"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.DB().ExecContext(ctx, `WITH RECURSIVE n(v) AS (SELECT 1 UNION ALL SELECT v+1 FROM n WHERE v < ?) INSERT INTO fs_symbols(file_path,name,kind,signature,start_line,end_line) SELECT 'a.go','Target','function','header',v,v FROM n`, DefaultSymbolIndexRecords+1)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.FindSymbolDefinitions(ctx, "Target", []string{"function"}, []string{"."}, 20)
	if err == nil || !strings.Contains(err.Error(), "record budget") {
		t.Fatalf("expected record budget failure, got %v", err)
	}
}
