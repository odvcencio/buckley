package commands

import (
	"sort"
	"strings"

	"m31labs.dev/mdpp"
)

// This file is the one reader of Markdown structure in the pull request code.
// It parses text with Markdown++ (m31labs.dev/mdpp), a CommonMark parser built
// on gotreesitter, and reports what the parser found, with source line numbers.
// Nothing else decides what a heading, a list item, a code block or a comment
// is: template parsing, checkbox ticking, the demotion of placed text, the
// carried-section filter and the final check of the body's shape all ask this
// reader. Text that the parser places inside a code block, an HTML block or a
// comment is text, never structure.

// normalizeNewlines gives every line ending as LF. All text (template, notes,
// model fields) passes through it before anything reads it.
func normalizeNewlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// mdHeading is a heading as the parser reads it.
type mdHeading struct {
	Level  int
	Text   string // the heading's words, comments left out, lines joined by a space
	Setext bool   // a text over an underline of "=" or "-"; else an ATX "#" heading
	Top    bool   // a child of the document, not of a list item or a quote
	// Start and End are the 0-based first and last line; a setext heading's
	// underline is its last line.
	Start, End int
}

// mdTask is a task-list item: a list item that starts with "[ ]" or "[x]".
type mdTask struct {
	Line    int    // the item's first line
	Checked bool   // ticked
	Item    string // identity: the rest of the first line, comments out, white space collapsed
	MarkAt  int    // byte offset of the check mark in the text
	List    int    // number of the list the item belongs to, in document order
	// CommentEnd is the last line of the comments that start on Line; it is
	// Line when there are none.
	CommentEnd int
}

// mdCode is a fenced or an indented code block; End includes the closing fence.
type mdCode struct {
	Start, End int
	Fenced     bool
}

// mdHTML is an HTML block. Comment reports one that opens with "<!--".
type mdHTML struct {
	Start, End int
	Comment    bool
}

// mdComment is an HTML comment: a comment block, or inline raw HTML that opens
// with "<!--". Start and Stop are byte offsets in the text (Stop excluded).
type mdComment struct {
	Start, Stop        int
	StartLine, EndLine int
	Text               string // the words inside, without the delimiters
	Block              bool
}

// mdBlock is a block of the document at any depth, in order of its first
// line (a container before its children).
type mdBlock struct {
	Kind       string // heading, paragraph, list, code, html, quote, break
	Start, End int
}

// mdDoc is what the parser found in a text.
type mdDoc struct {
	text      string
	Lines     []string
	lineStart []int
	Headings  []mdHeading // every heading, in document order
	Tasks     []mdTask
	Code      []mdCode
	HTML      []mdHTML
	Comments  []mdComment
	Blocks    []mdBlock
}

// readMarkdown parses text with Markdown++ (m31labs.dev/mdpp). Line endings are
// normalized first, so the byte offsets of the parse are offsets in d.text.
func readMarkdown(src string) *mdDoc {
	src = normalizeNewlines(src)
	d := &mdDoc{text: src}
	d.Lines = strings.Split(src, "\n")
	d.lineStart = make([]int, len(d.Lines))
	off := 0
	for i, l := range d.Lines {
		d.lineStart[i] = off
		off += len(l) + 1
	}
	doc, err := mdpp.Parse([]byte(src))
	if err != nil || doc == nil || doc.Root == nil {
		return d
	}
	d.readComments(doc.Root)
	sort.Slice(d.Comments, func(i, j int) bool { return d.Comments[i].Start < d.Comments[j].Start })
	lists := 0
	var walk func(n *mdpp.Node, parent *mdpp.Node, list int)
	walk = func(n *mdpp.Node, parent *mdpp.Node, list int) {
		if kind := blockKind(n.Type); kind != "" {
			s, e := d.nodeLines(n)
			d.Blocks = append(d.Blocks, mdBlock{Kind: kind, Start: s, End: e})
		}
		switch n.Type {
		case mdpp.NodeList:
			lists++
			list = lists - 1
		case mdpp.NodeHeading:
			d.Headings = append(d.Headings, d.readHeading(n, parent == doc.Root))
		case mdpp.NodeTaskListItem:
			d.addTask(n, list)
		case mdpp.NodeCodeBlock:
			s, e := d.nodeLines(n)
			d.Code = append(d.Code, mdCode{Start: s, End: e, Fenced: d.fencedAt(s)})
		case mdpp.NodeHTMLBlock:
			s, e := d.nodeLines(n)
			d.HTML = append(d.HTML, mdHTML{Start: s, End: e, Comment: strings.HasPrefix(strings.TrimLeft(d.Lines[s], " "), "<!--")})
		}
		for _, c := range n.Children {
			walk(c, n, list)
		}
	}
	walk(doc.Root, nil, 0)
	sort.SliceStable(d.Blocks, func(i, j int) bool { return d.Blocks[i].Start < d.Blocks[j].Start })
	sort.Slice(d.Headings, func(i, j int) bool { return d.Headings[i].Start < d.Headings[j].Start })
	sort.Slice(d.Tasks, func(i, j int) bool { return d.Tasks[i].MarkAt < d.Tasks[j].MarkAt })
	return d
}

