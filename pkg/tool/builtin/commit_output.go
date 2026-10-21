package builtin

import (
	"strings"
	"unicode/utf8"
)

// commitOutput retains the start and latest diagnostics while draining a command.
type commitOutput struct {
	max        int
	head, tail []byte
	total      int64
}

func (b *commitOutput) Write(p []byte) (int, error) {
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

func (b *commitOutput) finish() (string, bool) {
	text := strings.ToValidUTF8(string(b.head)+string(b.tail), "�")
	if b.total <= int64(b.max) && len(text) <= b.max {
		return text, false
	}
	const marker = "\n... (output omitted) ...\n"
	if b.max <= len(marker) {
		return commitOutputSuffix(text, b.max), true
	}
	head := strings.ToValidUTF8(string(b.head), "�")
	tail := strings.ToValidUTF8(string(b.tail), "�")
	headBytes := (b.max - len(marker) + 1) / 2
	return commitOutputPrefix(head, headBytes) + marker + commitOutputSuffix(tail, b.max-len(marker)-headBytes), true
}

func commitOutputPrefix(text string, limit int) string {
	limit = min(limit, len(text))
	for limit > 0 && limit < len(text) && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func commitOutputSuffix(text string, limit int) string {
	start := max(0, len(text)-limit)
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}
