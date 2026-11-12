package commands

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/tools"
)

// PRTemplate is a repository's pull request template, split at its headings.
// `buckley pr` writes the body in this shape: the headings, checkboxes and
// fixed text stay as they are, and text goes only where the template leaves a
// place for it.
type PRTemplate struct {
	// Preamble is the text before the first heading.
	Preamble string
	Sections []TemplateSection

	// rawUnclosed and rawHeadings count the raw HTML in the template that a
	// renderer acts on (see mdHazard): comment openers with no closer, and heading
	// elements at or above the template's deepest level. The body may have no more
	// of them than the template.
	rawUnclosed, rawHeadings int

	// Source names where the template came from (a path), for messages.
	Source string

	preFrag  *mdFrag     // the lines before the first heading, from the whole-document parse
	problems []string    // why the reading of the template cannot be trusted (see mdDoc.Problems)
	foreign  []mdForeign // Markdown++-only constructs in the template
}

// Unreadable returns why Markdown++ could not read the template exactly (a parse
// error, a recovery diagnostic, a source range that cannot be used), or "".
// Generation stops on such a template: its shape cannot be certified.
func (t *PRTemplate) Unreadable() string {
	if t == nil {
		return ""
	}
	return strings.Join(t.problems, "; ")
}

// TemplateSection is one heading of a template and the lines under it.
type TemplateSection struct {
	Level   int
	Heading string
	// HeadingLine is the heading as the template writes it, markers included.
	HeadingLine string
	Lines       []string // the lines under the heading, as written
	// Guide is the text of the HTML comments in the section. A template uses
	// them to tell the author what to write.
	Guide string
	// Open reports a section that asks for text: it has only comments and
	// blank lines under its heading, no checkbox and no fixed text.
	Open bool
	// Key names the section in the model's tool call ("section_<Key>").
	Key string

	frag *mdFrag // Lines with the items and comments of the whole-document parse
}

// fragment returns the section's lines with their inventory. A section built by
// hand, not by ParsePRTemplate, is parsed here as a document of its own.
func (s TemplateSection) fragment() *mdFrag {
	if s.frag != nil {
		return s.frag
	}
	if len(s.Lines) == 0 {
		return &mdFrag{}
	}
	return readMarkdown(strings.Join(s.Lines, "\n")).fragment(0, len(s.Lines)-1)
}

var sectionKeyRe = regexp.MustCompile(`[^a-z0-9]+`)

// sectionFieldPrefix starts the name of a tool argument that carries the text
// of one template section.
const sectionFieldPrefix = "section_"

// ParsePRTemplate splits a template at its top-level headings, as a Markdown
// parser reads them (mdshape.go). A heading inside a code block, an HTML block
// or a comment is text. It returns nil for a blank template.
func ParsePRTemplate(text string) *PRTemplate {
	text = normalizeNewlines(text)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	d := readMarkdown(strings.TrimRight(text, "\n"))
	t := &PRTemplate{}
	tops := d.topHeadings()
	pre := d.Lines
	if len(tops) > 0 {
		pre = d.Lines[:tops[0].Start]
	}
	t.Preamble = trimBlankEdges(pre)
	t.preFrag = d.fragment(0, len(pre)-1)
	t.problems, t.foreign = d.Problems, d.Foreign
	for _, line := range d.Underlines {
		t.problems = append(t.problems, underlineProblem(line))
	}
	keys := map[string]int{}
	for _, sec := range d.sections(0) {
		h := sec.Heading
		s := TemplateSection{
			Level:       h.Level,
			Heading:     h.Text,
			HeadingLine: strings.Join(d.Lines[h.Start:h.End+1], "\n"),
		}
		if sec.Last >= sec.First {
			s.Lines = append([]string(nil), d.Lines[sec.First:sec.Last+1]...)
			s.frag = d.fragment(sec.First, sec.Last)
			start, stop := d.lineSpan(sec.First, sec.Last)
			// Comment text is guidance; anything else that is not blank makes the
			// section fixed.
			s.Open = blankText(d.outsideComments(start, stop))
			var guide []string
			for _, c := range d.Comments {
				if c.Start >= start && c.Stop <= stop {
					for _, l := range strings.Split(c.Text, "\n") {
						if l = strings.TrimSpace(l); l != "" {
							guide = append(guide, l)
						}
					}
				}
			}
			s.Guide = strings.Join(guide, "\n")
		} else {
			s.Open = true
		}
		key := strings.Trim(sectionKeyRe.ReplaceAllString(strings.ToLower(s.Heading), "_"), "_")
		if len(key) > 40 {
			key = strings.TrimRight(key[:40], "_")
		}
		if key == "" {
			key = "section"
		}
		if keys[key]++; keys[key] > 1 {
			key += "_" + string(rune('0'+keys[key]))
		}
		s.Key = key
		t.Sections = append(t.Sections, s)
	}
	t.rawUnclosed, t.rawHeadings = t.countRawHTML(d)
	return t
}

