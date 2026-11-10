package commands

import (
	"regexp"
	"strings"
)

// PRNote is one author-supplied file from `buckley pr --context-file`.
type PRNote struct {
	Path string
	Text string
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
)

// sectionOrder is the order in which carried sections appear in the body.
var sectionOrder = []string{SectionTesting, SectionCounters, SectionEvidence, SectionNotVerified, SectionKnownGaps}

// AuthorSection is one part of an author note that the PR body carries word
// for word.
type AuthorSection struct {
	Kind    string
	Heading string // the author's heading, without Markdown markers
	Body    string // the text under the heading, as written
	Source  string // path of the note it came from
}

var (
	atxHeadingRe = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$`)
	listItemRe   = regexp.MustCompile(`^(?:[-*+]|\d{1,3}[.)])[ \t]`)
	wordRe       = regexp.MustCompile(`[a-z0-9]+`)
)

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
		for _, s := range noteSections(note, template) {
			if len(found[s.Kind]) == 0 {
				foundKinds = append(foundKinds, s.Kind)
			}
			found[s.Kind] = append(found[s.Kind], s)
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
	return sections
}

// noteSections splits one note into its carried sections, in file order.
// template holds the heading keys of the pull request template.
func noteSections(note PRNote, template map[string]bool) []AuthorSection {
	kindOf := func(heading string) string {
		if key := headingKey(heading); template[key] {
			return headingKindPrefix + key
		}
		return sectionKind(heading)
	}
	lines := strings.Split(strings.ReplaceAll(note.Text, "\r\n", "\n"), "\n")
	var (
		out      []AuthorSection
		cur      *AuthorSection
		curLevel int // heading level of cur; 0 when cur started at a label
		body     []string
		inFence  bool
	)
	flush := func() {
		if cur == nil {
			return
		}
		if cur.Body = trimBlankEdges(body); cur.Body != "" {
			out = append(out, *cur)
		}
		cur, body = nil, nil
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		} else if !inFence {
			if m := atxHeadingRe.FindStringSubmatch(line); m != nil {
				level := len(m[1])
				if cur != nil && curLevel > 0 && level > curLevel {
					body = append(body, line) // a sub-heading of the carried section
					continue
				}
				flush()
				if kind := kindOf(m[2]); kind != "" {
					cur, curLevel = &AuthorSection{Kind: kind, Heading: plainHeading(m[2]), Source: note.Path}, level
				}
				continue
			}
			if label, ok := labelLine(lines, i); ok && (cur == nil || curLevel == 0) {
				if kind := kindOf(label); kind != "" {
					flush()
					cur, curLevel = &AuthorSection{Kind: kind, Heading: label, Source: note.Path}, 0
					continue
				}
			}
		}
		if cur != nil {
			body = append(body, line)
		}
	}
	flush()
	return out
}

// labelLine reports whether lines[i] is a short label on a line of its own:
// it follows a blank line (or starts the note), has at most eight words and no
// sentence punctuation at its end, and either ends with a colon or stands
// directly above a list, a table or a code block.
func labelLine(lines []string, i int) (string, bool) {
	line := lines[i]
	if line != strings.TrimLeft(line, " \t") || (i > 0 && strings.TrimSpace(lines[i-1]) != "") {
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
		next = strings.TrimSpace(next)
		if next == "" {
			continue
		}
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
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// carriedHeadings lists the headings of sections, for the prompt.
func carriedHeadings(sections []AuthorSection) []string {
	var out []string
	for _, s := range sections {
		out = append(out, s.Heading)
	}
	return out
}

func hasSection(sections []AuthorSection, kind string) bool {
	for _, s := range sections {
		if s.Kind == kind {
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
		names[headingKey(s.Heading)] = true
	}
	var out []string
	skipLevel := 0
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		} else if m := atxHeadingRe.FindStringSubmatch(line); m != nil && !inFence {
			level := len(m[1])
			if skipLevel > 0 && level <= skipLevel {
				skipLevel = 0
			}
			if skipLevel == 0 && names[headingKey(m[2])] {
				skipLevel = level
			}
		}
		if skipLevel == 0 {
			out = append(out, line)
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}
