package execmode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrokerReadFileLimit(t *testing.T) {
	root := t.TempDir()
	broker, err := NewBroker(root, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"below", maxReadBytes - 1},
		{"exact", maxReadBytes},
		{"over", maxReadBytes + 1},
		{"large", 8 * maxReadBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := strings.Repeat("x", tc.size)
			if err := os.WriteFile(filepath.Join(root, tc.name), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			value, err := broker.filesRead(map[string]any{"path": tc.name})
			if err != nil {
				t.Fatal(err)
			}
			data := value.(map[string]any)
			if got := data["content"]; got != content[:min(tc.size, maxReadBytes)] {
				t.Fatalf("returned content differs from bounded prefix")
			}
			if data["truncated"] != (tc.size > maxReadBytes) {
				t.Fatalf("truncated=%v size=%d", data["truncated"], tc.size)
			}
		})
	}
	for _, path := range []string{"missing", "."} {
		if _, err := broker.filesRead(map[string]any{"path": path}); err == nil {
			t.Fatalf("expected read error for %q", path)
		}
	}
}

func BenchmarkBrokerReadFileLarge(b *testing.B) {
	root := b.TempDir()
	file, err := os.Create(filepath.Join(root, "large.txt"))
	if err != nil {
		b.Fatal(err)
	}
	if err := file.Truncate(32 * 1024 * 1024); err != nil {
		b.Fatal(err)
	}
	if err := file.Close(); err != nil {
		b.Fatal(err)
	}
	broker, err := NewBroker(root, &recordingSink{})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := broker.filesRead(map[string]any{"path": "large.txt"}); err != nil {
			b.Fatal(err)
		}
	}
}