// underlineProblem says why the line (0-based) of a text cannot be certified.
func underlineProblem(line int) string {
	return fmt.Sprintf("line %d has the form of a setext underline under a paragraph line, but Markdown++ does not read a heading there, and GitHub may", line+1)
}

// countRawHTML counts the hazards of d: comment openers with no closer, and raw
// heading elements at or above the template's deepest level.
func (t *PRTemplate) countRawHTML(d *mdDoc) (unclosed, headings int) {
	deepest := t.deepestLevel()
	for _, h := range d.Hazards {
		switch {
		case h.Unclosed:
			unclosed++
		case h.Level <= deepest:
			headings++
		}
	}
	return unclosed, headings
}

// finalBodyWarnings checks the complete rendered body, after Buckley's own
// sections and the template are combined, in every layout. Everything here is
// blocking: it reads the body as Markdown++ and as raw HTML.
//
//   - A reading problem of the parser (a parse error, a recovery diagnostic, a
//     source range that cannot be used) means the structure cannot be certified.
//
//   - A Markdown++-only construct is read differently by GitHub.
//
//   - Raw HTML that a renderer acts on although the Markdown tree does not show
//     it: a comment opener with no closer hides the text after it, and a raw
//     heading element at or above the template's level is a heading the heading
//     check does not list.
//
//   - A line with the form of a setext underline under a paragraph line that the
//     parser does not read as a heading: Markdown++ 0.5.0 misses a multi-line
//     setext heading after a leading HTML block or thematic break, GitHub does
//     not, so no reading of the parser can certify the headings there.
//
//   - The checkbox lines must be the template's, word for word and in order, each
//     under the template's heading (checkboxWarning); only the check mark may
//     differ, and the lines of the author's own notes (author) may be added.
//
// The body may have no more constructs of a kind than the template has.
func (t *PRTemplate) finalBodyWarnings(body string, author []AuthorSection) []BodyWarning {
	d := readMarkdown(body)
	var out []BodyWarning
	block := func(format string, args ...any) {
		out = append(out, BodyWarning{Text: fmt.Sprintf(format, args...) + "; the body is out of the template's shape", Blocking: true})
	}
	if len(d.Problems) > 0 {
		block("Markdown++ could not read the body exactly (%s)", d.Problems[0])
	}
	if len(d.Underlines) > 0 {
		block("%s", underlineProblem(d.Underlines[0]))
	}
	if w := t.checkboxWarning(body, author, t.placeable()); w != "" {
		block("%s", w)
	}
	have := map[string]int{}
	for _, f := range t.foreign {
		have[f.Name]++
	}
	for _, f := range d.Foreign {
		if have[f.Name]--; have[f.Name] < 0 {
			block("the body has %s at line %d; Markdown++ reads this differently from GitHub", f.Name, f.Line+1)
			break
		}
	}
	deepest := t.deepestLevel()
	unclosed, headings := t.countRawHTML(d)
	for _, h := range d.Hazards {
		switch {
		case h.Unclosed && unclosed > t.rawUnclosed:
			block("the body has an HTML comment opener with no closer at line %d, which hides the text after it", h.Line+1)
		case !h.Unclosed && h.Level <= deepest && headings > t.rawHeadings:
			block("the body has a raw <h%d> heading element at line %d, which is a heading at or above the template's level", h.Level, h.Line+1)
		default:
			continue
		}
		break
	}
	return out
}

