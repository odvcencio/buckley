package builtin

import (
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestExcelToolMetadata(t *testing.T) {
	tool := &ExcelTool{}
	if tool.Name() != "excel" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "excel")
	}
	if tool.Description() == "" {
		t.Fatal("Description() should not be empty")
	}
	if params := tool.Parameters(); params.Type != "object" {
		t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
	}
}

func TestExcelToolValidatesInputs(t *testing.T) {
	tool := &ExcelTool{}

	result, err := tool.Execute(map[string]any{})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success || result.Error == "" {
		t.Fatalf("expected validation failure, got %+v", result)
	}

	result, err = tool.Execute(map[string]any{"action": "read"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success || result.Error == "" {
		t.Fatalf("expected validation failure for missing file_path, got %+v", result)
	}
}

func TestExcelToolReadRange(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.xlsx")

	f := excelize.NewFile()
	_ = f.SetSheetName("Sheet1", "Data")
	_ = f.SetCellValue("Data", "A1", "one")
	_ = f.SetCellValue("Data", "B1", "two")
	_ = f.SetCellValue("Data", "A2", "three")
	if err := f.SaveAs(filePath); err != nil {
		t.Fatalf("SaveAs: %v", err)
	}

	tool := &ExcelTool{}
	result, err := tool.Execute(map[string]any{
		"action":    "read",
		"file_path": filePath,
		"range":     "A1:B2",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success reading range, got error: %s", result.Error)
	}
	data, ok := result.Data["data"].([][]string)
	if !ok {
		t.Fatalf("expected [][]string data, got %T", result.Data["data"])
	}
	if len(data) != 2 || len(data[0]) != 2 || data[0][0] != "one" || data[1][0] != "three" {
		t.Fatalf("unexpected data returned: %+v", data)
	}
}

func TestExcelParseRangeAndDataErrors(t *testing.T) {
	tool := &ExcelTool{}

	if _, err := tool.parseRange(nil, "A1"); err == nil {
		t.Fatal("expected error for malformed range")
	}
	rows := [][]string{{"a", "b"}, {"c"}}
	out, err := tool.parseRange(rows, "A1:B2")
	if err != nil {
		t.Fatalf("parseRange: %v", err)
	}
	if len(out) != 2 || len(out[0]) != 2 || len(out[1]) != 1 || out[1][0] != "c" {
		t.Fatalf("unexpected parsed range: %+v", out)
	}

	if _, err := tool.parseDataArray("bad"); err == nil {
		t.Fatal("expected error for non-array data")
	}
	if _, err := tool.parseDataArray([]any{"not a row"}); err == nil {
		t.Fatal("expected error when row is not array")
	}
}
