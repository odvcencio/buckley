package widgets

import (
	"reflect"
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter/grammars"
	"m31labs.dev/buckley/pkg/ui/scrollback"
)

func decodedGrammarLimit(t testing.TB, limit int) {
	t.Helper()
	_, previousLimit := grammars.EmbeddedLanguageCacheStats()
	previousTTL, _ := grammars.EmbeddedLanguageIdleConfig()
	grammars.SetEmbeddedLanguageIdleTTL(0)
	grammars.SetEmbeddedLanguageCacheLimit(limit)
	grammars.PurgeEmbeddedLanguageCache()
	t.Cleanup(func() {
		grammars.PurgeEmbeddedLanguageCache()
		grammars.SetEmbeddedLanguageCacheLimit(previousLimit)
		grammars.SetEmbeddedLanguageIdleTTL(previousTTL)
	})
}

func grammarHitLines(code string) []scrollback.Line {
	var lines []scrollback.Line
	for _, content := range strings.Split(code, "\n") {
		lines = append(lines, scrollback.Line{Content: content})
	}
	return lines
}

func TestChatView_CachedParserSurvivesGrammarEviction(t *testing.T) {
	for _, tc := range []struct {
		name          string
		limit         int
		otherLanguage bool
	}{{name: "manual unload", limit: 16}, {name: "LRU eviction", limit: 1, otherLanguage: true}} {
		t.Run(tc.name, func(t *testing.T) {
			decodedGrammarLimit(t, tc.limit)
			view := NewChatView()
			const code = "package main\nfunc greet() { println(\"café 日本語\") }"
			before := grammarHitLines(code)
			view.highlightCodeBlock(before, "go")
			if len(view.highlighters) != 1 || len(before[1].Spans) < 2 {
				t.Fatal("fixture did not create a highlighted cached Go parser")
			}
			parser := view.highlighters[0].highlighter
			if tc.otherLanguage {
				view.highlightCodeBlock(grammarHitLines("def greet():\n    return 'café 日本語'"), "python")
				if grammars.UnloadEmbeddedLanguage("go.bin") {
					t.Fatal("fixture did not evict the Go grammar")
				}
			} else if !grammars.UnloadEmbeddedLanguage("go.bin") {
				t.Fatal("fixture grammar was not cached")
			}
			loaded, _ := grammars.EmbeddedLanguageCacheStats()
			after := grammarHitLines(code)
			view.highlightCodeBlock(after, "go")
			if grammars.UnloadEmbeddedLanguage("go.bin") {
				t.Fatal("cached highlighter decoded its grammar again")
			}
			if current, _ := grammars.EmbeddedLanguageCacheStats(); current != loaded {
				t.Fatal("cached highlighting changed decoded grammar retention")
			}
			if view.highlighters[len(view.highlighters)-1].highlighter != parser || !reflect.DeepEqual(before, after) {
				t.Fatal("grammar eviction changed the cached parser, source or styles")
			}
		})
	}
}

func BenchmarkChatView_CachedHighlight(b *testing.B) {
	for _, tc := range []struct {
		name  string
		limit int
	}{{name: "default", limit: -1}, {name: "disabled", limit: 0}, {name: "one", limit: 1}} {
		b.Run(tc.name, func(b *testing.B) {
			decodedGrammarLimit(b, tc.limit)
			view := NewChatView()
			blocks := []struct {
				language string
				lines    []scrollback.Line
			}{
				{language: "go", lines: grammarHitLines("package main\nfunc greet() { println(\"café 日本語\") }")},
				{language: "python", lines: grammarHitLines("def greet():\n    return 'café 日本語'")},
			}
			for _, block := range blocks {
				view.highlightCodeBlock(block.lines, block.language)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				block := blocks[i%len(blocks)]
				view.highlightCodeBlock(block.lines, block.language)
			}
		})
	}
}
