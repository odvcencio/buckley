package execmode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

type excerptSearchResult struct {
	Matches []struct {
		File          string
		Line          int
		Text          string
		TextTruncated bool `json:"text_truncated"`
	}
	Capped bool
}

func TestBrokerSearchText_Excerpts(t *testing.T) {
	for _, tc := range []struct {
		name, source, pattern string
		truncated             bool
	}{
		{"exact", "needle" + strings.Repeat("x", maxSearchMatchTextBytes-6), "needle", false},
		{"one_over", "needle" + strings.Repeat("x", maxSearchMatchTextBytes-5), "needle", true},
		{"late_match", strings.Repeat("x", 8<<10) + "needle tail", "needle", true},
		{"unicode", strings.Repeat("界", 1000) + "needle" + strings.Repeat("界", 1000), "needle", true},
		{"whitespace_literal", "\u2003needle-skip" + strings.Repeat("x", 8<<10) + " needle ", " needle ", true},
		{"long_literal", "head" + strings.Repeat("needle", 1000) + "tail", strings.Repeat("needle", 1000), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			broker, err := NewBroker(root, &recordingSink{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := broker.searchText(context.Background(), map[string]any{"pattern": tc.pattern})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var decoded excerptSearchResult
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded.Matches) != 1 || decoded.Capped {
				t.Fatalf("match coverage changed: %+v", decoded)
			}
			hit := decoded.Matches[0]
			if hit.File != "source.txt" || hit.Line != 1 || hit.TextTruncated != tc.truncated || len(hit.Text) > maxSearchMatchTextBytes || !utf8.ValidString(hit.Text) || !strings.Contains(hit.Text, "needle") {
				t.Fatalf("excerpt: file=%q line=%d truncated=%v bytes=%d", hit.File, hit.Line, hit.TextTruncated, len(hit.Text))
			}
			if !tc.truncated && hit.Text != tc.source {
				t.Fatal("complete short text changed")
			}
			if tc.name == "whitespace_literal" && strings.Contains(hit.Text, "-skip") {
				t.Fatal("excerpt centered on a nonmatching occurrence")
			}
		})
	}
}

func TestBrokerSearchText_TextBudgetPreservesLocations(t *testing.T) {
	root := t.TempDir()
	line := "needle" + strings.Repeat("x", maxSearchMatchTextBytes)
	const count = 70
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte(strings.Repeat(line+"\n", count)), 0600); err != nil {
		t.Fatal(err)
	}
	broker, err := NewBroker(root, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	for call := 0; call < 2; call++ {
		result, err := broker.searchText(context.Background(), map[string]any{"pattern": "needle"})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var decoded excerptSearchResult
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Capped || len(decoded.Matches) != count {
			t.Fatalf("text budget dropped match locations: matches=%d capped=%v", len(decoded.Matches), decoded.Capped)
		}
		total, omitted := 0, 0
		for i, hit := range decoded.Matches {
			if hit.File != "source.txt" || hit.Line != i+1 || !hit.TextTruncated || len(hit.Text) > maxSearchMatchTextBytes {
				t.Fatalf("hit %d changed: bytes=%d truncated=%v", i, len(hit.Text), hit.TextTruncated)
			}
			total += len(hit.Text)
			if hit.Text == "" {
				omitted++
			} else if !strings.Contains(hit.Text, "needle") {
				t.Fatal("nonempty excerpt lost the literal")
			}
		}
		if total != maxSearchTextBytes || omitted == 0 {
			t.Fatalf("text budget: total=%d omitted=%d", total, omitted)
		}
	}
}

func TestRunner_SearchExcerptClient(t *testing.T) {
	if DetectIsolation() != IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte(strings.Repeat("x", 8<<10)+"needle"), 0600); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(root, &recordingSink{}, DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	source := `package main
import("fmt";"strings";"execprogram/caps")
func main(){hits,capped,err:=caps.SearchTextGlob("needle","*.txt");if err!=nil{panic(err)};fmt.Printf("%d %t %t %t",len(hits),hits[0].TextTruncated,strings.Contains(hits[0].Text,"needle"),capped)}`
	result, err := runner.Run(context.Background(), source)
	if err != nil || result.ExitCode != 0 || result.Stdout != "1 true true false" {
		t.Fatalf("typed excerpt client: result=%+v error=%v", result, err)
	}
}
