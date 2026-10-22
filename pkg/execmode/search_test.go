package execmode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBrokerSearchText_LineSemantics(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("x", 70<<10) + "needle"
	source := "\n  needle  \r\n\u2003needle\u2003\n" + long + "\nlast needle"
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	broker, err := NewBroker(root, AuditSinkFunc(func(AuditRecord) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	result, err := broker.searchText(context.Background(), map[string]any{"pattern": "needle", "glob": "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Matches []struct {
			File string
			Line int
			Text string
		}
		Capped bool
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"needle", "needle", long, "last needle"}
	if got.Capped || len(got.Matches) != len(want) {
		t.Fatalf("capped=%v matches=%d", got.Capped, len(got.Matches))
	}
	for i, match := range got.Matches {
		if match.File != "source.go" || match.Line != i+2 || match.Text != want[i] {
			t.Fatalf("match %d: file=%q line=%d text_bytes=%d", i, match.File, match.Line, len(match.Text))
		}
	}
}

func TestBrokerSearchText_StopAtCap(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires directory permission enforcement")
	}
	for _, tc := range []struct {
		name string
		dir  string
		fail bool
	}{
		{"error_before_cap", "0-blocked", true},
		{"stop_before_later_error", "z-blocked", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(strings.Repeat("needle\n", maxSearchResults)), 0600); err != nil {
				t.Fatal(err)
			}
			blocked := filepath.Join(root, tc.dir)
			if err := os.Mkdir(blocked, 0000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(blocked, 0700) })
			if _, err := os.ReadDir(blocked); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("fixture does not enforce permissions: %v", err)
			}
			broker, err := NewBroker(root, AuditSinkFunc(func(AuditRecord) error { return nil }))
			if err != nil {
				t.Fatal(err)
			}
			result, err := broker.searchText(context.Background(), map[string]any{"pattern": "needle"})
			if tc.fail {
				if !errors.Is(err, os.ErrPermission) {
					t.Fatalf("error before cap: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("walk continued after cap: %v", err)
			}
			if result.(map[string]any)["capped"] != true || globSearchCount(t, broker, "needle", "") != maxSearchResults {
				t.Fatal("capped result changed")
			}
		})
	}
}

func BenchmarkBrokerSearchText_DenseLines(b *testing.B) {
	root := b.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte(strings.Repeat("ordinary\n", (2<<20)/9)), 0600); err != nil {
		b.Fatal(err)
	}
	broker, err := NewBroker(root, AuditSinkFunc(func(AuditRecord) error { return nil }))
	if err != nil {
		b.Fatal(err)
	}
	params := map[string]any{"pattern": "needle"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := broker.searchText(context.Background(), params); err != nil {
			b.Fatal(err)
		}
	}
}