// authorHTMLWarnings names raw HTML in a carried author section that would hide
// or forge structure. The author's text is not rewritten.
func (t *PRTemplate) authorHTMLWarnings(s AuthorSection) []BodyWarning {
	var out []BodyWarning
	deepest := t.deepestLevel()
	d := readMarkdown(s.Body())
	for _, line := range d.Underlines {
		out = append(out, BodyWarning{Text: fmt.Sprintf("the notes section %q: %s", s.Heading(), underlineProblem(line)), Blocking: true})
	}
	for _, h := range d.Hazards {
		switch {
		case h.Unclosed:
			out = append(out, BodyWarning{Text: fmt.Sprintf("the notes section %q has an HTML comment opener with no closer at its line %d; it would hide the text after it", s.Heading(), h.Line+1), Blocking: true})
		case h.Level <= deepest:
			out = append(out, BodyWarning{Text: fmt.Sprintf("the notes section %q has a raw <h%d> heading element at its line %d; it would add a heading at or above the template's level", s.Heading(), h.Level, h.Line+1), Blocking: true})
		}
	}
	return out
}

// headingKey reduces a heading to its words, so that "What changed" in a
// notes file matches "### What changed:" in a template.
func headingKey(heading string) string {
	return strings.Join(wordRe.FindAllString(strings.ToLower(heading), -1), " ")
}

// takes reports whether the template section is the place for the author
// section: the headings are the same, or the template heading names the
// section's kind ("Testing" takes an author's "Test plan").
func (s TemplateSection) takes(a AuthorSection) bool {
	if headingKey(s.Heading) == headingKey(a.Heading()) {
		return true
	}
	kind := sectionKind(s.Heading)
	return kind != "" && kind == a.Kind()
}

// modelSections returns the open sections that no author section fills. The
// model writes these and nothing else of the body.
func (t *PRTemplate) modelSections(author []AuthorSection) []TemplateSection {
	if t == nil {
		return nil
	}
	var out []TemplateSection
next:
	for _, s := range t.Sections {
		if !s.Open {
			continue
		}
		for _, a := range author {
			if s.takes(a) {
				continue next
			}
		}
		out = append(out, s)
	}
	return out
}

// hasKind reports whether a heading of the template names the kind, as
// "Not verified" names SectionNotVerified.
func (t *PRTemplate) hasKind(kind string) bool {
	if t == nil {
		return false
	}
	for _, s := range t.Sections {
		if sectionKind(s.Heading) == kind {
			return true
		}
	}
	return false
}

// hasOpenSection reports whether the template leaves a place for text. A
// template that is only a checklist does not: Buckley's own sections then
// come first and the template follows unchanged.
func (t *PRTemplate) hasOpenSection() bool {
	if t == nil {
		return false
	}
	for _, s := range t.Sections {
		if s.Open {
			return true
		}
	}
	return false
}

// sectionProperties adds one string argument per section the model writes and
// returns the argument names in template order.
func sectionProperties(props map[string]tools.Property, sections []TemplateSection) []string {
	var names []string
	for _, s := range sections {
		desc := "Text for the template section \"" + s.Heading + "\". Markdown for the body of the section only: no heading and no checkboxes."
		if s.Guide != "" {
			desc += " The template says: " + squeezeSpaces(s.Guide)
		}
		name := sectionFieldPrefix + s.Key
		props[name] = tools.StringProperty(desc)
		names = append(names, name)
	}
	return names
}

// sectionTexts reads the "section_<key>" arguments of a tool call. A list is
// accepted for a string and becomes bullets.
func sectionTexts(raw json.RawMessage) map[string]string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	var out map[string]string
	for name, value := range fields {
		key, ok := strings.CutPrefix(name, sectionFieldPrefix)
		if !ok {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) != nil {
			var list StringList
			if json.Unmarshal(value, &list) != nil {
				continue
			}
			text = prBullets(list, trimBulletMarker)
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[key] = text
	}
	return out
}

