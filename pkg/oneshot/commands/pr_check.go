package commands

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/oneshot"
)

// PRSources is the text a PR was written from: the author's notes in full and
// what the framework gathered from git. Generated text is checked against it,
// because a model states commands, numbers and decisions that nothing it read
// contains (seen on 2026-11-09: "go test ./internal/benchfixtures -run Ledger",
// a path cut to "cgo_h/docker", and "were rejected" for "not taken here; the
// owner decides").
type PRSources struct {
	// Record is where decisions are written down: the notes and the commit
	// messages.
	record string
	// All is Record plus the diff, the file list and the project guidelines.
	all string
}

// Record is where decisions are written down: the notes and the commit messages.
func (s PRSources) Record() string { return s.record }

// All is Record plus the diff, the file list and the project guidelines.
func (s PRSources) All() string { return s.all }

// NewPRSources collects the sources for notes and the context gathered for a
// PR definition. ctx may be nil.
func NewPRSources(notes []PRNote, ctx *oneshot.Context) PRSources {
	var record, rest strings.Builder
	for _, note := range notes {
		record.WriteString(note.Text() + "\n")
	}
	if ctx != nil {
		for label, text := range ctx.Sources {
			if strings.HasPrefix(label, "git_log") {
				record.WriteString(text + "\n")
			} else {
				rest.WriteString(text + "\n")
			}
		}
	}
	return PRSources{record: record.String(), all: record.String() + rest.String()}
}

// commandPrograms are first words that make a testing step a command.
var commandPrograms = map[string]bool{
	"go": true, "bash": true, "sh": true, "zsh": true, "make": true, "npm": true, "npx": true, "pnpm": true,
	"yarn": true, "node": true, "deno": true, "bun": true, "python": true, "python3": true, "pytest": true,
	"pip": true, "uv": true, "cargo": true, "docker": true, "kubectl": true, "git": true, "gh": true,
	"curl": true, "buckley": true, "hypha": true, "canopy": true, "env": true, "sudo": true, "time": true,
	"timeout": true, "local-heavy": true, "tailnet-run": true,
}

var (
	backtickSpanRe = regexp.MustCompile("`([^`]+)`")
	envAssignRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	// commandTailRe marks where prose starts after a command that is not in
	// backticks: "make test (81 pass)", "go vet: clean", "go vet - clean". It
	// is also the boundary of a command written with code spans: a lone "-" or
	// "--" before a space, an em or en dash, a colon before a space, a
	// semicolon, or a parenthesis. What follows the boundary is prose.
	commandTailRe = regexp.MustCompile(`\s+\(|\s+(?:[—–]|--?)\s+|:\s+|;\s+`)
	numberRe      = regexp.MustCompile(`\d[\d,]*(?:\.\d+)?`)
)

// decisionWords pair the words that state a decision with the stem that the
// record must contain for the statement to have a source.
var decisionWords = []struct{ said, source *regexp.Regexp }{
	{regexp.MustCompile(`(?i)\b(?:rejected|rejection)\b`), regexp.MustCompile(`(?i)\breject`)},
	{regexp.MustCompile(`(?i)\bruled out\b`), regexp.MustCompile(`(?i)\brul(?:ed|es|ing|e) out\b`)},
	{regexp.MustCompile(`(?i)\b(?:approved|approval)\b`), regexp.MustCompile(`(?i)\bapprov`)},
	{regexp.MustCompile(`(?i)\bdecided\b`), regexp.MustCompile(`(?i)\b(?:decid|decision)`)},
	{regexp.MustCompile(`(?i)\bagreed\b`), regexp.MustCompile(`(?i)\bagree`)},
	{regexp.MustCompile(`(?i)\babandoned\b`), regexp.MustCompile(`(?i)\babandon`)},
	{regexp.MustCompile(`(?i)\bdismissed\b`), regexp.MustCompile(`(?i)\bdismiss`)},
	{regexp.MustCompile(`(?i)\bdeclined\b`), regexp.MustCompile(`(?i)\bdeclin`)},
}

