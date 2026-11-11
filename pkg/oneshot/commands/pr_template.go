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

var (
	checkboxRe   = regexp.MustCompile(`^\s*[-*+]\s+\[[ xX]\]`)
	sectionKeyRe = regexp.MustCompile(`[^a-z0-9]+`)
)

// sectionFieldPrefix starts the name of a tool argument that carries the text
// of one template section.
const sectionFieldPrefix = "section_"

// ParsePRTemplate splits a template at its Markdown headings. Headings inside
// a code block or an HTML comment are text. It returns nil for a blank
// template.
func ParsePRTemplate(text string) *PRTemplate {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	t := &PRTemplate{}
	var (
		preamble  []string
		cur       *TemplateSection
		guide     []string
		inFence   bool
		inComment bool
		fixed     bool // the current section has a checkbox or fixed text
	)
	flush := func() {
		if cur == nil {
			return
		}
		cur.Guide = strings.TrimSpace(strings.Join(guide, "\n"))
		cur.Open = !fixed
		t.Sections = append(t.Sections, *cur)
		cur, guide, fixed = nil, nil, false
	}
	keys := map[string]int{}
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if !inComment && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")) {
			inFence = !inFence
		}
		if !inFence && !inComment {
			if m := atxHeadingRe.FindStringSubmatch(line); m != nil {
				flush()
				cur = &TemplateSection{Level: len(m[1]), Heading: strings.TrimSpace(m[2]), HeadingLine: line}
				key := strings.Trim(sectionKeyRe.ReplaceAllString(strings.ToLower(cur.Heading), "_"), "_")
				if len(key) > 40 {
					key = strings.TrimRight(key[:40], "_")
				}
				if key == "" {
					key = "section"
				}
				if keys[key]++; keys[key] > 1 {
					key += "_" + string(rune('0'+keys[key]))
				}
				cur.Key = key
				continue
			}
		}
		// Classify the line: comment text is guidance, anything else that is
		// not blank makes the section fixed. Comments are followed in the
		// preamble too, so that a heading inside one stays text.
		var lineGuide []string
		lineFixed := false
		rest := line
		for rest != "" {
			if inComment {
				end := strings.Index(rest, "-->")
				if end < 0 {
					lineGuide = append(lineGuide, strings.TrimSpace(rest))
					break
				}
				lineGuide = append(lineGuide, strings.TrimSpace(rest[:end]))
				rest, inComment = rest[end+3:], false
				continue
			}
			start := strings.Index(rest, "<!--")
			if inFence || start < 0 {
				if strings.TrimSpace(rest) != "" {
					lineFixed = true
				}
				break
			}
			if strings.TrimSpace(rest[:start]) != "" {
				lineFixed = true
			}
			rest, inComment = rest[start+4:], true
		}
		if cur == nil {
			preamble = append(preamble, line)
			continue
		}
		cur.Lines = append(cur.Lines, line)
		guide = append(guide, lineGuide...)
		fixed = fixed || lineFixed
	}
	flush()
	t.Preamble = trimBlankEdges(preamble)
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
	if headingKey(s.Heading) == headingKey(a.Heading) {
		return true
	}
	kind := sectionKind(s.Heading)
	return kind != "" && kind == a.Kind
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

var checkboxItemRe = regexp.MustCompile(`^\s*[-*+]\s+\[([ xX])\]\s*(.*)$`)

// stripInlineComments removes the HTML comments of one line; a comment that
// the line does not close removes the rest.
func stripInlineComments(line string) string {
	var out strings.Builder
	rest := line
	for {
		start := strings.Index(rest, "<!--")
		if start < 0 {
			out.WriteString(rest)
			return out.String()
		}
		out.WriteString(rest[:start])
		end := strings.Index(rest[start+4:], "-->")
		if end < 0 {
			return out.String()
		}
		rest = rest[start+4+end+3:]
	}
}

