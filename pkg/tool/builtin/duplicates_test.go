package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func TestFindDuplicatesTool_HelperPresence(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	// Presence of an optional executable must not turn real duplicates into a clean report.
	if err := os.WriteFile(filepath.Join(bin, "jscpd"), []byte("#!/bin/sh\nexit 42\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	code := "alpha()\nbeta()\ngamma()"
	writeFile(t, filepath.Join(dir, "a.go"), code)
	writeFile(t, filepath.Join(dir, "b.go"), code)
	tool := &FindDuplicatesTool{}
	tool.SetWorkDir(dir)
	result, err := tool.Execute(map[string]any{"path": ".", "min_lines": float64(3)})
	if err != nil || !result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Data["count"] != 1 {
		t.Fatalf("count=%v, want one repeated window", result.Data["count"])
	}
}

func TestFindDuplicatesTool_NormalizedWindowOracle(t *testing.T) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(19))
	normalize := regexp.MustCompile(`\s+`)
	choices := []string{"", "  ", "alpha()", "  beta(  x )\t", "\treturn \"a  b\"", "# comment", "// comment", "λ(文)", "\fcontrol\v", "tail\r"}
	for trial := 0; trial < 40; trial++ {
		var lines []string
		for i := 0; i < 40; i++ {
			lines = append(lines, choices[rng.Intn(len(choices))])
		}
		source := strings.Join(lines, "\n")
		for width := 1; width <= 9; width++ {
			a := duplicateAnalysis{sourceScan: sourceScan{ctx: ctx}, minLines: width, index: make(map[duplicateKey]*duplicateGroup), comparisonRemaining: DefaultDuplicateComparisonBytes}
			for _, path := range []string{"z.go", "a.go"} {
				if err := a.file(path, source); err != nil {
					t.Fatal(err)
				}
			}
			want := map[string]int{}
			for i := 0; i+width <= len(lines); i++ {
				significant := 0
				for _, line := range lines[i : i+width] {
					trimmed := strings.TrimSpace(line)
					if trimmed != "" && !strings.HasPrefix(trimmed, "//") && !strings.HasPrefix(trimmed, "#") {
						significant++
					}
				}
				if significant < (width+1)/2 {
					continue
				}
				want[normalize.ReplaceAllString(strings.Join(lines[i:i+width], "\n"), " ")] += 2
			}
			got := map[string]int{}
			for _, bucket := range a.index {
				for group := bucket; group != nil; group = group.next {
					file := a.snapshots[group.first.file]
					first, last := file.lines[group.first.line], file.lines[group.first.line+width-1]
					text := file.normalized[first.start:last.end]
					got[text] = group.count
					if file.path != "a.go" {
						t.Fatalf("first location=%s, want deterministic a.go", file.path)
					}
					key := duplicateFingerprint(first, last)
					var direct [2]uint64
					for i := 0; i < len(text); i++ {
						direct[0] = direct[0]*duplicateHashBase1 + uint64(text[i]) + 1
						direct[1] = direct[1]*duplicateHashBase2 + uint64(text[i]) + 1
					}
					if key.hash != direct || key.length != len(text) {
						t.Fatal("rolling fingerprint differs from direct window fingerprint")
					}
				}
			}
			if len(got) != len(want) {
				t.Fatalf("trial=%d width=%d groups=%d want=%d", trial, width, len(got), len(want))
			}
			for code, count := range want {
				if got[code] != count {
					t.Fatalf("trial=%d width=%d code=%q count=%d want=%d", trial, width, code, got[code], count)
				}
			}
		}
	}
}

func TestFindDuplicatesTool_HashCollisionDoesNotMergeText(t *testing.T) {
	a := duplicateAnalysis{sourceScan: sourceScan{ctx: context.Background()}, minLines: 2, index: make(map[duplicateKey]*duplicateGroup), comparisonRemaining: DefaultDuplicateComparisonBytes}
	if err := a.file("a.go", "alpha()\nbeta()"); err != nil {
		t.Fatal(err)
	}
	other, err := a.snapshot("b.go", "gamma()\ndelta()")
	if err != nil {
		t.Fatal(err)
	}
	key := duplicateFingerprint(other.lines[0], other.lines[1])
	for old, group := range a.index {
		delete(a.index, old)
		a.index[key] = group
		break
	}
	if err := a.file(other.path, other.source); err != nil {
		t.Fatal(err)
	}
	group := a.index[key]
	if group == nil || group.next == nil || group.count != 1 || group.next.count != 1 {
		t.Fatal("different normalized text merged on fingerprint collision")
	}
}

