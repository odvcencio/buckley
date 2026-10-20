package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkGenerateDocstring_ExistingLargeDoc(b *testing.B) {
	for _, language := range []string{"go", "js"} {
		b.Run(language, func(b *testing.B) {
			source := "package fixture\n// " + strings.Repeat("Existing documentation. ", 45000) + "\nfunc Add()int{return 7}\n"
			name := "Add"
			if language == "js" {
				source = "/** " + strings.Repeat("Existing documentation. ", 45000) + " */\nexport function add(){return 7;}\n"
				name = "add"
			}
			dir := b.TempDir()
			file := "source." + language
			if err := os.WriteFile(filepath.Join(dir, file), []byte(source), 0600); err != nil {
				b.Fatal(err)
			}
			tool := &GenerateDocstringTool{}
			tool.SetWorkDir(dir)
			params := map[string]any{"file": file, "function_name": name}
			b.ReportAllocs()
			b.SetBytes(int64(len(source)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, err := tool.Execute(params)
				if err != nil || !result.Success || result.Data["message"] != "Function already has documentation" {
					b.Fatalf("existing docs: %+v %v", result, err)
				}
			}
		})
	}
}
