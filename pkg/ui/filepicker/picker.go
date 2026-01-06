package filepicker

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// FilePicker provides fuzzy file search with @ trigger.
type FilePicker struct {
	mu sync.RWMutex

	// State
	active    bool
	query     string // Characters typed after @
	cursorPos int    // Position where @ was typed

	// File index
	projectRoot string
	files       []string   // All indexed files (relative paths)
	gitignore   *GitIgnore // Pattern matcher
	indexReady  bool

	// Results
	matches    []FileMatch
	selected   int
	maxResults int

	// Dimensions
	width, height   int
	offsetX, offsetY int // Position in screen
}

// FileMatch represents a fuzzy match result.
type FileMatch struct {
	Path       string // Relative path from project root
	Score      int    // Match score (higher = better)
	Highlights []int  // Indices of matched characters
}

// NewFilePicker creates a new file picker.
func NewFilePicker(projectRoot string) *FilePicker {
	fp := &FilePicker{
		projectRoot: projectRoot,
		maxResults:  10,
		gitignore:   NewGitIgnore(projectRoot),
		width:       60,
		height:      12,
	}
	go fp.indexFiles() // Index in background
	return fp
}

// Activate shows the picker at the given cursor position.
func (fp *FilePicker) Activate(cursorPos int) {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	fp.active = true
	fp.query = ""
	fp.cursorPos = cursorPos
	fp.selected = 0
	fp.updateMatches()
}

// Deactivate hides the picker.
func (fp *FilePicker) Deactivate() {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.active = false
	fp.query = ""
	fp.selected = 0
}

// IsActive returns whether the picker is visible.
func (fp *FilePicker) IsActive() bool {
	fp.mu.RLock()
	defer fp.mu.RUnlock()
	return fp.active
}

// SetQuery updates the search query.
func (fp *FilePicker) SetQuery(query string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	fp.query = query
	fp.selected = 0
	fp.updateMatches()
}

// AppendQuery adds a character to the query.
func (fp *FilePicker) AppendQuery(char rune) {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	fp.query += string(char)
	fp.updateMatches()
}

// Backspace removes the last character.
// Returns false if query is empty (signal to remove @ and deactivate).
func (fp *FilePicker) Backspace() bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	if len(fp.query) == 0 {
		fp.active = false
		return false
	}

	fp.query = fp.query[:len(fp.query)-1]
	fp.updateMatches()
	return true
}

// MoveUp selects the previous match.
func (fp *FilePicker) MoveUp() {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	if fp.selected > 0 {
		fp.selected--
	}
}

// MoveDown selects the next match.
func (fp *FilePicker) MoveDown() {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	if fp.selected < len(fp.matches)-1 {
		fp.selected++
	}
}

// GetSelected returns the selected file path.
func (fp *FilePicker) GetSelected() string {
	fp.mu.RLock()
	defer fp.mu.RUnlock()

	if fp.selected >= 0 && fp.selected < len(fp.matches) {
		return fp.matches[fp.selected].Path
	}
	return ""
}

// GetMatches returns current matches for rendering.
func (fp *FilePicker) GetMatches() []FileMatch {
	fp.mu.RLock()
	defer fp.mu.RUnlock()

	result := make([]FileMatch, len(fp.matches))
	copy(result, fp.matches)
	return result
}

// SelectedIndex returns current selection.
func (fp *FilePicker) SelectedIndex() int {
	fp.mu.RLock()
	defer fp.mu.RUnlock()
	return fp.selected
}

// Query returns current query.
func (fp *FilePicker) Query() string {
	fp.mu.RLock()
	defer fp.mu.RUnlock()
	return fp.query
}

// CursorPosition returns where @ was typed.
func (fp *FilePicker) CursorPosition() int {
	fp.mu.RLock()
	defer fp.mu.RUnlock()
	return fp.cursorPos
}

// SetDimensions sets the picker display size.
func (fp *FilePicker) SetDimensions(width, height int) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.width = width
	fp.height = height
	fp.maxResults = height - 2 // Account for border
	if fp.maxResults < 1 {
		fp.maxResults = 1
	}
}

