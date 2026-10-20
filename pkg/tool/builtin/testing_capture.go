package builtin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

const (
	DefaultTestCaptureBytes  = 16 << 10
	DefaultTestResultBytes   = 24 << 10
	DefaultTestReportBytes   = 8 << 20
	DefaultTestEventBytes    = 1 << 20
	DefaultTestMetadataBytes = 1 << 20
)

// testText keeps a prefix and the latest output without retaining the stream.
type testText struct {
	head, tail []byte
	total      int64
}

func (b *testText) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)
	if room := DefaultTestCaptureBytes/2 - len(b.head); room > 0 {
		take := min(room, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	const limit = DefaultTestCaptureBytes / 2
	if len(p) >= limit {
		b.tail = append(b.tail[:0], p[len(p)-limit:]...)
	} else {
		if extra := len(b.tail) + len(p) - limit; extra > 0 {
			copy(b.tail, b.tail[extra:])
			b.tail = b.tail[:len(b.tail)-extra]
		}
		b.tail = append(b.tail, p...)
	}
	return n, nil
}
func (b *testText) String() string {
	if b.total <= DefaultTestCaptureBytes {
		return string(b.head) + string(b.tail)
	}
	head := strings.ToValidUTF8(string(b.head), "�")
	tail := b.tail
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return head + "\n... (test output omitted) ...\n" + string(tail)
}

type testStream struct {
	text      testText
	goReport  *goTestStream
	cargo     testReport
	pending   []byte
	dropping  bool
	oversized bool
	framework string
	failures  []string
	inFailure bool
}

func (s *testStream) Write(p []byte) (int, error) {
	n := len(p)
	if s.framework != "go" {
		_, _ = s.text.Write(p)
	}
	if s.framework == "" || ((s.framework == "jest" || s.framework == "pytest") && len(s.failures) >= 10) {
		return n, nil
	}
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		part := p
		if end >= 0 {
			part = p[:end]
		}
		if !s.dropping {
			if len(s.pending)+len(part) > DefaultTestEventBytes {
				if s.framework == "go" {
					s.goReport.problem = "go test event exceeds the capture budget; reduce oversized test names or output events"
				} else if s.framework == "cargo" {
					s.cargo.verificationError = "cargo output line exceeds capture budget; emit shorter newline-delimited output"
				}
				s.pending = s.pending[:0]
				s.dropping = true
				s.oversized = true
			} else {
				s.pending = append(s.pending, part...)
			}
		}
		if end < 0 {
			break
		}
		if !s.dropping {
			s.line(true)
		}
		s.pending = s.pending[:0]
		s.dropping = false
		p = p[end+1:]
	}
	return n, nil
}
func (s *testStream) line(newline bool) {
	if s.framework == "go" {
		s.goReport.event(s.pending, &s.text, newline)
		return
	}
	if s.framework == "jest" || s.framework == "pytest" {
		if bytes.Contains(s.pending, []byte("FAIL")) || bytes.Contains(s.pending, []byte("ERROR")) {
			s.inFailure = true
		}
		if s.inFailure && len(s.failures) < 10 {
			line := bytes.TrimSpace(s.pending)
			text := strings.ToValidUTF8(string(line[:min(len(line), 512)]), "�")
			if len(line) > 512 {
				text += "... (failure line omitted)"
			}
			s.failures = append(s.failures, text)
		}
		return
	}
	if !bytes.HasPrefix(s.pending, []byte("test result: ")) {
		return
	}
	p, f, k := (&RunTestsTool{}).parseCargoResults(string(s.pending))
	s.cargo.passed += p
	s.cargo.failed += f
	s.cargo.skipped += k
}
func (s *testStream) finish() {
	if !s.dropping && len(s.pending) > 0 {
		s.line(false)
	}
	s.pending = nil
}

func boundTestResult(result *Result, budget int) (*Result, error) {
	codec := toon.New(true)
	for {
		raw, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		text, err := codec.MarshalString(result)
		if err != nil {
			return nil, err
		}
		if max(len(raw), len(text)) <= budget {
			return result, nil
		}
		output, _ := result.Data["output"].(string)
		if len(output) > 128 {
			half := len(output) / 2
			result.Data["output"] = strings.Clone(outputTail(output, half))
			result.Data["output_truncated"] = true
			continue
		}
		if details, ok := result.DisplayData["failures"].([]string); ok {
			reduced := false
			for i, detail := range details {
				if len(detail) > 256 {
					details[i] = strings.Clone(functionSignaturePrefix(detail, len(detail)/2)) + "\n... (failure detail omitted)"
					reduced = true
				}
			}
			if reduced {
				result.DisplayData["failures"] = details
				continue
			}
		}
		if tail, ok := result.DisplayData["output_tail"].(string); ok && len(tail) > 128 {
			result.DisplayData["output_tail"] = strings.Clone(outputTail(tail, len(tail)/2))
			continue
		}
		return &Result{Error: fmt.Sprintf("test result metadata exceeds %d-byte output budget", budget)}, nil
	}
}
