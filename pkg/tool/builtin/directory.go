package builtin

import (
	"container/heap"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

const (
	DefaultDirectoryPageEntries = 100
	MaxDirectoryPageEntries     = 1000
	DefaultDirectoryPageBytes   = 16 << 10
	directoryReadBatch          = 256
	maxDirectoryCursorBytes     = 4096
)

// ListDirectoryTool lists bounded, sorted pages of directory entries.
type ListDirectoryTool struct{ workDirAware }

func (t *ListDirectoryTool) Name() string { return "list_directory" }
func (t *ListDirectoryTool) Description() string {
	return "List directory entries in name order with bounded pages. Returns name, type, and size; count is the total number of entries and returned is this page's count. Pass next_cursor unchanged as cursor until absent. Unavailable metadata is marked with metadata_error. Pages reflect current directory contents."
}
func (t *ListDirectoryTool) Parameters() ParameterSchema {
	schema := ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"path":   {Type: "string", Description: "Directory to list (default current directory).", Default: "."},
		"cursor": {Type: "string", Description: "Opaque next_cursor from the previous page of the same directory. Omit or use null for the first page."},
		"limit":  {Type: "integer", Description: "Maximum entries per page (default 100, maximum 1000); the output byte budget can return fewer. Omit or use null for the default; zero is invalid.", Default: DefaultDirectoryPageEntries},
	}}
	for name, p := range schema.Properties {
		p.RawSchema = map[string]any{"type": []string{p.Type, "null"}, "description": p.Description}
		if p.Default != nil {
			p.RawSchema["default"] = p.Default
		}
		if name == "limit" {
			p.RawSchema["minimum"] = 1
			p.RawSchema["maximum"] = MaxDirectoryPageEntries
		}
		if name == "cursor" {
			p.RawSchema["maxLength"] = maxDirectoryCursorBytes
		}
		schema.Properties[name] = p
	}
	return schema
}
func (t *ListDirectoryTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *ListDirectoryTool) ExecuteWithContext(parent context.Context, params map[string]any) (*Result, error) {
	ctx, cancel := t.execContextWithParent(parent)
	defer cancel()
	path, cursor := ".", ""
	for _, field := range []string{"path", "cursor"} {
		if value, present := params[field]; present && value != nil {
			text, ok := value.(string)
			if !ok {
				return &Result{Error: field + " parameter must be a string"}, nil
			}
			if field == "path" {
				path = text
			} else {
				cursor = text
			}
		}
	}
	limit := DefaultDirectoryPageEntries
	if value, present := params["limit"]; present && value != nil {
		var err error
		limit, err = positiveIntParameter("limit", value)
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
	}
	if limit > MaxDirectoryPageEntries {
		return &Result{Error: fmt.Sprintf("limit must be between 1 and %d", MaxDirectoryPageEntries)}, nil
	}
	after := ""
	if cursor != "" {
		if len(cursor) > maxDirectoryCursorBytes {
			return &Result{Error: "invalid directory cursor; use next_cursor unchanged"}, nil
		}
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
		if err != nil || len(decoded) == 0 || strings.ContainsAny(string(decoded), "/\x00") {
			return &Result{Error: "invalid directory cursor; use next_cursor unchanged"}, nil
		}
		after = string(decoded)
	}
	if err := ctx.Err(); err != nil {
		return &Result{Error: fmt.Sprintf("directory listing canceled: %v", err)}, nil
	}
	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	dir, err := os.Open(absPath)
	if err != nil {
		return &Result{Error: fmt.Sprintf("failed to read directory: %v", err)}, nil
	}
	defer dir.Close()
	selected := make(directoryPage, 0, limit+1)
	count, remaining := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return &Result{Error: fmt.Sprintf("directory listing canceled: %v", err)}, nil
		}
		batch, err := dir.Readdirnames(directoryReadBatch)
		for _, name := range batch {
			count++
			if name <= after {
				continue
			}
			remaining++
			if len(selected) < limit || name < selected[0] {
				heap.Push(&selected, name)
				if len(selected) > limit {
					heap.Pop(&selected)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return &Result{Error: fmt.Sprintf("failed to read directory: %v", err)}, nil
		}
	}
	slices.Sort(selected)
	files := make([]map[string]any, 0, len(selected))
	for _, name := range selected {
		if err := ctx.Err(); err != nil {
			return &Result{Error: fmt.Sprintf("directory listing canceled: %v", err)}, nil
		}
		record := map[string]any{"name": name, "is_dir": nil, "size": nil}
		info, err := os.Lstat(filepath.Join(absPath, name))
		if err != nil {
			record["metadata_error"] = err.Error()
		} else {
			record["size"] = info.Size()
			record["is_dir"] = info.IsDir()
		}
		files = append(files, record)
	}
	budget := DefaultDirectoryPageBytes
	if t.maxOutputBytes > 0 {
		budget = min(budget, t.maxOutputBytes)
	}
	data := map[string]any{"path": absPath, "files": files, "count": count, "returned": len(files)}
	payload := map[string]any{"success": true, "data": data}
	codec := toon.New(true)
	// A smaller prefix always preserves ordering and continuation, even when a
	// cursor's encoded length changes. No result holds the discarded row maps.
	for n := len(files); ; n = max(n/2, 1) {
		if err := ctx.Err(); err != nil {
			return &Result{Error: fmt.Sprintf("directory listing canceled: %v", err)}, nil
		}
		data["files"] = files[:n]
		data["returned"] = n
		delete(data, "next_cursor")
		if remaining > n && n > 0 {
			data["next_cursor"] = base64.RawURLEncoding.EncodeToString([]byte(files[n-1]["name"].(string)))
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode directory page: %w", err)
		}
		text, err := codec.MarshalString(payload)
		if err != nil {
			return nil, fmt.Errorf("encode directory page: %w", err)
		}
		if max(len(encoded), len(text)) <= budget {
			page := make([]map[string]any, n)
			copy(page, files[:n])
			data["files"] = page
			return &Result{Success: true, Data: data}, nil
		}
		if n <= 1 {
			return &Result{Error: fmt.Sprintf("directory entry and continuation metadata exceed the %d-byte page budget; use a shorter directory path or raise the configured tool output limit", budget)}, nil
		}
	}
}

// directoryPage keeps only the smallest names after the cursor. The largest
// candidate is removed first, independent of the filesystem's iteration order.
type directoryPage []string

func (p directoryPage) Len() int           { return len(p) }
func (p directoryPage) Less(i, j int) bool { return p[i] > p[j] }
func (p directoryPage) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
func (p *directoryPage) Push(value any)    { *p = append(*p, value.(string)) }
func (p *directoryPage) Pop() any {
	last := len(*p) - 1
	entry := (*p)[last]
	(*p)[last] = ""
	*p = (*p)[:last]
	return entry
}
