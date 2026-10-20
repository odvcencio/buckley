package builtin

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

type conflictExcerpt struct {
	prefix []byte
	bytes  int64
}

func (e *conflictExcerpt) append(prefix []byte, size int64) {
	e.prefix = append(e.prefix, prefix[:min(len(prefix), 256-len(e.prefix))]...)
	e.bytes += size
}

func (e *conflictExcerpt) value(limit int) (string, int64, bool, string) {
	size := max(int64(0), e.bytes-1) // Exclude the final body-line LF.
	prefix := e.prefix[:min(int64(len(e.prefix)), size, int64(limit))]
	if utf8.Valid(prefix) {
		return string(prefix), size, int64(len(prefix)) < size, "utf8"
	}
	return strconv.QuoteToGraphic(string(prefix)), size, int64(len(prefix)) < size, "go_quoted_bytes"
}

type conflictSection struct {
	start, end         int64
	ours, base, theirs conflictExcerpt
	hasBase            bool
}

func (s *conflictSection) data(limit int) map[string]any {
	d := map[string]any{"start_line": s.start, "end_line": s.end}
	truncated := make([]string, 0)
	encodings := make(map[string]string)
	for _, side := range []struct {
		name string
		text *conflictExcerpt
	}{{"ours", &s.ours}, {"base", &s.base}, {"theirs", &s.theirs}} {
		if side.name == "base" && !s.hasBase {
			continue
		}
		text, size, clipped, encoding := side.text.value(limit)
		d[side.name], d[side.name+"_bytes"] = text, size
		if clipped {
			truncated = append(truncated, side.name)
		}
		if encoding != "utf8" {
			encodings[side.name] = encoding
		}
	}
	d["truncated_sides"], d["excerpt_encodings"] = truncated, encodings
	return d
}

type conflictFileSummary struct {
	path, kind, sourceHash string
	count, offset, bytes   int64
	section                *conflictSection
}

func scanConflictFile(ctx context.Context, abs, path string, markerSize, offset, limit int64) (*conflictFileSummary, error) {
	result := &conflictFileSummary{path: path, offset: offset}
	info, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		result.kind = "missing"
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	var source io.Reader
	switch {
	case info.Mode().IsRegular():
		if info.Size() > limit {
			return nil, fmt.Errorf("conflict source exceeds %d-byte scan budget", limit)
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		source, result.kind = f, "file"
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(abs)
		if err != nil {
			return nil, err
		}
		source, result.kind = strings.NewReader(target), "symlink"
	case info.IsDir():
		result.kind = "directory"
		return result, nil
	default:
		return nil, fmt.Errorf("conflict path is not a file, symlink, or directory")
	}
	digest := sha256.New()
	fmt.Fprintf(digest, "%q\x00%s\x00%d\x00", path, result.kind, markerSize)
	input := &io.LimitedReader{R: source, N: limit + 1}
	reader := bufio.NewReaderSize(io.TeeReader(input, digest), 4096)
	phase := byte(0)
	var line int64
	var selected *conflictSection
	for {
		kind, prefix, size, err := readConflictLine(ctx, reader, markerSize, 256)
		if input.N == 0 {
			return nil, fmt.Errorf("conflict source exceeds %d-byte scan budget", limit)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		line++
		switch kind {
		case '<':
			if phase != 0 {
				return nil, fmt.Errorf("nested conflict marker on line %d", line)
			}
			phase = 'o'
			if result.count == offset {
				selected = &conflictSection{start: line}
			}
		case '|':
			if phase != 'o' {
				return nil, fmt.Errorf("unexpected base marker on line %d", line)
			}
			phase = 'b'
			if selected != nil {
				selected.hasBase = true
			}
		case '=':
			if phase != 'o' && phase != 'b' {
				return nil, fmt.Errorf("unexpected separator on line %d", line)
			}
			phase = 't'
		case '>':
			if phase != 't' {
				return nil, fmt.Errorf("unexpected closing marker on line %d", line)
			}
			phase = 0
			result.count++
			if selected != nil {
				selected.end = line
				result.section, selected = selected, nil
			}
		default:
			if selected != nil {
				switch phase {
				case 'o':
					selected.ours.append(prefix, size)
				case 'b':
					selected.base.append(prefix, size)
				case 't':
					selected.theirs.append(prefix, size)
				}
			}
		}
	}
	if phase != 0 {
		return nil, fmt.Errorf("unterminated conflict section at end of file")
	}
	result.bytes = limit + 1 - input.N
	result.sourceHash = hex.EncodeToString(digest.Sum(nil))
	return result, nil
}