// checkboxItem reads a checkbox line: whether it is ticked and its item
// identity: the text with inline HTML comments removed and white space
// collapsed. The inventory of a template and the rendering use this one
// identity. ok is false for any other line.
func checkboxItem(line string) (ticked bool, item string, ok bool) {
	m := checkboxItemRe.FindStringSubmatch(stripInlineComments(line))
	if m == nil {
		return false, "", false
	}
	return m[1] != " ", strings.Join(strings.Fields(m[2]), " "), true
}

// commentOpenAfter reports whether an HTML comment is still open after line.
func commentOpenAfter(line string, open bool) bool {
	rest := line
	for rest != "" {
		if open {
			end := strings.Index(rest, "-->")
			if end < 0 {
				return true
			}
			rest, open = rest[end+3:], false
			continue
		}
		start := strings.Index(rest, "<!--")
		if start < 0 {
			return false
		}
		rest, open = rest[start+4:], true
	}
	return open
}

// itemLines marks the lines of a template that are checkbox items: checkbox
// lines outside code fences and outside HTML comments. A checkbox-shaped line
// in a fence or a comment is fixed text.
func itemLines(lines []string) []bool {
	flags := make([]bool, len(lines))
	var (
		fence   fenceTracker
		comment bool
	)
	for i, line := range lines {
		if !comment && (fence.line(line) || fence.inFence()) {
			continue
		}
		if !comment && checkboxRe.MatchString(line) {
			flags[i] = true
		}
		comment = commentOpenAfter(line, comment)
	}
	return flags
}

// splitCheckboxes separates the checkbox lines of text (outside code fences)
// from the other lines.
func splitCheckboxes(text string) (others string, boxes []string) {
	var (
		kept  []string
		fence fenceTracker
	)
	for _, line := range strings.Split(text, "\n") {
		if fence.line(line) || fence.inFence() {
			kept = append(kept, line)
			continue
		}
		if _, _, ok := checkboxItem(line); ok {
			boxes = append(boxes, line)
			continue
		}
		kept = append(kept, line)
	}
	return trimBlankEdges(kept), boxes
}

