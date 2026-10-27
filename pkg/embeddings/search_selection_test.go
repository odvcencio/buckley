package embeddings

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/storage"
	"modernc.org/sqlite"
)

type selectionEmbeddingProvider struct{}

func (selectionEmbeddingProvider) Embed(ctx context.Context, _ string) ([]float64, error) {
	return []float64{1, 0}, ctx.Err()
}

func newSelectionSearcher(t testing.TB) (*Searcher, *sql.DB) {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewSearcher(selectionEmbeddingProvider{}, store.DB()), store.DB()
}

func insertSelectionRow(t testing.TB, db *sql.DB, path, content, metadata string, vector []float64) {
	t.Helper()
	data, err := serializeEmbedding(vector)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO embeddings (file_path, content_hash, content, embedding, metadata) VALUES (?, 'fixture', ?, ?, ?)`, path, content, data, metadata); err != nil {
		t.Fatal(err)
	}
}

func TestSearcherSearch_RankingAndThreshold(t *testing.T) {
	for _, limit := range []int{-1, 0, 1, 2, 5} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			s, db := newSelectionSearcher(t)
			insertSelectionRow(t, db, "low.go", "low", `{}`, []float64{3, 4})
			insertSelectionRow(t, db, "second.go", "second", `{"file":"src/second.go","language":"go"}`, []float64{4, 3})
			insertSelectionRow(t, db, "best.go", "best", `{invalid`, []float64{1, 0})
			insertSelectionRow(t, db, "wrong-dimension.go", "wrong", `{}`, []float64{1})
			if _, err := db.Exec(`INSERT INTO embeddings (file_path, content_hash, content, embedding) VALUES ('corrupt.go','fixture','corrupt',X'00')`); err != nil {
				t.Fatal(err)
			}
			got, err := s.Search(context.Background(), "query", limit)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"best.go", "second.go"}
			if limit == 1 {
				want = want[:1]
			}
			var ids []string
			for _, result := range got {
				ids = append(ids, result.ID)
			}
			if !reflect.DeepEqual(ids, want) {
				t.Fatalf("IDs = %v, want %v", ids, want)
			}
			if got[0].Content != "best" || got[0].Metadata["file"] != "best.go" {
				t.Fatalf("best result payload = %+v", got[0])
			}
			if len(got) > 1 && (got[1].Content != "second" || got[1].Metadata["file"] != "src/second.go" || got[1].Metadata["language"] != "go") {
				t.Fatalf("second result payload = %+v", got[1])
			}
		})
	}
}

func TestSearcherSearch_BestBelowThreshold(t *testing.T) {
	s, db := newSelectionSearcher(t)
	insertSelectionRow(t, db, "opposite.go", "opposite", `{}`, []float64{-1, 0})
	insertSelectionRow(t, db, "nearest.go", "nearest", `{}`, []float64{3, 4})
	got, err := s.Search(context.Background(), "query", 5)
	if err != nil || len(got) != 1 || got[0].ID != "nearest.go" {
		t.Fatalf("Search = %+v, %v", got, err)
	}
}

func TestSearcherSearch_LimitBoundsRetainedResults(t *testing.T) {
	s, db := newSelectionSearcher(t)
	for i := range 257 {
		insertSelectionRow(t, db, fmt.Sprintf("file-%03d.go", i), strings.Repeat("x", 4096), `{}`, []float64{1, float64(i) / 1000})
	}
	got, err := s.Search(context.Background(), "query", 5)
	if err != nil || len(got) != 5 {
		t.Fatalf("Search returned %d results, %v", len(got), err)
	}
	if cap(got) > 5 {
		t.Fatalf("five results retain capacity for %d indexed documents", cap(got))
	}
	for i, result := range got {
		if result.ID != fmt.Sprintf("file-%03d.go", i) || len(result.Content) != 4096 {
			t.Fatalf("result %d = %+v", i, result)
		}
	}
}

func TestSearcherSearch_EmptyAndCancelled(t *testing.T) {
	s, _ := newSelectionSearcher(t)
	got, err := s.Search(context.Background(), "query", 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty Search = %v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Search(ctx, "query", 5); err == nil {
		t.Fatal("cancelled search succeeded")
	}
}

func TestSearcherSearch_OneConnection(t *testing.T) {
	s, db := newSelectionSearcher(t)
	db.SetMaxOpenConns(1)
	insertSelectionRow(t, db, "best.go", "payload", `{}`, []float64{1, 0})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if got, err := s.Search(ctx, "query", 5); err != nil || len(got) != 1 {
		t.Fatalf("single-connection Search = %v, %v", got, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE embeddings SET content='new payload'`); err != nil {
		t.Fatalf("search left its read transaction open: %v", err)
	}
}

func TestSearcherSearch_ConcurrentRefreshKeepsSnapshot(t *testing.T) {
	var writer *sql.DB
	swapped := false
	function := "buckley_search_snapshot_" + rand.Text()
	if err := sqlite.RegisterScalarFunction(function, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if !swapped {
			swapped = true
			vector, err := serializeEmbedding([]float64{0, 1})
			if err != nil {
				return nil, err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := writer.ExecContext(ctx, `UPDATE search_snapshot_source SET content='new generation', embedding=?`, vector); err != nil {
				return nil, err
			}
		}
		return args[0], nil
	}); err != nil {
		t.Fatal(err)
	}
	s, db := newSelectionSearcher(t)
	writer = db
	t.Cleanup(func() { writer = nil })
	insertSelectionRow(t, db, "best.go", "old generation", `{}`, []float64{1, 0})
	if _, err := db.Exec(`ALTER TABLE embeddings RENAME TO search_snapshot_source`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE VIEW embeddings AS SELECT file_path, content, ` + function + `(embedding) AS embedding, metadata FROM search_snapshot_source`); err != nil {
		t.Fatal(err)
	}
	got, err := s.Search(context.Background(), "query", 5)
	if err != nil || len(got) != 1 {
		t.Fatalf("Search = %v, %v", got, err)
	}
	if !swapped || got[0].Content != "old generation" || got[0].Similarity < .99 {
		t.Fatalf("result mixed ranking and payload generations: %+v (refresh=%v)", got[0], swapped)
	}
	var content string
	if err := db.QueryRow(`SELECT content FROM search_snapshot_source`).Scan(&content); err != nil || content != "new generation" {
		t.Fatalf("refresh did not commit: %q, %v", content, err)
	}
}

func BenchmarkSearcherSearchLimited(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			s, db := newSelectionSearcher(b)
			tx, err := db.Begin()
			if err != nil {
				b.Fatal(err)
			}
			stmt, err := tx.Prepare(`INSERT INTO embeddings (file_path, content_hash, content, embedding, metadata) VALUES (?, 'fixture', ?, ?, ?)`)
			if err != nil {
				b.Fatal(err)
			}
			for i := range count {
				vector, err := serializeEmbedding([]float64{1, float64(count-i) / float64(count) / 2})
				if err != nil {
					b.Fatal(err)
				}
				path := fmt.Sprintf("file-%05d.go", i)
				if _, err := stmt.Exec(path, strings.Repeat("x", 4096), vector, fmt.Sprintf(`{"file":%q,"language":"go"}`, path)); err != nil {
					b.Fatal(err)
				}
			}
			_ = stmt.Close()
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				got, err := s.Search(context.Background(), "query", 5)
				if err != nil || len(got) != 5 || got[0].ID != fmt.Sprintf("file-%05d.go", count-1) {
					b.Fatalf("Search returned %d results: %v", len(got), err)
				}
			}
		})
	}
}
