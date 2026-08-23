package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/secretsafety"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

const (
	maxLaunchPathBytes    = 512
	maxLaunchListResults  = 1000
	maxLaunchSearchResult = 200
	maxLaunchSearchBytes  = 32 << 20
	maxLaunchCommandBytes = 32 << 10
	maxLaunchCommandTime  = 10 * time.Minute
	maxLaunchBoundaryScan = 100_000
	launchBoundaryTimeout = 5 * time.Second
)

var (
	errLaunchWorkspaceBoundary = errors.New("launch workspace boundary changed")
	errLaunchSandboxTerminal   = errors.New("launch sandbox terminal failure")
)

type launchWorkspace struct {
	root                 *os.Root
	maxFileBytes         int64
	afterAtomicWriteSync func(string)
}

func (w *launchWorkspace) read(path string) ([]byte, os.FileInfo, error) {
	path, err := cleanLaunchPath(path, false)
	if err != nil {
		return nil, nil, err
	}
	if err := ensureLaunchPath(w.root, path, false); err != nil {
		return nil, nil, err
	}
	before, err := w.root.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, nil, errors.New("path is not a regular file")
	}
	if before.Size() < 0 || before.Size() > w.maxFileBytes {
		return nil, nil, errors.New("file exceeds launch read limit")
	}
	if launchFileHasMultipleLinks(before) {
		return nil, nil, errors.New("hardlinked files are not accessible")
	}
	file, err := w.root.Open(path)
	if err != nil {
		return nil, nil, errors.New("open file failed")
	}
	opened, statErr := file.Stat()
	data, readErr := io.ReadAll(io.LimitReader(file, w.maxFileBytes+1))
	afterRead, afterReadErr := file.Stat()
	closeErr := file.Close()
	after, lstatErr := w.root.Lstat(path)
	if statErr != nil || readErr != nil || afterReadErr != nil || closeErr != nil || lstatErr != nil || int64(len(data)) > w.maxFileBytes {
		return nil, nil, errors.New("stable file read failed")
	}
	for _, current := range []os.FileInfo{opened, afterRead, after} {
		if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || launchFileHasMultipleLinks(current) || !os.SameFile(before, current) || current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) {
			return nil, nil, errors.New("file changed during read")
		}
	}
	return data, before, nil
}

func (w *launchWorkspace) write(path string, data []byte, expected os.FileInfo) error {
	path, err := cleanLaunchPath(path, false)
	if err != nil {
		return err
	}
	if int64(len(data)) > w.maxFileBytes {
		return errors.New("content exceeds launch write limit")
	}
	dir := filepath.Dir(path)
	if err := ensureLaunchDirectories(w.root, dir); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if existing, statErr := w.root.Lstat(path); statErr == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return errors.New("destination is not a regular file")
		}
		if launchFileHasMultipleLinks(existing) {
			return errors.New("hardlinked files are not accessible")
		}
		if expected != nil && (!os.SameFile(expected, existing) || existing.Size() != expected.Size() || !existing.ModTime().Equal(expected.ModTime())) {
			return errors.New("destination changed before write")
		}
		mode = existing.Mode().Perm()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("inspect destination failed")
	} else if expected != nil {
		return errors.New("destination disappeared before write")
	}

	temp, err := launchTempName(dir)
	if err != nil {
		return errors.New("prepare atomic write failed")
	}
	file, err := w.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return errors.New("create atomic write failed")
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = w.root.Remove(temp)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return errors.New("write file failed")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return errors.New("sync file failed")
	}
	written, statErr := file.Stat()
	if statErr != nil || !written.Mode().IsRegular() || launchFileHasMultipleLinks(written) || written.Size() != int64(len(data)) {
		_ = file.Close()
		return errors.New("validate atomic write failed")
	}
	if err := file.Close(); err != nil {
		return errors.New("close file failed")
	}
	if w.afterAtomicWriteSync != nil {
		w.afterAtomicWriteSync(temp)
	}
	if err := ensureLaunchPath(w.root, dir, false); err != nil {
		return err
	}
	if err := w.root.Rename(temp, path); err != nil {
		return errors.New("commit atomic write failed")
	}
	removeTemp = false
	committed, err := w.root.Lstat(path)
	if err != nil || committed.Mode()&os.ModeSymlink != 0 || !committed.Mode().IsRegular() || launchFileHasMultipleLinks(committed) || !os.SameFile(written, committed) || committed.Size() != int64(len(data)) || !committed.ModTime().Equal(written.ModTime()) {
		return errLaunchWorkspaceBoundary
	}
	return nil
}

