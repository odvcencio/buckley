package commands

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

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
			start, stop := d.lineSpan(sec.First, sec.Last)
			// Comment text is guidance; anything else that is not blank makes the
			// section fixed.
			s.Open = strings.TrimSpace(d.outsideComments(start, stop)) == ""
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
	return t
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

// stripComments removes the HTML comments from lines. A line that holds only
// comment text is dropped. With keepItems, the comments that start on a task
// item's line stay with it, through the line that closes them, so that only its
// check mark can change; independent guide comments are still removed.
func stripComments(lines []string, keepItems bool) []string {
	d := readMarkdown(strings.Join(lines, "\n"))
	attached := map[int]bool{} // comments (by start offset) that stay
	if keepItems {
		for _, t := range d.Tasks {
			for _, c := range d.Comments {
				if c.StartLine == t.Line {
					attached[c.Start] = true
				}
			}
		}
	}
	var out []string
	for i, line := range d.Lines {
		from := d.lineStart[i]
		to := from + len(line)
		var spans [][2]int
		for _, c := range d.Comments {
			if attached[c.Start] || c.Stop <= from || c.Start >= to {
				continue
			}
			spans = append(spans, [2]int{max(c.Start, from) - from, min(c.Stop, to) - from})
		}
		if len(spans) == 0 {
			out = append(out, line)
		} else if kept := removeSpans(line, spans); strings.TrimSpace(kept) != "" {
			out = append(out, kept)
		}
	}
	return out
}

// checkboxes returns the checkbox item lines of the section, as written.
func (s TemplateSection) checkboxes() []string {
	d := readMarkdown(strings.Join(s.Lines, "\n"))
	var out []string
	for _, t := range d.Tasks {
		out = append(out, d.Lines[t.Line])
	}
	return out
}

// pureChecklist reports a section whose lines, comments aside, are checkbox
// lines only: it has no place for text.
func (s TemplateSection) pureChecklist() bool {
	d := readMarkdown(strings.Join(stripComments(s.Lines, false), "\n"))
	isBox := map[int]bool{}
	for _, t := range d.Tasks {
		isBox[t.Line] = true
	}
	for i, line := range d.Lines {
		if !isBox[i] && strings.TrimSpace(line) != "" {
			return false
		}
	}
	return len(isBox) > 0
}

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

// tickLines sets the check mark of every item line from ticks, the items the
// author ticked: an item in ticks is written ticked, any other is written
// unticked, and the rest of the line stays byte for byte. cleared lists the
// items that the template had ticked and the author did not.
func tickLines(lines []string, ticks map[string]bool) (out []string, cleared []string) {
	d := readMarkdown(strings.Join(lines, "\n"))
	mark := []byte(d.text)
	for _, t := range d.Tasks {
		switch {
		case ticks[t.Item] && !t.Checked:
			mark[t.MarkAt] = 'x'
		case !ticks[t.Item] && t.Checked:
			mark[t.MarkAt] = ' '
			cleared = append(cleared, t.Item)
		}
	}
	return strings.Split(string(mark), "\n"), cleared
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
	addItems := func(lines []string) {
		for item := range itemIdentities(lines) {
			allItems[item] = true
		}
	}
	var preamble []string
	if t.Preamble != "" {
		preamble = strings.Split(t.Preamble, "\n")
		addItems(preamble)
	}
	for _, s := range t.Sections {
		addItems(s.Lines)
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
		pre, clearedPre := tickLines(preamble, checklistTicks)
		for _, item := range clearedPre {
			warn(false, "the template item %q is ticked in the template, cleared because the notes do not tick it", item)
		}
		b.WriteString(strings.Join(pre, "\n") + "\n\n")
	}
	for i, s := range t.Sections {
		b.WriteString(s.HeadingLine + "\n")
		own := itemIdentities(s.Lines)
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
		lines := s.Lines
		if received {
			lines = stripComments(lines, true)
		}
		if s.Open && received {
			lines = nil // an open section holds only comments: the text takes their place
		}
		allTicks := map[string]bool{}
		for item := range checklistTicks {
			allTicks[item] = true
		}
		for item := range ticks {
			allTicks[item] = true
		}
		section, cleared := tickLines(lines, allTicks)
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

// itemIdentities returns the identities of the task items of lines.
func itemIdentities(lines []string) map[string]bool {
	out := map[string]bool{}
	for _, t := range readMarkdown(strings.Join(lines, "\n")).Tasks {
		out[t.Item] = true
	}
	return out
}
