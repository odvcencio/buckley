package builtin

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

// ExcelTool provides Excel file manipulation capabilities
type ExcelTool struct{ workDirAware }

func (t *ExcelTool) Name() string {
	return "excel"
}

func (t *ExcelTool) Description() string {
	return "**EXCEL FILE OPERATIONS** - Read, write, and manipulate Excel files (.xlsx only). Trigger phrases: 'read spreadsheet', 'edit Excel', 'write to cell', 'add formula', 'create sheet', 'update workbook'. Supports reading cell values, writing data/formulas, creating/deleting sheets, and listing sheet names. Use this whenever user mentions Excel files, spreadsheets, or .xlsx files. Can perform multiple operations in one call for efficiency."
}

func (t *ExcelTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"action": {
				Type:        "string",
				Description: "Action to perform: read, write, write_formula, list_sheets, create_sheet, delete_sheet, get_info",
			},
			"file_path": {
				Type:        "string",
				Description: "Path to Excel file (.xlsx or .xls)",
			},
			"sheet": {
				Type:        "string",
				Description: "Sheet name (defaults to first sheet if not specified)",
			},
			"cell": {
				Type:        "string",
				Description: "Cell reference (e.g., 'A1', 'B5') for read/write operations",
			},
			"range": {
				Type:        "string",
				Description: "Cell range (e.g., 'A1:C10') for reading multiple cells",
			},
			"value": {
				Type:        "string",
				Description: "Value to write to cell (for write action)",
			},
			"formula": {
				Type:        "string",
				Description: "Formula to write (e.g., '=SUM(A1:A10)') for write_formula action",
			},
			"new_sheet_name": {
				Type:        "string",
				Description: "Name for new sheet (for create_sheet action)",
			},
			"data": {
				Type:        "array",
				Description: "Array of arrays for batch write operations. Each inner array is a row. Example: [['Name', 'Age'], ['Alice', 30], ['Bob', 25]]",
			},
			"start_cell": {
				Type:        "string",
				Description: "Starting cell for batch data write (e.g., 'A1'). Default: 'A1'",
			},
		},
		Required: []string{"action", "file_path"},
	}
}

func (t *ExcelTool) Execute(params map[string]any) (*Result, error) {
	action, ok := params["action"].(string)
	if !ok || action == "" {
		return &Result{Success: false, Error: "action parameter required"}, nil
	}

	filePath, ok := params["file_path"].(string)
	if !ok || filePath == "" {
		return &Result{Success: false, Error: "file_path parameter required"}, nil
	}

	// Make path absolute
	absPath, err := resolvePath(t.workDir, filePath)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	if strings.ToLower(filepath.Ext(absPath)) == ".xls" {
		return &Result{
			Success: false,
			Error:   ".xls (BIFF8) workbooks are not supported; please convert the file to .xlsx and try again",
		}, nil
	}

	switch action {
	case "read":
		return t.readExcel(absPath, params)
	case "write":
		return t.writeExcel(absPath, params)
	case "write_formula":
		return t.writeFormula(absPath, params)
	case "list_sheets":
		return t.listSheets(absPath)
	case "create_sheet":
		return t.createSheet(absPath, params)
	case "delete_sheet":
		return t.deleteSheet(absPath, params)
	case "get_info":
		return t.getInfo(absPath)
	default:
		return &Result{Success: false, Error: fmt.Sprintf("unknown action: %s. Valid actions: read, write, write_formula, list_sheets, create_sheet, delete_sheet, get_info", action)}, nil
	}
}

