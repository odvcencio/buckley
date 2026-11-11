package commands

import (
	"sort"
	"strings"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// This file is the one reader of Markdown structure in the pull request code.
// It parses text with a CommonMark parser (goldmark, with its GitHub task-list
// extension) and reports what the parser found, with source line numbers.
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

// mdBlock is a child of the document, in order.
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

const (
	attrStart = "mdshape-start"
	attrEnd   = "mdshape-end"
)

// lineRecorder wraps a goldmark block parser to record the first and the last
// line of every block it opens. The parser does not keep source positions
// itself.
type lineRecorder struct{ parser.BlockParser }

func (p lineRecorder) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, _ := reader.Position()
	node, state := p.BlockParser.Open(parent, reader, pc)
	if node != nil {
		node.SetAttributeString(attrStart, line)
		node.SetAttributeString(attrEnd, line)
	}
	return node, state
}

func (p lineRecorder) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	state := p.BlockParser.Continue(node, reader, pc)
	// A line that continues a block belongs to it. The closing fence of a fenced
	// code block and the closing line of an HTML block of the kinds that have
	// one close their block and belong to it too.
	closing := false
	switch v := node.(type) {
	case *ast.FencedCodeBlock:
		closing = true
	case *ast.HTMLBlock:
		closing = v.HasClosure()
	}
	if state&parser.Close == 0 || closing {
		line, _ := reader.Position()
		node.SetAttributeString(attrEnd, line)
	}
	return state
}

var (
	mdOnce   sync.Once
	mdParser goldmark.Markdown
)

func markdownParser() goldmark.Markdown {
	mdOnce.Do(func() {
		blocks := parser.DefaultBlockParsers()
		for i := range blocks {
			blocks[i].Value = lineRecorder{blocks[i].Value.(parser.BlockParser)}
		}
		mdParser = goldmark.New(
			goldmark.WithParser(parser.NewParser(
				parser.WithBlockParsers(blocks...),
				parser.WithInlineParsers(parser.DefaultInlineParsers()...),
				parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
			)),
			goldmark.WithExtensions(extension.TaskList),
		)
	})
	return mdParser
}

func nodeLine(n ast.Node, name string) (int, bool) {
	v, ok := n.AttributeString(name)
	line, isInt := v.(int)
	return line, ok && isInt
}

// readMarkdown parses text. Line endings are normalized first.
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
	source := []byte(src)
	root := markdownParser().Parser().Parse(text.NewReader(source))

	d.readComments(root)
	lists := map[ast.Node]int{}
	_ = ast.Walk(root, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.List:
			lists[n] = len(lists)
		case *ast.Heading:
			if h, ok := d.readHeading(v); ok {
				d.Headings = append(d.Headings, h)
			}
		case *ast.ListItem:
			d.addTask(v, lists)
		case *ast.FencedCodeBlock:
			if s, e, ok := d.blockRange(n); ok {
				d.Code = append(d.Code, mdCode{Start: s, End: e, Fenced: true})
			}
		case *ast.CodeBlock:
			if s, e, ok := d.blockRange(n); ok {
				d.Code = append(d.Code, mdCode{Start: s, End: e})
			}
		case *ast.HTMLBlock:
			if s, e, ok := d.blockRange(n); ok {
				d.HTML = append(d.HTML, mdHTML{Start: s, End: e, Comment: v.HTMLBlockType == ast.HTMLBlockType2})
			}
		}
		return ast.WalkContinue, nil
	})
	for c := root.FirstChild(); c != nil; c = c.NextSibling() {
		kind := ""
		switch c.(type) {
		case *ast.Heading:
			kind = "heading"
		case *ast.Paragraph, *ast.TextBlock:
			kind = "paragraph"
		case *ast.List:
			kind = "list"
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			kind = "code"
		case *ast.HTMLBlock:
			kind = "html"
		case *ast.Blockquote:
			kind = "quote"
		case *ast.ThematicBreak:
			kind = "break"
		default:
			kind = "other"
		}
		if h, isHeading := c.(*ast.Heading); isHeading {
			if hd, ok := d.readHeading(h); ok {
				d.Blocks = append(d.Blocks, mdBlock{Kind: kind, Start: hd.Start, End: hd.End})
			}
		} else if s, e, ok := d.blockRange(c); ok {
			d.Blocks = append(d.Blocks, mdBlock{Kind: kind, Start: s, End: e})
		}
	}
	sort.Slice(d.Headings, func(i, j int) bool { return d.Headings[i].Start < d.Headings[j].Start })
	sort.Slice(d.Tasks, func(i, j int) bool { return d.Tasks[i].MarkAt < d.Tasks[j].MarkAt })
	return d
}

