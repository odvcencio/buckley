package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkListDirectoryTool_FirstPage(b *testing.B) {
	for _, count := range []int{100, 20000} {
		b.Run(fmt.Sprintf("entries=%d", count), func(b *testing.B) {
			dir := b.TempDir()
			for i := 0; i < count; i++ {
				file, err := os.Create(filepath.Join(dir, fmt.Sprintf("file-%06d.txt", i)))
				if err != nil {
					b.Fatal(err)
				}
				if err := file.Close(); err != nil {
					b.Fatal(err)
				}
			}
			tool := &ListDirectoryTool{}
			tool.SetWorkDir(dir)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, err := tool.Execute(map[string]any{"path": ".", "limit": 100})
				if err != nil || !result.Success || result.Data["count"] != count {
					b.Fatalf("listing: %+v %v", result, err)
				}
			}
		})
	}
}
