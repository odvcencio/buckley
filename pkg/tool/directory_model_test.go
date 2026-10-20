package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestToModelOutput_DirectoryPagesKeepStructuredContinuation(t *testing.T) {
	t.Cleanup(func() { SetResultEncoding(true) })
	dir := t.TempDir()
	for i := 0; i < 45; i++ {
		name := fmt.Sprintf("%03d-", i) + strings.Repeat("界&", 40) + ".txt"
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, useToon := range []bool{false, true} {
		SetResultEncoding(useToon)
		list := &builtin.ListDirectoryTool{}
		list.SetWorkDir(dir)
		list.SetMaxOutputBytes(2048)
		cursor := ""
		returned := 0
		for range 45 {
			r, err := list.Execute(map[string]any{"cursor": cursor, "limit": 1000})
			if err != nil || !r.Success {
				t.Fatal(r, err)
			}
			wire, err := ToModelOutput(r)
			if err != nil || len(wire) > 2048 || strings.Contains(wire, "output_head") {
				t.Fatalf("page lost structure: %d %v", len(wire), err)
			}
			files := r.Data["files"].([]map[string]any)
			returned += len(files)
			if !useToon {
				var decoded builtin.Result
				if err := json.Unmarshal([]byte(wire), &decoded); err != nil {
					t.Fatal(err)
				}
				if len(decoded.Data["files"].([]any)) != len(files) || decoded.Data["count"] != float64(45) {
					t.Fatal("JSON page changed records or total")
				}
			}
			next, more := r.Data["next_cursor"].(string)
			if !more {
				break
			}
			if !strings.Contains(wire, next) {
				t.Fatal("model lost opaque continuation")
			}
			cursor = next
		}
		if returned != 45 {
			t.Fatalf("model received %d entries", returned)
		}
	}
}