// UnsourcedCommands lists, as sentences for the author, the commands in the
// text that the sources do not contain as written: a command in a testing step
// (the whole command, as stepCommands reads it) and a command in a backtick
// span of a section's running text. The check is advisory. It changes nothing:
// a step stays in the body, and it can miss an unsourced command or warn about
// a sourced one, because it reads prose with a heuristic. Testing steps that
// the author wrote in a Testing section are copied by code and never reach it.
func (pr PRResult) UnsourcedCommands(src PRSources) []string {
	all := normalizeCommandText(src.all)
	missing := func(step string) string {
		for _, command := range stepCommands(step) {
			if !strings.Contains(all, normalizeCommandText(command)) {
				return command
			}
		}
		return ""
	}
	var warnings []string
	warned := map[string]bool{}
	step := func(text string) {
		if command := missing(text); command != "" {
			warned[normalizeCommandText(command)] = true
			warnings = append(warnings, fmt.Sprintf("the testing step %q names the command %q, which is not in the notes, the commits, the diff or the project guidelines; the step stays in the text", strings.TrimSpace(text), normalizeCommandText(command)))
		}
	}
	for _, text := range pr.Testing {
		step(text)
	}
	for _, key := range sortedKeys(pr.Sections) {
		// Only a Testing section lists steps. A Summary bullet that names a
		// command describes the change.
		if sectionKind(strings.ReplaceAll(key, "_", " ")) != SectionTesting {
			continue
		}
		for _, line := range strings.Split(pr.Sections[key], "\n") {
			if item := strings.TrimSpace(line); listItemRe.MatchString(item) {
				step(listItemRe.ReplaceAllString(item, ""))
			}
		}
	}
	for _, key := range sortedKeys(pr.Sections) {
		for _, span := range backtickSpanRe.FindAllStringSubmatch(pr.Sections[key], -1) {
			if norm := normalizeCommandText(span[1]); isCommand(span[1], false) && !warned[norm] && !strings.Contains(all, norm) {
				warned[norm] = true
				warnings = append(warnings, fmt.Sprintf("the text names the command `%s`, which is not in the notes, the commits, the diff or the project guidelines", span[1]))
			}
		}
	}
	return warnings
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// stepCommands returns the commands a testing step names: each backtick span
// that starts with a program, or the start of the step when it has no backtick
// span and starts with a program or a flag.
func stepCommands(step string) []string {
	spans := backtickSpanRe.FindAllStringSubmatch(step, -1)
	var commands []string
	for _, span := range spans {
		if isCommand(span[1], false) {
			commands = append(commands, span[1])
		}
	}
	text := trimBulletMarker(step)
	if len(spans) > 0 {
		// A command written in plain text with arguments in code format
		// ("go test ./pkg -run `TestFoo`") is one command: its prefix and every
		// code span that follows it directly, flags between them included.
		if before, _, _ := strings.Cut(text, "`"); strings.TrimSpace(before) != "" {
			if full, stray := plainCommandWithSpans(text); isCommand(full, false) {
				commands = append(commands, full)
				// A flag that comes after prose is no part of the command
				// text, but it still states what was run: it needs a source.
				commands = append(commands, stray...)
			}
		}
		return commands
	}
	if fields := strings.Fields(text); len(fields) > 0 && strings.HasPrefix(fields[0], "-") {
		// A step that starts with a flag: the flag is what must have a
		// source. The words after it may describe what the flag does.
		text = fields[0]
	} else if loc := commandTailRe.FindStringIndex(text); loc != nil {
		text = text[:loc[0]]
	}
	if isCommand(text, true) {
		commands = append(commands, text)
	}
	return commands
}

// isCommand reports whether text reads as a shell command: after any
// environment assignments, its first word is a known program, a path to a
// script, or (when flagStart is set) a flag such as "-count=1".
func isCommand(text string, flagStart bool) bool {
	fields := strings.Fields(text)
	for len(fields) > 0 && envAssignRe.MatchString(fields[0]) {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return false
	}
	first := fields[0]
	switch {
	case strings.HasPrefix(first, "./"), strings.HasPrefix(first, "../"):
		return true
	case strings.HasSuffix(first, ".sh"), strings.HasSuffix(first, ".py"), strings.HasSuffix(first, ".mjs"):
		return true
	case flagStart && strings.HasPrefix(first, "-") && len(first) > 1:
		return true
	}
	return commandPrograms[first] && len(fields) > 1
}

// UnbackedClaims lists what the model's prose states without a source: a word
// that states a decision when the notes and commits have no such word, and a number of two
// or more digits that appears nowhere in the sources. Each entry
// is a sentence for the author; the text is left as it is, because code cannot
// rewrite prose.
func (pr PRResult) UnbackedClaims(src PRSources) []string {
	parts := append(append(StringList{pr.Title, pr.Summary}, pr.Changes...), pr.ReviewersHint)
	for _, key := range sortedKeys(pr.Sections) {
		parts = append(parts, pr.Sections[key])
	}
	prose := strings.Join(parts, "\n")
	var claims []string

	for _, word := range decisionWords {
		if said := word.said.FindString(prose); said != "" && !word.source.MatchString(src.record) {
			claims = append(claims, fmt.Sprintf("the text says %q, but the author's notes and the commits record no such decision", said))
		}
	}

	known := map[string]bool{}
	for _, n := range numberRe.FindAllString(src.all, -1) {
		known[plainNumber(n)] = true
	}
	seen := map[string]bool{}
	for _, n := range numberRe.FindAllString(prose+"\n"+strings.Join(pr.Testing, "\n"), -1) {
		plain := plainNumber(n)
		if digits := strings.ReplaceAll(plain, ".", ""); len(digits) < 2 || known[plain] || seen[plain] {
			continue
		}
		seen[plain] = true
		claims = append(claims, fmt.Sprintf("the text has the number %s, which is not in the notes, the commits or the diff", strings.TrimRight(n, ",")))
	}
	return claims
}

// plainNumber removes thousands separators, so "1,204" equals "1204".
func plainNumber(n string) string {
	return strings.ReplaceAll(n, ",", "")
}

func squeezeSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// plainCommandWithSpans returns the command a step writes in plain text with
// its arguments in code format, backticks removed, and the flags that the step
// states after the command ended.
//
// The rule: the command is the text of the step up to the first token that
// cannot belong to a command line. That is the boundary of commandTailRe (a
// lone "-" or "--", an em or en dash, a colon before a space, a semicolon, a
// parenthesis), or a word that is neither a flag, a path, nor the value of the
// flag before it (a value has a digit or one of "/.=,_" in it: "-count 1",
// "-timeout 30s"). The command, with the spans unwrapped, is compared as a
// whole with the sources. Past that word, every other flag up to the boundary
// is returned in stray: it was run, so it needs a source too.
func plainCommandWithSpans(text string) (command string, stray []string) {
	before, rest, _ := strings.Cut(text, "`")
	if loc := commandTailRe.FindStringIndex(before); loc != nil {
		return strings.TrimSpace(before[:loc[0]]), nil
	}
	parts := []string{strings.TrimSpace(before)}
	ended := false
	for {
		span, after, found := strings.Cut(rest, "`")
		if !found {
			break
		}
		if ended {
			// After prose, a flag in a code span is still a flag that was run.
			if isFlagWord(span) {
				stray = append(stray, span)
			}
		} else {
			parts = append(parts, span)
		}
		next, more, ok := strings.Cut(after, "`")
		boundary := false
		if loc := commandTailRe.FindStringIndex(next); loc != nil {
			next, boundary = next[:loc[0]], true
		}
		afterFlag := false
		for _, word := range strings.Fields(next) {
			switch {
			case word == "-" || word == "--":
				boundary, ended = true, true
			case ended:
				if isFlagWord(word) {
					stray = append(stray, word)
				}
			case isFlagWord(word):
				parts = append(parts, word)
				afterFlag = !strings.Contains(word, "=")
				continue
			case strings.HasPrefix(word, "./") || strings.HasPrefix(word, "../") || (afterFlag && isFlagValue(word)):
				parts = append(parts, word)
			default:
				ended = true
			}
			afterFlag = false
		}
		if boundary || !ok {
			break
		}
		rest = more
	}
	return strings.Join(parts, " "), stray
}

// isFlagWord reports whether word is a command-line flag such as "-race" or
// "--strict" (not a lone dash and not a negative number).
func isFlagWord(word string) bool {
	trimmed := strings.TrimLeft(word, "-")
	if len(word)-len(trimmed) == 0 || len(word)-len(trimmed) > 2 || trimmed == "" {
		return false
	}
	c := trimmed[0]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// isFlagValue reports whether word can be the separate value of a flag: it has
// a digit or one of "/.=,_" in it. A plain word is prose.
func isFlagValue(word string) bool {
	// Sentence punctuation alone does not make a value: "passed." is prose.
	return strings.ContainsAny(strings.TrimRight(word, ".,;:!?"), "0123456789/.=,_")
}

// normalizeCommandText prepares text for comparing a command with its sources:
// inline code marks are dropped and spaces squeezed on both sides.
func normalizeCommandText(text string) string {
	return squeezeSpaces(strings.ReplaceAll(text, "`", ""))
}
