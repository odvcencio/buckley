package widgets

import (
	"strings"

	"m31labs.dev/fluffyui/compositor"
	"m31labs.dev/fluffyui/markdown"
)

// prepareMarkdownForWorkspace keeps Markdown++ as Buckley's document parser,
// then applies terminal-specific density affordances to FluffyUI's styled
// output. Task-list checkboxes become recognizable TODO glyphs and table cell
// padding is removed without changing the semantic content.
func prepareMarkdownForWorkspace(lines []markdown.StyledLine) []markdown.StyledLine {
	for i := range lines {
		lines[i].Spans = renderTaskCheckboxes(lines[i].Spans)
		if isMarkdownTableRow(lines[i].Spans) {
			lines[i].Spans = compactMarkdownTableSpans(lines[i].Spans)
		}
	}
	return lines
}

func renderTaskCheckboxes(spans []markdown.StyledSpan) []markdown.StyledSpan {
	if len(spans) == 0 {
		return spans
	}
	out := append([]markdown.StyledSpan(nil), spans...)
	for i := range out {
		if strings.Contains(out[i].Text, "[ ] ") {
			out[i].Text = strings.Replace(out[i].Text, "[ ] ", "☐ ", 1)
			return out
		}
		if strings.Contains(out[i].Text, "[x] ") {
			out[i].Text = strings.Replace(out[i].Text, "[x] ", "☑ ", 1)
			return out
		}
		if strings.Contains(out[i].Text, "[X] ") {
			out[i].Text = strings.Replace(out[i].Text, "[X] ", "☑ ", 1)
			return out
		}
	}
	return out
}

func isMarkdownTableRow(spans []markdown.StyledSpan) bool {
	text := markdownSpanText(spans)
	return strings.HasPrefix(text, "│") && strings.HasSuffix(text, "│") && strings.Count(text, "│") >= 2
}

type compactMarkdownRune struct {
	value rune
	style compositor.Style
}

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
		if cell.value == ' ' && ((i > 0 && cells[i-1].value == '│') || (i+1 < len(cells) && cells[i+1].value == '│')) {
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
