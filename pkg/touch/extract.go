package touch

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/internal/textdiff"
	"m31labs.dev/buckley/pkg/approval"
)

const (
	diffLineLimit = 200
	// DefaultPreviewFileBytes bounds optional whole-file preview work.
	DefaultPreviewFileBytes = 64 << 10
	// DefaultPreviewBytes bounds retained diff text across all preview lines.
	DefaultPreviewBytes = 16 << 10
	previewLineBytes    = 2 << 10
)

// ExtractOptions controls optional file preview I/O; metadata is always extracted.
type ExtractOptions struct {
	MaxFileBytes int64
	ResolvePath  func(string) (string, error)
	SkipFileDiff bool
}

func (o ExtractOptions) fileLimit() int64 {
	if o.MaxFileBytes > 0 {
		return min(o.MaxFileBytes, DefaultPreviewFileBytes)
	}
	return DefaultPreviewFileBytes
}

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
	return ExtractFromArgsWithOptions(toolName, args, ExtractOptions{})
}

func ExtractFromArgsWithOptions(toolName string, args map[string]any, options ExtractOptions) RichFields {
	name := strings.ToLower(strings.TrimSpace(toolName))
	rich := RichFields{
		OperationType: inferOperationType(name, ""),
	}

	path := pathParam(args)
	if path != "" && options.ResolvePath != nil {
		resolved, err := options.ResolvePath(path)
		if err != nil {
			options.SkipFileDiff = true
		} else {
			path = resolved
		}
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
		rich.OperationType = approval.OpWrite.String()
		rich.Description = "Write file"
		rich.FilePath = path
		if !options.SkipFileDiff {
			rich.DiffLines, rich.AddedLines, rich.RemovedLines, rich.Ranges = diffPreviewForWrite(path, rawTextParam(args, "content"), options.fileLimit())
		}
		if !options.SkipFileDiff && path != "" && isNewFile(path) {
			rich.Description = "Create file"
		}
	case "edit_file":
		rich.OperationType = approval.OpWrite.String()
		rich.Description = "Edit file"
		rich.FilePath = path
		if !options.SkipFileDiff && !hasBatchEdits(args) {
			rich.DiffLines, rich.AddedLines, rich.RemovedLines, rich.Ranges = diffPreviewForEdit(path, rawTextParam(args, "old_string"), rawTextParam(args, "new_string"), boolParam(args, "replace_all"), options.fileLimit())
		}
	case "apply_patch":
		patch := rawTextParam(args, "patch")
		rich.OperationType = approval.OpWrite.String()
		rich.Description = "Apply patch"
		rich.FilePath = firstPatchFilePath(patch)
		rich.DiffLines, rich.AddedLines, rich.RemovedLines, rich.Ranges = diffFromPatch(patch)
	case "read_file":
		rich.OperationType = approval.OpRead.String()
		rich.Description = "Read file"
		rich.FilePath = path
	case "list_directory":
		rich.OperationType = approval.OpRead.String()
		rich.Description = "List directory"
		rich.FilePath = path
	default:
		if rich.OperationType == "" {
			rich.OperationType = inferOperationType(name, "")
		}
		rich.Command = stringParam(args, "command")
		rich.FilePath = path
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
	path := rawTextParam(args, "path")
	if path == "" {
		path = rawTextParam(args, "file_path")
	}
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

func diffPreviewForWrite(path, newContent string, limit int64) ([]DiffLine, int32, int32, []LineRange) {
	if path == "" || int64(len(newContent)) > limit {
		return nil, 0, 0, nil
	}
	oldContent, ok := readFileAllowMissing(path, limit)
	if !ok {
		return nil, 0, 0, nil
	}
	return parseUnifiedDiff(textdiff.Unified(path, oldContent, newContent))
}

func diffPreviewForEdit(path, oldString, newString string, replaceAll bool, limit int64) ([]DiffLine, int32, int32, []LineRange) {
	if path == "" || oldString == "" || int64(len(newString)) > limit {
		return nil, 0, 0, nil
	}
	oldContent, ok := readFileAllowMissing(path, limit)
	if !ok {
		return nil, 0, 0, nil
	}
	if !strings.Contains(oldContent, oldString) {
		return nil, 0, 0, nil
	}
	if !replaceAll && strings.Count(oldContent, oldString) > 1 {
		return nil, 0, 0, nil
	}
	count := strings.Count(oldContent, oldString)
	if !replaceAll {
		count = min(count, 1)
	}
	growth := int64(len(newString)) - int64(len(oldString))
	if growth > 0 && int64(count) > (limit-int64(len(oldContent)))/growth {
		return nil, 0, 0, nil
	}
	var newContent string
	if replaceAll {
		newContent = strings.ReplaceAll(oldContent, oldString, newString)
	} else {
		newContent = strings.Replace(oldContent, oldString, newString, 1)
	}
	return parseUnifiedDiff(textdiff.Unified(path, oldContent, newContent))
}

func diffFromPatch(patch string) ([]DiffLine, int32, int32, []LineRange) {
	if strings.TrimSpace(patch) == "" {
		return nil, 0, 0, nil
	}
	return parseUnifiedDiff(patch)
}

// WithDiff enriches metadata from an already computed unified diff without I/O.
func WithDiff(rich RichFields, diff string) RichFields {
	rich.DiffLines, rich.AddedLines, rich.RemovedLines, rich.Ranges = parseUnifiedDiff(diff)
	return rich
}

func parseUnifiedDiff(diff string) ([]DiffLine, int32, int32, []LineRange) {
	if strings.TrimSpace(diff) == "" {
		return nil, 0, 0, nil
	}
	preview := make([]DiffLine, 0, min(strings.Count(diff, "\n"), diffLineLimit))
	remaining := DefaultPreviewBytes
	ranges := make([]LineRange, 0, 4)
	var added int32
	var removed int32

	for line := range strings.Lines(diff) {
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "@@") && len(ranges) < diffLineLimit && len(line) <= 512 {
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
		if len(preview) < diffLineLimit && remaining > 0 {
			content = boundedDiffLine(content, min(remaining, previewLineBytes))
			if content != "" {
				preview = append(preview, DiffLine{Type: lineType, Content: content})
				remaining -= len(content)
			}
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

func readFileAllowMissing(path string, limit int64) (string, bool) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "", true
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return "", false
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(content)) > limit {
		return "", false
	}
	return string(content), true
}

func rawTextParam(args map[string]any, key string) string {
	text, _ := args[key].(string)
	return text
}

func hasBatchEdits(args map[string]any) bool {
	raw, exists := args["edits"]
	if !exists {
		return false
	}
	edits, ok := raw.([]any)
	return !ok || len(edits) > 0
}

func boundedDiffLine(text string, limit int) string {
	if len(text) <= limit {
		return strings.Clone(text)
	}
	const marker = "... [truncated]"
	if limit < len(marker) {
		return ""
	}
	end := limit - len(marker)
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + marker
}

func isNewFile(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

func firstPatchFilePath(patch string) string {
	if strings.TrimSpace(patch) == "" {
		return ""
	}
	for line := range strings.Lines(patch) {
		line = strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(line, "+++ ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if path == "/dev/null" {
				continue
			}
			return strings.Clone(cleanPatchPath(path))
		}
		if strings.HasPrefix(line, "--- ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			if path == "/dev/null" {
				continue
			}
			return strings.Clone(cleanPatchPath(path))
		}
		if strings.HasPrefix(line, "diff --git ") && len(line) <= 4096 {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				return strings.Clone(cleanPatchPath(fields[3]))
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
