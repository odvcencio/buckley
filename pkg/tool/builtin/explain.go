package builtin

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

// DefaultExplainOutputBytes bounds an explanation, including its source excerpt.
const DefaultExplainOutputBytes = 8 << 10

// ExplainCodeTool returns code and lightweight indicators for explanation.
type ExplainCodeTool struct{ workDirAware }

func (t *ExplainCodeTool) Name() string { return "explain_code" }
func (t *ExplainCodeTool) Description() string {
	return "Inspect a code snippet, file, or named function to help explain it. Returns a bounded source excerpt and keyword-based indicators, which can include comments and strings; use the source to reason about purpose and behavior. Omit unused selectors or use null; zero is not a placeholder. Truncated code is a prefix of the selected range; use read_file for more. This does not verify correctness."
}
func (t *ExplainCodeTool) Parameters() ParameterSchema {
	schema := ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"file":          {Type: "string", Description: "File to inspect; provide exactly one of file or code."},
		"code":          {Type: "string", Description: "Code snippet to inspect; provide exactly one of file or code."},
		"function_name": {Type: "string", Description: "Named Go, JS/TS, or Python function or method in file; qualified names such as Service.add disambiguate. Mutually exclusive with line selectors."},
		"start_line":    {Type: "integer", Description: "First line (1-indexed). With end_line alone, starts at 1. Mutually exclusive with function_name."},
		"end_line":      {Type: "integer", Description: "Last line (inclusive); defaults to start_line when start_line is supplied."},
	}, Required: []string{}}
	for name, property := range schema.Properties {
		property.Description += " Use null when unused."
		property.RawSchema = map[string]any{"type": []string{property.Type, "null"}, "description": property.Description}
		schema.Properties[name] = property
	}
	return schema
}

func (t *ExplainCodeTool) Execute(params map[string]any) (*Result, error) {
	values := map[string]string{}
	for _, name := range []string{"file", "code", "function_name"} {
		if value, present := params[name]; present && value != nil {
			text, ok := value.(string)
			if !ok {
				return &Result{Error: name + " parameter must be a string"}, nil
			}
			values[name] = text
		}
	}
	file, code, function := values["file"], values["code"], values["function_name"]
	if (file == "") == (code == "") {
		return &Result{Error: "provide exactly one non-empty file or code parameter"}, nil
	}
	start, end := 1, 0
	_, hasStart := params["start_line"]
	_, hasEnd := params["end_line"]
	hasStart = hasStart && params["start_line"] != nil
	hasEnd = hasEnd && params["end_line"] != nil
	if function != "" && (hasStart || hasEnd || file == "") {
		return &Result{Error: "function_name requires file and cannot be combined with line selectors"}, nil
	}
	if hasStart {
		line, err := readFileLineNumber("start_line", params["start_line"])
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
		start = line
		end = line
	}
	if hasEnd {
		line, err := readFileLineNumber("end_line", params["end_line"])
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
		end = line
	}
	if (hasStart || hasEnd) && end < start {
		return &Result{Error: "end_line must be greater than or equal to start_line"}, nil
	}
	limit := t.mutationFileLimit()
	if int64(len(code)) > limit || int64(len(function)) > limit {
		return &Result{Error: fmt.Sprintf("analysis input exceeds %d-byte limit; use a smaller snippet or line selection", limit)}, nil
	}
	source := "provided code snippet"
	if file != "" {
		path, err := resolvePath(t.workDir, file)
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return &Result{Error: fmt.Sprintf("read source: %v", err)}, nil
		}
		if !info.Mode().IsRegular() {
			return &Result{Error: "analysis requires a regular file"}, nil
		}
		content, _, exceeded, err := readFileSnapshot(path, limit)
		if err != nil {
			return &Result{Error: fmt.Sprintf("read source: %v", err)}, nil
		}
		if exceeded {
			return &Result{Error: fmt.Sprintf("analysis source exceeds %d-byte input limit; use read_file for bounded pages", limit)}, nil
		}
		code = content
		source = path
	}
	if function != "" {
		selection, err := selectExplainFunction(code, function, file)
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
		start = strings.Count(code[:selection.start], "\n") + 1
		end = start + strings.Count(code[selection.start:selection.end], "\n")
		code = code[selection.start:selection.end]
		source += ":" + function
	} else if hasStart || hasEnd {
		lines := strings.Count(code, "\n") + 1
		if start > lines || end > lines {
			return &Result{Error: fmt.Sprintf("requested lines %d-%d exceed source length of %d lines", start, end, lines)}, nil
		}
		from := mutationLineOffset(code, start)
		to := mutationLineOffset(code, end+1)
		if to > from && code[to-1] == '\n' {
			to--
		}
		code = code[from:to]
		source = fmt.Sprintf("%s:%d-%d", source, start, end)
	} else {
		end = strings.Count(code, "\n") + 1
	}
	if code == "" {
		return &Result{Error: "no code to explain"}, nil
	}
	data := map[string]any{"source": source, "explanation": t.analyzeCode(code), "start_line": start, "end_line": end, "selected_bytes": len(code)}
	budget := DefaultExplainOutputBytes
	if t.maxOutputBytes > 0 {
		budget = min(budget, t.maxOutputBytes)
	}
	data["code"] = ""
	data["code_truncated"] = false
	data["returned_bytes"] = len(code)
	payload := map[string]any{"success": true, "data": data}
	codec := toon.New(true)
	size := func() (int, error) {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		jsonBytes := len(encoded)
		toonEncoded, err := codec.MarshalString(payload)
		return max(jsonBytes, len(toonEncoded)), err
	}
	baseBytes, err := size()
	if err != nil {
		return nil, err
	}
	if baseBytes >= budget {
		return &Result{Error: fmt.Sprintf("analysis metadata exceeds %d-byte output budget; use a narrower selection", budget)}, nil
	}
	low, high := 0, min(len(code), budget)
	for low < high {
		mid := low + (high-low+1)/2
		boundary := mid
		for boundary > 0 && boundary < len(code) && !utf8.RuneStart(code[boundary]) {
			boundary--
		}
		encoded, _ := json.Marshal(code[:boundary])
		if len(encoded)-2 <= budget-baseBytes {
			low = mid
		} else {
			high = mid - 1
		}
	}
	for {
		for low > 0 && low < len(code) && !utf8.RuneStart(code[low]) {
			low--
		}
		data["code"] = code[:low]
		data["code_truncated"] = low < len(code)
		data["returned_bytes"] = low
		encodedBytes, err := size()
		if err != nil {
			return nil, err
		}
		if encodedBytes <= budget {
			break
		}
		low /= 2
	}
	data["code"] = strings.Clone(code[:low])

	return &Result{Success: true, Data: data}, nil
}