// SetOffset sets the screen position for rendering.
func (fp *FilePicker) SetOffset(x, y int) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.offsetX = x
	fp.offsetY = y
}

// Dimensions returns width and height.
func (fp *FilePicker) Dimensions() (int, int) {
	fp.mu.RLock()
	defer fp.mu.RUnlock()
	return fp.width, fp.height
}

// Offset returns screen position.
func (fp *FilePicker) Offset() (int, int) {
	fp.mu.RLock()
	defer fp.mu.RUnlock()
	return fp.offsetX, fp.offsetY
}

// FileCount returns total indexed files.
func (fp *FilePicker) FileCount() int {
	fp.mu.RLock()
	defer fp.mu.RUnlock()
	return len(fp.files)
}

// IsIndexReady returns true if file indexing is complete.
func (fp *FilePicker) IsIndexReady() bool {
	fp.mu.RLock()
	defer fp.mu.RUnlock()
	return fp.indexReady
}

// updateMatches recalculates matches based on current query.
func (fp *FilePicker) updateMatches() {
	if len(fp.files) == 0 {
		fp.matches = nil
		return
	}

	type scored struct {
		match FileMatch
		score int
	}

	var results []scored

	// Support multi-pattern search (space-separated)
	patterns := strings.Fields(fp.query)

	for _, file := range fp.files {
		var score int
		var highlights []int

		if len(patterns) <= 1 {
			score, highlights = fuzzyMatch(file, fp.query)
		} else {
			score, highlights = MultiPatternMatch(file, patterns)
		}

		if score > 0 {
			results = append(results, scored{
				match: FileMatch{
					Path:       file,
					Score:      score,
					Highlights: highlights,
				},
				score: score,
			})
		}
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		// Tie-breaker: shorter path first
		return len(results[i].match.Path) < len(results[j].match.Path)
	})

	// Take top N results
	limit := fp.maxResults
	if limit > len(results) {
		limit = len(results)
	}

	fp.matches = make([]FileMatch, limit)
	for i := 0; i < limit; i++ {
		fp.matches[i] = results[i].match
	}

	// Adjust selection if out of bounds
	if fp.selected >= len(fp.matches) {
		fp.selected = len(fp.matches) - 1
	}
	if fp.selected < 0 {
		fp.selected = 0
	}
}

// indexFiles walks the project directory and builds file index.
func (fp *FilePicker) indexFiles() {
	fp.mu.Lock()
	fp.files = nil
	fp.indexReady = false
	fp.mu.Unlock()

	var files []string

	_ = filepath.Walk(fp.projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		// Get relative path
		relPath, err := filepath.Rel(fp.projectRoot, path)
		if err != nil {
			return nil
		}

		// Skip hidden directories
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			// Skip common non-source directories
			switch info.Name() {
			case "node_modules", "vendor", "__pycache__", ".git", "dist", "build", "target":
				return filepath.SkipDir
			}
			return nil
		}

		// Skip hidden files
		if strings.HasPrefix(info.Name(), ".") {
			return nil
		}

		// Check gitignore
		if fp.gitignore != nil && fp.gitignore.Match(relPath) {
			return nil
		}

		// Skip binary and large files by extension
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".exe", ".dll", ".so", ".dylib", ".a", ".o",
			".zip", ".tar", ".gz", ".rar", ".7z",
			".png", ".jpg", ".jpeg", ".gif", ".ico", ".svg", ".webp",
			".pdf", ".doc", ".docx", ".xls", ".xlsx",
			".mp3", ".mp4", ".avi", ".mov", ".wav",
			".woff", ".woff2", ".ttf", ".eot",
			".lock":
			return nil
		}

		files = append(files, relPath)
		return nil
	})

	fp.mu.Lock()
	fp.files = files
	fp.indexReady = true
	fp.updateMatches()
	fp.mu.Unlock()
}

// RefreshIndex triggers a re-scan of the file system.
func (fp *FilePicker) RefreshIndex() {
	go fp.indexFiles()
}

// ProjectRoot returns the project root path.
func (fp *FilePicker) ProjectRoot() string {
	fp.mu.RLock()
	defer fp.mu.RUnlock()
	return fp.projectRoot
}
