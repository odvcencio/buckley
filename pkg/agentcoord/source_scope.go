package agentcoord

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"
)

// MaxSourceScopeFiles is the hard cap on files in one SourceScope.
const MaxSourceScopeFiles = 32

// SourceFile names one workspace-relative file and an optional bounded line
// range. When both bounds are zero the entire file is in scope; when
// bounded, both must be positive with StartLine <= EndLine.
type SourceFile struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

func (f *SourceFile) UnmarshalJSON(data []byte) error {
	type plain SourceFile
	var decoded plain
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decoded); err != nil {
		return err
	}
	// encoding/json otherwise treats null integer bounds as zero, which
	// would silently turn two malformed bounds into full-file permission.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"start_line", "end_line"} {
		if bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			return fmt.Errorf("source file %s must be an integer, not null", name)
		}
	}
	*f = SourceFile(decoded)
	return nil
}

// SourceScope restricts a child run to reading only the listed source files.
// A nil *SourceScope means unconstrained legacy behavior; a non-nil scope with
// zero files permits no source reads at all.
type SourceScope struct {
	Files []SourceFile `json:"files"`
}

// UnmarshalJSON decodes SourceScope strictly: unknown fields (including inside
// file entries) and trailing JSON values are rejected, and the decoded shape
// is validated rather than silently broadened. The receiver is left unchanged
// on any error.
func (s *SourceScope) UnmarshalJSON(data []byte) error {
	type plain SourceScope
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var tmp plain
	if err := dec.Decode(&tmp); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing data after source scope object")
	}
	if err := ValidateSourceScope((*SourceScope)(&tmp)); err != nil {
		return err
	}
	*s = SourceScope(tmp)
	return nil
}

// normalizeSourcePath validates one workspace-relative path and returns its
// normalized form for duplicate detection.
func normalizeSourcePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("source file path is empty")
	}
	if strings.TrimSpace(p) != p {
		return "", fmt.Errorf("source file path %q has leading or trailing whitespace", p)
	}
	if !utf8.ValidString(p) {
		return "", errors.New("source file path is not valid utf-8")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("source file path contains nul byte")
	}
	if strings.ContainsRune(p, '\\') {
		return "", errors.New("source file path contains backslash")
	}
	if len(p) >= 2 && p[1] == ':' && isASCIIAlpha(p[0]) {
		return "", fmt.Errorf("source file path %q is a windows drive-absolute path", p)
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("source file path %q is absolute", p)
	}
	clean := path.Clean(p)
	if strings.HasSuffix(p, "/") {
		return "", fmt.Errorf("source file path %q is a directory", p)
	}
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("source file path %q escapes the workspace", p)
	}
	return clean, nil
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// ValidateSourceScope validates a source scope without modifying it. A nil scope is
// always valid (unconstrained legacy behavior).
func ValidateSourceScope(s *SourceScope) error {
	if s == nil {
		return nil
	}
	if len(s.Files) > MaxSourceScopeFiles {
		return fmt.Errorf("source scope has %d files, maximum is %d", len(s.Files), MaxSourceScopeFiles)
	}
	seen := make(map[string]bool, len(s.Files))
	for _, f := range s.Files {
		clean, err := normalizeSourcePath(f.Path)
		if err != nil {
			return err
		}
		if f.StartLine < 0 || f.EndLine < 0 {
			return fmt.Errorf("source file %q has negative line bounds", f.Path)
		}
		if f.StartLine == 0 || f.EndLine == 0 {
			if f.StartLine != f.EndLine {
				return fmt.Errorf("source file %q has only one of start_line/end_line set", f.Path)
			}
		} else if f.StartLine > f.EndLine {
			return fmt.Errorf("source file %q has start_line %d after end_line %d", f.Path, f.StartLine, f.EndLine)
		}
		if seen[clean] {
			return fmt.Errorf("source scope has duplicate file %q", f.Path)
		}
		seen[clean] = true
	}
	return nil
}

// CloneSourceScope returns a deep copy of a source scope. Nil stays nil and
// every non-nil scope, including an invalid one, clones to a non-nil scope
// with the same constraints and exact path strings; validation is separate
// and belongs at admission boundaries. Mutating either scope never affects
// the other.
func CloneSourceScope(s *SourceScope) *SourceScope {
	if s == nil {
		return nil
	}
	clone := &SourceScope{Files: make([]SourceFile, len(s.Files))}
	copy(clone.Files, s.Files)
	return clone
}
