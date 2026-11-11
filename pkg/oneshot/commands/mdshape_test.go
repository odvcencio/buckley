package commands

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// readingOf writes what the reader found as short strings, to compare with a
// reading that is written out by hand: "2:Text" is an ATX heading, "2s:Text" a
// setext heading, and "x:Item" or " :Item" a ticked or an unticked task item.
// Only top-level headings are listed.
func readingOf(src string) (headings, tasks []string) {
	d := readMarkdown(src)
	for _, h := range d.topHeadings() {
		mark := ""
		if h.Setext {
			mark = "s"
		}
		headings = append(headings, fmt.Sprintf("%d%s:%s", h.Level, mark, h.Text))
	}
	for _, t := range d.Tasks {
		box := " "
		if t.Checked {
			box = "x"
		}
		tasks = append(tasks, box+":"+t.Item)
	}
	return headings, tasks
}

// Every input that the four reviews of #478 used, and the reading that
// CommonMark gives it, written by hand from the specification. The report that
// supplied the input is named in the case.
func TestMarkdownReaderReadings(t *testing.T) {
	for _, tc := range []struct {
		name, report, in string
		headings, tasks  []string
	}{
		// First review.
		{"r1 fenced heading and box", "478", "## Example\n```md\n## Sample\n- [ ] Ready\n```\n", []string{"2:Example"}, nil},
		{"r1 nested heading", "478", "## Evidence\n### Measurements\nObserved output.", []string{"2:Evidence", "3:Measurements"}, nil},
		{"r1 preamble box", "478", "- [ ] License acknowledged\n\n## Summary\n<!-- describe -->", []string{"2:Summary"}, []string{" :License acknowledged"}},
		{"r1 inline comment identity", "478", "## Checklist\n- [ ] A <!-- guide -->", []string{"2:Checklist"}, []string{" :A"}},
		{"r1 level 6", "478", "###### Details\n<!-- describe -->", []string{"6:Details"}, nil},
		{"r1 box with X and star", "478", "* [X] one\n+ [ ] two\n  - [x] three", nil, []string{"x:one", " :two", "x:three"}},
		{"r1 indented box is code", "478", "para\n\n    - [ ] code, not an item", nil, nil},
		// Second review.
		{"r2 setext after Evidence", "478-r2", "Measurements\n------------\nObserved output.", []string{"2s:Measurements"}, nil},
		{"r2 unequal fence", "478-r2", "## Example\n````md\n```\n## Sample\n- [ ] Ready\n````\n", []string{"2:Example"}, nil},
		{"r2 comment and box", "478-r2", "## Checklist\n- [ ] A <!-- guide -->\n- [ ] B", []string{"2:Checklist"}, []string{" :A", " :B"}},
		// Third review.
		{"r3 two-line paragraph over ---", "478-r3", "Intro\nMeasurements\n---\nObserved output.", []string{"2s:Intro Measurements"}, nil},
		{"r3 two-line paragraph over =", "478-r3", "Intro\nMeasurements\n=\nObserved output.", []string{"1s:Intro Measurements"}, nil},
		{"r3 heading after closing fence", "478-r3", "```\ncode\n```\nTitle\n---\nText", []string{"2s:Title"}, nil},
		{"r3 title with inline code", "478-r3", "`Measurements`\n---\nText", []string{"2s:`Measurements`"}, nil},
		{"r3 list item then dashes", "478-r3", "- item\n  more\n---\nText", nil, nil},
		{"r3 block quote then dashes", "478-r3", "> quote\n---\nText", nil, nil},
		{"r3 setext template", "478-r3", "What\n=\n<!-- guide -->\nChecks\n-\n- [ ] Ran it\n", []string{"1s:What", "2s:Checks"}, []string{" :Ran it"}},
		{"r3 pre-ticked", "478-r3", "## Checklist\n- [x] A\n- [X] B", []string{"2:Checklist"}, []string{"x:A", "x:B"}},
		{"r3 comment over two lines", "478-r3", "## Checklist\n- [ ] A <!-- first\nsecond -->\n- [ ] B", []string{"2:Checklist"}, []string{" :A", " :B"}},
		// Fourth review.
		{"r4 CRLF model text", "478-r4", "Intro\r\nMeasurements\r\n---\r\nObserved.", []string{"2s:Intro Measurements"}, nil},
		{"r4 list interrupts paragraph", "478-r4", "## Summary\n<!-- describe -->\nIntro\n- [x] A\n---", []string{"2:Summary"}, []string{"x:A"}},
		{"r4 raw HTML swallows a heading", "478-r4", "## Summary\n\n<pre>\nlog\n\n## Checks\n- [ ] A", []string{"2:Summary"}, nil},
		{"r4 closed raw HTML holds a heading", "478-r4", "## Summary\n\n<pre>\n## Extra\n</pre>", []string{"2:Summary"}, nil},
		{"r4 fence marker in a comment", "478-r4", "## Summary\n<!--\n```\n-->\nTitle\n---\nfixed", []string{"2:Summary", "2s:Title"}, nil},
		{"r4 list then dashes", "478-r4", "Intro\n- item\n---\nAfter", nil, nil},
		{"r4 underline with trailing text", "478-r4", "Title\n--- trailing\nAfter", nil, nil},
		{"r4 mixed underline", "478-r4", "Title\n=-\nAfter", nil, nil},
		{"r4 CRLF underline", "478-r4", "Title\r\n---\r\nText", []string{"2s:Title"}, nil},
		{"r4 setext inside HTML block", "478-r4", "<pre>\nTitle\n---\n</pre>", nil, nil},
		// Other readings the code relies on.
		{"one-character underline", "own", "Title\n-\nText", []string{"2s:Title"}, nil},
		{"underline indented three spaces", "own", "Title\n   ---\nText", []string{"2s:Title"}, nil},
		{"underline indented four spaces", "own", "Title\n    ---\nText", nil, nil},
		{"title indented four spaces", "own", "    Title\n---\nText", nil, nil},
		{"dashes after a blank line", "own", "Text\n\n---\nAfter", nil, nil},
		{"heading in a comment", "own", "<!--\n## no\n-->\n## yes", []string{"2:yes"}, nil},
		{"heading in a list item", "own", "- ## inside\n\n## outside", []string{"2:outside"}, nil},
		{"closing hashes", "own", "## Title ##\n#NotAHeading", []string{"2:Title"}, nil},
		{"empty heading", "own", "##\n", []string{"2:"}, nil},
		{"tilde fence holds a box", "own", "~~~\n- [ ] x\n~~~\n- [ ] y", nil, []string{" :y"}},
		{"box in a quote", "own", "> - [x] quoted", nil, []string{"x:quoted"}},
		{"ordered box", "own", "1. [ ] first\n2. [x] second", nil, []string{" :first", "x:second"}},
		{"item text spaces collapse", "own", "- [x]   A    b  ", nil, []string{"x:A b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headings, tasks := readingOf(tc.in)
			if !reflect.DeepEqual(headings, tc.headings) || !reflect.DeepEqual(tasks, tc.tasks) {
				t.Errorf("%s: reading of %q =\nheadings %q\ntasks    %q\nwant\nheadings %q\ntasks    %q", tc.report, tc.in, headings, tasks, tc.headings, tc.tasks)
			}
		})
	}
}

