package commands

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// RawPRNote is one author-supplied file from `buckley pr --context-file`, as
// read from disk. It may hold HTML comments. Nothing but NewPRNote reads it.
type RawPRNote struct {
	Path string
	Text string
}

// PRNote is a note whose comments are gone. NewPRNote is the only place that
// removes comments, and it removes them once: the prompt, the carried
// sections and the sources all read Text as it is and never scan it for
// comments again. A second removal could join characters into a new "<!--" and
// drop text that was outside the original comment.
//
// The text is private: only NewPRNote can set it, so a struct literal, a zero
// value or a copy built by hand is an empty note everywhere (sections, prompt,
// sources). Read the text with Text.
type PRNote struct {
	Path string
	text string // sanitized, trimmed
}

// Text returns the sanitized text of the note; it is empty for a note that
// NewPRNote did not make.
func (n PRNote) Text() string { return n.text }

// NewPRNote sanitizes a raw note once. ok is false when nothing is left. The
// error names the line of an unclosed or newly joined "<!--".
func NewPRNote(raw RawPRNote) (note PRNote, ok bool, err error) {
	clean, err := SanitizePRNote(normalizeNewlines(raw.Text))
	if err != nil {
		return PRNote{}, false, err
	}
	text := strings.TrimSpace(clean)
	return PRNote{Path: raw.Path, text: text}, text != "", nil
}

// Kinds of author sections that the PR body carries word for word. A model
// rewrites what it reads, so numbers, commands and caveats that must survive
// are copied by code instead (ADR 0015, decision 5).
const (
	SectionTesting     = "testing"
	SectionCounters    = "counters"
	SectionEvidence    = "evidence"
	SectionNotVerified = "not_verified"
	SectionKnownGaps   = "known_gaps"
	// SectionChecklist is a notes section headed "Checklist". The body never
	// carries it; its ticked checkbox lines tick the same items of the
	// pull request template.
	SectionChecklist = "checklist"
)

// sectionOrder is the order in which carried sections appear in the body.
var sectionOrder = []string{SectionTesting, SectionCounters, SectionEvidence, SectionNotVerified, SectionKnownGaps}

// AuthorSection is one part of an author note that the PR body carries word
// for word.
//
// The fields are private: only AuthorSections makes a section with text, from
// notes that passed NewPRNote. A struct literal or a zero value is an empty
// section everywhere. Read a section with its accessors.
type AuthorSection struct {
	kind    string
	heading string // the author's heading, without Markdown markers
	body    string // the text under the heading, as written
	source  string // path of the note it came from
	// problem is set on a section that carries a whole note because Markdown++
	// could not read the note's structure exactly (see noteSections).
	problem string
	// foreign names the Markdown++-only constructs in the text, for a warning.
	foreign []mdForeign
}

// Kind is the kind of the section (SectionTesting and the others).
func (s AuthorSection) Kind() string { return s.kind }

// Heading is the author's heading, without Markdown markers.
func (s AuthorSection) Heading() string { return s.heading }

// Body is the text under the heading, as written.
func (s AuthorSection) Body() string { return s.body }

// Source is the path of the note the section came from.
func (s AuthorSection) Source() string { return s.source }

var wordRe = regexp.MustCompile(`[a-z0-9]+`)

// headingKindPrefix starts the kind of a section that was selected by a
// heading of the pull request template instead of by one of the fixed kinds.
const headingKindPrefix = "heading:"

// AuthorSections returns the sections of the notes that the PR body carries
// word for word: testing, counters, evidence, not verified and known gaps, and
// any section whose heading is one of headings (the headings of the
// repository's pull request template).
//
// A section starts at a Markdown heading ("## Evidence") and ends at the next
// heading of the same or a higher level. It also starts at a short label on a
// line of its own ("Evidence" before a list, or "Not verified:") and then ends
// at the next heading or the next such label.
//
// Notes are read in the order given. When two notes have a section of the same
// kind, the body keeps all of them, in the order of the notes and then of the
// file. A later note never replaces an earlier one.
func AuthorSections(notes []PRNote, headings ...string) []AuthorSection {
	return authorSections(notes, headings, false)
}

// TemplateAuthorSections is AuthorSections for a run that uses the pull
// request template t. Only here does a notes section headed "Checklist" tick
// the template's checkboxes (see SectionChecklist); without a template it is
// an ordinary section, as it was before.
func TemplateAuthorSections(notes []PRNote, t *PRTemplate) []AuthorSection {
	var headings []string
	if t != nil {
		for _, s := range t.Sections {
			headings = append(headings, s.Heading)
		}
	}
	return authorSections(notes, headings, t != nil)
}