func (t *ExcelTool) readExcel(filePath string, params map[string]any) (*Result, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to open file: %v", err)}, nil
	}
	defer f.Close()

	sheet := t.getSheet(f, params)

	// Check if reading a range
	if cellRange, ok := params["range"].(string); ok {
		rows, err := f.GetRows(sheet)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("failed to read sheet: %v", err)}, nil
		}

		// Parse range (e.g., "A1:C10")
		data, err := t.parseRange(rows, cellRange)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("failed to parse range: %v", err)}, nil
		}

		return &Result{
			Success: true,
			Data: map[string]any{
				"file":  filePath,
				"sheet": sheet,
				"range": cellRange,
				"data":  data,
				"rows":  len(data),
				"cols":  len(data[0]),
			},
		}, nil
	}

	// Read single cell
	cell, ok := params["cell"].(string)
	if !ok || cell == "" {
		// No cell specified, read entire sheet
		rows, err := f.GetRows(sheet)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("failed to read sheet: %v", err)}, nil
		}

		return &Result{
			Success: true,
			Data: map[string]any{
				"file":  filePath,
				"sheet": sheet,
				"data":  rows,
				"rows":  len(rows),
			},
		}, nil
	}

	value, err := f.GetCellValue(sheet, cell)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to read cell %s: %v", cell, err)}, nil
	}

	// Also get formula if present
	formula, _ := f.GetCellFormula(sheet, cell)

	return &Result{
		Success: true,
		Data: map[string]any{
			"file":    filePath,
			"sheet":   sheet,
			"cell":    cell,
			"value":   value,
			"formula": formula,
		},
	}, nil
}

func (t *ExcelTool) writeExcel(filePath string, params map[string]any) (*Result, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		// File doesn't exist, create new
		f = excelize.NewFile()
	}
	defer f.Close()

	sheet := t.getSheet(f, params)

	// Check if batch data write
	if dataParam, ok := params["data"]; ok {
		startCell := "A1"
		if sc, ok := params["start_cell"].(string); ok && sc != "" {
			startCell = sc
		}

		data, err := t.parseDataArray(dataParam)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("invalid data format: %v", err)}, nil
		}

		// Parse start cell to get row/col
		col, row, err := excelize.CellNameToCoordinates(startCell)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("invalid start_cell: %v", err)}, nil
		}

		// Write data row by row
		for rowOffset, rowData := range data {
			for colOffset, value := range rowData {
				cellName, err := excelize.CoordinatesToCellName(col+colOffset, row+rowOffset)
				if err != nil {
					continue
				}
				f.SetCellValue(sheet, cellName, value)
			}
		}

		if err := f.SaveAs(filePath); err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("failed to save file: %v", err)}, nil
		}

		return &Result{
			Success: true,
			Data: map[string]any{
				"file":         filePath,
				"sheet":        sheet,
				"rows_written": len(data),
				"start_cell":   startCell,
				"message":      fmt.Sprintf("Wrote %d rows starting at %s", len(data), startCell),
			},
		}, nil
	}

	// Single cell write
	cell, ok := params["cell"].(string)
	if !ok || cell == "" {
		return &Result{Success: false, Error: "cell parameter required for write action"}, nil
	}

	value, ok := params["value"]
	if !ok {
		return &Result{Success: false, Error: "value parameter required for write action"}, nil
	}

	if err := f.SetCellValue(sheet, cell, value); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to write cell: %v", err)}, nil
	}

	if err := f.SaveAs(filePath); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to save file: %v", err)}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"file":    filePath,
			"sheet":   sheet,
			"cell":    cell,
			"value":   value,
			"message": fmt.Sprintf("Wrote '%v' to %s", value, cell),
		},
	}, nil
}

func (t *ExcelTool) writeFormula(filePath string, params map[string]any) (*Result, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to open file: %v", err)}, nil
	}
	defer f.Close()

	sheet := t.getSheet(f, params)

	cell, ok := params["cell"].(string)
	if !ok || cell == "" {
		return &Result{Success: false, Error: "cell parameter required for write_formula action"}, nil
	}

	formula, ok := params["formula"].(string)
	if !ok || formula == "" {
		return &Result{Success: false, Error: "formula parameter required for write_formula action"}, nil
	}

	// Ensure formula starts with =
	if !strings.HasPrefix(formula, "=") {
		formula = "=" + formula
	}

	if err := f.SetCellFormula(sheet, cell, formula); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to write formula: %v", err)}, nil
	}

	if err := f.SaveAs(filePath); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to save file: %v", err)}, nil
	}

	// Calculate and get result
	_ = f.UpdateLinkedValue()
	result, _ := f.GetCellValue(sheet, cell)

	return &Result{
		Success: true,
		Data: map[string]any{
			"file":    filePath,
			"sheet":   sheet,
			"cell":    cell,
			"formula": formula,
			"result":  result,
			"message": fmt.Sprintf("Wrote formula '%s' to %s (result: %s)", formula, cell, result),
		},
	}, nil
}

