package commandoutput

import (
	"strings"
	"unicode/utf8"
)

// DefaultCaptureBytes is the default bound for command diagnostic previews.
const DefaultCaptureBytes = 2 << 10

// Capture retains the start and latest diagnostics while draining a command.
// A command must use a separate Capture for each independently written stream.
type Capture struct {
	max        int
	head, tail []byte
	total      int64
}

// NewCapture creates a bounded diagnostic capture; nonpositive limits use the default.
func NewCapture(limit int) *Capture {
	if limit <= 0 {
		limit = DefaultCaptureBytes
	}
	return &Capture{max: limit}
}

// Write drains p, retaining only a bounded prefix and suffix.
func (b *Capture) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)
	if room := (b.max+1)/2 - len(b.head); room > 0 {
		take := min(room, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	limit := b.max / 2
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

// Finish returns a valid UTF-8 preview and whether any output was omitted.
func (b *Capture) Finish() (string, bool) {
	text := strings.ToValidUTF8(string(b.head)+string(b.tail), "�")
	if b.total <= int64(b.max) && len(text) <= b.max {
		return text, false
	}
	const marker = "\n... (output omitted) ...\n"
	if b.max <= len(marker) {
		return captureSuffix(text, b.max), true
	}
	head := strings.ToValidUTF8(string(b.head), "�")
	tail := strings.ToValidUTF8(string(b.tail), "�")
	headBytes := (b.max - len(marker) + 1) / 2
	return capturePrefix(head, headBytes) + marker + captureSuffix(tail, b.max-len(marker)-headBytes), true
}

func capturePrefix(text string, limit int) string {
	limit = min(limit, len(text))
	for limit > 0 && limit < len(text) && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func captureSuffix(text string, limit int) string {
	start := max(0, len(text)-limit)
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

// String returns the bounded preview, marking gaps when output was omitted.
func (b *Capture) String() string {
	text, _ := b.Finish()
	return text
}

// TotalBytes returns the number of bytes drained from the command.
func (b *Capture) TotalBytes() int64 { return b.total }