func authorSections(notes []PRNote, headings []string, checklist bool) []AuthorSection {
	var sections []AuthorSection
	template := map[string]bool{}
	for _, heading := range headings {
		if key := headingKey(heading); key != "" {
			template[key] = true
		}
	}
	byKind := map[string][]AuthorSection{}
	var kinds []string // in the order first seen
	for _, note := range notes {
		found := map[string][]AuthorSection{}
		var foundKinds []string
		for _, s := range noteSections(note, template, checklist) {
			if len(found[s.kind]) == 0 {
				foundKinds = append(foundKinds, s.kind)
			}
			found[s.kind] = append(found[s.kind], s)
		}
		for _, kind := range foundKinds {
			if len(byKind[kind]) == 0 {
				kinds = append(kinds, kind)
			}
			byKind[kind] = append(byKind[kind], found[kind]...)
		}
	}
	for _, kind := range sectionOrder {
		sections = append(sections, byKind[kind]...)
	}
	for _, kind := range kinds {
		if strings.HasPrefix(kind, headingKindPrefix) {
			sections = append(sections, byKind[kind]...)
		}
	}
	return append(sections, byKind[SectionChecklist]...)
}

// noteSections splits one note into its carried sections, in file order.
// template holds the heading keys of the pull request template.
func noteSections(note PRNote, template map[string]bool, checklist bool) []AuthorSection {
	kindOf := func(heading string) string {
		// The five carried kinds come first, so that "Testing" and "Test plan"
		// keep one kind and the order of the notes. A template heading that
		// names none of them is the fallback.
		if kind := sectionKind(heading); kind != "" {
			return kind
		}
		if checklist && headingKey(heading) == "checklist" {
			return SectionChecklist
		}
		if key := headingKey(heading); template[key] {
			return headingKindPrefix + key
		}
		return ""
	}
	// note.Text was sanitized when the note was made; it is not scanned for
	// comments again.
	d := readMarkdown(note.text)
	if len(d.Problems) > 0 {
		// Where its sections start and end cannot be told, so nothing of the note
		// may be dropped: the whole note is carried as written, as one section, and
		// BodyWithWarnings says so.
		return []AuthorSection{{kind: SectionEvidence, heading: "Notes from " + note.Path, body: note.text, source: note.Path, problem: d.Problems[0]}}
	}
	lines := d.Lines
	atx := map[int]mdHeading{} // the headings that start or end a section, by line
	for _, h := range d.topHeadings() {
		if !h.Setext {
			atx[h.Start] = h
		}
	}
	var (
		out      []AuthorSection
		cur      *AuthorSection
		curLevel int // heading level of cur; 0 when cur started at a label
		body     []string
	)
	flush := func() {
		if cur == nil {
			return
		}
		if cur.body = trimBlankEdges(body); cur.body != "" {
			out = append(out, *cur)
		}
		cur, body = nil, nil
	}
	// Only ATX headings (a line that starts with "#") start or end a section of
	// the notes. A setext heading ("Title" over "-----") inside a carried
	// section stays in it, so that no text of the author is dropped; placing the
	// section moves such a heading below the template's (demoteHeadings).
	for i, line := range lines {
		if h, ok := atx[i]; ok {
			if cur != nil && curLevel > 0 && h.Level > curLevel {
				body = append(body, line) // a sub-heading of the carried section
				continue
			}
			flush()
			if kind := kindOf(h.Text); kind != "" {
				cur, curLevel = &AuthorSection{kind: kind, heading: plainHeading(h.Text), source: note.Path}, h.Level
			}
			continue
		}
		if label, ok := labelLine(d, i); ok && (cur == nil || curLevel == 0) {
			if kind := kindOf(label); kind != "" {
				flush()
				cur, curLevel = &AuthorSection{kind: kind, heading: label, source: note.Path}, 0
				continue
			}
		}
		if cur != nil {
			body = append(body, line)
		}
	}
	flush()
	return out
}

