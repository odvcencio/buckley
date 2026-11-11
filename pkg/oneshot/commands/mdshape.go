package commands

import (
	"fmt"
	"regexp"
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

// mdHazard is raw HTML that a renderer acts on although the Markdown tree does
// not show it: a comment opener with no closer, which hides the rest of the
// rendered page, or a raw heading element. Start is a byte offset; Line is
// 0-based.
type mdHazard struct {
	Unclosed bool // else a raw heading element
	Level    int  // of the heading element
	Start    int
	Line     int
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
	Hazards   []mdHazard // raw HTML in HTML blocks and inline HTML
	inFence   []bool     // per line: inside a fenced code block, delimiters included
	// Problems say why the reading cannot be trusted: a parse error, a recovery
	// diagnostic of the parser (a block kept as text, a budget hit) or a source
	// range that cannot be used. An empty list means the reading is exact.
	Problems []string
	// Foreign lists the constructs that only Markdown++ reads (not GitHub's
	// Markdown): they are read differently by GitHub, so the shape of the text
	// cannot be certified.
	Foreign []mdForeign
}

// mdForeign is a Markdown++-only construct; Line is 0-based.
type mdForeign struct {
	Name string
	Line int
}

// foreignNodes are the node types that Markdown++ adds to GitHub's Markdown.
// This guard goes away when the reader can ask mdpp for GitHub's reading.
var foreignNodes = map[mdpp.NodeType]string{
	mdpp.NodeComponent:          "a Markdown++ component (a tag such as <H2 ...> in upper case)",
	mdpp.NodeExpression:         "a Markdown++ expression ({...})",
	mdpp.NodeContainerDirective: "a Markdown++ container directive (:::name)",
	mdpp.NodeAdmonition:         "a Markdown++ admonition",
	mdpp.NodeFrontmatter:        "Markdown++ front matter",
	mdpp.NodeDefinitionList:     "a Markdown++ definition list",
	mdpp.NodeTableOfContents:    "a Markdown++ table of contents",
	mdpp.NodeAutoEmbed:          "a Markdown++ embed",
	mdpp.NodeDiagram:            "a Markdown++ diagram",
	mdpp.NodeSlide:              "a Markdown++ slide",
	mdpp.NodeMathBlock:          "a Markdown++ math block",
}

func (d *mdDoc) problem(format string, args ...any) {
	d.Problems = append(d.Problems, fmt.Sprintf(format, args...))
}

// readMarkdown parses text with Markdown++ (m31labs.dev/mdpp). Line endings are
// normalized first, so the byte offsets of the parse are offsets in d.text.
func readMarkdown(src string) *mdDoc {
	src = normalizeNewlines(src)
	d := &mdDoc{text: src}
	d.Lines = strings.Split(src, "\n")
	d.inFence = make([]bool, len(d.Lines))
	d.lineStart = make([]int, len(d.Lines))
	off := 0
	for i, l := range d.Lines {
		d.lineStart[i] = off
		off += len(l) + 1
	}
	doc, err := mdpp.Parse([]byte(src))
	if err != nil || doc == nil || doc.Root == nil {
		d.problem("Markdown++ could not parse the text (%v)", err)
		return d
	}
	for _, diag := range doc.Diagnostics() {
		if strings.HasPrefix(diag.Code, "MDPP-PARSE-") {
			d.problem("Markdown++ %s at line %d: %s", diag.Code, diag.Range.StartLine, diag.Message)
		}
	}
	d.readComments(doc.Root)
	sort.Slice(d.Comments, func(i, j int) bool { return d.Comments[i].Start < d.Comments[j].Start })
	lists := 0
	var walk func(n *mdpp.Node, parent *mdpp.Node, list int)
	walk = func(n *mdpp.Node, parent *mdpp.Node, list int) {
		if name, ok := foreignNodes[n.Type]; ok {
			d.Foreign = append(d.Foreign, mdForeign{Name: name, Line: d.lineOf(min(max(n.Range.StartByte, 0), len(d.text)))})
		}
		if isBlockNode(n.Type) && !d.checkRange(n) {
			return // its range cannot be used, so neither can anything below it
		}
		switch n.Type {
		case mdpp.NodeList:
			lists++
			list = lists - 1
		case mdpp.NodeHeading:
			if h, ok := d.readHeading(n, parent == doc.Root); ok {
				d.Headings = append(d.Headings, h)
			}
		case mdpp.NodeTaskListItem:
			d.addTask(n, list)
		case mdpp.NodeCodeBlock:
			s, e := d.nodeLines(n)
			fenced := d.fencedAt(s)
			d.Code = append(d.Code, mdCode{Start: s, End: e, Fenced: fenced})
			for i := s; fenced && i <= e; i++ {
				d.inFence[i] = true
			}
		case mdpp.NodeHTMLBlock, mdpp.NodeHTMLInline:
			d.addHazards(n)
		}
		switch n.Type {
		case mdpp.NodeHTMLBlock:
			s, e := d.nodeLines(n)
			d.HTML = append(d.HTML, mdHTML{Start: s, End: e, Comment: strings.HasPrefix(strings.TrimLeft(d.Lines[s], " "), "<!--")})
		}
		for _, c := range n.Children {
			walk(c, n, list)
		}
	}
	walk(doc.Root, nil, 0)
	sort.Slice(d.Headings, func(i, j int) bool { return d.Headings[i].Start < d.Headings[j].Start })
	sort.Slice(d.Tasks, func(i, j int) bool { return d.Tasks[i].MarkAt < d.Tasks[j].MarkAt })
	return d
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
			// The comments of an HTML block are the delimited spans of the block's
			// own text, wherever they sit in it: a comment block holds its comment
			// and may hold text after the closing "-->", and any other block
			// (<div>, <pre>) may hold comments inside it.
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

// isBlockNode reports the node types that stand for a block of lines.
func isBlockNode(t mdpp.NodeType) bool {
	switch t {
	case mdpp.NodeHeading, mdpp.NodeParagraph, mdpp.NodeList, mdpp.NodeListItem, mdpp.NodeTaskListItem,
		mdpp.NodeCodeBlock, mdpp.NodeHTMLBlock, mdpp.NodeBlockquote, mdpp.NodeThematicBreak:
		return true
	}
	return false
}

// checkRange rejects a source range that cannot be used: one outside the text,
// or an empty one for a block (a block always has source text). It records a
// problem and reports false for such a range.
func (d *mdDoc) checkRange(n *mdpp.Node) bool {
	r := n.Range
	if r.StartByte < 0 || r.EndByte > len(d.text)+1 || r.EndByte <= r.StartByte { // a range may end one past the text: the line break that a last line lacks
		d.problem("Markdown++ gave a block (node type %d) the source range %d..%d, which cannot be used (the text has %d bytes)", n.Type, r.StartByte, r.EndByte, len(d.text))
		return false
	}
	return true
}

// underlineOnly reports a setext underline: up to three spaces, then only "="
// or only "-", then optional spaces.
func underlineOnly(line string) bool {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return false
	}
	t = strings.TrimRight(t, " \t")
	return t != "" && (strings.Trim(t, "=") == "" || strings.Trim(t, "-") == "")
}

// readHeading reads a heading node. A setext heading is a heading whose range
// spans its text lines and the underline line. The range is rejected, with a
// problem recorded, when it fits neither form: one line that opens with "#"
// (ATX), or several lines that end with an underline (setext).
func (d *mdDoc) readHeading(h *mdpp.Node, top bool) (mdHeading, bool) {
	s, e := d.nodeLines(h)
	out := mdHeading{Level: h.Level(), Top: top, Start: s, End: e, Setext: e > s}
	first := strings.TrimLeft(d.Lines[s], " ")
	switch {
	case !top:
		// A heading inside a list item or a quote starts after the container's
		// markers; it is not part of the text's structure and is not checked.
	case e == s && strings.HasPrefix(first, "#") && len(d.Lines[s])-len(first) <= 3:
	case e > s && !strings.HasPrefix(first, "#") && underlineOnly(d.Lines[e]):
	default:
		d.problem("Markdown++ gave a heading the source range lines %d to %d, which fits neither an ATX nor a setext heading", s+1, e+1)
		return out, false
	}
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
	return out, true
}

func (d *mdDoc) addTask(item *mdpp.Node, list int) {
	from := min(max(item.Range.StartByte, 0), len(d.text))
	line := d.lineOf(from)
	_, lineStop := d.lineSpan(line, line)
	open := strings.IndexByte(d.text[from:lineStop], '[')
	if open < 0 || from+open+2 >= len(d.text) || d.text[from+open+2] != ']' {
		d.problem("Markdown++ gave a task-list item at line %d a source range with no check box in it", line+1)
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

var headingTagRe = regexp.MustCompile(`^<[hH]([1-6])[\s/>]`)

// addHazards scans the source text of an HTML node, which the parser handed
// over as raw HTML, for the two constructs a renderer acts on: a comment opener
// with no closer, and a heading element.
func (d *mdDoc) addHazards(n *mdpp.Node) {
	from, to := min(max(n.Range.StartByte, 0), len(d.text)), min(max(n.Range.EndByte, 0), len(d.text))
	if to <= from {
		return
	}
	t := d.text[from:to]
	for i := 0; i < len(t); i++ {
		if t[i] != '<' {
			continue
		}
		switch {
		case strings.HasPrefix(t[i:], "<!--"):
			end := strings.Index(t[i+4:], "-->")
			if end < 0 {
				d.Hazards = append(d.Hazards, mdHazard{Unclosed: true, Start: from + i, Line: d.lineOf(from + i)})
				return
			}
			i += 4 + end + 2
		default:
			if m := headingTagRe.FindStringSubmatch(t[i:]); m != nil {
				d.Hazards = append(d.Hazards, mdHazard{Level: int(m[1][0] - '0'), Start: from + i, Line: d.lineOf(from + i)})
			}
		}
	}
}

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

// mdFrag is a run of lines of a document with the task items and comments that
// the whole-document parse found in them, at offsets relative to the run. The
// run is never parsed again on its own: a fragment parsed alone can read
// differently, and its ranges cannot be trusted as the document's.
type mdFrag struct {
	text     string
	tasks    []mdTask
	comments []mdComment
}

// fragment cuts lines first..last (inclusive) out of the document.
func (d *mdDoc) fragment(first, last int) *mdFrag {
	f := &mdFrag{}
	if last < first || first < 0 || last >= len(d.Lines) {
		return f
	}
	base, stop := d.lineSpan(first, last)
	f.text = d.text[base:stop]
	for _, t := range d.Tasks {
		if t.Line >= first && t.Line <= last {
			t.Line -= first
			t.CommentEnd -= first
			t.MarkAt -= base
			f.tasks = append(f.tasks, t)
		}
	}
	for _, c := range d.Comments {
		if c.StartLine >= first && c.EndLine <= last {
			c.Start -= base
			c.Stop -= base
			c.StartLine -= first
			c.EndLine -= first
			f.comments = append(f.comments, c)
		}
	}
	return f
}

func (f *mdFrag) lines() []string {
	if f.text == "" {
		return nil
	}
	return strings.Split(f.text, "\n")
}

// identities returns the identities of the task items of the run.
func (f *mdFrag) identities() map[string]bool {
	out := map[string]bool{}
	for _, t := range f.tasks {
		out[t.Item] = true
	}
	return out
}

// taskLines returns the task item lines as written.
func (f *mdFrag) taskLines() []string {
	lines := f.lines()
	var out []string
	for _, t := range f.tasks {
		out = append(out, lines[t.Line])
	}
	return out
}

// removeCommentSpans removes the comments (offsets in text) from text, except
// those whose start is in keep, and drops the lines that a removal left empty.
func removeCommentSpans(text string, comments []mdComment, keep map[int]bool) []string {
	lines := strings.Split(text, "\n")
	var out []string
	from := 0
	for _, line := range lines {
		to := from + len(line)
		var spans [][2]int
		for _, c := range comments {
			if keep[c.Start] || c.Stop <= from || c.Start >= to {
				continue
			}
			spans = append(spans, [2]int{max(c.Start, from) - from, min(c.Stop, to) - from})
		}
		if len(spans) == 0 {
			out = append(out, line)
		} else if kept := removeSpans(line, spans); strings.TrimSpace(kept) != "" {
			out = append(out, kept)
		}
		from = to + 1
	}
	return out
}

// pure reports a run whose lines, comments aside, are task item lines only.
func (f *mdFrag) pure() bool {
	if len(f.tasks) == 0 {
		return false
	}
	isBox := map[int]bool{}
	for _, t := range f.tasks {
		isBox[t.Line] = true
	}
	from := 0
	for i, line := range f.lines() {
		to := from + len(line)
		var spans [][2]int
		for _, c := range f.comments {
			if c.Stop > from && c.Start < to {
				spans = append(spans, [2]int{max(c.Start, from) - from, min(c.Stop, to) - from})
			}
		}
		if !isBox[i] && strings.TrimSpace(removeSpans(line, spans)) != "" {
			return false
		}
		from = to + 1
	}
	return true
}

// render writes the run with the check mark of every task item set from ticks
// (an item in ticks is ticked, any other is not; the rest of the line stays
// byte for byte), and with the comments removed when strip is set, except those
// attached to an item line. cleared lists the items that were ticked and are
// not in ticks.
func (f *mdFrag) render(ticks map[string]bool, strip bool) (lines []string, cleared []string) {
	mark := []byte(f.text)
	for _, t := range f.tasks {
		switch {
		case ticks[t.Item] && !t.Checked:
			mark[t.MarkAt] = 'x'
		case !ticks[t.Item] && t.Checked:
			mark[t.MarkAt] = ' '
			cleared = append(cleared, t.Item)
		}
	}
	text := string(mark)
	if !strip {
		if text == "" {
			return nil, cleared
		}
		return strings.Split(text, "\n"), cleared
	}
	attached := map[int]bool{}
	for _, t := range f.tasks {
		for _, c := range f.comments {
			if c.StartLine == t.Line {
				attached[c.Start] = true
			}
		}
	}
	return removeCommentSpans(text, f.comments, attached), cleared
}

// withoutComments returns the lines of the document with every comment removed;
// a line that a removal left empty is dropped.
func (d *mdDoc) withoutComments() []string {
	return removeCommentSpans(d.text, d.Comments, nil)
}