type launchReadFileTool struct{ workspace *launchWorkspace }

func (*launchReadFileTool) Name() string { return "read_file" }
func (*launchReadFileTool) Description() string {
	return "Read a regular workspace file without following symlinks."
}
func (*launchReadFileTool) Parameters() builtin.ParameterSchema {
	return pathSchema("Path to the workspace file")
}
func (t *launchReadFileTool) Execute(params map[string]any) (*builtin.Result, error) {
	path, err := requiredString(params, "path", maxLaunchPathBytes)
	if err != nil {
		return failedResult(err), nil
	}
	data, _, err := t.workspace.read(path)
	if err != nil {
		return failedResult(err), nil
	}
	if secretsafety.BinaryContent(data) || secretsafety.BinaryPath(path) {
		return failedResult(errors.New("binary files are not disclosed by launch tools")), nil
	}
	return &builtin.Result{Success: true, Data: map[string]any{"path": filepath.ToSlash(path), "content": string(data), "size": len(data)}}, nil
}

type launchWriteFileTool struct{ workspace *launchWorkspace }

func (*launchWriteFileTool) Name() string { return "write_file" }
func (*launchWriteFileTool) Description() string {
	return "Atomically write a regular workspace file without following symlinks."
}
func (*launchWriteFileTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object", Properties: map[string]builtin.PropertySchema{
		"path":    {Type: "string", Description: "Path to the workspace file"},
		"content": {Type: "string", Description: "Complete replacement content"},
	}, Required: []string{"path", "content"}, AdditionalProperties: false}
}
func (t *launchWriteFileTool) Execute(params map[string]any) (*builtin.Result, error) {
	path, err := requiredString(params, "path", maxLaunchPathBytes)
	if err != nil {
		return failedResult(err), nil
	}
	content, ok := params["content"].(string)
	if !ok {
		return failedResult(errors.New("content must be a string")), nil
	}
	if err := t.workspace.write(path, []byte(content), nil); err != nil {
		if errors.Is(err, errLaunchWorkspaceBoundary) {
			return nil, err
		}
		return failedResult(err), nil
	}
	return &builtin.Result{Success: true, Data: map[string]any{"path": filepath.ToSlash(path), "size": len(content)}}, nil
}

type launchEditFileTool struct{ workspace *launchWorkspace }

func (*launchEditFileTool) Name() string { return "edit_file" }
func (*launchEditFileTool) Description() string {
	return "Replace one exact string in a regular workspace file."
}
func (*launchEditFileTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object", Properties: map[string]builtin.PropertySchema{
		"path":       {Type: "string", Description: "Path to the workspace file"},
		"old_string": {Type: "string", Description: "Unique text to replace"},
		"new_string": {Type: "string", Description: "Replacement text"},
	}, Required: []string{"path", "old_string", "new_string"}, AdditionalProperties: false}
}
func (t *launchEditFileTool) Execute(params map[string]any) (*builtin.Result, error) {
	path, err := requiredString(params, "path", maxLaunchPathBytes)
	if err != nil {
		return failedResult(err), nil
	}
	oldText, ok := params["old_string"].(string)
	if !ok || oldText == "" {
		return failedResult(errors.New("old_string must be a non-empty string")), nil
	}
	newText, ok := params["new_string"].(string)
	if !ok {
		return failedResult(errors.New("new_string must be a string")), nil
	}
	data, info, err := t.workspace.read(path)
	if err != nil {
		return failedResult(err), nil
	}
	if secretsafety.BinaryContent(data) || secretsafety.BinaryPath(path) {
		return failedResult(errors.New("binary files cannot be edited by launch tools")), nil
	}
	count := strings.Count(string(data), oldText)
	if count != 1 {
		return failedResult(fmt.Errorf("old_string must occur exactly once (found %d)", count)), nil
	}
	replacement := strings.Replace(string(data), oldText, newText, 1)
	if err := t.workspace.write(path, []byte(replacement), info); err != nil {
		if errors.Is(err, errLaunchWorkspaceBoundary) {
			return nil, err
		}
		return failedResult(err), nil
	}
	return &builtin.Result{Success: true, Data: map[string]any{"path": filepath.ToSlash(path), "replacements": 1}}, nil
}

type launchListFilesTool struct{ workspace *launchWorkspace }

