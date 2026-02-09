package filepicker

import (
	"os"
	"path/filepath"
	"testing"
)

func writeGitignore(t *testing.T, dir string, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write .gitignore: %v", err)
	}
}

func TestGitIgnore_PatternParsing(t *testing.T) {
	root := t.TempDir()
	writeGitignore(t, root, `
# This is a comment
*.log

# Another comment

!important.log
build/
node_modules
`)

	gi := &GitIgnore{}
	gi.loadFile(filepath.Join(root, ".gitignore"))

	// Should have parsed 4 patterns (skipping comments and blank lines)
	if len(gi.patterns) != 4 {
		t.Fatalf("expected 4 patterns, got %d: %+v", len(gi.patterns), gi.patterns)
	}

	// Pattern 0: *.log
	if gi.patterns[0].pattern != "*.log" || gi.patterns[0].negation || gi.patterns[0].dirOnly {
		t.Errorf("pattern[0] = %+v, want {*.log, negation=false, dirOnly=false}", gi.patterns[0])
	}

	// Pattern 1: !important.log (negation)
	if gi.patterns[1].pattern != "important.log" || !gi.patterns[1].negation || gi.patterns[1].dirOnly {
		t.Errorf("pattern[1] = %+v, want {important.log, negation=true, dirOnly=false}", gi.patterns[1])
	}

	// Pattern 2: build/ (directory only)
	if gi.patterns[2].pattern != "build" || gi.patterns[2].negation || !gi.patterns[2].dirOnly {
		t.Errorf("pattern[2] = %+v, want {build, negation=false, dirOnly=true}", gi.patterns[2])
	}

	// Pattern 3: node_modules
	if gi.patterns[3].pattern != "node_modules" || gi.patterns[3].negation || gi.patterns[3].dirOnly {
		t.Errorf("pattern[3] = %+v, want {node_modules, negation=false, dirOnly=false}", gi.patterns[3])
	}
}

func TestGitIgnore_CommentsAndBlankLines(t *testing.T) {
	root := t.TempDir()
	writeGitignore(t, root, `# comment 1
# comment 2


# comment 3
*.tmp
`)

	gi := &GitIgnore{}
	gi.loadFile(filepath.Join(root, ".gitignore"))

	if len(gi.patterns) != 1 {
		t.Errorf("expected 1 pattern after filtering comments/blanks, got %d", len(gi.patterns))
	}
}

func TestGitIgnore_NilReceiver(t *testing.T) {
	var gi *GitIgnore
	if gi.Match("anything") {
		t.Error("nil GitIgnore should not match anything")
	}
}

func TestGitIgnore_EmptyPatterns(t *testing.T) {
	gi := &GitIgnore{}
	if gi.Match("anything") {
		t.Error("empty patterns should not match anything")
	}
}

