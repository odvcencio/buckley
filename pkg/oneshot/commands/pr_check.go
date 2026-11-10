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
	Record string
	// All is Record plus the diff, the file list and the project guidelines.
	All string
}

// NewPRSources collects the sources for notes and the context gathered for a
// PR definition. ctx may be nil.
func NewPRSources(notes []PRNote, ctx *oneshot.Context) PRSources {
	var record, rest strings.Builder
	for _, note := range notes {
		record.WriteString(note.Text + "\n")
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
	return PRSources{Record: record.String(), All: record.String() + rest.String()}
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
	// backticks: "make test (81 pass)", "go vet: clean", "go vet - clean".
	commandTailRe = regexp.MustCompile(`\s+\(|\s+[—–-]\s+|:\s+|;\s+`)
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

// DropUnbackedTesting removes the testing steps that name a command which the
// sources do not contain as written, and returns the removed steps. A step
// that names no command stays. In the text for a template section, a list item
// is a step.
func (pr *PRResult) DropUnbackedTesting(src PRSources) []string {
	all := squeezeSpaces(src.All)
	backed := func(step string) bool {
		for _, command := range stepCommands(step) {
			if !strings.Contains(all, squeezeSpaces(command)) {
				return false
			}
		}
		return true
	}
	var kept, dropped StringList
	for _, step := range pr.Testing {
		if backed(step) {
			kept = append(kept, step)
		} else {
			dropped = append(dropped, strings.TrimSpace(step))
		}
	}
	pr.Testing = kept

	for _, key := range sortedKeys(pr.Sections) {
		// Only a Testing section lists steps. A Summary bullet that names a
		// command describes the change, and code must not delete it.
		if sectionKind(strings.ReplaceAll(key, "_", " ")) != SectionTesting {
			continue
		}
		var lines []string
		for _, line := range strings.Split(pr.Sections[key], "\n") {
			if item := strings.TrimSpace(line); listItemRe.MatchString(item) && !backed(listItemRe.ReplaceAllString(item, "")) {
				dropped = append(dropped, item)
				continue
			}
			lines = append(lines, line)
		}
		pr.Sections[key] = strings.Join(lines, "\n")
	}
	return dropped
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
		// A span that is not the whole command (an argument in code format)
		// must not hide the command before it.
		if before, _, _ := strings.Cut(text, "`"); strings.TrimSpace(before) != "" {
			if loc := commandTailRe.FindStringIndex(before); loc != nil {
				before = before[:loc[0]]
			}
			if isCommand(before, false) {
				commands = append(commands, strings.TrimSpace(before))
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

// UnbackedClaims lists what the model's prose states without a source: a
// command in the running text of a template section, a word that states a
// decision when the notes and commits have no such word, and a number of two
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

	// A command in running text cannot be cut out like a list item can.
	all := squeezeSpaces(src.All)
	for _, key := range sortedKeys(pr.Sections) {
		for _, span := range backtickSpanRe.FindAllStringSubmatch(pr.Sections[key], -1) {
			if isCommand(span[1], false) && !strings.Contains(all, squeezeSpaces(span[1])) {
				claims = append(claims, fmt.Sprintf("the text names the command `%s`, which is not in the notes, the commits, the diff or the project guidelines", span[1]))
			}
		}
	}
	for _, word := range decisionWords {
		if said := word.said.FindString(prose); said != "" && !word.source.MatchString(src.Record) {
			claims = append(claims, fmt.Sprintf("the text says %q, but the author's notes and the commits record no such decision", said))
		}
	}

	known := map[string]bool{}
	for _, n := range numberRe.FindAllString(src.All, -1) {
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