// Kinds of text that Buckley adds to a template that has no place for it.
const (
	kindBreaking = "breaking"
	kindIssues   = "issues"
)

// kindKeywords start the words that make a template section the place for a
// block of the kind (case-insensitive, word prefixes). The first template
// section whose heading, guide comments or checkbox text has one of them
// takes the block as a subsection.
var kindKeywords = map[string][]string{
	SectionTesting:     {"test", "validat", "verif", "correct"},
	SectionEvidence:    {"test", "validat", "verif", "correct"},
	SectionCounters:    {"counter", "perf", "benchmark"},
	SectionNotVerified: {"unvalidated", "not verified", "unverified", "unsure", "risk", "limit", "gap"},
	SectionKnownGaps:   {"unvalidated", "not verified", "unverified", "unsure", "risk", "limit", "gap"},
	kindBreaking:       {"break", "compat"},
	kindIssues:         {"issue", "relat", "link"},
}

// BodyWarning is something the author should know about the body. Blocking
// warnings are errors under --strict.
type BodyWarning struct {
	Text     string
	Blocking bool
}

// extraBlock is text that no template section takes: an author section of the
// notes, or a statement that Buckley writes. The template's shape stays as it
// is, so the block becomes a subsection of one template section.
type extraBlock struct {
	Kind    string
	Heading string
	Body    string
}

// mdBox is a checkbox line that splitCheckboxes took out of a text.
type mdBox struct {
	Line    string // the line as written
	Checked bool
	Item    string // identity: the text with comments removed and white space collapsed
}

// splitCheckboxes separates the task-list item lines of text, as the parser
// reads them (so not those in code blocks, HTML blocks or comments), from the
// other lines.
func splitCheckboxes(text string) (others string, boxes []mdBox) {
	d := readMarkdown(text)
	isBox := map[int]mdTask{}
	for _, t := range d.Tasks {
		isBox[t.Line] = t
	}
	var kept []string
	for i, line := range d.Lines {
		if t, ok := isBox[i]; ok {
			boxes = append(boxes, mdBox{Line: line, Checked: t.Checked, Item: t.Item})
			continue
		}
		kept = append(kept, line)
	}
	return trimBlankEdges(kept), boxes
}

// checkboxes returns the checkbox item lines of the section, as written.
func (s TemplateSection) checkboxes() []string { return s.fragment().taskLines() }

// pureChecklist reports a section whose lines, comments aside, are checkbox
// lines only: it has no place for text.
func (s TemplateSection) pureChecklist() bool { return s.fragment().pure() }

// placeable reports whether some section of the template is not a pure
// checklist, so that a block can be written at its end.
func (t *PRTemplate) placeable() bool {
	if t == nil {
		return false
	}
	for _, s := range t.Sections {
		if !s.pureChecklist() {
			return true
		}
	}
	return false
}

// deepestLevel is the level of the deepest heading of the template.
func (t *PRTemplate) deepestLevel() int {
	level := 1
	for _, s := range t.Sections {
		level = max(level, s.Level)
	}
	return level
}

// demoteHeadings shifts the top-level headings of text, as the parser reads
// them (so none in a code block, an HTML block or a comment), so that the
// highest has level top, keeping the relative nesting. An ATX heading changes
// its markers in place; a setext heading (a text over a line of "=" or "-")
// becomes one ATX line. A heading that would pass level 6 becomes a bold line.
func demoteHeadings(text string, top int) string {
	d := readMarkdown(text)
	var found []mdHeading
	highest := 7
	for _, h := range d.topHeadings() {
		found = append(found, h)
		highest = min(highest, h.Level)
	}
	if len(found) == 0 {
		return d.text
	}
	var out []string
	next := 0
	for i := 0; i < len(d.Lines); i++ {
		if next >= len(found) || found[next].Start != i {
			out = append(out, d.Lines[i])
			continue
		}
		h := found[next]
		next++
		level := h.Level + top - highest
		switch {
		case level > 6:
			if h.Text != "" {
				out = append(out, "**"+h.Text+"**")
			}
		case h.Setext:
			out = append(out, strings.Repeat("#", level)+" "+h.Text)
		default:
			line := d.Lines[i]
			indent := len(line) - len(strings.TrimLeft(line, " "))
			marks := len(line[indent:]) - len(strings.TrimLeft(line[indent:], "#"))
			out = append(out, line[:indent]+strings.Repeat("#", level)+line[indent+marks:])
		}
		i = h.End
	}
	return strings.Join(out, "\n")
}

