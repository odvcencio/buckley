package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runMixedEdit(t *testing.T, input string, build func(path string) map[string]any) (*Result, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := (&EditFileTool{}).Execute(build(path))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	return res, string(got)
}

func edit(o, n string) map[string]any { return map[string]any{"old_string": o, "new_string": n} }

func TestEditFileMixedEditsAndTopLevel(t *testing.T) {
	t.Run("edits only", func(t *testing.T) {
		res, got := runMixedEdit(t, "a b", func(p string) map[string]any {
			return map[string]any{"path": p, "edits": []any{edit("a", "A"), edit("b", "B")}}
		})
		if !res.Success || got != "A B" {
			t.Fatalf("%+v %q", res, got)
		}
	})
	t.Run("top level only", func(t *testing.T) {
		res, got := runMixedEdit(t, "a b", func(p string) map[string]any {
			return map[string]any{"path": p, "old_string": "a", "new_string": "A"}
		})
		if !res.Success || got != "A b" {
			t.Fatalf("%+v %q", res, got)
		}
	})
	t.Run("both duplicate applies once", func(t *testing.T) {
		res, got := runMixedEdit(t, "a b", func(p string) map[string]any {
			return map[string]any{"path": p, "old_string": "a", "new_string": "A", "edits": []any{edit("a", "A"), edit("b", "B")}}
		})
		if !res.Success || got != "A B" {
			t.Fatalf("%+v %q", res, got)
		}
	})
	t.Run("empty placeholder top level is ignored", func(t *testing.T) {
		res, got := runMixedEdit(t, "a b", func(p string) map[string]any {
			return map[string]any{"path": p, "old_string": "", "new_string": "", "replace_all": false, "edits": []any{edit("a", "A")}}
		})
		if !res.Success || got != "A b" {
			t.Fatalf("%+v %q", res, got)
		}
	})
	t.Run("both different appends", func(t *testing.T) {
		res, got := runMixedEdit(t, "a b c", func(p string) map[string]any {
			return map[string]any{"path": p, "old_string": "c", "new_string": "C", "edits": []any{edit("a", "A"), edit("b", "B")}}
		})
		if !res.Success || got != "A B C" {
			t.Fatalf("%+v %q", res, got)
		}
	})
	t.Run("different top level that conflicts fails atomically", func(t *testing.T) {
		res, got := runMixedEdit(t, "a b", func(p string) map[string]any {
			return map[string]any{"path": p, "old_string": "zzz", "new_string": "Z", "edits": []any{edit("a", "A")}}
		})
		if res.Success || got != "a b" {
			t.Fatalf("%+v %q", res, got)
		}
	})
	t.Run("incomplete top level gives retry text", func(t *testing.T) {
		res, got := runMixedEdit(t, "a b", func(p string) map[string]any {
			return map[string]any{"path": p, "old_string": "a", "edits": []any{edit("a", "A")}}
		})
		want := "Send either `edits` or old_string/new_string, not both; retry with only `edits`."
		if res.Success || !strings.Contains(res.Error, want) || got != "a b" {
			t.Fatalf("%+v %q", res, got)
		}
	})
	t.Run("lone replace_all false is ignored", func(t *testing.T) {
		res, got := runMixedEdit(t, "a", func(p string) map[string]any {
			return map[string]any{"path": p, "replace_all": false, "edits": []any{edit("a", "A")}}
		})
		if !res.Success || got != "A" {
			t.Fatalf("%+v %q", res, got)
		}
	})
	t.Run("lone replace_all true is rejected", func(t *testing.T) {
		res, got := runMixedEdit(t, "a", func(p string) map[string]any {
			return map[string]any{"path": p, "replace_all": true, "edits": []any{edit("a", "A")}}
		})
		if res.Success || !strings.Contains(res.Error, "retry with only `edits`") || got != "a" {
			t.Fatalf("%+v %q", res, got)
		}
	})
	t.Run("empty edits error text", func(t *testing.T) {
		res, _ := runMixedEdit(t, "a", func(p string) map[string]any {
			return map[string]any{"path": p, "edits": []any{}}
		})
		if res.Success || !strings.Contains(res.Error, "retry with only `edits`") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("empty edits with top level applies top level", func(t *testing.T) {
		res, got := runMixedEdit(t, "a", func(p string) map[string]any {
			return map[string]any{"path": p, "old_string": "a", "new_string": "A", "edits": []any{}}
		})
		if !res.Success || got != "A" {
			t.Fatalf("%+v %q", res, got)
		}
	})
}
