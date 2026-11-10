package commands

import (
	"encoding/json"
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

// hasCheckbox reports whether text has a Markdown checkbox line.
func hasCheckbox(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if checkboxRe.MatchString(line) {
			return true
		}
	}
	return false
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

// templateBody renders the body in the template's shape. It returns the
// author sections that no template section took, for the caller to place
// after the template.
func (pr PRResult) templateBody(t *PRTemplate, author []AuthorSection) (string, []AuthorSection) {
	var b strings.Builder
	if t.Preamble != "" {
		b.WriteString(t.Preamble + "\n\n")
	}
	taken := make([]bool, len(author))
	for _, s := range t.Sections {
		b.WriteString(s.HeadingLine + "\n")
		var supplied []string
		for i, a := range author {
			if !taken[i] && s.takes(a) {
				taken[i] = true
				supplied = append(supplied, a.Body)
			}
		}
		text := strings.Join(supplied, "\n\n")
		if text == "" && s.Open {
			text = commitmsg.NeutralizeCloseDirectives(strings.Trim(withoutCarriedSections(pr.Sections[s.Key], author), "\n"))
		}
		switch {
		case strings.TrimSpace(text) == "":
			// Nothing to add: the section stays as the template has it.
			b.WriteString(strings.Join(s.Lines, "\n") + "\n")
		case s.Open || hasCheckbox(text):
			// An open section takes the text in place of its comments. A
			// checklist that the author restated stands as the author wrote it.
			b.WriteString("\n" + text + "\n\n")
		default:
			b.WriteString(strings.TrimRight(strings.Join(s.Lines, "\n"), "\n") + "\n\n" + text + "\n\n")
		}
	}
	var rest []AuthorSection
	for i, a := range author {
		if !taken[i] {
			rest = append(rest, a)
		}
	}
	return strings.TrimSpace(b.String()), rest
}
