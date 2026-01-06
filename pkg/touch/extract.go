package touch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/approval"
	"github.com/pmezard/go-difflib/difflib"
)

const diffLineLimit = 200

type DiffLine struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type RichFields struct {
	OperationType string
	Description   string
	Command       string
	FilePath      string
	DiffLines     []DiffLine
	AddedLines    int32
	RemovedLines  int32
	Ranges        []LineRange
}

func ExtractFromJSON(toolName, raw string) RichFields {
	return ExtractFromArgs(toolName, parseArgs(raw))
}

func ExtractFromArgs(toolName string, args map[string]any) RichFields {
	name := strings.ToLower(strings.TrimSpace(toolName))
	rich := RichFields{
		OperationType: inferOperationType(name, ""),
	}

	switch name {
	case "run_shell":
		cmd := stringParam(args, "command", "cmd")
		rich.Command = cmd
		if cmd != "" {
			rich.OperationType = approval.ClassifyCommand(cmd).String()
		} else if rich.OperationType == "" {
			rich.OperationType = approval.OpShellWrite.String()
		}
		rich.Description = "Run shell command"
	case "write_file":
		path := pathParam(args)
		rich.OperationType = approval.OpWrite.String()
		rich.Description = "Write file"
		rich.FilePath = path
		rich.DiffLines, rich.AddedLines, rich.RemovedLines, rich.Ranges = diffPreviewForWrite(path, stringParam(args, "content"))
		if path != "" && isNewFile(path) {
			rich.Description = "Create file"
		}
	case "edit_file":
		path := pathParam(args)
		rich.OperationType = approval.OpWrite.String()
		rich.Description = "Edit file"
		rich.FilePath = path
		rich.DiffLines, rich.AddedLines, rich.RemovedLines, rich.Ranges = diffPreviewForEdit(
			path,
			stringParam(args, "old_string"),
			stringParam(args, "new_string"),
			boolParam(args, "replace_all"),
		)
	case "apply_patch":
		patch := stringParam(args, "patch")
		rich.OperationType = approval.OpWrite.String()
		rich.Description = "Apply patch"
		rich.FilePath = firstPatchFilePath(patch)
		rich.DiffLines, rich.AddedLines, rich.RemovedLines, rich.Ranges = diffFromPatch(patch)
	case "read_file":
		path := pathParam(args)
		rich.OperationType = approval.OpRead.String()
		rich.Description = "Read file"
		rich.FilePath = path
	case "list_directory":
		path := pathParam(args)
		rich.OperationType = approval.OpRead.String()
		rich.Description = "List directory"
		rich.FilePath = path
	default:
		if rich.OperationType == "" {
			rich.OperationType = inferOperationType(name, "")
		}
		rich.Command = stringParam(args, "command")
		rich.FilePath = pathParam(args)
		if rich.Description == "" && strings.TrimSpace(toolName) != "" {
			rich.Description = strings.ReplaceAll(toolName, "_", " ")
		}
	}

	return rich
}

func TTLForOperation(operationType string) time.Duration {
	switch operationType {
	case approval.OpRead.String(), approval.OpShellRead.String(), approval.OpGitRead.String():
		return 2 * time.Minute
	case approval.OpWrite.String(), approval.OpDelete.String(), approval.OpShellWrite.String(), approval.OpGitWrite.String():
		return 10 * time.Minute
	case approval.OpShellNetwork.String(), approval.OpNetwork.String():
		return 5 * time.Minute
	default:
		return 3 * time.Minute
	}
}

func parseArgs(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil
	}
	return args
}

func stringParam(args map[string]any, keys ...string) string {
	if args == nil {
		return ""
	}
	for _, key := range keys {
		value, ok := args[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			return strings.TrimSpace(v)
		case json.Number:
			return v.String()
		}
	}
	return ""
}

func boolParam(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	value, ok := args[key]
	if !ok {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		trimmed := strings.TrimSpace(strings.ToLower(v))
		return trimmed == "true" || trimmed == "1" || trimmed == "yes"
	case float64:
		return v != 0
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n != 0
		}
	}
	return false
}

