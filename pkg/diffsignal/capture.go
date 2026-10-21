package diffsignal

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
)

const (
	// MaxCaptureFiles and MaxCaptureMetadata bound the inventory retained while
	// streaming a diff. Exceeding either requires a narrower path selection.
	MaxCaptureFiles    = 8192
	MaxCaptureMetadata = 2 << 20
	maxCaptureLine     = 64 << 10
)

// Diff contains a bounded prefix per file and metadata from the complete stream.
// Generated and minified bodies are discarded after classification. Files whose
// source content could not be retained remain explicit in the inventory.
type Diff struct {
	Files     []FileDiff
	preamble  string
	truncated bool
}

// Prioritize shapes a captured diff using the same policies as PrioritizeWith.
// Rendering does not mutate the captured inventory or its classification.
func (d *Diff) Prioritize(maxBytes int, opts Options) Result {
	if len(d.Files) == 0 {
		result := PrioritizeWith(d.preamble, maxBytes, opts)
		result.Truncated = result.Truncated || d.truncated
		return result
	}
	files := slices.Clone(d.Files)
	for i := range files {
		if opts.Generated != nil && opts.Generated(files[i].Path) {
			files[i].Reason = ReasonGeneratedAttr
		}
	}
	if opts.ForPR {
		return prioritizeFiles(d.preamble, files, d.truncated, maxBytes, PRFileDiffCap, rankFilesForPR)
	}
	return prioritizeFiles(d.preamble, files, d.truncated, maxBytes, MaxFileDiffBytes, nil)
}

// Capture is an io.Writer for Git's unified diff stream. Its zero value is
// ready to use. It drains arbitrarily large bodies, counting and classifying
// every file, while retaining at most MaxParseBytes of source prefixes and
// MaxFileDiffBytes per file. Finish must follow the final Write.
type Capture struct {
	files                     []FileDiff
	line                      []byte
	lineBytes                 int
	headers, segment, prefix  strings.Builder
	retained, metadata        int
	fileBytes                 int
	bodyBytes, bodyLines      int
	longest, added, removed   int
	haveFile, inHunks, binary bool
	prefixCut                 bool
	err                       error
}

func (c *Capture) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		part := p
		if i >= 0 {
			part = p[:i]
		}
		c.lineBytes += len(part)
		keep := min(len(part), maxCaptureLine-len(c.line))
		c.line = append(c.line, part[:keep]...)
		if i < 0 {
			break
		}
		if err := c.consumeLine(true); err != nil {
			c.err = err
			return n - len(p), err
		}
		c.line = c.line[:0]
		c.lineBytes = 0
		p = p[i+1:]
	}
	return n, nil
}

func (c *Capture) consumeLine(newline bool) error {
	boundary := bytes.HasPrefix(c.line, []byte(diffBoundary))
	if boundary {
		c.finishFile()
		if len(c.files) >= MaxCaptureFiles {
			return fmt.Errorf("diff exceeds %d files; narrow the selected paths", MaxCaptureFiles)
		}
		c.haveFile = true
	}
	if !c.haveFile {
		c.prefixCut = c.retainLine(&c.prefix, MaxFileDiffBytes, newline) || c.prefixCut
		return nil
	}
	if !c.inHunks {
		if bytes.HasPrefix(c.line, []byte("@@")) {
			c.inHunks = true
		} else if boundary || (!c.binary && isCaptureMetadata(c.line)) {
			if c.lineBytes > maxCaptureLine || c.metadata+c.lineBytes+1 > MaxCaptureMetadata {
				return fmt.Errorf("diff metadata exceeds capture budget; narrow the selected paths")
			}
			c.metadata += c.lineBytes + 1
			c.headers.Write(c.line)
			c.headers.WriteByte('\n')
			c.binary = bytes.Equal(c.line, []byte("GIT binary patch")) || bytes.HasPrefix(c.line, []byte("Binary files "))
		}
	}
	if c.inHunks {
		c.bodyBytes += c.lineBytes
		if newline {
			c.bodyBytes++
		}
		c.bodyLines++
		c.longest = max(c.longest, c.lineBytes)
		if bytes.HasPrefix(c.line, []byte("+")) && !bytes.HasPrefix(c.line, []byte("+++")) {
			c.added++
		} else if bytes.HasPrefix(c.line, []byte("-")) && !bytes.HasPrefix(c.line, []byte("---")) {
			c.removed++
		}
	}
	before := c.segment.Len()
	c.retainLine(&c.segment, min(MaxFileDiffBytes, before+MaxParseBytes-c.retained), newline)
	c.retained += c.segment.Len() - before
	c.fileBytes += c.lineBytes
	if newline {
		c.fileBytes++
	}
	return nil
}

func isCaptureMetadata(line []byte) bool {
	return bytes.HasPrefix(line, []byte("+++ ")) || bytes.HasPrefix(line, []byte("--- ")) ||
		bytes.HasPrefix(line, []byte("rename to ")) || bytes.HasPrefix(line, []byte("rename from ")) ||
		bytes.HasPrefix(line, []byte("Binary files ")) || bytes.Equal(line, []byte("GIT binary patch")) ||
		bytes.Equal(line, []byte(UnavailableMarker))
}

func (c *Capture) retainLine(b *strings.Builder, limit int, newline bool) bool {
	remaining := limit - b.Len()
	keep := min(len(c.line), remaining)
	b.Write(c.line[:keep])
	if keep < c.lineBytes {
		return true
	}
	if newline {
		if keep == remaining {
			return true
		}
		b.WriteByte('\n')
	}
	return false
}

func (c *Capture) finishFile() {
	if !c.haveFile {
		return
	}
	fd := parseSegment(c.headers.String())
	fd.Path, fd.OldPath = strings.Clone(fd.Path), strings.Clone(fd.OldPath)
	fd.Segment = c.segment.String()
	fd.Insertions, fd.Deletions = c.added, c.removed
	fd.truncated = c.fileBytes > len(fd.Segment)
	classifyBody(&fd, nil, c.longest > MaxSignalLineLen ||
		(c.bodyBytes >= MinifiedRatioMinBytes && c.bodyLines > 0 && c.bodyBytes/c.bodyLines > MinifiedAvgBytesPerLine))
	if fd.LowSignal() {
		c.retained -= len(fd.Segment)
		fd.Segment = ""
	}
	c.files = append(c.files, fd)
	c.headers.Reset()
	c.segment.Reset()
	c.haveFile, c.inHunks, c.binary = false, false, false
	c.fileBytes, c.bodyBytes, c.bodyLines, c.longest, c.added, c.removed = 0, 0, 0, 0, 0, 0
}

// Finish flushes the last unterminated line and returns no partial inventory
// when a metadata limit was exceeded.
func (c *Capture) Finish() (*Diff, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.lineBytes > 0 {
		if err := c.consumeLine(false); err != nil {
			return nil, err
		}
	}
	c.finishFile()
	return &Diff{Files: c.files, preamble: c.prefix.String(), truncated: c.prefixCut}, nil
}