// blockRange returns the first and last line the wrapper recorded for a block.
func (d *mdDoc) blockRange(n ast.Node) (start, end int, ok bool) {
	start, ok1 := nodeLine(n, attrStart)
	end, ok2 := nodeLine(n, attrEnd)
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	if end < start {
		end = start
	}
	return start, min(end, len(d.Lines)-1), true
}

// lineOf returns the 0-based line that holds a byte offset.
func (d *mdDoc) lineOf(off int) int {
	return max(0, sort.Search(len(d.lineStart), func(i int) bool { return d.lineStart[i] > off })-1)
}

func (d *mdDoc) readComments(root ast.Node) {
	add := func(start, stop int, block bool) {
		raw := d.text[start:stop]
		inner := strings.TrimPrefix(raw, "<!--")
		inner = strings.TrimSuffix(inner, "-->")
		d.Comments = append(d.Comments, mdComment{
			Start: start, Stop: stop,
			StartLine: d.lineOf(start), EndLine: d.lineOf(max(start, stop-1)),
			Text: strings.TrimSpace(inner), Block: block,
		})
	}
	_ = ast.Walk(root, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.HTMLBlock:
			if v.HTMLBlockType != ast.HTMLBlockType2 {
				break
			}
			// A comment block holds its comment and may hold text after the
			// closing "-->" on the same line. The comments are the delimited spans
			// of the block's own text.
			ls := v.Lines()
			if ls.Len() == 0 {
				break
			}
			first, last := ls.At(0).Start, ls.At(ls.Len()-1).Stop
			if v.HasClosure() {
				last = max(last, v.ClosureLine.Stop)
			}
			rest := first
			for rest < last {
				open := strings.Index(d.text[rest:last], "<!--")
				if open < 0 {
					break
				}
				open += rest
				stop := last
				if end := strings.Index(d.text[open+4:last], "-->"); end >= 0 {
					stop = open + 4 + end + 3
				}
				add(open, stop, true)
				rest = stop
			}
		case *ast.RawHTML:
			segs := v.Segments
			if segs.Len() == 0 {
				break
			}
			start, stop := segs.At(0).Start, segs.At(segs.Len()-1).Stop
			if strings.HasPrefix(d.text[start:stop], "<!--") {
				add(start, stop, false)
			}
		}
		return ast.WalkContinue, nil
	})
	sort.Slice(d.Comments, func(i, j int) bool { return d.Comments[i].Start < d.Comments[j].Start })
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

// readHeading reads a heading node. A setext heading is recorded at its
// underline, so its first line comes from its text lines.
func (d *mdDoc) readHeading(h *ast.Heading) (mdHeading, bool) {
	s, e, ok := d.blockRange(h)
	if !ok {
		return mdHeading{}, false
	}
	out := mdHeading{Level: h.Level, Top: h.Parent() != nil && h.Parent().Kind() == ast.KindDocument, Start: s, End: e}
	ls := h.Lines()
	var parts []string
	for i := 0; i < ls.Len(); i++ {
		seg := ls.At(i)
		parts = append(parts, strings.TrimSpace(d.outsideComments(seg.Start, seg.Stop)))
	}
	if ls.Len() > 0 {
		if first := d.lineOf(ls.At(0).Start); first < e {
			out.Setext, out.Start = true, first
		}
	}
	out.Text = strings.TrimSpace(strings.Join(parts, " "))
	return out, true
}

func (d *mdDoc) addTask(item *ast.ListItem, lists map[ast.Node]int) {
	tb := item.FirstChild()
	if tb == nil || tb.FirstChild() == nil {
		return
	}
	box, ok := tb.FirstChild().(*extast.TaskCheckBox)
	if !ok || tb.Lines().Len() == 0 {
		return
	}
	at := tb.Lines().At(0).Start // the "["
	line := d.lineOf(at)
	_, lineStop := d.lineSpan(line, line)
	t := mdTask{
		Line: line, Checked: box.IsChecked, MarkAt: at + 1,
		Item:       strings.Join(strings.Fields(d.outsideComments(min(at+3, lineStop), lineStop)), " "),
		CommentEnd: line,
	}
	for _, c := range d.Comments {
		if c.StartLine == line {
			t.CommentEnd = max(t.CommentEnd, c.EndLine)
		}
	}
	for p := item.Parent(); p != nil; p = p.Parent() {
		if n, ok := lists[p]; ok {
			t.List = n
			break
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
