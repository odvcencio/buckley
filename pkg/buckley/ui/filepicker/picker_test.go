package filepicker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/fluffyui/compositor"
)

func TestFuzzyMatch(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		query     string
		wantMatch bool
	}{
		{"empty query matches all", "pkg/api/handler.go", "", true},
		{"exact prefix", "handler.go", "han", true},
		{"fuzzy match", "pkg/api/handler.go", "pah", true},
		{"case insensitive", "Handler.go", "handler", true},
		{"no match", "config.yaml", "xyz", false},
		{"filename match", "pkg/deep/nested/main.go", "main", true},
		{"path segment match", "pkg/api/handler.go", "api", true},
		{"non-contiguous", "abcdef.go", "ace", true},
		{"order matters", "abcdef.go", "fba", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, _ := fuzzyMatch(tt.path, tt.query)
			gotMatch := score > 0

			if gotMatch != tt.wantMatch {
				t.Errorf("fuzzyMatch(%q, %q) match = %v, want %v (score=%d)",
					tt.path, tt.query, gotMatch, tt.wantMatch, score)
			}
		})
	}
}

func TestFuzzyMatchScoring(t *testing.T) {
	// Filename match should score higher
	scoreFilename, _ := fuzzyMatch("pkg/api/main.go", "main")
	scorePath, _ := fuzzyMatch("pkg/main/api.go", "main")

	if scoreFilename <= scorePath {
		t.Errorf("filename match should score higher: %d <= %d", scoreFilename, scorePath)
	}

	// Consecutive match should score higher
	scoreConsec, _ := fuzzyMatch("handler.go", "hand")
	scoreNonConsec, _ := fuzzyMatch("hasnd.go", "hand")

	if scoreConsec <= scoreNonConsec {
		t.Errorf("consecutive match should score higher: %d <= %d", scoreConsec, scoreNonConsec)
	}

	// Word start match should score higher
	scoreWordStart, _ := fuzzyMatch("file_handler.go", "han")
	scoreMid, _ := fuzzyMatch("file_xhandler.go", "han")

	if scoreWordStart <= scoreMid {
		t.Errorf("word start match should score higher: %d <= %d", scoreWordStart, scoreMid)
	}
}

func TestFuzzyMatchHighlights(t *testing.T) {
	_, highlights := fuzzyMatch("handler.go", "hnd")

	if len(highlights) != 3 {
		t.Fatalf("expected 3 highlights, got %d", len(highlights))
	}

	// "handler.go": h(0) a(1) n(2) d(3) l(4) e(5) r(6) .(7) g(8) o(9)
	// Query "hnd" matches: h(0), n(2), d(3)
	expected := []int{0, 2, 3}
	for i, exp := range expected {
		if highlights[i] != exp {
			t.Errorf("highlight[%d] = %d, want %d", i, highlights[i], exp)
		}
	}
}

