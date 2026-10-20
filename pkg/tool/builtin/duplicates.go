package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

const (
	DefaultDuplicateOutputBytes            = 8 << 10
	DefaultDuplicateGroups                 = 10
	DefaultDuplicateLocations              = 3
	DefaultDuplicateLines                  = 100000
	DefaultDuplicateComparisonBytes        = 64 << 20
	duplicateExcerptBytes                  = 256
	duplicateHashBase1              uint64 = 1099511628211
	duplicateHashBase2              uint64 = 14029467366897019727
)

// FindDuplicatesTool finds bounded candidates for repeated text windows.
type FindDuplicatesTool struct{ workDirAware }

func (t *FindDuplicatesTool) Name() string { return "find_duplicates" }
func (t *FindDuplicatesTool) Description() string {
	return "Find repeated windows of min_lines consecutive lines in source files. Matches normalize ASCII whitespace, including within literals; these are text candidates, not semantic equivalence. Inspect before refactoring. Requires at least half the lines to contain more than blanks or // or # comments. Returns frequent groups first, bounded code prefixes and locations, exact counts, and explicit truncation. Overlapping windows are separate groups. Scans skip dependencies and directory-entry symlinks; exhausted budgets fail rather than report a clean scan."
}
func (t *FindDuplicatesTool) Parameters() ParameterSchema {
	schema := ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"path":      {Type: "string", Description: "Source file or directory to scan. Explicit files may contain any UTF-8 text; directories scan Go, JS/TS, Python, Java, C/C++, and Rust."},
		"min_lines": {Type: "integer", Description: "Consecutive lines per candidate (default 5, maximum 100000). Omit or use null for default; zero is invalid.", Default: 5},
		"language":  {Type: "string", Description: "Optional directory filter: go, javascript, typescript, python, java, c, cpp, rust. Omit or use null to scan all supported extensions."},
	}, Required: []string{"path"}}
	for _, name := range []string{"min_lines", "language"} {
		p := schema.Properties[name]
		p.RawSchema = map[string]any{"type": []string{p.Type, "null"}, "description": p.Description}
		if p.Default != nil {
			p.RawSchema["default"] = p.Default
		}
		if name == "min_lines" {
			p.RawSchema["minimum"] = 1
			p.RawSchema["maximum"] = DefaultDuplicateLines
		}
		schema.Properties[name] = p
	}
	return schema
}
func (t *FindDuplicatesTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *FindDuplicatesTool) ExecuteWithContext(parent context.Context, params map[string]any) (*Result, error) {
	ctx, cancel := t.execContextWithParent(parent)
	defer cancel()
	path, ok := params["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return &Result{Error: "path parameter must be a non-empty string"}, nil
	}
	minLines := 5
	if value := params["min_lines"]; value != nil {
		n, err := positiveIntParameter("min_lines", value)
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
		if n > DefaultDuplicateLines {
			return &Result{Error: fmt.Sprintf("min_lines must not exceed %d", DefaultDuplicateLines)}, nil
		}
		minLines = n
	}
	language := ""
	if value := params["language"]; value != nil {
		var ok bool
		language, ok = value.(string)
		if !ok {
			return &Result{Error: "language parameter must be a string"}, nil
		}
		switch language {
		case "", "go", "javascript", "typescript", "python", "java", "c", "cpp", "rust":
		default:
			return &Result{Error: "unsupported language filter"}, nil
		}
	}
	path, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	analysis := duplicateAnalysis{sourceScan: sourceScan{ctx: ctx, remaining: t.mutationFileLimit()}, minLines: minLines, index: make(map[duplicateKey]*duplicateGroup), comparisonRemaining: DefaultDuplicateComparisonBytes}
	err = analysis.sourceScan.scan(path, 0, func(file string, root bool) (bool, error) {
		detected := duplicateLanguage(file)
		return root || (detected != "" && (language == "" || language == detected)), nil
	}, analysis.file)
	if err != nil {
		message := err.Error()
		if len(message) > 512 {
			message = strings.ToValidUTF8(message[:512], "�") + "... (diagnostic truncated)"
		}
		return &Result{Error: "duplicate detection failed: " + message}, nil
	}
	selected := make([]*duplicateGroup, 0, DefaultDuplicateGroups)
	count := 0
	for _, bucket := range analysis.index {
		for group := bucket; group != nil; group = group.next {
			if group.count < 2 {
				continue
			}
			count++
			at := sort.Search(len(selected), func(i int) bool {
				other := selected[i]
				if group.count != other.count {
					return group.count > other.count
				}
				return analysis.locationLess(group.first, other.first)
			})
			if at == DefaultDuplicateGroups {
				continue
			}
			if len(selected) < DefaultDuplicateGroups {
				selected = append(selected, nil)
			}
			copy(selected[at+1:], selected[at:len(selected)-1])
			selected[at] = group
		}
	}
	data := map[string]any{"path": path, "min_lines": minLines, "metric": "whitespace_normalized_text", "count": count, "files_analyzed": analysis.files, "source_bytes": t.mutationFileLimit() - analysis.remaining, "windows_analyzed": analysis.windows}
	budget := DefaultDuplicateOutputBytes
	if t.maxOutputBytes > 0 {
		budget = min(budget, t.maxOutputBytes)
	}
	groups, locations, excerpt := len(selected), DefaultDuplicateLocations, duplicateExcerptBytes
	codec := toon.New(true)
	for {
		rows := make([]map[string]any, 0, groups)
		for _, group := range selected[:groups] {
			first := analysis.snapshots[group.first.file]
			line := first.lines[group.first.line]
			last := first.lines[group.first.line+minLines-1]
			code := first.source[line.rawStart:last.rawEnd]
			prefix := min(len(code), excerpt)
			for prefix > 0 && prefix < len(code) && !utf8.RuneStart(code[prefix]) {
				prefix--
			}
			points := make([]map[string]any, 0, min(locations, group.count))
			all := append([]duplicateLocation{group.first}, group.locations...)
			for _, point := range all[:min(locations, len(all))] {
				file := analysis.snapshots[point.file]
				points = append(points, map[string]any{"file": file.path, "start_line": point.line + 1, "end_line": point.line + minLines})
			}
			rows = append(rows, map[string]any{"count": group.count, "locations": points, "returned_locations": len(points), "locations_truncated": len(points) < group.count, "code": strings.Clone(code[:prefix]), "code_bytes": len(code), "code_truncated": prefix < len(code)})
		}
		data["duplicates"] = rows
		data["returned"] = groups
		data["truncated"] = groups < count
		result := &Result{Success: true, Data: data}
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		text, err := codec.MarshalString(result)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return &Result{Error: fmt.Sprintf("duplicate detection canceled: %v", err)}, nil
		}
		if max(len(encoded), len(text)) <= budget {
			return result, nil
		}
		switch {
		case locations > 2:
			locations = 2
		case excerpt > 64:
			excerpt /= 2
		case groups > 1:
			groups /= 2
		case excerpt > 0:
			excerpt = 0
		default:
			return &Result{Error: fmt.Sprintf("duplicate result exceeds %d-byte output budget; narrow the path", budget)}, nil
		}
	}
}