func (*launchListFilesTool) Name() string { return "list_files" }
func (*launchListFilesTool) Description() string {
	return "List regular workspace files without following symlinks."
}
func (*launchListFilesTool) Parameters() builtin.ParameterSchema {
	return pathOptionalSchema("Workspace directory to list")
}
func (t *launchListFilesTool) Execute(params map[string]any) (*builtin.Result, error) {
	path, err := optionalPath(params)
	if err != nil {
		return failedResult(err), nil
	}
	files, truncated, err := t.workspace.walkRegular(path, maxLaunchListResults, nil)
	if err != nil {
		return failedResult(err), nil
	}
	return &builtin.Result{Success: true, Data: map[string]any{"files": files, "count": len(files), "truncated": truncated}}, nil
}

type launchSearchFilesTool struct{ workspace *launchWorkspace }

func (*launchSearchFilesTool) Name() string { return "search_files" }
func (*launchSearchFilesTool) Description() string {
	return "Search regular workspace text files without following symlinks."
}
func (*launchSearchFilesTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object", Properties: map[string]builtin.PropertySchema{
		"query": {Type: "string", Description: "Literal text to search for"},
		"path":  {Type: "string", Description: "Workspace directory to search", Default: "."},
	}, Required: []string{"query"}, AdditionalProperties: false}
}
func (t *launchSearchFilesTool) Execute(params map[string]any) (*builtin.Result, error) {
	query, err := requiredString(params, "query", 4096)
	if err != nil || query == "" {
		return failedResult(errors.New("query must be a non-empty bounded string")), nil
	}
	path, err := optionalPath(params)
	if err != nil {
		return failedResult(err), nil
	}
	matches := make([]map[string]any, 0)
	_, truncated, err := t.workspace.walkRegular(path, maxLaunchListResults, func(rel string, data []byte) bool {
		if secretsafety.BinaryPath(rel) || secretsafety.BinaryContent(data) {
			return true
		}
		for idx, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, query) {
				matches = append(matches, map[string]any{"path": rel, "line": idx + 1, "text": boundedLaunchText(line, 512)})
				if len(matches) >= maxLaunchSearchResult {
					return false
				}
			}
		}
		return len(matches) < maxLaunchSearchResult
	})
	if err != nil {
		return failedResult(err), nil
	}
	return &builtin.Result{Success: true, Data: map[string]any{"matches": matches, "count": len(matches), "truncated": truncated || len(matches) >= maxLaunchSearchResult}}, nil
}

func (w *launchWorkspace) walkRegular(start string, max int, visit func(string, []byte) bool) ([]string, bool, error) {
	start, err := cleanLaunchPath(start, true)
	if err != nil {
		return nil, false, err
	}
	if err := ensureLaunchPath(w.root, start, false); err != nil {
		return nil, false, err
	}
	files := make([]string, 0)
	truncated := false
	scannedBytes := int64(0)
	err = fs.WalkDir(w.root.FS(), start, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("workspace traversal failed")
		}
		if path == ".git" || strings.Contains(filepath.ToSlash(path), "/.git/") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return errors.New("git metadata is not accessible")
		}
		info, err := entry.Info()
		if err != nil {
			return errors.New("workspace entry inspection failed")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if launchFileHasMultipleLinks(info) {
			return errors.New("hardlinked files are not accessible")
		}
		if len(files) >= max {
			truncated = true
			return fs.SkipAll
		}
		rel := filepath.ToSlash(path)
		files = append(files, rel)
		if visit != nil {
			if secretsafety.BinaryPath(rel) {
				return nil
			}
			if scannedBytes >= maxLaunchSearchBytes {
				truncated = true
				return fs.SkipAll
			}
			data, binary, skipped, readErr := w.readForSearch(rel, maxLaunchSearchBytes-scannedBytes)
			if readErr != nil {
				return readErr
			}
			if binary {
				return nil
			}
			if skipped {
				truncated = true
				return fs.SkipAll
			}
			scannedBytes += int64(len(data))
			if scannedBytes > maxLaunchSearchBytes {
				return errors.New("workspace search byte limit exceeded")
			}
			if !visit(rel, data) {
				truncated = true
				return fs.SkipAll
			}
		}
		return nil
	})
	sort.Strings(files)
	return files, truncated, err
}

