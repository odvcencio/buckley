package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestConfigureGrammarCache_DefaultsOverridesAndIdleEviction(t *testing.T) {
	if scenario := os.Getenv("BUCKLEY_TEST_GRAMMAR_CACHE"); scenario != "" {
		configureGrammarCache()
		defer grammars.SetEmbeddedLanguageIdleTTL(0)
		want := defaultGrammarCacheLimit
		switch scenario {
		case "bounded", "idle":
			want = 2
		case "disabled":
			want = 0
		case "unlimited":
			want = -1
		}
		if _, limit := grammars.EmbeddedLanguageCacheStats(); limit != want {
			t.Fatalf("limit=%d, want %d", limit, want)
		}
		wantTTL, wantSweep := defaultGrammarIdleTTL, defaultGrammarIdleSweep
		switch scenario {
		case "bounded", "disabled", "unlimited":
			wantTTL = 0
		case "idle":
			wantTTL, wantSweep = 50*time.Millisecond, 5*time.Millisecond
		}
		if ttl, sweep := grammars.EmbeddedLanguageIdleConfig(); ttl != wantTTL || sweep != wantSweep {
			t.Fatalf("idle settings=(%s,%s), want (%s,%s)", ttl, sweep, wantTTL, wantSweep)
		}
		entry := grammars.DetectLanguageByName("json")
		highlighter, err := gotreesitter.NewHighlighter(entry.Language(), entry.HighlightQuery)
		if err != nil {
			t.Fatal(err)
		}
		languages := []string{"go", "css", "json", "python", "javascript", "rust", "c", "cpp", "java", "ruby", "bash", "yaml", "html", "toml", "sql", "c_sharp", "typescript", "lua"}
		for _, name := range languages {
			entry := grammars.DetectLanguageByName(name)
			if entry == nil || entry.Language == nil || entry.Language() == nil {
				t.Fatalf("missing fixture grammar %q", name)
			}
			if loaded, _ := grammars.EmbeddedLanguageCacheStats(); want >= 0 && loaded > want {
				t.Fatalf("loaded=%d exceeds limit=%d", loaded, want)
			}
		}
		if loaded, _ := grammars.EmbeddedLanguageCacheStats(); want < 0 && loaded < len(languages) {
			t.Fatalf("unlimited cache retained only %d grammars", loaded)
		}
		if scenario == "idle" {
			deadline := time.Now().Add(3 * time.Second)
			for {
				loaded, _ := grammars.EmbeddedLanguageCacheStats()
				if loaded == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("idle eviction retained %d grammars", loaded)
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		if ranges := highlighter.Highlight([]byte(`{"answer":42}`)); len(ranges) == 0 {
			t.Fatal("cache eviction invalidated an active highlighter")
		}
		return
	}
	for _, scenario := range []string{"defaults", "invalid", "bounded", "disabled", "unlimited", "idle"} {
		t.Run(scenario, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestConfigureGrammarCache_DefaultsOverridesAndIdleEviction$", "-test.v")
			for _, value := range os.Environ() {
				key, _, _ := strings.Cut(value, "=")
				if key == "BUCKLEY_TEST_GRAMMAR_CACHE" || key == "GOTREESITTER_GRAMMAR_CACHE_LIMIT" || key == "GOTREESITTER_GRAMMAR_IDLE_TTL" || key == "GOTREESITTER_GRAMMAR_IDLE_SWEEP" {
					continue
				}
				cmd.Env = append(cmd.Env, value)
			}
			cmd.Env = append(cmd.Env, "BUCKLEY_TEST_GRAMMAR_CACHE="+scenario)
			switch scenario {
			case "invalid":
				cmd.Env = append(cmd.Env, "GOTREESITTER_GRAMMAR_CACHE_LIMIT=invalid", "GOTREESITTER_GRAMMAR_IDLE_TTL=invalid", "GOTREESITTER_GRAMMAR_IDLE_SWEEP=invalid")
			case "bounded":
				cmd.Env = append(cmd.Env, "GOTREESITTER_GRAMMAR_CACHE_LIMIT=2", "GOTREESITTER_GRAMMAR_IDLE_TTL=0s")
			case "disabled":
				cmd.Env = append(cmd.Env, "GOTREESITTER_GRAMMAR_CACHE_LIMIT=0", "GOTREESITTER_GRAMMAR_IDLE_TTL=0s")
			case "unlimited":
				cmd.Env = append(cmd.Env, "GOTREESITTER_GRAMMAR_CACHE_LIMIT=-1", "GOTREESITTER_GRAMMAR_IDLE_TTL=0s")
			case "idle":
				cmd.Env = append(cmd.Env, "GOTREESITTER_GRAMMAR_CACHE_LIMIT=2", "GOTREESITTER_GRAMMAR_IDLE_TTL=50ms", "GOTREESITTER_GRAMMAR_IDLE_SWEEP=5ms")
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("isolated grammar-cache scenario: %v\n%s", err, out)
			}
		})
	}
}