func duplicateLanguage(file string) string {
	if language, _ := complexityLanguage(file); language != "" {
		return language
	}
	switch strings.ToLower(filepath.Ext(file)) {
	case ".java":
		return "java"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".hpp", ".hh":
		return "cpp"
	case ".rs":
		return "rust"
	}
	return ""
}

type duplicateLine struct {
	rawStart, rawEnd, start, end int
	before, after                [2]uint64
	significant                  int
}
type duplicateSnapshot struct {
	path, source, normalized string
	lines                    []duplicateLine
}
type duplicateLocation struct{ file, line int }
type duplicateKey struct {
	hash   [2]uint64
	length int
}
type duplicateGroup struct {
	first     duplicateLocation
	count     int
	locations []duplicateLocation
	next      *duplicateGroup
}
type duplicateAnalysis struct {
	sourceScan
	minLines, lineCount, windows int
	comparisonRemaining          int64
	snapshots                    []duplicateSnapshot
	index                        map[duplicateKey]*duplicateGroup
}

func (a *duplicateAnalysis) locationLess(left, right duplicateLocation) bool {
	x, y := a.snapshots[left.file].path, a.snapshots[right.file].path
	if x != y {
		return x < y
	}
	return left.line < right.line
}

func (a *duplicateAnalysis) recordLocation(group *duplicateGroup, point duplicateLocation) {
	group.count++
	if a.locationLess(point, group.first) {
		point, group.first = group.first, point
	}
	at := sort.Search(len(group.locations), func(i int) bool { return a.locationLess(point, group.locations[i]) })
	if at == DefaultDuplicateLocations-1 {
		return
	}
	if len(group.locations) < DefaultDuplicateLocations-1 {
		group.locations = append(group.locations, duplicateLocation{})
	}
	copy(group.locations[at+1:], group.locations[at:len(group.locations)-1])
	group.locations[at] = point
}

func duplicateWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}

func (a *duplicateAnalysis) snapshot(path, source string) (duplicateSnapshot, error) {
	if !utf8.ValidString(source) || strings.IndexByte(source, 0) >= 0 {
		return duplicateSnapshot{}, fmt.Errorf("%s: duplicate detection requires UTF-8 text without NUL bytes", path)
	}
	snapshot := duplicateSnapshot{path: path, source: source}
	var normalized strings.Builder
	normalized.Grow(len(source))
	var hash, previous [2]uint64
	lastSpace := false
	appendByte := func(b byte) {
		space := duplicateWhitespace(b)
		if space {
			if lastSpace {
				return
			}
			b = ' '
		}
		previous = hash
		hash[0] = hash[0]*duplicateHashBase1 + uint64(b) + 1
		hash[1] = hash[1]*duplicateHashBase2 + uint64(b) + 1
		normalized.WriteByte(b)
		lastSpace = space
	}
	rawStart, significant := 0, 0
	for line := range strings.SplitSeq(source, "\n") {
		if err := a.ctx.Err(); err != nil {
			return duplicateSnapshot{}, err
		}
		a.lineCount++
		if a.lineCount > DefaultDuplicateLines {
			return duplicateSnapshot{}, fmt.Errorf("line budget exceeds %d; narrow the path", DefaultDuplicateLines)
		}
		start, before := normalized.Len(), hash
		// A window starting inside a whitespace run still needs one leading space.
		if (line == "" || duplicateWhitespace(line[0])) && lastSpace {
			start--
			before = previous
		}
		for i := 0; i < len(line); i++ {
			appendByte(line[i])
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "//") && !strings.HasPrefix(trimmed, "#") {
			significant++
		}
		rawEnd := rawStart + len(line)
		snapshot.lines = append(snapshot.lines, duplicateLine{rawStart: rawStart, rawEnd: rawEnd, start: start, end: normalized.Len(), before: before, after: hash, significant: significant})
		if rawEnd < len(source) {
			appendByte('\n')
		}
		rawStart = rawEnd + 1
	}
	snapshot.normalized = normalized.String()
	return snapshot, nil
}

// Prefix fingerprints avoid hashing every byte again for overlapping windows.
// Matches are verified against normalized text before grouping.
func duplicateFingerprint(first, last duplicateLine) duplicateKey {
	length := last.end - first.start
	power := [2]uint64{1, 1}
	base := [2]uint64{duplicateHashBase1, duplicateHashBase2}
	for n := length; n > 0; n >>= 1 {
		if n&1 != 0 {
			power[0] *= base[0]
			power[1] *= base[1]
		}
		base[0] *= base[0]
		base[1] *= base[1]
	}
	return duplicateKey{hash: [2]uint64{last.after[0] - first.before[0]*power[0], last.after[1] - first.before[1]*power[1]}, length: length}
}
func (a *duplicateAnalysis) file(path, source string) error {
	snapshot, err := a.snapshot(path, source)
	if err != nil {
		return err
	}
	file := len(a.snapshots)
	a.snapshots = append(a.snapshots, snapshot)
	for i := 0; i+a.minLines <= len(snapshot.lines); i++ {
		if err := a.ctx.Err(); err != nil {
			return err
		}
		first, last := snapshot.lines[i], snapshot.lines[i+a.minLines-1]
		significant := last.significant
		if i > 0 {
			significant -= snapshot.lines[i-1].significant
		}
		if significant < (a.minLines+1)/2 {
			continue
		}
		a.windows++
		key := duplicateFingerprint(first, last)
		var match *duplicateGroup
		for group := a.index[key]; group != nil; group = group.next {
			if int64(key.length) > a.comparisonRemaining {
				return fmt.Errorf("duplicate verification byte budget exhausted; narrow the path or reduce min_lines")
			}
			a.comparisonRemaining -= int64(key.length)
			other := a.snapshots[group.first.file]
			start := other.lines[group.first.line].start
			end := other.lines[group.first.line+a.minLines-1].end
			if snapshot.normalized[first.start:last.end] == other.normalized[start:end] {
				match = group
				break
			}
		}
		if match == nil {
			a.index[key] = &duplicateGroup{first: duplicateLocation{file, i}, count: 1, next: a.index[key]}
			continue
		}
		a.recordLocation(match, duplicateLocation{file, i})
	}
	return nil
}