func pathParam(args map[string]any) string {
	path := stringParam(args, "path", "file_path")
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func inferOperationType(toolName, command string) string {
	if toolName == "" {
		return ""
	}
	if toolName == "run_shell" {
		if strings.TrimSpace(command) != "" {
			return approval.ClassifyCommand(command).String()
		}
		return approval.OpShellWrite.String()
	}
	if strings.HasPrefix(toolName, "git_") || strings.Contains(toolName, "git") {
		return approval.OpGitRead.String()
	}
	if strings.Contains(toolName, "http") || strings.Contains(toolName, "fetch") || strings.Contains(toolName, "request") {
		return approval.OpNetwork.String()
	}
	if containsAny(toolName, "write", "edit", "apply", "patch", "create", "rename", "extract", "delete", "remove", "merge") {
		return approval.OpWrite.String()
	}
	if containsAny(toolName, "read", "list", "search", "find", "view", "status", "diff", "log", "show") {
		return approval.OpRead.String()
	}
	return ""
}

func containsAny(source string, tokens ...string) bool {
	for _, token := range tokens {
		if strings.Contains(source, token) {
			return true
		}
	}
	return false
}

func diffPreviewForWrite(path, newContent string) ([]DiffLine, int32, int32, []LineRange) {
	if strings.TrimSpace(path) == "" {
		return nil, 0, 0, nil
	}
	oldContent, ok := readFileAllowMissing(path)
	if !ok {
		return nil, 0, 0, nil
	}
	diffText, err := buildUnifiedDiff(path, oldContent, newContent)
	if err != nil {
		return nil, 0, 0, nil
	}
	return parseUnifiedDiff(diffText)
}

func diffPreviewForEdit(path, oldString, newString string, replaceAll bool) ([]DiffLine, int32, int32, []LineRange) {
	if strings.TrimSpace(path) == "" || oldString == "" {
		return nil, 0, 0, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, 0, nil
	}
	oldContent := string(content)
	if !strings.Contains(oldContent, oldString) {
		return nil, 0, 0, nil
	}
	if !replaceAll && strings.Count(oldContent, oldString) > 1 {
		return nil, 0, 0, nil
	}
	var newContent string
	if replaceAll {
		newContent = strings.ReplaceAll(oldContent, oldString, newString)
	} else {
		newContent = strings.Replace(oldContent, oldString, newString, 1)
	}
	diffText, err := buildUnifiedDiff(path, oldContent, newContent)
	if err != nil {
		return nil, 0, 0, nil
	}
	return parseUnifiedDiff(diffText)
}

func diffFromPatch(patch string) ([]DiffLine, int32, int32, []LineRange) {
	if strings.TrimSpace(patch) == "" {
		return nil, 0, 0, nil
	}
	return parseUnifiedDiff(patch)
}

func buildUnifiedDiff(path, from, to string) (string, error) {
	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(from),
		B:        difflib.SplitLines(to),
		FromFile: path,
		ToFile:   path,
		Context:  3,
	}
	return difflib.GetUnifiedDiffString(diff)
}

func parseUnifiedDiff(diff string) ([]DiffLine, int32, int32, []LineRange) {
	if strings.TrimSpace(diff) == "" {
		return nil, 0, 0, nil
	}
	lines := strings.Split(diff, "\n")
	preview := make([]DiffLine, 0, len(lines))
	ranges := make([]LineRange, 0, 4)
	var added int32
	var removed int32

	for _, line := range lines {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			if r, ok := parseHunkHeader(line); ok {
				ranges = append(ranges, r)
			}
		}
		lineType, content, add, remove := classifyDiffLine(line)
		if add {
			added++
		}
		if remove {
			removed++
		}
		if lineType == "" {
			continue
		}
		if len(preview) < diffLineLimit {
			preview = append(preview, DiffLine{Type: lineType, Content: content})
		}
	}
	return preview, added, removed, ranges
}

func parseHunkHeader(line string) (LineRange, bool) {
	header := strings.TrimSpace(strings.TrimPrefix(line, "@@"))
	header = strings.TrimSuffix(header, "@@")
	header = strings.TrimSpace(header)
	if header == "" {
		return LineRange{}, false
	}
	parts := strings.Fields(header)
	if len(parts) < 2 {
		return LineRange{}, false
	}
	oldStart, oldCount, ok := parseRangePart(parts[0])
	if !ok {
		return LineRange{}, false
	}
	newStart, newCount, ok := parseRangePart(parts[1])
	if !ok {
		return LineRange{}, false
	}

	start := newStart
	count := newCount
	if count == 0 {
		start = oldStart
		count = oldCount
	}
	if count <= 0 {
		return LineRange{}, false
	}
	return LineRange{Start: start, End: start + count - 1}, true
}

func parseRangePart(raw string) (int, int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, 0, false
	}
	if raw[0] != '-' && raw[0] != '+' {
		return 0, 0, false
	}
	raw = raw[1:]
	if raw == "" {
		return 0, 0, false
	}
	parts := strings.Split(raw, ",")
	start, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	count := 1
	if len(parts) > 1 {
		if parsed, err := strconv.Atoi(parts[1]); err == nil {
			count = parsed
		}
	}
	return start, count, true
}

func classifyDiffLine(line string) (string, string, bool, bool) {
	switch {
	case strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index "):
		return "context", line, false, false
	case strings.HasPrefix(line, "@@"):
		return "context", line, false, false
	case strings.HasPrefix(line, "+"):
		if strings.HasPrefix(line, "+++") {
			return "context", line, false, false
		}
		return "add", strings.TrimPrefix(line, "+"), true, false
	case strings.HasPrefix(line, "-"):
		if strings.HasPrefix(line, "---") {
			return "context", line, false, false
		}
		return "remove", strings.TrimPrefix(line, "-"), false, true
	case strings.HasPrefix(line, " "):
		return "context", strings.TrimPrefix(line, " "), false, false
	case strings.HasPrefix(line, "\\"):
		return "context", line, false, false
	default:
		return "context", line, false, false
	}
}

func readFileAllowMissing(path string) (string, bool) {
	content, err := os.ReadFile(path)
	if err == nil {
		return string(content), true
	}
	if os.IsNotExist(err) {
		return "", true
	}
	return "", false
}

func isNewFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

func firstPatchFilePath(patch string) string {
	if strings.TrimSpace(patch) == "" {
		return ""
	}
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "+++ ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if path == "/dev/null" {
				continue
			}
			return cleanPatchPath(path)
		}
		if strings.HasPrefix(line, "--- ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			if path == "/dev/null" {
				continue
			}
			return cleanPatchPath(path)
		}
		if strings.HasPrefix(line, "diff --git ") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				return cleanPatchPath(fields[3])
			}
		}
	}
	return ""
}

func cleanPatchPath(path string) string {
	cleaned := strings.Trim(strings.TrimSpace(path), "\"")
	cleaned = strings.TrimPrefix(cleaned, "a/")
	cleaned = strings.TrimPrefix(cleaned, "b/")
	return cleaned
}