func (t *ExplainCodeTool) analyzeCode(code string) map[string]any {
	loops, conditions, functions, errors, errorSummary, returns := false, false, false, false, false, false
	complexity := 0
	word := func(b byte) bool {
		return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
	}
	for i := 0; i < len(code); {
		c := code[i]

		if word(c) {
			start := i
			for i < len(code) && word(code[i]) {
				i++
			}
			switch code[start:i] {
			case "for", "while":
				loops = true
				complexity++
			case "if":
				conditions = true
				complexity++
			case "switch", "case":
				complexity++
			case "func", "function", "def":
				functions = true
			case "try", "catch", "error":
				errors = true
				errorSummary = true
			case "err":
				errors = true
			case "return":
				returns = true
			}
		} else if i+1 < len(code) && (code[i:i+2] == "&&" || code[i:i+2] == "||") {
			complexity++
			i += 2
		} else {
			i++
		}
	}
	estimate := "low"
	if complexity > 15 {
		estimate = "high"
	} else if complexity > 5 {
		estimate = "medium"
	}
	parts := make([]string, 0, 5)
	if functions {
		parts = append(parts, "function declarations")
	}
	if loops {
		parts = append(parts, "loops")
	}
	if conditions {
		parts = append(parts, "conditional logic")
	}
	if errorSummary {
		parts = append(parts, "error handling")
	}
	if returns {
		parts = append(parts, "return values")
	}
	summary := "No recognized control-flow keywords"
	if len(parts) > 0 {
		summary = "Text indicators: " + strings.Join(parts, ", ")
	}
	return map[string]any{"lines": strings.Count(code, "\n") + 1, "has_loops": loops, "has_conditions": conditions, "has_functions": functions, "has_error_handling": errors, "complexity_estimate": estimate, "summary": summary, "analysis_kind": "keyword_heuristic", "limitations": "Comments and strings can contribute indicators; this does not infer purpose or verify behavior."}
}