func (w *launchWorkspace) readForSearch(path string, remaining int64) ([]byte, bool, bool, error) {
	if remaining < 0 {
		return nil, false, true, nil
	}
	before, err := w.root.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || launchFileHasMultipleLinks(before) {
		return nil, false, false, errors.New("workspace search boundary is invalid")
	}
	if before.Size() < 0 {
		return nil, false, false, errors.New("workspace search boundary is invalid")
	}
	file, err := w.root.Open(path)
	if err != nil {
		return nil, false, false, errors.New("open search file failed")
	}
	opened, statErr := file.Stat()
	prefixSize := before.Size()
	if prefixSize > 64<<10 {
		prefixSize = 64 << 10
	}
	prefix := make([]byte, int(prefixSize))
	n, prefixErr := io.ReadFull(file, prefix)
	if errors.Is(prefixErr, io.EOF) || errors.Is(prefixErr, io.ErrUnexpectedEOF) {
		prefixErr = nil
	}
	prefix = prefix[:n]
	binary := secretsafety.BinaryContent(prefix)
	skipped := !binary && before.Size() > remaining
	data := prefix
	var readErr error
	if !binary && !skipped {
		var rest []byte
		rest, readErr = io.ReadAll(io.LimitReader(file, remaining-int64(len(prefix))+1))
		data = append(data, rest...)
	}
	afterRead, afterReadErr := file.Stat()
	closeErr := file.Close()
	after, lstatErr := w.root.Lstat(path)
	if statErr != nil || prefixErr != nil || readErr != nil || afterReadErr != nil || closeErr != nil || lstatErr != nil || !binary && !skipped && (int64(len(data)) > remaining || int64(len(data)) != before.Size()) {
		return nil, false, false, errors.New("stable search file read failed")
	}
	for _, current := range []os.FileInfo{opened, afterRead, after} {
		if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || launchFileHasMultipleLinks(current) || !os.SameFile(before, current) || current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) {
			return nil, false, false, errors.New("search file changed during read")
		}
	}
	if binary || skipped {
		return nil, binary, skipped, nil
	}
	return data, secretsafety.BinaryContent(data), false, nil
}

type launchCommandTool struct {
	name      string
	sandbox   SandboxExecutor
	workspace *launchWorkspace
}

func (t *launchCommandTool) Name() string { return t.name }
func (t *launchCommandTool) Description() string {
	if t.name == "run_tests" {
		return "Run an explicit test command in the networkless launch sandbox."
	}
	return "Run a command in the networkless launch sandbox."
}
func (t *launchCommandTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object", Properties: map[string]builtin.PropertySchema{
		"command":           {Type: "string", Description: "Command to execute"},
		"working_directory": {Type: "string", Description: "Workspace-relative directory", Default: "."},
		"timeout_seconds":   {Type: "integer", Description: "Timeout in seconds", Default: 120},
	}, Required: []string{"command"}, AdditionalProperties: false}
}
func (t *launchCommandTool) Execute(params map[string]any) (*builtin.Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *launchCommandTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*builtin.Result, error) {
	command, err := requiredString(params, "command", maxLaunchCommandBytes)
	if err != nil || strings.TrimSpace(command) == "" {
		return failedResult(errors.New("command must be a non-empty bounded string")), nil
	}
	workDir := "."
	if raw, ok := params["working_directory"]; ok {
		value, ok := raw.(string)
		if !ok {
			return failedResult(errors.New("working_directory must be a string")), nil
		}
		workDir, err = cleanLaunchPath(value, true)
		if err != nil {
			return failedResult(err), nil
		}
	}
	timeout, err := launchTimeout(params["timeout_seconds"])
	if err != nil {
		return failedResult(err), nil
	}
	containerDir := "/workspace"
	if workDir != "." {
		containerDir += "/" + filepath.ToSlash(workDir)
	}
	if t.workspace == nil || t.workspace.validateBoundary(ctx) != nil {
		return nil, errLaunchWorkspaceBoundary
	}
	result, err := t.sandbox.Execute(ctx, SandboxRequest{Command: command, WorkDir: containerDir, Timeout: timeout, ToolName: t.name})
	if t.workspace.validateBoundary(context.Background()) != nil {
		return nil, errLaunchWorkspaceBoundary
	}
	if err != nil {
		return nil, errLaunchSandboxTerminal
	}
	if result == nil || result.Killed {
		return nil, errLaunchSandboxTerminal
	}
	return sandboxResultToBuiltin(result), nil
}

func (w *launchWorkspace) validateBoundary(parent context.Context) error {
	if w == nil || w.root == nil {
		return errLaunchWorkspaceBoundary
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, launchBoundaryTimeout)
	defer cancel()
	entries := 0
	return fs.WalkDir(w.root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return errLaunchWorkspaceBoundary
		}
		if walkErr != nil || entry == nil {
			return errLaunchWorkspaceBoundary
		}
		if path == "." {
			return nil
		}
		entries++
		if entries > maxLaunchBoundaryScan {
			return errLaunchWorkspaceBoundary
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errLaunchWorkspaceBoundary
		}
		if path == ".git" {
			if info.IsDir() {
				return fs.SkipDir
			}
			if !info.Mode().IsRegular() || launchFileHasMultipleLinks(info) {
				return errLaunchWorkspaceBoundary
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() || launchFileHasMultipleLinks(info) {
			return errLaunchWorkspaceBoundary
		}
		return nil
	})
}

