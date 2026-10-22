package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkReadSnapshotString(b *testing.B) {
	for _, size := range []int{80, 4096, 65536, 1 << 20, 8 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "source")
			if err := os.WriteFile(path, []byte(strings.Repeat("a", size)), 0600); err != nil {
				b.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { file.Close() })
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := file.Seek(0, 0); err != nil {
					b.Fatal(err)
				}
				content, exceeded, err := readSnapshotString(file, int64(size), int64(size))
				if err != nil || exceeded || len(content) != size {
					b.Fatalf("length=%d exceeded=%v err=%v", len(content), exceeded, err)
				}
			}
		})
	}
}