// labelLine reports whether lines[i] is a short label on a line of its own: it
// follows a blank line (or starts the note), has at most eight words and no
// sentence punctuation at its end, and either ends with a colon or stands
// directly above a list, a table or a code fence. This is a property of the line
// in the notes, by the rule that existed before the parser came in; it does not
// depend on how the parser groups the label and the lines after it into blocks
// (a table or a numbered list right under a label is one paragraph to the
// parser). A line inside a fenced code block is not a label.
func labelLine(d *mdDoc, i int) (string, bool) {
	lines := d.Lines
	line := lines[i]
	if d.inFence[i] || line != strings.TrimLeft(line, " \t") || (i > 0 && !blankText(lines[i-1])) {
		return "", false
	}
	text := strings.TrimSpace(line)
	if text == "" || listItemRe.MatchString(text) {
		return "", false
	}
	// A list item, quote, table row, heading or code fence is not a label.
	// "**Evidence**" is: bold is the usual way to write one.
	if strings.ContainsAny(text[:1], "-*+>|#`~") && !strings.HasPrefix(text, "**") {
		return "", false
	}
	text = strings.TrimSpace(strings.Trim(text, "*_"))
	colon := strings.HasSuffix(text, ":")
	text = strings.TrimSpace(strings.Trim(strings.TrimSuffix(text, ":"), "*_"))
	if text == "" || len(strings.Fields(text)) > 8 || strings.ContainsAny(text[len(text)-1:], ".!?,;:") {
		return "", false
	}
	if colon {
		return text, true
	}
	for _, next := range lines[i+1:] {
		if blankText(next) {
			continue // blank by CommonMark: only spaces and tabs
		}
		next = strings.TrimSpace(next)
		return text, listItemRe.MatchString(next) || strings.HasPrefix(next, "|") || strings.HasPrefix(next, "```") || strings.HasPrefix(next, "~~~")
	}
	return "", false
}

// sectionKind classifies a heading. The checks run from the most specific
// kind to the least, so "Not verified evidence" is a not-verified section.
func sectionKind(heading string) string {
	h := strings.Join(wordRe.FindAllString(strings.ToLower(heading), -1), " ")
	has := func(phrases ...string) bool {
		for _, p := range phrases {
			if strings.Contains(" "+h+" ", " "+p+" ") {
				return true
			}
		}
		return false
	}
	starts := func(prefixes ...string) bool {
		for _, p := range prefixes {
			if h == p || strings.HasPrefix(h, p+" ") {
				return true
			}
		}
		return false
	}
	switch {
	case has("not verified", "unverified", "not verify", "not tested", "untested", "not measured", "not validated", "not checked"):
		return SectionNotVerified
	case has("known gaps", "known gap", "known limitations", "known limitation", "known issues", "known issue") || h == "gaps" || h == "limitations":
		return SectionKnownGaps
	case has("counters"):
		return SectionCounters
	case has("evidence"):
		return SectionEvidence
	case starts("testing", "tests", "test plan", "verification", "validation", "how to verify", "how to test"):
		return SectionTesting
	}
	return ""
}

// plainHeading removes the emphasis markers and the trailing colon of a heading.
func plainHeading(heading string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.Trim(strings.TrimSpace(heading), "*_")), ":"))
}