// renderBlock writes a block as a subsection one level below the deepest
// heading of the template. At level 6 the label is bold text, because a
// Markdown heading cannot go deeper; headings inside the block move below it.
func (t *PRTemplate) renderBlock(block extraBlock) string {
	level := t.deepestLevel() + 1
	body := demoteHeadings(strings.Trim(block.Body, "\n"), level+1)
	if level > 6 {
		return "**" + block.Heading + "**\n\n" + body
	}
	return strings.Repeat("#", level) + " " + block.Heading + "\n\n" + body
}

// hasWordPrefix reports whether text has a word that starts with the keyword
// (a keyword of several words must start at a word and follow word by word).
func hasWordPrefix(text, keyword string) bool {
	words := " " + strings.Join(wordRe.FindAllString(strings.ToLower(text), -1), " ")
	return strings.Contains(words, " "+keyword)
}

// placeFor returns the index of the template section that takes a block of
// the kind: the first whose heading, guide comments or checkbox text has a
// keyword of the kind; else the last section that is not a pure checklist.
// It is -1 when every section is a pure checklist.
func (t *PRTemplate) placeFor(kind string) int {
	for i, s := range t.Sections {
		haystack := s.Heading + "\n" + s.Guide + "\n" + strings.Join(s.checkboxes(), "\n")
		for _, keyword := range kindKeywords[kind] {
			if hasWordPrefix(haystack, keyword) {
				return i
			}
		}
	}
	for i := len(t.Sections) - 1; i >= 0; i-- {
		if !t.Sections[i].pureChecklist() {
			return i
		}
	}
	return -1
}

// templateResult is what templateBody returns.
type templateResult struct {
	Body       string
	Left       []AuthorSection // author sections that nothing placed (see templateBody)
	Warnings   []BodyWarning
	ModelBoxes int // checkbox lines of the model's text that were dropped
}

// headingRef is a heading of a body or a template at some level.
type headingRef struct {
	Level int
	Key   string
}

// bodyHeadings reads the top-level headings of text, as the parser reads them.
func bodyHeadings(text string) []headingRef {
	var out []headingRef
	for _, h := range readMarkdown(text).topHeadings() {
		out = append(out, headingRef{h.Level, headingKey(h.Text)})
	}
	return out
}

// shapeWarning compares the headings of the rendered body that sit at the
// template's levels with the template's headings, in order. It returns a
// sentence naming the extra or missing heading, or "" when they agree.
func (t *PRTemplate) shapeWarning(body string) string {
	deepest := t.deepestLevel()
	var want, got []headingRef
	for _, s := range t.Sections {
		want = append(want, headingRef{s.Level, headingKey(s.Heading)})
	}
	for _, h := range bodyHeadings(body) {
		if h.Level <= deepest {
			got = append(got, h)
		}
	}
	for i := 0; i < len(want) || i < len(got); i++ {
		switch {
		case i >= len(got):
			return fmt.Sprintf("the body lacks the template heading %q", want[i].Key)
		case i >= len(want):
			return fmt.Sprintf("the body has the heading %q, which the template does not have", got[i].Key)
		case want[i] != got[i]:
			return fmt.Sprintf("the body has the heading %q where the template has %q", got[i].Key, want[i].Key)
		}
	}
	return ""
}

// boxRecord is a checkbox line of a text and where it stands.
type boxRecord struct {
	Line    string // the line as written, its check mark replaced by a space, trailing white space out
	Section int    // -1 before the first heading; else the index among the headings of the template's levels
	Where   string // the words of that heading, for messages
	// First and Last say that the line is the first or the last non-blank line of
	// the text. Only a body's records set them: the body is trimmed, so these two
	// lines lose their leading and their trailing white space.
	First, Last bool
}

