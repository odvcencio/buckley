package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkExplainCode_WholeFile(b *testing.B) {
	for _, tc := range []struct{ name, code string }{
		{"ordinary", strings.Repeat("// padding with data\n", 5000) + "func Add(a,b int)int{return a+b}\n"},
		{"dense", strings.Repeat("for if && ||\n", 20000)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			dir := b.TempDir()
			path := filepath.Join(dir, "source.txt")
			if err := os.WriteFile(path, []byte(tc.code), 0600); err != nil {
				b.Fatal(err)
			}
			tool := &ExplainCodeTool{}
			tool.SetWorkDir(dir)
			params := map[string]any{"file": "source.txt"}
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.code)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, err := tool.Execute(params)
				if err != nil || !result.Success {
					b.Fatalf("explain: %+v %v", result, err)
				}
			}
		})
	}
}