// trimBlankEdges joins lines and drops the blank lines at both ends. It keeps
// the indentation of the first line, which a nested list or a code block needs.
func trimBlankEdges(lines []string) string {
	for len(lines) > 0 && blankText(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blankText(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// carriedHeadings lists the headings of sections, for the prompt.
func carriedHeadings(sections []AuthorSection) []string {
	var out []string
	for _, s := range sections {
		out = append(out, s.heading)
	}
	return out
}

func hasSection(sections []AuthorSection, kind string) bool {
	for _, s := range sections {
		if s.kind == kind {
			return true
		}
	}
	return false
}

// withoutCarriedSections removes from model-written text any block that
// starts at a Markdown heading naming a carried author section. Code writes
// those sections word for word; a model that pastes them again into its own
// field makes the body say everything twice (seen on 10 November 2026: Testing,
// Not verified and Known gaps appeared twice in a body). A block runs to the
// next heading of the same or a higher level.
func withoutCarriedSections(text string, carried []AuthorSection) string {
	if len(carried) == 0 || text == "" {
		return text
	}
	names := map[string]bool{}
	for _, s := range carried {
		names[headingKey(s.heading)] = true
	}
	d := readMarkdown(text)
	var out []string
	skipLevel := 0
	next := 0
	tops := d.topHeadings()
	for i := 0; i < len(d.Lines); i++ {
		if next < len(tops) && tops[next].Start == i {
			h := tops[next]
			next++
			if skipLevel > 0 && h.Level <= skipLevel {
				skipLevel = 0
			}
			if skipLevel == 0 && names[headingKey(h.Text)] {
				skipLevel = h.Level
			}
			if skipLevel == 0 {
				out = append(out, d.Lines[h.Start:h.End+1]...)
			}
			i = h.End
			continue
		}
		if skipLevel == 0 {
			out = append(out, d.Lines[i])
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// SanitizePRNote removes every HTML comment (<!-- ... -->) from a note, wherever
// it is: in prose, in inline code and in fenced code. It does not track
// Markdown structure on purpose: each rule that spared comments "inside code"
// opened a new way to publish a private token, so the rule is simple and fails
// closed. Line breaks inside a removed comment stay, so that the rest of the
// note keeps its layout.
//
// A "<!--" with no closing "-->" is an error that names the line of the opener,
// and so is a "<!--" that the removal itself creates:
// the rest of the note cannot be trusted. Write the opener another way (for
// example "&lt;!--") when it is meant as text.
func SanitizePRNote(text string) (string, error) {
	out, unclosed := stripHTMLComments(text)
	if unclosed > 0 {
		return "", fmt.Errorf("line %d: \"<!--\" has no closing \"-->\"; close the comment, or write the opener another way (for example \"&lt;!--\") if it is meant as text", unclosed)
	}
	// Removing a comment can join the characters around it into a new opener
	// ("<!<!-- x -->--"). The text after it cannot be trusted either, and a
	// second removal would drop it without a word: refuse the note.
	if i := strings.Index(out, "<!--"); i >= 0 {
		return "", fmt.Errorf("line %d: removing a comment leaves a new \"<!--\" made of the characters around it; write the opener another way (for example \"&lt;!--\") if it is meant as text", 1+strings.Count(out[:i], "\n"))
	}
	return out, nil
}

// stripHTMLComments removes the comments. When one is never closed it removes
// the rest of the text and returns the line of its opener (1-based); otherwise
// that line is 0.
func stripHTMLComments(text string) (string, int) {
	var out strings.Builder
	rest := text
	line := 1
	for {
		start := strings.Index(rest, "<!--")
		if start < 0 {
			out.WriteString(rest)
			return out.String(), 0
		}
		out.WriteString(rest[:start])
		line += strings.Count(rest[:start], "\n")
		body := rest[start+4:]
		end := strings.Index(body, "-->")
		if end < 0 {
			// Keep the line breaks so that nothing else moves.
			out.WriteString(strings.Repeat("\n", strings.Count(body, "\n")))
			return out.String(), line
		}
		newlines := strings.Count(body[:end], "\n")
		out.WriteString(strings.Repeat("\n", newlines))
		line += newlines
		rest = body[end+3:]
	}
}

// PRPromptNotes is the text of the author's notes as the prompt shows it. Only
// NewPRPromptNotes makes one, from notes that passed NewPRNote, so a raw string
// cannot reach the prompt as author-note text. The zero value is no notes.
type PRPromptNotes struct{ text string }

// String returns the rendered notes; it is empty for the zero value.
func (n PRPromptNotes) String() string { return n.text }

// NewPRPromptNotes renders the notes for the prompt, each labeled by its path
// and, when there are several, by its position, because a later note is the
// current one. The text is bounded to maxBytes; the size bound truncates rather
// than errors, since a legitimate note that merely ran long is still useful
// context. cut is the number of bytes that were left out. A note that
// NewPRNote did not make has no text and is left out. The text is not scanned
// for comments again.
func NewPRPromptNotes(notes []PRNote, maxBytes int) (n PRPromptNotes, cut int) {
	var made []PRNote
	for _, note := range notes {
		if note.Text() != "" {
			made = append(made, note)
		}
	}
	var b strings.Builder
	for i, note := range made {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		switch {
		case len(made) == 1:
			fmt.Fprintf(&b, "### %s\n\n%s", note.Path, note.Text())
		case i == len(made)-1:
			fmt.Fprintf(&b, "### Note %d of %d, the latest: %s\n\n%s", i+1, len(made), note.Path, note.Text())
		default:
			fmt.Fprintf(&b, "### Note %d of %d: %s\n\n%s", i+1, len(made), note.Path, note.Text())
		}
	}
	out := b.String()
	if len(out) <= maxBytes {
		return PRPromptNotes{text: out}, 0
	}
	// Cut at the end of a line when there is one in the last part of the
	// budget, and never inside a UTF-8 sequence.
	keep := maxBytes
	if i := strings.LastIndexByte(out[:keep], '\n'); i > keep/2 {
		keep = i
	}
	for keep > 0 && !utf8.RuneStart(out[keep]) {
		keep--
	}
	return PRPromptNotes{text: out[:keep] + "\n... (truncated)"}, len(out) - keep
}