// boxLine masks the check mark of a checkbox line: the one character in the
// brackets becomes a space. Nothing else changes: the list marker, the
// indentation, the text and any trailing spaces stay, because they are part of
// the line (two trailing spaces are a hard break).
func boxLine(line string, markCol int) string {
	if markCol >= 0 && markCol < len(line) {
		line = line[:markCol] + " " + line[markCol+1:]
	}
	return line
}

// edgeLines returns the index of the first and of the last line that is not
// blank for strings.TrimSpace (which trims the body), or -1.
func edgeLines(lines []string) (first, last int) {
	first, last = -1, -1
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	return first, last
}

// boxRecords lists the checkbox lines of the run, as the whole-document parse
// found them, with their white space as written. The template's records are
// never trimmed: whether an edge of the final body trims a line depends on where
// the line ends up, and only the body's records know that (First, Last).
func (f *mdFrag) boxRecords(section int, where string) []boxRecord {
	lines := f.lines()
	starts := make([]int, len(lines))
	off := 0
	for i, l := range lines {
		starts[i] = off
		off += len(l) + 1
	}
	var out []boxRecord
	for _, t := range f.tasks {
		out = append(out, boxRecord{Line: boxLine(lines[t.Line], t.MarkAt-starts[t.Line]), Section: section, Where: where})
	}
	return out
}

// boxRecords lists the checkbox lines of the template: the text before the first
// heading, then each section in order.
func (t *PRTemplate) boxRecords() []boxRecord {
	var out []boxRecord
	if t.preFrag != nil {
		out = append(out, t.preFrag.boxRecords(-1, "the text before the first heading")...)
	}
	for i, s := range t.Sections {
		out = append(out, s.fragment().boxRecords(i, s.Heading)...)
	}
	return out
}

// boxRecords lists the checkbox lines of a text. Each stands in the section of
// the last top-level heading at or above level deepest before it.
func (d *mdDoc) boxRecords(deepest int) []boxRecord {
	var heads []mdHeading
	for _, h := range d.topHeadings() {
		if h.Level <= deepest {
			heads = append(heads, h)
		}
	}
	first, last := edgeLines(d.Lines)
	var out []boxRecord
	for _, t := range d.Tasks {
		r := boxRecord{Line: boxLine(d.Lines[t.Line], t.MarkAt-d.lineStart[t.Line]), Section: -1, Where: "the text before the first heading", First: t.Line == first, Last: t.Line == last}
		for i, h := range heads {
			if h.Start < t.Line {
				r.Section, r.Where = i, h.Text
			}
		}
		out = append(out, r)
	}
	return out
}

// checkboxWarning compares the checkbox lines of the body with the template's:
// the same lines, word for word, in the same order, each under the same heading
// (when bySection is set); only the check mark may differ. The checkbox lines of
// the author's own notes may be added anywhere. It returns a sentence naming the
// first difference, or "" when they agree.
//
// A line the template hides in a comment or an HTML block is not a checkbox of
// the template, and the same line in the body, where the comment is gone, is one:
// that shows here as a line the template does not have.
func (t *PRTemplate) checkboxWarning(body string, author []AuthorSection, bySection bool) string {
	d := readMarkdown(body)
	if len(d.Problems) > 0 {
		return "" // reported as a reading problem; the task list cannot be trusted
	}
	want, got := t.boxRecords(), d.boxRecords(t.deepestLevel())
	owned := map[string]int{}
	for _, a := range author {
		for _, r := range readMarkdown(a.Body()).boxRecords(0) {
			owned[strings.TrimSpace(r.Line)]++
		}
	}
	// The body is trimmed, so a line at its edge may differ from the template's
	// line by that edge's white space, and only there.
	same := func(g, w boxRecord) bool {
		gl, wl := g.Line, w.Line
		if g.First {
			gl, wl = strings.TrimLeftFunc(gl, unicode.IsSpace), strings.TrimLeftFunc(wl, unicode.IsSpace)
		}
		if g.Last {
			gl, wl = strings.TrimRightFunc(gl, unicode.IsSpace), strings.TrimRightFunc(wl, unicode.IsSpace)
		}
		return gl == wl && (!bySection || g.Section == w.Section)
	}
	j := 0
	for i := 0; i < len(got); i++ {
		switch {
		case j < len(want) && same(got[i], want[j]):
			j++
		case owned[strings.TrimSpace(got[i].Line)] > 0:
			owned[strings.TrimSpace(got[i].Line)]--
		default:
			for _, later := range want[min(j+1, len(want)):] {
				if same(got[i], later) {
					return fmt.Sprintf("the body lacks the template checkbox line %q under %q", strings.TrimSpace(want[j].Line), want[j].Where)
				}
			}
			if j < len(want) {
				return fmt.Sprintf("the body has the checkbox line %q under %q where the template has %q under %q", strings.TrimSpace(got[i].Line), got[i].Where, strings.TrimSpace(want[j].Line), want[j].Where)
			}
			return fmt.Sprintf("the body has the checkbox line %q under %q, which the template does not have", strings.TrimSpace(got[i].Line), got[i].Where)
		}
	}
	if j < len(want) {
		return fmt.Sprintf("the body lacks the template checkbox line %q under %q", strings.TrimSpace(want[j].Line), want[j].Where)
	}
	return ""
}

