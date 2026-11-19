package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/model"
)

func TestParseStartupOptionsContextFiles(t *testing.T) {
	opts, err := parseStartupOptions([]string{"--context-file", "a.md", "--context-file=b.md", "-p", "question"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(opts.contextFiles, ",") != "a.md,b.md" {
		t.Fatalf("context files = %v, want a.md,b.md in order", opts.contextFiles)
	}
	if _, err := parseStartupOptions([]string{"--context-file"}); err == nil || !strings.Contains(err.Error(), "--context-file requires a path") {
		t.Fatalf("missing path error = %v", err)
	}
}

func TestOneShotContextFilesFormTheSamePrefixOnEveryRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "spec.md"), []byte("# Spec\nrule one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("no trailing newline"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := []string{"spec.md", "notes.txt"}
	first, err := loadOneShotContextFiles(dir, paths)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOneShotContextFiles(dir, paths)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("two runs with the same files differ:\n%s\n%s", first, second)
	}
	if strings.Index(first, `path="spec.md"`) > strings.Index(first, `path="notes.txt"`) {
		t.Fatal("files are not in the order given")
	}
	if !strings.Contains(first, "no trailing newline\n</context_file>") {
		t.Fatalf("file end not closed on its own line:\n%s", first)
	}

	// Two questions about the same files: the marked context part is the
	// same, and only the question after it changes.
	a := oneShotContextMessage(first, "What is rule one?").Content.([]model.ContentPart)
	b := oneShotContextMessage(second, "Is there a rule two?").Content.([]model.ContentPart)
	if len(a) != 2 || !a[0].CacheBreakpoint || a[1].CacheBreakpoint {
		t.Fatalf("parts = %#v, want the context marked as the stable prefix, then the question", a)
	}
	if a[0] != b[0] || a[1].Text == b[1].Text {
		t.Fatal("the stable prefix differs between runs")
	}

	if _, err := loadOneShotContextFiles(dir, []string{"missing.md"}); err == nil || !strings.Contains(err.Error(), "missing.md") {
		t.Fatalf("missing file error = %v", err)
	}
	if block, err := loadOneShotContextFiles(dir, nil); err != nil || block != "" {
		t.Fatalf("no files = %q, %v; want empty", block, err)
	}
}

func TestOneShotContextPromptCacheIsOnWithoutABudget(t *testing.T) {
	var notes bytes.Buffer
	if cache := oneShotContextPromptCache(nil, "anthropic/claude-fable-5-2", 0, &notes); cache == nil || !cache.Enabled {
		t.Fatalf("uncapped run: cache = %#v, want on", cache)
	}
	if notes.Len() != 0 {
		t.Fatalf("uncapped run printed %q", notes.String())
	}
	// A capped run on a model with no known cache prices runs uncached and
	// says which prices are missing; it is not refused.
	if cache := oneShotContextPromptCache(nil, "vendor/model", 0.5, &notes); cache != nil {
		t.Fatalf("capped run without cache prices: cache = %#v, want off", cache)
	}
	if !strings.Contains(notes.String(), "cache_read_per_million") {
		t.Fatalf("note = %q, want the config key", notes.String())
	}
}

func TestOneShotUsageLineIsPrintedWithoutABudget(t *testing.T) {
	result := &agentloop.Result{ModelRequests: 1}
	result.Usage = model.Usage{
		PromptTokens:        9000,
		CompletionTokens:    100,
		PromptTokensDetails: &model.PromptTokensDetails{CachedTokens: 8000},
		CacheWriteTokens:    500,
	}
	line := oneShotUsageLine(nil, "vendor/model", result, 0)
	for _, want := range []string{"Usage detail:", "cache_read_tokens=8000", "cache_write_tokens=500", "estimated_cost_usd=unknown"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q lacks %q", line, want)
		}
	}
	if strings.Contains(line, "Budgeted run") {
		t.Fatalf("uncapped run printed a budget line: %q", line)
	}
	if !strings.Contains(oneShotUsageLine(nil, "vendor/model", result, 0.5), "ceiling_usd=0.500000") {
		t.Fatal("capped run lacks its budget line")
	}
}