func TestMultiPatternMatch(t *testing.T) {
	tests := []struct {
		path     string
		patterns []string
		want     bool
	}{
		{"pkg/api/handler.go", []string{"api", "han"}, true},
		{"pkg/api/handler.go", []string{"api", "xyz"}, false},
		{"pkg/buckley/ui/viewmodel/viewmodel.go", []string{"ui", "model"}, true},
		{"config.yaml", []string{"config", "yaml"}, true},
	}

	for _, tt := range tests {
		name := strings.Join(tt.patterns, " ")
		t.Run(name, func(t *testing.T) {
			score, _ := MultiPatternMatch(tt.path, tt.patterns)
			got := score > 0
			if got != tt.want {
				t.Errorf("MultiPatternMatch(%q, %v) = %v, want %v",
					tt.path, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestGitIgnore(t *testing.T) {
	t.Run("basic patterns", func(t *testing.T) {
		gi := &GitIgnore{}
		gi.AddPattern("*.log")
		gi.AddPattern("node_modules/")
		gi.AddPattern("dist")

		tests := []struct {
			path string
			want bool
		}{
			{"app.log", true},
			{"logs/debug.log", true},
			{"node_modules/lodash/index.js", true},
			{"dist/bundle.js", true},
			{"src/main.go", false},
		}

		for _, tt := range tests {
			if got := gi.Match(tt.path); got != tt.want {
				t.Errorf("Match(%q) = %v, want %v", tt.path, got, tt.want)
			}
		}
	})

	t.Run("negation", func(t *testing.T) {
		gi := &GitIgnore{}
		gi.AddPattern("*.log")
		gi.AddPattern("!important.log")

		if gi.Match("important.log") {
			t.Error("negated pattern should not match")
		}
		if !gi.Match("debug.log") {
			t.Error("non-negated should match")
		}
	})
}

func TestFilePicker(t *testing.T) {
	// Create temp directory with test files
	tmpDir, err := os.MkdirTemp("", "filepicker_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test file structure
	files := []string{
		"main.go",
		"pkg/api/handler.go",
		"pkg/api/routes.go",
		"pkg/config/config.go",
		"pkg/buckley/ui/viewmodel/viewmodel.go",
		"README.md",
	}

	for _, f := range files {
		path := filepath.Join(tmpDir, f)
		os.MkdirAll(filepath.Dir(path), 0755)
		os.WriteFile(path, []byte("test"), 0644)
	}

	fp := NewFilePicker(tmpDir)

	// Wait for indexing
	for i := 0; i < 50; i++ {
		if fp.IsIndexReady() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !fp.IsIndexReady() {
		t.Fatal("indexing did not complete in time")
	}

	t.Run("file count", func(t *testing.T) {
		count := fp.FileCount()
		if count != len(files) {
			t.Errorf("FileCount() = %d, want %d", count, len(files))
		}
	})

	t.Run("activate/deactivate", func(t *testing.T) {
		fp.Activate(5)
		if !fp.IsActive() {
			t.Error("should be active after Activate()")
		}
		if fp.CursorPosition() != 5 {
			t.Errorf("CursorPosition() = %d, want 5", fp.CursorPosition())
		}

		fp.Deactivate()
		if fp.IsActive() {
			t.Error("should not be active after Deactivate()")
		}
	})

	t.Run("query and filtering", func(t *testing.T) {
		fp.Activate(0)
		fp.SetQuery("api")

		matches := fp.GetMatches()
		if len(matches) != 2 {
			t.Errorf("expected 2 matches for 'api', got %d", len(matches))
		}

		// Top match should be handler or routes
		for _, m := range matches {
			if !strings.Contains(m.Path, "api") {
				t.Errorf("match %q should contain 'api'", m.Path)
			}
		}
	})

	t.Run("selection", func(t *testing.T) {
		fp.Activate(0)
		fp.SetQuery("")

		fp.MoveDown()
		if fp.SelectedIndex() != 1 {
			t.Errorf("SelectedIndex() = %d, want 1", fp.SelectedIndex())
		}

		fp.MoveUp()
		if fp.SelectedIndex() != 0 {
			t.Errorf("SelectedIndex() = %d, want 0", fp.SelectedIndex())
		}

		// Can't go above 0
		fp.MoveUp()
		if fp.SelectedIndex() != 0 {
			t.Errorf("SelectedIndex() = %d, want 0 (shouldn't go negative)", fp.SelectedIndex())
		}
	})

	t.Run("append and backspace", func(t *testing.T) {
		fp.Activate(0)
		fp.AppendQuery('m')
		fp.AppendQuery('a')
		fp.AppendQuery('i')
		fp.AppendQuery('n')

		if fp.Query() != "main" {
			t.Errorf("Query() = %q, want 'main'", fp.Query())
		}

		// Backspace
		fp.Backspace()
		if fp.Query() != "mai" {
			t.Errorf("Query() = %q after backspace, want 'mai'", fp.Query())
		}

		// Backspace until empty
		fp.Backspace()
		fp.Backspace()
		fp.Backspace()
		result := fp.Backspace() // Should return false and deactivate
		if result {
			t.Error("Backspace on empty query should return false")
		}
		if fp.IsActive() {
			t.Error("should deactivate after backspace on empty query")
		}
	})
}

func TestFilePickerRender(t *testing.T) {
	// Create temp directory with test files
	tmpDir, err := os.MkdirTemp("", "filepicker_render_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	files := []string{"main.go", "handler.go", "config.go"}
	for _, f := range files {
		os.WriteFile(filepath.Join(tmpDir, f), []byte("test"), 0644)
	}

	fp := NewFilePicker(tmpDir)
	fp.SetDimensions(40, 8)

	// Wait for indexing
	for i := 0; i < 50; i++ {
		if fp.IsIndexReady() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Run("render when inactive", func(t *testing.T) {
		screen := compositor.NewScreen(80, 24)
		Render(fp, screen, DefaultRenderConfig())
		// Should not render anything when inactive
		cell := screen.Get(0, 0)
		if cell.Rune != ' ' {
			t.Error("should not render when inactive")
		}
	})

	t.Run("render when active", func(t *testing.T) {
		fp.Activate(0)
		screen := compositor.NewScreen(80, 24)
		Render(fp, screen, DefaultRenderConfig())

		// Should have border
		cell := screen.Get(0, 0)
		if cell.Rune != '╭' {
			t.Errorf("expected top-left corner '╭', got %q", cell.Rune)
		}
	})

	t.Run("RenderToString", func(t *testing.T) {
		fp.Activate(0)
		output := RenderToString(fp, DefaultRenderConfig())
		if output == "" {
			t.Error("RenderToString should produce output when active")
		}
	})

	t.Run("CompactView", func(t *testing.T) {
		fp.Activate(0)
		fp.SetQuery("main")

		compact := CompactView(fp)
		if !strings.HasPrefix(compact, "@main") {
			t.Errorf("CompactView() = %q, should start with @main", compact)
		}
	})
}

func BenchmarkFuzzyMatch(b *testing.B) {
	path := "pkg/buckley/ui/filepicker/picker_test.go"
	query := "uifp"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fuzzyMatch(path, query)
	}
}

func BenchmarkFilePickerFilter(b *testing.B) {
	// Create picker with many files
	fp := &FilePicker{
		maxResults: 10,
	}

	// Simulate 1000 files
	fp.files = make([]string, 1000)
	for i := 0; i < 1000; i++ {
		fp.files[i] = filepath.Join("pkg", "component"+string(rune(i%26+'a')), "file.go")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fp.query = "compf"
		fp.updateMatches()
	}
}