// templateBody renders the body in the template's shape. The template's
// headings stay as they are, in order. Its checkbox lines and fixed text are
// written word for word; a box is ticked only when the notes tick the same
// item (in a section the template section takes, or in a section headed
// Checklist, which only ticks). The guide comments of a section that received
// text are left out. A block that no template section takes (see extras)
// becomes a subsection at the end of the section chosen by placeFor, with its
// own headings moved below the template's.
//
// When extras is nil, or the template is only checklists, the author sections
// that no template section took are returned in Left for the caller to place.
// The warnings name checkbox lines of the notes that match no template item
// and Checklist text that has no place.
func (pr PRResult) templateBody(t *PRTemplate, author []AuthorSection, notVerified string, extras func(leftover []AuthorSection) []extraBlock) templateResult {
	var res templateResult
	var b strings.Builder
	warn := func(blocking bool, format string, args ...any) {
		res.Warnings = append(res.Warnings, BodyWarning{Text: fmt.Sprintf(format, args...), Blocking: blocking})
	}
	taken := make([]bool, len(author))
	supplied := make([][]AuthorSection, len(t.Sections))
	for i, s := range t.Sections {
		for j, a := range author {
			if !taken[j] && s.takes(a) {
				taken[j] = true
				supplied[i] = append(supplied[i], a)
			}
		}
	}

	// One inventory of the template's items: the preamble and every section.
	// The rendering below reads the same identities.
	allItems := map[string]bool{}
	addItems := func(f *mdFrag) {
		for item := range f.identities() {
			allItems[item] = true
		}
	}
	if t.preFrag != nil {
		addItems(t.preFrag)
	}
	for _, s := range t.Sections {
		addItems(s.fragment())
	}

	// A Checklist section of the notes only ticks. Its other text is placed
	// like a section of no known kind, or named in a warning.
	checklistTicks := map[string]bool{}
	var checklistBlocks []extraBlock
	for j, a := range author {
		if a.Kind() != SectionChecklist {
			continue
		}
		others, boxes := splitCheckboxes(a.Body())
		for _, box := range boxes {
			if !allItems[box.Item] {
				warn(true, "the checkbox line %q in the notes section %q matches no item of the template; it is not written", strings.TrimSpace(box.Line), a.Heading())
			} else if box.Checked {
				checklistTicks[box.Item] = true
			}
		}
		if !taken[j] && strings.TrimSpace(others) != "" {
			checklistBlocks = append(checklistBlocks, extraBlock{Heading: a.Heading(), Body: others})
		}
	}

	for j, a := range author {
		if !taken[j] && a.Kind() != SectionChecklist {
			res.Left = append(res.Left, a)
		}
	}
	var blocks [][]extraBlock
	if extras != nil && t.placeable() {
		blocks = make([][]extraBlock, len(t.Sections))
		all := append(extras(res.Left), checklistBlocks...)
		for _, block := range all {
			if i := t.placeFor(block.Kind); i >= 0 {
				blocks[i] = append(blocks[i], block)
			}
		}
		res.Left = nil
	} else {
		for _, block := range checklistBlocks {
			warn(true, "the template has only checklists, so the text of the notes section %q has no place and is not written", block.Heading)
		}
	}

	// The model writes only the open sections that no author section fills.
	modelKeys := map[string]bool{}
	for _, s := range t.modelSections(author) {
		modelKeys[s.Key] = true
	}

	if t.Preamble != "" {
		pre, clearedPre := t.preFrag.render(checklistTicks, false)
		for _, item := range clearedPre {
			warn(false, "the template item %q is ticked in the template, cleared because the notes do not tick it", item)
		}
		b.WriteString(trimBlankEdges(pre) + "\n\n")
	}
	for i, s := range t.Sections {
		b.WriteString(s.HeadingLine + "\n")
		frag := s.fragment()
		own := frag.identities()
		ticks := map[string]bool{}
		var parts []string
		for _, a := range supplied[i] {
			text := a.Body()
			switch {
			case a.Kind() == SectionChecklist:
				text, _ = splitCheckboxes(a.Body()) // its boxes were read above
			case len(own) > 0:
				// The template's checklist stands as the template wrote it. The
				// author's boxes only tick; the author's other lines follow.
				var boxes []mdBox
				text, boxes = splitCheckboxes(a.Body())
				for _, box := range boxes {
					switch {
					case !own[box.Item]:
						warn(true, "the checkbox line %q in the notes section %q matches no item of the template section %q; it is not written", strings.TrimSpace(box.Line), a.Heading(), s.Heading)
					case box.Checked:
						ticks[box.Item] = true
					}
				}
			}
			if strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		text := strings.Join(parts, "\n\n")
		if text == "" && s.Open && len(supplied[i]) == 0 && modelKeys[s.Key] {
			model := commitmsg.NeutralizeCloseDirectives(strings.Trim(withoutCarriedSections(pr.Sections[s.Key], author), "\n"))
			var dropped []mdBox
			text, dropped = splitCheckboxes(model) // the model never adds a checkbox
			res.ModelBoxes += len(dropped)
		}
		if text != "" {
			text = demoteHeadings(text, t.deepestLevel()+1)
		}
		if notVerified != "" && sectionKind(s.Heading) == SectionNotVerified && len(supplied[i]) == 0 {
			// The template's own slot for what was not verified, and no author
			// section fills it: the slot says that the author gave nothing, also
			// when the model wrote text of its own. That text follows the
			// statement; it is the model's, not the author's.
			if strings.TrimSpace(text) != "" {
				text = notVerified + "\n\n" + text
			} else {
				text = notVerified
			}
		}

		var here []extraBlock
		if len(blocks) > 0 {
			here = blocks[i]
		}
		received := strings.TrimSpace(text) != "" || len(here) > 0
		allTicks := map[string]bool{}
		for item := range checklistTicks {
			allTicks[item] = true
		}
		for item := range ticks {
			allTicks[item] = true
		}
		section, cleared := frag.render(allTicks, received)
		if s.Open && received {
			section, cleared = nil, nil // an open section holds only comments: the text takes their place
		}
		for _, item := range cleared {
			warn(false, "the template item %q is ticked in the template, cleared because the notes do not tick it", item)
		}
		if !received {
			// Nothing to add: the section is the template's, with the ticks.
			b.WriteString(strings.Join(section, "\n") + "\n")
			continue
		}
		out := strings.TrimRight(strings.Join(section, "\n"), "\n")
		if strings.TrimSpace(text) != "" {
			out += "\n\n" + text
		}
		for _, block := range here {
			out += "\n\n" + t.renderBlock(block)
		}
		if s.Open {
			out = "\n" + strings.TrimLeft(out, "\n")
		}
		b.WriteString(out + "\n\n")
	}
	res.Body = strings.TrimSpace(b.String())
	return res
}
