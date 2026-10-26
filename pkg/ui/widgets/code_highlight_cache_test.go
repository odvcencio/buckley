package widgets

import (
	"reflect"
	"runtime"
	"strings"
	"testing"
	"weak"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"m31labs.dev/buckley/pkg/ui/scrollback"
)

func TestChatView_HighlighterCacheBoundsAndRebuildsEvictedLanguages(t *testing.T) {
	view := NewChatView()
	languages := []string{"go", "python", "javascript", "rust", "c", "cpp", "java", "ruby", "bash", "json", "yaml", "css"}
	var first *gotreesitter.Highlighter
	for i, name := range languages[:maxCachedHighlighters] {
		entry := grammars.DetectLanguageByName(name)
		if entry == nil || entry.Language == nil {
			t.Fatalf("missing fixture grammar %q", name)
		}
		h, ok := view.highlighterFor(entry)
		if !ok {
			t.Fatalf("cannot highlight fixture language %q", name)
		}
		if i == 0 {
			first = h
		}
	}
	goEntry := grammars.DetectLanguageByName("go")
	if h, ok := view.highlighterFor(goEntry); !ok || h != first {
		t.Fatal("recent language was recompiled")
	}
	entry := grammars.DetectLanguageByName("bash")
	if _, ok := view.highlighterFor(entry); !ok {
		t.Fatal("new language was not highlighted")
	}
	for _, cached := range view.highlighters {
		if cached.language == "python" {
			t.Fatal("least recently used language was retained")
		}
	}
	if h, ok := view.highlighterFor(goEntry); !ok || h != first {
		t.Fatal("active language was evicted before an older language")
	}
	code := "package main\nfunc greet() { println(\"café 日本語 🐱\") }"
	before := []scrollback.Line{{Content: "package main"}, {Content: "func greet() { println(\"café 日本語 🐱\") }"}}
	view.highlightCodeBlock(before, "go")
	for range 3 {
		for _, name := range languages {
			entry := grammars.DetectLanguageByName(name)
			if entry == nil || entry.Language == nil {
				t.Fatalf("missing fixture grammar %q", name)
			}
			if _, ok := view.highlighterFor(entry); !ok {
				t.Fatalf("cannot rebuild highlighter %q", name)
			}
			if len(view.highlighters) > maxCachedHighlighters {
				t.Fatalf("cached %d languages", len(view.highlighters))
			}
		}
	}
	after := []scrollback.Line{{Content: "package main"}, {Content: "func greet() { println(\"café 日本語 🐱\") }"}}
	view.highlightCodeBlock(after, "go")
	if !reflect.DeepEqual(before, after) || len(after[1].Spans) < 2 {
		t.Fatal("recreated highlighter changed styles or source text")
	}
	var recovered []string
	for _, line := range after {
		recovered = append(recovered, spansText(line.Spans))
	}
	if strings.Join(recovered, "\n") != code {
		t.Fatal("highlighting changed Unicode code bytes")
	}
}

func TestChatView_ClearReleasesCachedHighlighters(t *testing.T) {
	view := NewChatView()
	ref := func() weak.Pointer[gotreesitter.Highlighter] {
		entry := grammars.DetectLanguageByName("go")
		h, ok := view.highlighterFor(entry)
		if !ok {
			t.Fatal("cannot create Go highlighter")
		}
		return weak.Make(h)
	}()
	runtime.GC()
	if ref.Value() == nil {
		t.Fatal("active cache did not retain its highlighter")
	}
	view.Clear()
	runtime.GC()
	if len(view.highlighters) != 0 || ref.Value() != nil {
		t.Fatal("cleared transcript retained highlighter state")
	}
	entry := grammars.DetectLanguageByName("go")
	if _, ok := view.highlighterFor(entry); !ok || len(view.highlighters) != 1 {
		t.Fatal("cleared view could not highlight a new message")
	}
	runtime.KeepAlive(view)
}

func TestChatView_HighlightAfterGrammarUnloadUsesCachedParser(t *testing.T) {
	_, previousLimit := grammars.EmbeddedLanguageCacheStats()
	grammars.SetEmbeddedLanguageCacheLimit(16)
	t.Cleanup(func() { grammars.SetEmbeddedLanguageCacheLimit(previousLimit) })
	view := NewChatView()
	before := []scrollback.Line{{Content: "package main"}, {Content: "func greet() { println(\"café 日本語\") }"}}
	view.highlightCodeBlock(before, "go")
	if !grammars.UnloadEmbeddedLanguage("go.bin") {
		t.Fatal("fixture grammar was not cached")
	}
	loaded, _ := grammars.EmbeddedLanguageCacheStats()
	after := []scrollback.Line{{Content: "package main"}, {Content: "func greet() { println(\"café 日本語\") }"}}
	view.highlightCodeBlock(after, "go")
	if current, _ := grammars.EmbeddedLanguageCacheStats(); current != loaded {
		t.Fatal("cached highlighter decoded its grammar again")
	}
	if !reflect.DeepEqual(before, after) || len(after[1].Spans) < 2 {
		t.Fatal("grammar eviction changed cached highlighting or Unicode source")
	}
}