func ensureLaunchPath(root *os.Root, path string, allowMissingLeaf bool) error {
	path, err := cleanLaunchPath(path, true)
	if err != nil {
		return err
	}
	if path == "." {
		return nil
	}
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	current := ""
	for idx, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := root.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) && allowMissingLeaf && idx == len(parts)-1 {
			return nil
		}
		if statErr != nil {
			return errors.New("workspace path does not exist")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("workspace symlinks are not allowed")
		}
		if idx < len(parts)-1 && !info.IsDir() {
			return errors.New("workspace path parent is not a directory")
		}
	}
	return nil
}

func ensureLaunchDirectories(root *os.Root, dir string) error {
	dir, err := cleanLaunchPath(dir, true)
	if err != nil || dir == "." {
		return err
	}
	parts := strings.Split(filepath.Clean(dir), string(filepath.Separator))
	current := ""
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := root.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if err := root.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return errors.New("create workspace directory failed")
			}
			info, statErr = root.Lstat(current)
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("workspace directory boundary is invalid")
		}
	}
	return nil
}

func cleanLaunchPath(raw string, allowDot bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" && allowDot {
		raw = "."
	}
	if raw == "" || len(raw) > maxLaunchPathBytes || !utf8.ValidString(raw) || filepath.IsAbs(raw) {
		return "", errors.New("workspace path is invalid")
	}
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return "", errors.New("workspace path is invalid")
		}
	}
	clean := filepath.Clean(filepath.FromSlash(raw))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == string(filepath.Separator) {
		return "", errors.New("workspace path escapes root")
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if strings.EqualFold(part, ".git") {
			return "", errors.New("git metadata is not accessible")
		}
	}
	if clean == "." && !allowDot {
		return "", errors.New("workspace file path is required")
	}
	return clean, nil
}

func launchTempName(dir string) (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return filepath.Join(dir, ".buckley-launch-"+hex.EncodeToString(random[:])+".tmp"), nil
}

func requiredString(params map[string]any, key string, max int) (string, error) {
	value, ok := params[key].(string)
	if !ok || strings.TrimSpace(value) == "" || len(value) > max || !utf8.ValidString(value) {
		return "", fmt.Errorf("%s must be a non-empty bounded string", key)
	}
	return value, nil
}

func optionalPath(params map[string]any) (string, error) {
	value := "."
	if raw, ok := params["path"]; ok {
		var valid bool
		value, valid = raw.(string)
		if !valid {
			return "", errors.New("path must be a string")
		}
	}
	return cleanLaunchPath(value, true)
}

func launchTimeout(value any) (time.Duration, error) {
	if value == nil {
		return 2 * time.Minute, nil
	}
	var seconds int64
	switch typed := value.(type) {
	case int:
		seconds = int64(typed)
	case int64:
		seconds = typed
	case float64:
		if typed != math.Trunc(typed) || typed > math.MaxInt64 || typed < math.MinInt64 {
			return 0, errors.New("timeout_seconds must be an integer")
		}
		seconds = int64(typed)
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, errors.New("timeout_seconds must be an integer")
		}
		seconds = parsed
	default:
		return 0, errors.New("timeout_seconds must be an integer")
	}
	if seconds <= 0 || seconds > int64(maxLaunchCommandTime/time.Second) {
		return 0, errors.New("timeout_seconds is outside the launch bound")
	}
	return time.Duration(seconds) * time.Second, nil
}

func pathSchema(description string) builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object", Properties: map[string]builtin.PropertySchema{"path": {Type: "string", Description: description}}, Required: []string{"path"}, AdditionalProperties: false}
}

func pathOptionalSchema(description string) builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object", Properties: map[string]builtin.PropertySchema{"path": {Type: "string", Description: description, Default: "."}}, AdditionalProperties: false}
}

func failedResult(err error) *builtin.Result {
	message := "launch tool request failed"
	if err != nil {
		message = err.Error()
	}
	return &builtin.Result{Success: false, Error: message}
}

func boundedLaunchText(value string, max int) string {
	if len(value) <= max {
		return value
	}
	for max > 0 && !utf8.RuneStart(value[max]) {
		max--
	}
	return value[:max]
}
