package widgets

import (
	"strings"

	"m31labs.dev/fluffyui/compositor"
	"m31labs.dev/fluffyui/markdown"
)

// prepareMarkdownForWorkspace keeps Markdown++ as Buckley's document parser,
// then applies terminal-specific density affordances to FluffyUI's styled
// output. Task-list checkboxes become recognizable TODO glyphs and table cell
// padding is removed without changing the semantic content. Fenced and
// indented code blocks are left untouched so code content never gets
// reinterpreted as task-list or table markup.
func prepareMarkdownForWorkspace(lines []markdown.StyledLine) []markdown.StyledLine {
	for i := range lines {
		if lines[i].IsCode {
			continue
		}
		lines[i].Spans = renderTaskCheckboxes(lines[i])
		if isMarkdownTableRow(lines[i].Spans) {
			lines[i].Spans = compactMarkdownTableSpans(lines[i].Spans)
		}
	}
	return lines
}

// renderTaskCheckboxes replaces a genuine task-list checkbox token with its
// glyph. It only matches when the line carries a list prefix (bullet or
// ordinal marker) and the token is the leading text of the line, which is
// exactly how FluffyUI's TaskCheckBox AST node renders it. This keeps prose
// and code containing "[ ] " or "[x] " substrings (for example an array
// index expression like "arr[x] value") untouched.
func renderTaskCheckboxes(line markdown.StyledLine) []markdown.StyledSpan {
	spans := line.Spans
	if len(spans) == 0 || len(line.Prefix) == 0 {
		return spans
	}
	tokens := []struct {
		literal string
		glyph   string
	}{
		{"[ ] ", "☐ "},
		{"[x] ", "☑ "},
		{"[X] ", "☑ "},
	}
	for _, token := range tokens {
		if strings.HasPrefix(spans[0].Text, token.literal) {
			out := append([]markdown.StyledSpan(nil), spans...)
			out[0].Text = token.glyph + strings.TrimPrefix(out[0].Text, token.literal)
			return out
		}
	}
	return spans
}

// tableMarkerRunes are the structural box-drawing characters FluffyUI emits
// around table cells and borders. A rune adjacent to one of these on a table
// line is padding, not content, regardless of whether the line is a data
// row (delimited by │) or a border row (delimited by corners/crosses).
var tableMarkerRunes = map[rune]bool{
	'│': true,
	'╭': true, '╮': true, '╰': true, '╯': true,
	'├': true, '┤': true, '┬': true, '┴': true, '┼': true,
}

func isTableMarkerRune(r rune) bool {
	return tableMarkerRunes[r]
}

// isTablePaddingRune reports whether r is a padding-fill character: a space
// in data rows or the horizontal box-drawing rule in border rows.
func isTablePaddingRune(r rune) bool {
	return r == ' ' || r == '─'
}

func isMarkdownTableRow(spans []markdown.StyledSpan) bool {
	text := markdownSpanText(spans)
	runes := []rune(text)
	if len(runes) == 0 {
		return false
	}
	if !isTableMarkerRune(runes[0]) || !isTableMarkerRune(runes[len(runes)-1]) {
		return false
	}
	markers := 0
	for _, r := range runes {
		if isTableMarkerRune(r) {
			markers++
		}
	}
	return markers >= 2
}

type compactMarkdownRune struct {
	value rune
	style compositor.Style
}

// compactMarkdownTableSpans strips the padding-fill rune immediately
// adjacent to a table marker on both data rows and border rows. Because
// FluffyUI derives border segment widths and data segment widths from the
// same column widths and padding setting, removing one padding rune per
// side of every marker on every table line keeps data-row width and
// border-row width equal after compaction, so the ┬/┼/┴ junctions still
// line up under the │ separators.
func compactMarkdownTableSpans(spans []markdown.StyledSpan) []markdown.StyledSpan {
	var cells []compactMarkdownRune
	for _, span := range spans {
		for _, value := range span.Text {
			cells = append(cells, compactMarkdownRune{value: value, style: span.Style})
		}
	}
	if len(cells) == 0 {
		return spans
	}

	filtered := make([]compactMarkdownRune, 0, len(cells))
	for i, cell := range cells {
		if isTablePaddingRune(cell.value) &&
			((i > 0 && isTableMarkerRune(cells[i-1].value)) || (i+1 < len(cells) && isTableMarkerRune(cells[i+1].value))) {
			continue
		}
		filtered = append(filtered, cell)
	}

	out := make([]markdown.StyledSpan, 0, len(spans))
	for _, cell := range filtered {
		if len(out) > 0 && out[len(out)-1].Style.Equal(cell.style) {
			out[len(out)-1].Text += string(cell.value)
			continue
		}
		out = append(out, markdown.StyledSpan{Text: string(cell.value), Style: cell.style})
	}
	return out
}

func markdownSpanText(spans []markdown.StyledSpan) string {
	var out strings.Builder
	for _, span := range spans {
		out.WriteString(span.Text)
	}
	return out.String()
}