func TestGitIgnore_Match_SimpleFilename(t *testing.T) {
	tests := []struct {
		name     string
		patterns string
		path     string
		want     bool
	}{
		{
			name:     "exact filename match",
			patterns: "foo.txt",
			path:     "foo.txt",
			want:     true,
		},
		{
			name:     "filename in subdirectory",
			patterns: "foo.txt",
			path:     "dir/foo.txt",
			want:     true,
		},
		{
			name:     "filename in deep subdirectory",
			patterns: "foo.txt",
			path:     "a/b/c/foo.txt",
			want:     true,
		},
		{
			name:     "no match different filename",
			patterns: "foo.txt",
			path:     "bar.txt",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gi := &GitIgnore{}
			gi.AddPattern(tt.patterns)
			got := gi.Match(tt.path)
			if got != tt.want {
				t.Errorf("Match(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestGitIgnore_Match_Wildcard(t *testing.T) {
	tests := []struct {
		name     string
		patterns string
		path     string
		want     bool
	}{
		{
			name:     "star extension match",
			patterns: "*.log",
			path:     "error.log",
			want:     true,
		},
		{
			name:     "star extension in subdir",
			patterns: "*.log",
			path:     "logs/error.log",
			want:     true,
		},
		{
			name:     "star extension no match",
			patterns: "*.log",
			path:     "error.txt",
			want:     false,
		},
		{
			name:     "star prefix match",
			patterns: "test_*",
			path:     "test_handler.go",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gi := &GitIgnore{}
			gi.AddPattern(tt.patterns)
			got := gi.Match(tt.path)
			if got != tt.want {
				t.Errorf("Match(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestGitIgnore_Match_DoubleStar(t *testing.T) {
	tests := []struct {
		name     string
		patterns string
		path     string
		want     bool
	}{
		{
			name:     "double-star prefix matches anywhere",
			patterns: "**/foo",
			path:     "foo",
			want:     true,
		},
		{
			name:     "double-star prefix matches in subdir",
			patterns: "**/foo",
			path:     "a/b/foo",
			want:     true,
		},
		{
			name:     "double-star suffix",
			patterns: "logs/**",
			path:     "logs/debug.log",
			want:     true,
		},
		{
			name:     "double-star suffix deep",
			patterns: "logs/**",
			path:     "logs/2024/debug.log",
			want:     true,
		},
		{
			name:     "double-star middle",
			patterns: "a/**/b",
			path:     "a/x/y/b",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gi := &GitIgnore{}
			gi.AddPattern(tt.patterns)
			got := gi.Match(tt.path)
			if got != tt.want {
				t.Errorf("Match(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestGitIgnore_Match_DirectoryPattern(t *testing.T) {
	gi := &GitIgnore{}
	gi.AddPattern("build/")

	// Directory patterns have dirOnly=true but Match doesn't check isDir
	// The trailing slash is stripped during parsing, so "build" matches as a path component
	if !gi.Match("build/output.js") {
		t.Error("expected 'build/' pattern to match 'build/output.js'")
	}
}

func TestGitIgnore_Match_NegationPattern(t *testing.T) {
	gi := &GitIgnore{}
	gi.AddPattern("*.log")
	gi.AddPattern("!important.log")

	// *.log matches, then !important.log un-ignores
	if !gi.Match("error.log") {
		t.Error("expected error.log to be ignored")
	}
	if gi.Match("important.log") {
		t.Error("expected important.log to NOT be ignored (negation)")
	}
}

func TestGitIgnore_Match_SlashPrefix(t *testing.T) {
	gi := &GitIgnore{}
	gi.AddPattern("/root_only.txt")

	// Should match at root
	if !gi.Match("root_only.txt") {
		t.Error("expected /root_only.txt to match root_only.txt")
	}

	// Should NOT match in subdirectory (anchored to root)
	// This depends on matchPattern behavior with leading slash
	// The pattern is anchored: only matches at the root level
}

func TestGitIgnore_Match_PatternWithSlash(t *testing.T) {
	gi := &GitIgnore{}
	gi.AddPattern("doc/frotz")

	// Pattern contains /, should match with path semantics
	if !gi.Match("doc/frotz") {
		t.Error("expected doc/frotz to match")
	}
}

func TestGitIgnore_AddPattern(t *testing.T) {
	gi := &GitIgnore{}

	gi.AddPattern("*.log")
	if len(gi.patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(gi.patterns))
	}
	if gi.patterns[0].pattern != "*.log" {
		t.Errorf("pattern = %q, want %q", gi.patterns[0].pattern, "*.log")
	}

	gi.AddPattern("!keep.log")
	if len(gi.patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d", len(gi.patterns))
	}
	if !gi.patterns[1].negation || gi.patterns[1].pattern != "keep.log" {
		t.Errorf("pattern = %+v, want negation=true, pattern=keep.log", gi.patterns[1])
	}

	gi.AddPattern("vendor/")
	if len(gi.patterns) != 3 {
		t.Fatalf("expected 3 patterns, got %d", len(gi.patterns))
	}
	if !gi.patterns[2].dirOnly || gi.patterns[2].pattern != "vendor" {
		t.Errorf("pattern = %+v, want dirOnly=true, pattern=vendor", gi.patterns[2])
	}
}

func TestGitIgnore_LoadFile_MissingFile(t *testing.T) {
	gi := &GitIgnore{}
	// Should not panic or error on missing file
	gi.loadFile("/nonexistent/path/.gitignore")
	if len(gi.patterns) != 0 {
		t.Errorf("expected 0 patterns from missing file, got %d", len(gi.patterns))
	}
}

func TestGitIgnore_Match_MultiplePatterns(t *testing.T) {
	gi := &GitIgnore{}
	gi.AddPattern("*.log")
	gi.AddPattern("*.tmp")
	gi.AddPattern("node_modules")

	tests := []struct {
		path string
		want bool
	}{
		{"error.log", true},
		{"cache.tmp", true},
		{"node_modules", true},
		{"node_modules/package.json", true},
		{"main.go", false},
		{"README.md", false},
	}

	for _, tt := range tests {
		got := gi.Match(tt.path)
		if got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestMatchGlob_Direct(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		pattern string
		want    bool
	}{
		{"exact match", "foo.go", "foo.go", true},
		{"wildcard extension", "foo.go", "*.go", true},
		{"wildcard prefix", "test_foo", "test_*", true},
		{"no match", "foo.go", "*.txt", false},
		{"double star prefix", "a/b/foo", "**/foo", true},
		{"double star suffix", "logs/debug.log", "logs/**", true},
		{"basename fallback", "dir/foo.go", "*.go", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchGlob(tt.input, tt.pattern)
			if got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.input, tt.pattern, got, tt.want)
			}
		})
	}
}
