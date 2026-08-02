package widgets

import (
	"strings"
	"unicode/utf8"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"m31labs.dev/buckley/pkg/ui/scrollback"
	"m31labs.dev/fluffyui/backend"
)

const codeHighlightTimeoutMicros = 10_000

type codeSyntaxStyles struct {
	Default   backend.Style
	Muted     backend.Style
	Accent    backend.Style
	AccentDim backend.Style
	Success   backend.Style
	Warning   backend.Style
	Info      backend.Style
	Error     backend.Style
}

func defaultCodeSyntaxStyles() codeSyntaxStyles {
	style := backend.DefaultStyle()
	return codeSyntaxStyles{
		Default:   style,
		Muted:     style.Dim(true),
		Accent:    style.Bold(true),
		AccentDim: style,
		Success:   style,
		Warning:   style,
		Info:      style,
		Error:     style.Bold(true),
	}
}

func (c *ChatView) applyTreeSitterHighlights(lines []scrollback.Line) {
	for start := 0; start < len(lines); {
		if !isHighlightableCodeLine(lines[start]) {
			start++
			continue
		}

		language := lines[start].Language
		end := start + 1
		for end < len(lines) && isHighlightableCodeLine(lines[end]) && lines[end].Language == language {
			end++
		}
		c.highlightCodeBlock(lines[start:end], language)
		start = end
	}
}

func isHighlightableCodeLine(line scrollback.Line) bool {
	return line.IsCode && !line.IsCodeHeader
}

func (c *ChatView) highlightCodeBlock(lines []scrollback.Line, language string) {
	if len(lines) == 0 {
		return
	}

	code := codeBlockText(lines)
	entry := grammars.DetectLanguageByName(language)
	if entry == nil {
		entry = grammars.DetectLanguageByShebang(firstCodeLine(code))
	}
	if entry == nil || entry.Language == nil || strings.TrimSpace(entry.HighlightQuery) == "" {
		return
	}

	lang := entry.Language()
	if lang == nil {
		return
	}
	highlighter, ok := c.highlighterFor(entry, lang)
	if !ok {
		return
	}
	ranges := highlighter.Highlight([]byte(code))
	if len(ranges) == 0 {
		return
	}

	lineStart := 0
	for i := range lines {
		content := lines[i].Content
		lineEnd := lineStart + len(content)
		lines[i].Spans = c.codeLineSpans(content, lineStart, lineEnd, ranges)
		lineStart = lineEnd + 1
	}
}

func codeBlockText(lines []scrollback.Line) string {
	var b strings.Builder
	for i := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(lines[i].Content)
	}
	return b.String()
}

func firstCodeLine(code string) string {
	if before, _, ok := strings.Cut(code, "\n"); ok {
		return before
	}
	return code
}

func (c *ChatView) highlighterFor(entry *grammars.LangEntry, lang *gotreesitter.Language) (*gotreesitter.Highlighter, bool) {
	if c.highlighters == nil {
		c.highlighters = make(map[string]*gotreesitter.Highlighter)
	}
	if highlighter := c.highlighters[entry.Name]; highlighter != nil {
		return highlighter, true
	}

	opts := []gotreesitter.HighlighterOption{
		gotreesitter.WithHighlighterTimeoutMicros(codeHighlightTimeoutMicros),
	}
	if factory := entry.TokenSourceFactory; factory != nil {
		opts = append(opts, gotreesitter.WithTokenSourceFactory(func(source []byte) gotreesitter.TokenSource {
			return factory(source, lang)
		}))
	}
	highlighter, err := gotreesitter.NewHighlighter(lang, entry.HighlightQuery, opts...)
	if err != nil {
		return nil, false
	}
	c.highlighters[entry.Name] = highlighter
	return highlighter, true
}

func (c *ChatView) codeLineSpans(content string, lineStart, lineEnd int, ranges []gotreesitter.HighlightRange) []scrollback.Span {
	if content == "" {
		return nil
	}

	spans := make([]scrollback.Span, 0, len(ranges)+1)
	cursor := 0
	for _, highlight := range ranges {
		start := maxCodeHighlightBoundary(int(highlight.StartByte), lineStart) - lineStart
		end := minCodeHighlightBoundary(int(highlight.EndByte), lineEnd) - lineStart
		if end <= 0 || start >= len(content) {
			continue
		}
		start = clampCodeHighlightBoundary(content, start)
		end = clampCodeHighlightBoundary(content, end)
		if start < cursor {
			start = cursor
		}
		if end <= start {
			continue
		}
		if start > cursor {
			spans = append(spans, scrollback.Span{Text: content[cursor:start], Style: c.codeStyleForCapture("")})
		}
		spans = append(spans, scrollback.Span{Text: content[start:end], Style: c.codeStyleForCapture(highlight.Capture)})
		cursor = end
	}
	if cursor < len(content) {
		spans = append(spans, scrollback.Span{Text: content[cursor:], Style: c.codeStyleForCapture("")})
	}
	return spans
}

func (c *ChatView) codeStyleForCapture(capture string) backend.Style {
	style := c.syntax.styleForCapture(capture)
	return style.Background(c.codeBlockBG.BackgroundColor())
}

func (s codeSyntaxStyles) styleForCapture(capture string) backend.Style {
	capture = strings.ToLower(capture)
	switch {
	case strings.Contains(capture, "comment"):
		return s.Muted.Italic(true)
	case strings.Contains(capture, "error"):
		return s.Error.Bold(true)
	case strings.Contains(capture, "string"), strings.Contains(capture, "character"), strings.Contains(capture, "escape"):
		return s.Success
	case strings.Contains(capture, "number"), strings.Contains(capture, "boolean"), strings.Contains(capture, "constant"):
		return s.Warning
	case strings.Contains(capture, "keyword"), strings.Contains(capture, "conditional"), strings.Contains(capture, "repeat"), strings.Contains(capture, "exception"):
		return s.Accent.Bold(true)
	case strings.Contains(capture, "type"), strings.Contains(capture, "class"), strings.Contains(capture, "namespace"):
		return s.Info
	case strings.Contains(capture, "function"), strings.Contains(capture, "method"), strings.Contains(capture, "constructor"):
		return s.AccentDim
	case strings.Contains(capture, "builtin"):
		return s.Info
	case strings.Contains(capture, "tag"), strings.Contains(capture, "attribute"):
		return s.Accent
	case strings.Contains(capture, "operator"), strings.Contains(capture, "punctuation"):
		return s.Muted
	default:
		return s.Default
	}
}

func clampCodeHighlightBoundary(value string, offset int) int {
	if offset <= 0 {
		return 0
	}
	if offset >= len(value) {
		return len(value)
	}
	for offset > 0 && !utf8.RuneStart(value[offset]) {
		offset--
	}
	return offset
}

func minCodeHighlightBoundary(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxCodeHighlightBoundary(a, b int) int {
	if a > b {
		return a
	}
	return b
}
