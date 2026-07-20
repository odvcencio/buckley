package reviewvalidate

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestRepoFileSourceResolvesPartialPaths verifies the resolution reviews depend
// on: a bare filename cited without its directory must still ground to the real
// file, or the validator flags real code as fabricated (the false-positive that
// showed up dogfooding on the real K3 review).
func TestRepoFileSourceResolvesPartialPaths(t *testing.T) {
	root := t.TempDir()
	must := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("grammars/hack_scanner.go", "package grammars\nfunc Deserialize() {}\n")
	must("cgo_harness/docker/Dockerfile", "FROM x\nRUN install tree-sitter-cli\n")

	src, err := NewRepoFileSource(root)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	// Bare filename → resolves to the subdirectory file.
	if _, ok := src.ReadFile("hack_scanner.go"); !ok {
		t.Fatalf("bare filename should resolve to grammars/hack_scanner.go")
	}
	// Suffix path → resolves.
	if _, ok := src.ReadFile("docker/Dockerfile"); !ok {
		t.Fatalf("suffix path should resolve to cgo_harness/docker/Dockerfile")
	}
	// Truly absent → missing.
	if _, ok := src.ReadFile("ghost_file.go"); ok {
		t.Fatalf("nonexistent file must not resolve")
	}
	// A bare filename cited without a directory still verifies its finding.
	v := ValidateRef(src, Ref{Path: "hack_scanner.go", Line: 2}, []string{"Deserialize"}, 3)
	if v.Status != StatusVerified {
		t.Fatalf("expected verified for resolved bare filename, got %s", v.Status)
	}
}

type mapSource map[string]string

func (m mapSource) ReadFile(path string) ([]byte, bool) {
	c, ok := m[path]
	return []byte(c), ok
}

func TestExtractRefs(t *testing.T) {
	text := "See `hack_scanner.go:79-89` and grammars/support.go:87, plus .github/workflows/ci.yml:5. " +
		"Version 0.25.1 and e.g. Node.js are not refs. Bare parser.go is too ambiguous."
	refs := ExtractRefs(text)

	got := map[string][2]int{}
	for _, r := range refs {
		got[r.Path] = [2]int{r.Line, r.EndLine}
	}
	want := map[string][2]int{
		// has a :line
		"hack_scanner.go": {79, 89},
		// has a slash + :line
		"grammars/support.go":      {87, 0},
		".github/workflows/ci.yml": {5, 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractRefs mismatch:\n got  %v\n want %v", got, want)
	}
	// Prose/version tokens and bare filenames must NOT be extracted.
	for _, r := range refs {
		if r.Path == "0.25.1" || r.Path == "Node.js" || r.Path == "parser.go" {
			t.Fatalf("extracted a non-source/ambiguous ref: %q", r.Path)
		}
	}
}

func TestValidateRefStatuses(t *testing.T) {
	src := mapSource{
		"grammars/hack_scanner.go": "package grammars\n\nfunc (s *state) Deserialize(buf []byte) {\n\tif len(buf) == 0 {\n\t\treturn\n\t}\n\ts.a = buf[1]\n}\n",
	}
	tokens := []string{"Deserialize", "buf"}

	// Verified: real file, in-range line, grounded token nearby.
	if v := ValidateRef(src, Ref{Path: "grammars/hack_scanner.go", Line: 3}, tokens, 3); v.Status != StatusVerified {
		t.Fatalf("expected verified, got %s (%+v)", v.Status, v)
	}
	// File missing (hallucinated path).
	if v := ValidateRef(src, Ref{Path: "grammars/ghost.go", Line: 10}, tokens, 3); v.Status != StatusFileMissing {
		t.Fatalf("expected file_missing, got %s", v.Status)
	}
	// Line out of range.
	if v := ValidateRef(src, Ref{Path: "grammars/hack_scanner.go", Line: 9999}, tokens, 3); v.Status != StatusLineOutOfRange {
		t.Fatalf("expected line_out_of_range, got %s", v.Status)
	}
	// Ungrounded: valid file/line but the claimed token isn't anywhere near it.
	if v := ValidateRef(src, Ref{Path: "grammars/hack_scanner.go", Line: 1}, []string{"NonexistentSymbolXYZ"}, 0); v.Status != StatusUngrounded {
		t.Fatalf("expected ungrounded, got %s", v.Status)
	}
	// Unlocated: file exists, no line cited.
	if v := ValidateRef(src, Ref{Path: "grammars/hack_scanner.go"}, tokens, 3); v.Status != StatusUnlocated {
		t.Fatalf("expected unlocated, got %s", v.Status)
	}
}

func TestGroundingTokens(t *testing.T) {
	toks := GroundingTokens("The `Deserialize` method on `hackScannerState` panics; this is a high issue.")
	has := func(s string) bool {
		for _, t := range toks {
			if t == s {
				return true
			}
		}
		return false
	}
	if !has("Deserialize") || !has("hackScannerState") {
		t.Fatalf("expected distinctive identifiers, got %v", toks)
	}
	// Stopwords/short words must be dropped.
	for _, bad := range []string{"this", "high", "issue", "The"} {
		if has(bad) {
			t.Fatalf("stopword leaked into grounding tokens: %q", bad)
		}
	}
}

// TestLineDriftTolerance: reviews often cite approximate line numbers; grounding
// should still verify when the token is within tolerance of the cited line.
func TestLineDriftTolerance(t *testing.T) {
	src := mapSource{"a.go": "l1\nl2\nl3\nfunc Target() {}\nl5\nl6\n"} // Target on line 4
	// Cited line 6 (drifted by 2); tolerance 3 should ground it line-accurately.
	if v := ValidateRef(src, Ref{Path: "a.go", Line: 6}, []string{"Target"}, 3); v.Status != StatusVerified {
		t.Fatalf("expected drift-tolerant verify, got %s", v.Status)
	}
	// Tolerance 0: token isn't near line 6 but IS in the file → line_drifted (a
	// real claim with a stale line, NOT a fabrication).
	if v := ValidateRef(src, Ref{Path: "a.go", Line: 6}, []string{"Target"}, 0); v.Status != StatusLineDrifted {
		t.Fatalf("expected line_drifted at tolerance 0, got %s", v.Status)
	}
	// A token that appears nowhere in the file → ungrounded (fabrication signal).
	if v := ValidateRef(src, Ref{Path: "a.go", Line: 6}, []string{"GhostSymbol"}, 3); v.Status != StatusUngrounded {
		t.Fatalf("expected ungrounded for a token absent from the file, got %s", v.Status)
	}
}
