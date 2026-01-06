package diff

import (
	"strings"
	"testing"
)

func TestNewDiffSummarizer(t *testing.T) {
	ds := NewDiffSummarizer()
	if ds == nil {
		t.Fatal("NewDiffSummarizer returned nil")
	}
	if ds.maxContextLines != 3 {
		t.Errorf("expected maxContextLines 3, got %d", ds.maxContextLines)
	}
	if ds.maxFunctions != 5 {
		t.Errorf("expected maxFunctions 5, got %d", ds.maxFunctions)
	}
}

func TestSummarizeFileDiff_NewFile(t *testing.T) {
	ds := NewDiffSummarizer()
	summary := ds.SummarizeFileDiff("", "line1\nline2\nline3", "test.go")

	if !summary.IsNew {
		t.Error("expected IsNew to be true")
	}
	if summary.LinesAdded != 3 {
		t.Errorf("expected 3 lines added, got %d", summary.LinesAdded)
	}
	if summary.TotalLines != 3 {
		t.Errorf("expected 3 total lines, got %d", summary.TotalLines)
	}
}

func TestSummarizeFileDiff_DeletedFile(t *testing.T) {
	ds := NewDiffSummarizer()
	summary := ds.SummarizeFileDiff("line1\nline2\nline3", "", "test.go")

	if !summary.IsDeleted {
		t.Error("expected IsDeleted to be true")
	}
	if summary.LinesRemoved != 3 {
		t.Errorf("expected 3 lines removed, got %d", summary.LinesRemoved)
	}
}

func TestSummarizeFileDiff_Modified(t *testing.T) {
	ds := NewDiffSummarizer()
	oldContent := "line1\nline2\nline3"
	newContent := "line1\nline2_modified\nline3\nline4"
	summary := ds.SummarizeFileDiff(oldContent, newContent, "test.go")

	if summary.IsNew || summary.IsDeleted {
		t.Error("expected modified file, not new or deleted")
	}
	if summary.LinesAdded == 0 {
		t.Error("expected some lines added")
	}
	if summary.TotalLines != 4 {
		t.Errorf("expected 4 total lines, got %d", summary.TotalLines)
	}
}

func TestCalculateLineChanges(t *testing.T) {
	ds := NewDiffSummarizer()
	oldLines := []string{"line1", "line2", "line3"}
	newLines := []string{"line1", "line2_modified", "line3", "line4"}

	added, removed := ds.calculateLineChanges(oldLines, newLines)

	if added == 0 {
		t.Error("expected some lines added")
	}
	if removed == 0 {
		t.Error("expected some lines removed")
	}
}

func TestDetectGoFunctions(t *testing.T) {
	ds := NewDiffSummarizer()
	oldContent := `
package main

func OldFunc() {
	println("old")
}
`
	newContent := `
package main

func NewFunc() {
	println("new")
}

func AnotherFunc() {
	println("another")
}
`

	functions := ds.detectGoFunctions(oldContent, newContent)

	if len(functions) == 0 {
		t.Error("expected some functions detected")
	}
}

func TestDetectJSFunctions(t *testing.T) {
	ds := NewDiffSummarizer()
	oldContent := ``
	newContent := `
function testFunc() {
	console.log("test");
}

const arrowFunc = () => {
	console.log("arrow");
}
`

	functions := ds.detectJSFunctions(oldContent, newContent)

	if len(functions) == 0 {
		t.Error("expected some functions detected")
	}
}

func TestDetectPythonFunctions(t *testing.T) {
	ds := NewDiffSummarizer()
	oldContent := ``
	newContent := `
def test_func():
    print("test")

def another_func():
    print("another")
`

	functions := ds.detectPythonFunctions(oldContent, newContent)

	if len(functions) != 2 {
		t.Errorf("expected 2 functions, got %d", len(functions))
	}
}

func TestDetectModifiedFunctions_Go(t *testing.T) {
	ds := NewDiffSummarizer()
	oldContent := `func TestFunc() {}`
	newContent := `func TestFunc() { println("new") }`

	functions := ds.detectModifiedFunctions(oldContent, newContent, "test.go")

	if len(functions) == 0 {
		t.Error("expected modified function detected")
	}
}

func TestDetectModifiedFunctions_MaxLimit(t *testing.T) {
	ds := NewDiffSummarizer()
	ds.maxFunctions = 2

	newContent := `
func Func1() {}
func Func2() {}
func Func3() {}
func Func4() {}
`

	functions := ds.detectModifiedFunctions("", newContent, "test.go")

	if len(functions) != 2 {
		t.Errorf("expected max 2 functions, got %d", len(functions))
	}
}

func TestSummary_Format(t *testing.T) {
	summary := &Summary{
		FilePath:     "test.go",
		LinesAdded:   5,
		LinesRemoved: 2,
		Functions:    []string{"TestFunc"},
	}

	result := summary.Format()

	if !strings.Contains(result, "test.go") {
		t.Error("expected file path in format")
	}
	if !strings.Contains(result, "+5/-2") {
		t.Error("expected line changes in format")
	}
	if !strings.Contains(result, "TestFunc") {
		t.Error("expected function name in format")
	}
}

