package main

import (
	"sort"
	"testing"
)

func TestBundlePaths(t *testing.T) {
	files := []string{"e.go", "a.go", "d.go", "b.go", "c.go"}
	bundles := bundlePaths(files, 2)
	if len(bundles) != 2 {
		t.Fatalf("expected 2 bundles, got %d", len(bundles))
	}
	// Every file appears exactly once, and bundles are size-balanced.
	var all []string
	for _, b := range bundles {
		all = append(all, b...)
	}
	if len(all) != len(files) {
		t.Fatalf("expected %d files across bundles, got %d", len(files), len(all))
	}
	sort.Strings(all)
	for i, want := range []string{"a.go", "b.go", "c.go", "d.go", "e.go"} {
		if all[i] != want {
			t.Fatalf("file set changed: got %v", all)
		}
	}
	if len(bundles[0]) != 3 || len(bundles[1]) != 2 {
		t.Fatalf("expected balanced 3/2 split, got %d/%d", len(bundles[0]), len(bundles[1]))
	}
	// Sorted contiguity keeps directory locality: bundle 0 is the lexicographically
	// first slice.
	if bundles[0][0] != "a.go" {
		t.Fatalf("bundles should be sorted contiguous chunks, got first=%q", bundles[0][0])
	}

	// maxBundles larger than the file count clamps to one-per-file.
	if got := bundlePaths([]string{"x", "y"}, 10); len(got) != 2 {
		t.Fatalf("expected 2 bundles for 2 files, got %d", len(got))
	}
	if got := bundlePaths(nil, 4); got != nil {
		t.Fatalf("expected nil for no files, got %v", got)
	}
}

func TestProjectBundleCount(t *testing.T) {
	cases := map[int]int{0: 2, 100: 2, 499: 2, 500: 2, 750: 3, 1500: 6, 2000: 8, 5000: 8}
	for in, want := range cases {
		if got := projectBundleCount(in); got != want {
			t.Fatalf("projectBundleCount(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestIsNonReviewPath(t *testing.T) {
	cases := map[string]bool{
		"pkg/parser/parser.go":       false,
		"cmd/buckley/main.go":        false,
		"testdata/x.go":              true,
		"pkg/foo/testdata/y.txt":     true,
		"vendor/mod/z.go":            true,
		"corpus_sources/a":           true,
		"node_modules/dep/index.js":  true,
		"README.md":                  false,
		"harness_out/report.json":    true,
	}
	for p, want := range cases {
		if got := isNonReviewPath(p); got != want {
			t.Fatalf("isNonReviewPath(%q) = %v, want %v", p, got, want)
		}
	}
}