func (t *ExcelTool) listSheets(filePath string) (*Result, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to open file: %v", err)}, nil
	}
	defer f.Close()

	sheets := f.GetSheetList()

	return &Result{
		Success: true,
		Data: map[string]any{
			"file":   filePath,
			"sheets": sheets,
			"count":  len(sheets),
		},
	}, nil
}

func (t *ExcelTool) createSheet(filePath string, params map[string]any) (*Result, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to open file: %v", err)}, nil
	}
	defer f.Close()

	newSheetName, ok := params["new_sheet_name"].(string)
	if !ok || newSheetName == "" {
		return &Result{Success: false, Error: "new_sheet_name parameter required for create_sheet action"}, nil
	}

	idx, err := f.NewSheet(newSheetName)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to create sheet: %v", err)}, nil
	}

	if err := f.SaveAs(filePath); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to save file: %v", err)}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"file":    filePath,
			"sheet":   newSheetName,
			"index":   idx,
			"message": fmt.Sprintf("Created sheet '%s'", newSheetName),
		},
	}, nil
}

func (t *ExcelTool) deleteSheet(filePath string, params map[string]any) (*Result, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to open file: %v", err)}, nil
	}
	defer f.Close()

	sheet, ok := params["sheet"].(string)
	if !ok || sheet == "" {
		return &Result{Success: false, Error: "sheet parameter required for delete_sheet action"}, nil
	}

	if err := f.DeleteSheet(sheet); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to delete sheet: %v", err)}, nil
	}

	if err := f.SaveAs(filePath); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to save file: %v", err)}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"file":    filePath,
			"sheet":   sheet,
			"message": fmt.Sprintf("Deleted sheet '%s'", sheet),
		},
	}, nil
}

func (t *ExcelTool) getInfo(filePath string) (*Result, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to open file: %v", err)}, nil
	}
	defer f.Close()

	sheets := f.GetSheetList()
	sheetInfo := make([]map[string]any, 0, len(sheets))

	for _, sheet := range sheets {
		rows, _ := f.GetRows(sheet)
		rowCount := len(rows)
		colCount := 0
		if rowCount > 0 {
			colCount = len(rows[0])
		}

		sheetInfo = append(sheetInfo, map[string]any{
			"name": sheet,
			"rows": rowCount,
			"cols": colCount,
		})
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"file":        filePath,
			"sheet_count": len(sheets),
			"sheets":      sheetInfo,
		},
	}, nil
}

// Helper functions

func (t *ExcelTool) getSheet(f *excelize.File, params map[string]any) string {
	if sheet, ok := params["sheet"].(string); ok && sheet != "" {
		return sheet
	}
	// Default to first sheet
	sheets := f.GetSheetList()
	if len(sheets) > 0 {
		return sheets[0]
	}
	return "Sheet1"
}

func (t *ExcelTool) parseRange(rows [][]string, cellRange string) ([][]string, error) {
	// Parse range like "A1:C10"
	parts := strings.Split(cellRange, ":")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid range format, expected 'A1:C10'")
	}

	startCol, startRow, err := excelize.CellNameToCoordinates(parts[0])
	if err != nil {
		return nil, err
	}

	endCol, endRow, err := excelize.CellNameToCoordinates(parts[1])
	if err != nil {
		return nil, err
	}

	result := [][]string{}
	for r := startRow - 1; r < endRow && r < len(rows); r++ {
		row := []string{}
		for c := startCol - 1; c < endCol && c < len(rows[r]); c++ {
			row = append(row, rows[r][c])
		}
		result = append(result, row)
	}

	return result, nil
}

func (t *ExcelTool) parseDataArray(dataParam any) ([][]any, error) {
	// Convert to [][]any
	dataSlice, ok := dataParam.([]any)
	if !ok {
		return nil, fmt.Errorf("data must be an array")
	}

	result := make([][]any, 0, len(dataSlice))
	for _, rowParam := range dataSlice {
		rowSlice, ok := rowParam.([]any)
		if !ok {
			return nil, fmt.Errorf("each row must be an array")
		}
		result = append(result, rowSlice)
	}

	return result, nil
}