// The reader reports line ranges, code blocks, HTML blocks, comments and the
// section that a heading opens.
func TestMarkdownReaderRanges(t *testing.T) {
	src := "Pre\n\nTitle\n=====\ntext\n\n## ATX ##\n```go\n# not\n```\n\n    code\n<!-- one\ntwo -->\n<div>\n\n## Next\n- [ ] A <!-- c\nd -->\n"
	d := readMarkdown(src)
	var heads []string
	for _, h := range d.topHeadings() {
		heads = append(heads, fmt.Sprintf("%d-%d", h.Start, h.End))
	}
	// Title occupies lines 2-3 (underline included); ATX is line 6; "## Next" is
	// line 16, after the <div> block that the blank line 15 ends.
	if want := []string{"2-3", "6-6", "16-16"}; !reflect.DeepEqual(heads, want) {
		t.Errorf("heading lines = %q, want %q", heads, want)
	}
	if len(d.Code) != 2 || !d.Code[0].Fenced || d.Code[0].Start != 7 || d.Code[0].End != 9 || d.Code[1].Fenced {
		t.Errorf("code blocks = %+v", d.Code)
	}
	if len(d.HTML) != 2 || !d.HTML[0].Comment || d.HTML[0].Start != 12 || d.HTML[0].End != 13 || d.HTML[1].Comment || d.HTML[1].Start != 14 {
		t.Errorf("html blocks = %+v", d.HTML)
	}
	if len(d.Comments) != 2 || d.Comments[0].Text != "one\ntwo" || d.Comments[0].StartLine != 12 || d.Comments[0].EndLine != 13 || d.Comments[1].StartLine != 17 || d.Comments[1].EndLine != 18 {
		t.Errorf("comments = %+v", d.Comments)
	}
	if len(d.Tasks) != 1 || d.Tasks[0].Line != 17 || d.Tasks[0].CommentEnd != 18 || d.Tasks[0].Item != "A" {
		t.Errorf("tasks = %+v", d.Tasks)
	}
	secs := d.sections(1)
	if len(secs) != 1 || secs[0].First != 4 || secs[0].Last != 19 {
		t.Errorf("sections(1) = %+v, want the lines 4-19 under Title", secs)
	}
	if got := d.sections(2); len(got) != 2 || got[0].Last != 15 || got[1].First != 17 {
		t.Errorf("sections(2) = %+v", got)
	}
	if got := strings.Join(readMarkdown("a\r\nb\rc").Lines, "|"); got != "a|b|c" {
		t.Errorf("line endings: %q", got)
	}
}