func blockKind(t mdpp.NodeType) string {
	switch t {
	case mdpp.NodeHeading:
		return "heading"
	case mdpp.NodeParagraph:
		return "paragraph"
	case mdpp.NodeList:
		return "list"
	case mdpp.NodeListItem, mdpp.NodeTaskListItem:
		return "item"
	case mdpp.NodeCodeBlock:
		return "code"
	case mdpp.NodeHTMLBlock:
		return "html"
	case mdpp.NodeBlockquote:
		return "quote"
	case mdpp.NodeThematicBreak:
		return "break"
	}
	return ""
}

// nodeLines returns the first and last line (0-based) of a node. A node's byte
// range usually ends after its last line break, so the last line is the one
// that holds the byte before the end.
func (d *mdDoc) nodeLines(n *mdpp.Node) (start, end int) {
	from := min(max(n.Range.StartByte, 0), len(d.text))
	to := min(max(n.Range.EndByte, from), len(d.text))
	start = d.lineOf(from)
	end = d.lineOf(max(from, to-1))
	return start, min(end, len(d.Lines)-1)
}

// fencedAt reports a code block whose first line opens with a fence.
func (d *mdDoc) fencedAt(line int) bool {
	t := strings.TrimLeft(d.Lines[line], " ")
	return len(d.Lines[line])-len(t) <= 3 && (strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~"))
}

// lineOf returns the 0-based line that holds a byte offset.
func (d *mdDoc) lineOf(off int) int {
	return max(0, sort.Search(len(d.lineStart), func(i int) bool { return d.lineStart[i] > off })-1)
}

func (d *mdDoc) readComments(root *mdpp.Node) {
	add := func(start, stop int, block bool) {
		raw := d.text[start:stop]
		inner := strings.TrimSuffix(strings.TrimPrefix(raw, "<!--"), "-->")
		d.Comments = append(d.Comments, mdComment{
			Start: start, Stop: stop,
			StartLine: d.lineOf(start), EndLine: d.lineOf(max(start, stop-1)),
			Text: strings.TrimSpace(inner), Block: block,
		})
	}
	root.Walk(func(n *mdpp.Node) bool {
		from, to := min(max(n.Range.StartByte, 0), len(d.text)), min(max(n.Range.EndByte, 0), len(d.text))
		if to < from {
			return true
		}
		switch n.Type {
		case mdpp.NodeHTMLBlock:
			if !strings.HasPrefix(strings.TrimLeft(d.text[from:to], " "), "<!--") {
				break
			}
			// A comment block holds its comment and may hold text after the
			// closing "-->" on the same line. The comments are the delimited spans
			// of the block's own text.
			rest := from
			for rest < to {
				open := strings.Index(d.text[rest:to], "<!--")
				if open < 0 {
					break
				}
				open += rest
				stop := to
				if end := strings.Index(d.text[open+4:to], "-->"); end >= 0 {
					stop = open + 4 + end + 3
				}
				add(open, stop, true)
				rest = stop
			}
		case mdpp.NodeHTMLInline:
			if strings.HasPrefix(d.text[from:to], "<!--") {
				add(from, to, false)
			}
		}
		return true
	})
}

// outsideComments returns the part of d.text[start:stop] that is outside
// comments.
func (d *mdDoc) outsideComments(start, stop int) string {
	var b strings.Builder
	at := start
	for _, c := range d.Comments {
		if c.Stop <= at || c.Start >= stop {
			continue
		}
		if c.Start > at {
			b.WriteString(d.text[at:c.Start])
		}
		at = max(at, min(c.Stop, stop))
	}
	if at < stop {
		b.WriteString(d.text[at:stop])
	}
	return b.String()
}

// lineSpan returns the byte range of lines first..last (inclusive), without the
// last line break.
func (d *mdDoc) lineSpan(first, last int) (start, stop int) {
	start = d.lineStart[first]
	stop = d.lineStart[last] + len(d.Lines[last])
	return start, stop
}

// readHeading reads a heading node. A setext heading is a heading whose range
// spans its text lines and the underline line.
func (d *mdDoc) readHeading(h *mdpp.Node, top bool) mdHeading {
	s, e := d.nodeLines(h)
	out := mdHeading{Level: h.Level(), Top: top, Start: s, End: e, Setext: e > s}
	var raw strings.Builder
	for _, c := range h.Children {
		from, to := min(max(c.Range.StartByte, 0), len(d.text)), min(max(c.Range.EndByte, 0), len(d.text))
		switch {
		case c.Type == mdpp.NodeHTMLInline && strings.HasPrefix(d.text[from:max(from, to)], "<!--"):
			// a comment in a heading is not part of its text
		case to > from:
			raw.WriteString(d.text[from:to])
		case c.Type == mdpp.NodeSoftBreak:
			raw.WriteString("\n")
		default:
			// mdpp gives some text nodes (those holding punctuation such as "?")
			// no range; their literal is the text.
			raw.WriteString(c.Text())
		}
	}
	var parts []string
	for _, line := range strings.Split(raw.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	out.Text = strings.Join(parts, " ")
	return out
}

func (d *mdDoc) addTask(item *mdpp.Node, list int) {
	from := min(max(item.Range.StartByte, 0), len(d.text))
	line := d.lineOf(from)
	_, lineStop := d.lineSpan(line, line)
	open := strings.IndexByte(d.text[from:lineStop], '[')
	if open < 0 || from+open+2 >= len(d.text) {
		return
	}
	at := from + open // the "["
	t := mdTask{
		Line: line, Checked: item.Attr("checked") == "true", MarkAt: at + 1, List: list,
		Item:       strings.Join(strings.Fields(d.outsideComments(min(at+3, lineStop), lineStop)), " "),
		CommentEnd: line,
	}
	for _, c := range d.Comments {
		if c.StartLine == line {
			t.CommentEnd = max(t.CommentEnd, c.EndLine)
		}
	}
	d.Tasks = append(d.Tasks, t)
}

// topHeadings returns the headings that are children of the document: the
// template's structure.
func (d *mdDoc) topHeadings() []mdHeading {
	var out []mdHeading
	for _, h := range d.Headings {
		if h.Top {
			out = append(out, h)
		}
	}
	return out
}

// mdSection is a top-level heading and the lines it opens: First..Last
// inclusive, empty when Last < First.
type mdSection struct {
	Heading     mdHeading
	First, Last int
}

// sections returns, for each top-level heading of the given level, the section
// it opens: the lines after the heading up to the next top-level heading of the
// same or a higher level. Level 0 reads every top-level heading as opening a
// section that ends at the next top-level heading of any level.
func (d *mdDoc) sections(level int) []mdSection {
	tops := d.topHeadings()
	var out []mdSection
	for i, h := range tops {
		if level != 0 && h.Level != level {
			continue
		}
		last := len(d.Lines) - 1
		for _, next := range tops[i+1:] {
			if level == 0 || next.Level <= h.Level {
				last = next.Start - 1
				break
			}
		}
		out = append(out, mdSection{Heading: h, First: h.End + 1, Last: last})
	}
	return out
}

// blankLine reports a line of white space only.
func (d *mdDoc) blankLine(i int) bool { return strings.TrimSpace(d.Lines[i]) == "" }

// removeSpans returns text without the byte ranges in spans (sorted, apart).
func removeSpans(src string, spans [][2]int) string {
	var b strings.Builder
	at := 0
	for _, s := range spans {
		if s[0] > at {
			b.WriteString(src[at:s[0]])
		}
		at = max(at, s[1])
	}
	b.WriteString(src[at:])
	return b.String()
}