func TestSummary_Format_NewFile(t *testing.T) {
	summary := &Summary{
		FilePath:   "test.go",
		LinesAdded: 10,
		IsNew:      true,
	}

	result := summary.Format()

	if !strings.Contains(result, "new file") {
		t.Error("expected 'new file' in format")
	}
}

func TestSummary_Format_DeletedFile(t *testing.T) {
	summary := &Summary{
		FilePath:     "test.go",
		LinesRemoved: 10,
		IsDeleted:    true,
	}

	result := summary.Format()

	if !strings.Contains(result, "deleted") {
		t.Error("expected 'deleted' in format")
	}
}

func TestSummary_FormatCompact(t *testing.T) {
	summary := &Summary{
		FilePath:     "path/to/test.go",
		LinesAdded:   5,
		LinesRemoved: 2,
	}

	result := summary.FormatCompact()

	if !strings.Contains(result, "test.go") {
		t.Error("expected filename in compact format")
	}
	if !strings.Contains(result, "+5/-2") {
		t.Error("expected line changes in compact format")
	}
}

func TestCreateAbridgedDiff(t *testing.T) {
	ds := NewDiffSummarizer()
	oldContent := "line1\nline2"
	newContent := "line1_modified\nline2\nline3"

	abridged := ds.CreateAbridgedDiff(oldContent, newContent, "test.go")

	if abridged.Summary == nil {
		t.Fatal("expected summary to be present")
	}
	if abridged.FullDiff == "" {
		t.Error("expected full diff to be present")
	}
	if abridged.Preview == "" {
		t.Error("expected preview to be present")
	}
}

func TestGenerateUnifiedDiff(t *testing.T) {
	ds := NewDiffSummarizer()
	oldContent := "line1\nline2"
	newContent := "line1\nline3"

	diff := ds.generateUnifiedDiff(oldContent, newContent, "test.go")

	if !strings.Contains(diff, "---") {
		t.Error("expected diff header")
	}
	if !strings.Contains(diff, "+++") {
		t.Error("expected diff header")
	}
}

func TestCreatePreview_Short(t *testing.T) {
	ds := NewDiffSummarizer()
	shortDiff := "line1\nline2\nline3"

	preview := ds.createPreview(shortDiff)

	if preview != shortDiff {
		t.Error("expected short diff to be returned unchanged")
	}
}

func TestCreatePreview_Long(t *testing.T) {
	ds := NewDiffSummarizer()
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "line")
	}
	longDiff := strings.Join(lines, "\n")

	preview := ds.createPreview(longDiff)

	if !strings.Contains(preview, "more lines") {
		t.Error("expected truncation message in preview")
	}
}

func TestSummarizeBatch(t *testing.T) {
	ds := NewDiffSummarizer()
	changes := map[string][2]string{
		"file1.go": {"old1", "new1\nnew2"},
		"file2.go": {"", "new"},
		"file3.go": {"old", ""},
	}

	batch := ds.SummarizeBatch(changes)

	if len(batch.Summaries) != 3 {
		t.Errorf("expected 3 summaries, got %d", len(batch.Summaries))
	}
	if batch.FilesNew != 1 {
		t.Errorf("expected 1 new file, got %d", batch.FilesNew)
	}
	if batch.FilesDeleted != 1 {
		t.Errorf("expected 1 deleted file, got %d", batch.FilesDeleted)
	}
	if batch.FilesChanged != 1 {
		t.Errorf("expected 1 changed file, got %d", batch.FilesChanged)
	}
}

func TestBatchSummary_Format(t *testing.T) {
	batch := &BatchSummary{
		FilesNew:     1,
		FilesChanged: 2,
		FilesDeleted: 1,
		TotalAdded:   10,
		TotalRemoved: 5,
	}

	result := batch.Format()

	if !strings.Contains(result, "1 new") {
		t.Error("expected new files count")
	}
	if !strings.Contains(result, "2 modified") {
		t.Error("expected modified files count")
	}
	if !strings.Contains(result, "1 deleted") {
		t.Error("expected deleted files count")
	}
	if !strings.Contains(result, "+10/-5") {
		t.Error("expected line changes")
	}
}

func TestBatchSummary_FormatDetailed(t *testing.T) {
	batch := &BatchSummary{
		Summaries: []*Summary{
			{FilePath: "test.go", LinesAdded: 5, LinesRemoved: 2},
		},
		TotalAdded:   5,
		TotalRemoved: 2,
	}

	result := batch.FormatDetailed()

	if !strings.Contains(result, "test.go") {
		t.Error("expected file path in detailed format")
	}
}

func TestFunctionBodyChanged(t *testing.T) {
	ds := NewDiffSummarizer()

	oldContent := `
func TestFunc() {
	println("old")
}
`
	newContent := `
func TestFunc() {
	println("new")
}
`

	changed := ds.functionBodyChanged(oldContent, newContent, "TestFunc")
	if !changed {
		t.Error("expected function body to be detected as changed")
	}
}

func TestExtractFunctionContext(t *testing.T) {
	ds := NewDiffSummarizer()

	content := `
package main

func TestFunc() {
	println("test")
}
`

	context := ds.extractFunctionContext(content, "TestFunc")
	if context == "" {
		t.Error("expected non-empty context")
	}
	if !strings.Contains(context, "TestFunc") {
		t.Error("expected context to contain function name")
	}
}