func TestFindDuplicatesTool_BoundedOutput(t *testing.T) {
	dir := t.TempDir()
	var source strings.Builder
	for repeat := 0; repeat < 5; repeat++ {
		for i := 0; i < 24; i++ {
			fmt.Fprintf(&source, "candidate_%02d(%s)\n", i, strings.Repeat("\"\\λ\t", 120))
		}
	}
	writeFile(t, filepath.Join(dir, "repeated.go"), source.String())
	tool := &FindDuplicatesTool{}
	tool.SetWorkDir(dir)
	for _, budget := range []int{DefaultDuplicateOutputBytes, 2048} {
		tool.SetMaxOutputBytes(budget)
		result, err := tool.Execute(map[string]any{"path": "repeated.go", "min_lines": json.Number("1")})
		if err != nil || !result.Success {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		groups := result.Data["duplicates"].([]map[string]any)
		if result.Data["count"] != 24 || result.Data["returned"] != len(groups) || result.Data["truncated"] != true || len(groups) == 0 || len(groups) > DefaultDuplicateGroups {
			t.Fatalf("bad counts: %+v", result.Data)
		}
		for _, group := range groups {
			points := group["locations"].([]map[string]any)
			if group["count"] != 5 || group["returned_locations"] != len(points) || len(points) < 2 || len(points) > DefaultDuplicateLocations || group["locations_truncated"] != true || group["code_truncated"] != true || !utf8.ValidString(group["code"].(string)) {
				t.Fatalf("bad bounded group: %+v", group)
			}
			if len(group["code"].(string)) > duplicateExcerptBytes {
				t.Fatal("excerpt exceeds budget")
			}
		}
		for _, useToon := range []bool{true, false} {
			encoded, err := toon.New(useToon).MarshalString(result)
			if err != nil || len(encoded) > budget {
				t.Fatalf("TOON=%v bytes=%d budget=%d err=%v", useToon, len(encoded), budget, err)
			}
		}
	}
	tool.SetMaxOutputBytes(20)
	result, err := tool.Execute(map[string]any{"path": "repeated.go", "min_lines": 1})
	if err != nil || result.Success || !strings.Contains(result.Error, "output budget") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestFindDuplicatesTool_ParametersAndFailureBudgets(t *testing.T) {
	for _, tc := range []struct {
		name, source, message string
		params                map[string]any
		limit                 int64
	}{
		{name: "fractional", source: "code()", params: map[string]any{"min_lines": 1.5}, message: "positive integer"},
		{name: "negative", source: "code()", params: map[string]any{"min_lines": -1}, message: "positive integer"},
		{name: "zero", source: "code()", params: map[string]any{"min_lines": 0}, message: "positive integer"},
		{name: "too large", source: "code()", params: map[string]any{"min_lines": DefaultDuplicateLines + 1}, message: "must not exceed"},
		{name: "language", source: "code()", params: map[string]any{"language": "unknown"}, message: "unsupported language"},
		{name: "NUL", source: "code\x00()", message: "UTF-8 text"},
		{name: "invalid UTF8", source: "code\xff()", message: "UTF-8 text"},
		{name: "source bytes", source: "code()", limit: 3, message: "byte budget"},
		{name: "line count", source: strings.Repeat("\n", DefaultDuplicateLines), message: "line budget"},
		{name: "verification work", source: strings.Repeat(strings.Repeat("x", 2000)+"\n", 1000), params: map[string]any{"min_lines": 200}, message: "verification byte budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "source.go"), tc.source)
			tool := &FindDuplicatesTool{}
			tool.SetWorkDir(dir)
			tool.SetMaxFileSizeBytes(tc.limit)
			params := map[string]any{"path": "source.go"}
			for key, value := range tc.params {
				params[key] = value
			}
			result, err := tool.Execute(params)
			if err != nil || result.Success || !strings.Contains(result.Error, tc.message) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestFindDuplicatesTool_DirectoryFilterAndCancellation(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.py", "c.java", "d.rs", "e.cpp", "f.js", "g.ts", "h.c"} {
		writeFile(t, filepath.Join(dir, name), "alpha()\nbeta()\ngamma()")
	}
	for _, name := range []string{"node_modules", ".git", "vendor", ".venv"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, name, "bad.go"), "\x00")
	}
	writeFile(t, filepath.Join(dir, "ignored.txt"), "\x00")
	tool := &FindDuplicatesTool{}
	tool.SetWorkDir(dir)
	for _, tc := range []struct {
		language     any
		files, count int
	}{{nil, 8, 1}, {"go", 1, 0}, {"python", 1, 0}, {"cpp", 1, 0}} {
		result, err := tool.Execute(map[string]any{"path": ".", "language": tc.language, "min_lines": 3})
		if err != nil || !result.Success || result.Data["files_analyzed"] != tc.files || result.Data["count"] != tc.count {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	tool.SetMaxFileSizeBytes(30)
	result, err := tool.Execute(map[string]any{"path": "."})
	if err != nil || result.Success || !strings.Contains(result.Error, "byte budget") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = tool.ExecuteWithContext(ctx, map[string]any{"path": "."})
	if err != nil || result.Success || !strings.Contains(result.Error, "canceled") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func BenchmarkFindDuplicatesTool_RepeatedFiles(b *testing.B) {
	b.Setenv("PATH", b.TempDir())
	dir := b.TempDir()
	var source strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&source, "var value%04d = operation(%04d)\n", i, i)
	}
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source.String()), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	tool := &FindDuplicatesTool{}
	params := map[string]any{"path": dir, "min_lines": float64(20)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := tool.Execute(params)
		if err != nil || !result.Success || result.Data["count"] != 1982 {
			b.Fatalf("count=%v success=%v err=%v", result.Data["count"], result.Success, err)
		}
	}
}

func TestFindDuplicatesTool_BlankWindows(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "empty.go"), "\n\n// only a comment\n\n")
	tool := &FindDuplicatesTool{}
	result, err := tool.Execute(map[string]any{"path": filepath.Join(dir, "empty.go"), "min_lines": float64(1)})
	if err != nil || !result.Success || result.Data["count"] != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