// withoutComments removes the HTML comments from lines. A line that holds only
// comment text is dropped; comments inside a code fence are text.
func withoutComments(lines []string) []string {
	var (
		out       []string
		inComment bool
		fence     fenceTracker
	)
	for _, line := range lines {
		if !inComment && (fence.line(line) || fence.inFence()) {
			out = append(out, line)
			continue
		}
		rest, hadComment := line, false
		var kept strings.Builder
		for rest != "" {
			if inComment {
				end := strings.Index(rest, "-->")
				if end < 0 {
					rest, hadComment = "", true
					break
				}
				rest, inComment, hadComment = rest[end+3:], false, true
				continue
			}
			start := strings.Index(rest, "<!--")
			if start < 0 {
				kept.WriteString(rest)
				break
			}
			kept.WriteString(rest[:start])
			rest, inComment, hadComment = rest[start+4:], true, true
		}
		if text := kept.String(); !hadComment || strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	return out
}

// checkboxes returns the checkbox item lines of the section, as written.
func (s TemplateSection) checkboxes() []string {
	var out []string
	for i, on := range itemLines(s.Lines) {
		if on {
			out = append(out, s.Lines[i])
		}
	}
	return out
}

// pureChecklist reports a section whose lines, comments aside, are checkbox
// lines only: it has no place for text.
func (s TemplateSection) pureChecklist() bool {
	lines := withoutComments(s.Lines)
	boxes := 0
	for i, on := range itemLines(lines) {
		switch {
		case on:
			boxes++
		case strings.TrimSpace(lines[i]) != "":
			return false
		}
	}
	return boxes > 0
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

// demoteHeadings shifts the Markdown headings of text (outside code fences)
// so that its highest heading has level top, keeping the relative nesting. A
// heading that would pass level 6 becomes a bold line.
func demoteHeadings(text string, top int) string {
	lines := strings.Split(text, "\n")
	var fence fenceTracker
	highest := 7
	for _, line := range lines {
		if fence.line(line) || fence.inFence() {
			continue
		}
		if m := atxHeadingRe.FindStringSubmatch(line); m != nil {
			highest = min(highest, len(m[1]))
		}
	}
	if highest == 7 {
		return text
	}
	fence = fenceTracker{}
	for i, line := range lines {
		if fence.line(line) || fence.inFence() {
			continue
		}
		if m := atxHeadingRe.FindStringSubmatch(line); m != nil {
			level := len(m[1]) + top - highest
			if level > 6 {
				lines[i] = "**" + strings.TrimSpace(m[2]) + "**"
			} else {
				lines[i] = strings.Repeat("#", level) + " " + strings.TrimSpace(m[2])
			}
		}
	}
	return strings.Join(lines, "\n")
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

// tickLines ticks the unticked item lines whose identity is in ticks. The
// other lines stay as they are.
func tickLines(lines []string, ticks map[string]bool) []string {
	out := make([]string, len(lines))
	copy(out, lines)
	for i, on := range itemLines(lines) {
		if !on {
			continue
		}
		if ticked, item, ok := checkboxItem(lines[i]); ok && !ticked && ticks[item] {
			out[i] = strings.Replace(lines[i], "[ ]", "[x]", 1)
		}
	}
	return out
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
		for i, on := range itemLines(lines) {
			if on {
				if _, item, ok := checkboxItem(lines[i]); ok {
					allItems[item] = true
				}
			}
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
		if a.Kind != SectionChecklist {
			continue
		}
		others, boxes := splitCheckboxes(a.Body)
		for _, line := range boxes {
			ticked, item, _ := checkboxItem(line)
			if !allItems[item] {
				warn(true, "the checkbox line %q in the notes section %q matches no item of the template; it is not written", strings.TrimSpace(line), a.Heading)
			} else if ticked {
				checklistTicks[item] = true
			}
		}
		if !taken[j] && strings.TrimSpace(others) != "" {
			checklistBlocks = append(checklistBlocks, extraBlock{Heading: a.Heading, Body: others})
		}
	}

	for j, a := range author {
		if !taken[j] && a.Kind != SectionChecklist {
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
		b.WriteString(strings.Join(tickLines(preamble, checklistTicks), "\n") + "\n\n")
	}
	for i, s := range t.Sections {
		b.WriteString(s.HeadingLine + "\n")
		own := itemIdentities(s.Lines)
		ticks := map[string]bool{}
		var parts []string
		for _, a := range supplied[i] {
			text := a.Body
			switch {
			case a.Kind == SectionChecklist:
				text, _ = splitCheckboxes(a.Body) // its boxes were read above
			case len(own) > 0:
				// The template's checklist stands as the template wrote it. The
				// author's boxes only tick; the author's other lines follow.
				var boxes []string
				text, boxes = splitCheckboxes(a.Body)
				for _, line := range boxes {
					ticked, item, _ := checkboxItem(line)
					switch {
					case !own[item]:
						warn(true, "the checkbox line %q in the notes section %q matches no item of the template section %q; it is not written", strings.TrimSpace(line), a.Heading, s.Heading)
					case ticked:
						ticks[item] = true
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
			var dropped []string
			text, dropped = splitCheckboxes(model) // the model never adds a checkbox
			res.ModelBoxes += len(dropped)
		}
		if text != "" {
			text = demoteHeadings(text, s.Level+1)
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
			lines = withoutComments(lines)
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
		section := tickLines(lines, allTicks)
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

// itemIdentities returns the identities of the item lines of a section.
func itemIdentities(lines []string) map[string]bool {
	out := map[string]bool{}
	for i, on := range itemLines(lines) {
		if on {
			if _, item, ok := checkboxItem(lines[i]); ok {
				out[item] = true
			}
		}
	}
	return out
}
