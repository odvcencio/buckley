package commitmsg

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var nestedHeaderRE = regexp.MustCompile(`^([A-Za-z]+)(?:\(([^()\r\n]*)\))?(!)?:[ \t]*`)

// ValidateHeader checks the shared commit and PR header format.
func ValidateHeader(action, scope, subject string, limit int) error {
	if hasCommitControl(scope) || hasCommitControl(subject) {
		return fmt.Errorf("header contains control characters")
	}
	if strings.TrimSpace(subject) == "" {
		return fmt.Errorf("subject is required")
	}
	if nestedHeaderPrefix(subject) != nil {
		return fmt.Errorf("subject contains a header prefix; supply only the summary after the colon")
	}
	header := composeHeader(action, scope, subject)
	if n := utf8.RuneCountInString(header); n > limit {
		return &HeaderTooLongError{Limit: limit, Length: n}
	}
	if scope != strings.Join(strings.Fields(scope), " ") || subject != strings.Join(strings.Fields(subject), " ") {
		return fmt.Errorf("header contains extra whitespace")
	}
	if strings.HasSuffix(subject, ".") {
		return fmt.Errorf("subject has a trailing period")
	}
	first, _ := utf8.DecodeRuneInString(subject)
	if unicode.IsUpper(first) {
		return fmt.Errorf("subject must start with lowercase")
	}
	return nil
}

func composeHeader(action, scope, subject string) string {
	if scope != "" {
		return action + "(" + scope + "): " + subject
	}
	return action + ": " + subject
}

// RepairHeader repairs format only. It leaves empty subjects and unknown actions
// for the model to correct. Callers must validate the complete result before use.
func RepairHeader(action, scope, subject string, limit int) (string, string, []string) {
	if !IsAllowedAction(action) || strings.TrimSpace(subject) == "" {
		return scope, subject, nil
	}
	for _, r := range scope + subject {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return scope, subject, nil
		}
	}
	originalScope, originalSubject := scope, subject
	var repairs []string
	cleanScope := strings.Join(strings.Fields(scope), " ")
	cleanSubject := strings.Join(strings.Fields(subject), " ")
	if cleanScope != scope || cleanSubject != subject {
		repairs = append(repairs, "normalized whitespace")
	}
	scope, subject = cleanScope, cleanSubject
	removedPrefix := false
	for prefix := nestedHeaderPrefix(subject); prefix != nil; prefix = nestedHeaderPrefix(subject) {
		remainder := strings.TrimSpace(subject[len(prefix[0]):])
		if NormalizeAction(prefix[1]) != NormalizeAction(action) ||
			(scope != "" && prefix[2] != "" && prefix[2] != scope) || prefix[3] != "" || remainder == "" {
			return originalScope, originalSubject, nil
		}
		if scope == "" && prefix[2] != "" {
			scope = prefix[2]
			repairs = append(repairs, "recovered scope from header prefix")
		}
		subject = remainder
		removedPrefix = true
	}
	if removedPrefix {
		repairs = append(repairs, "removed repeated header prefix")
		cleanSubject = subject
	}
	if trimmed := strings.TrimRight(subject, " ."); trimmed != subject && trimmed != "" {
		subject = trimmed
		repairs = append(repairs, "removed trailing period")
	}
	first, size := utf8.DecodeRuneInString(subject)
	if unicode.IsUpper(first) {
		subject = string(unicode.ToLower(first)) + subject[size:]
		repairs = append(repairs, "lowercased subject")
	}
	fits := func() bool { return utf8.RuneCountInString(composeHeader(action, scope, subject)) <= limit }
	if fits() {
		return scope, subject, repairs
	}
	// Order of the remedies: keep the scope and shorten the subject first
	// (filler words, a trailing qualifier, then whole words from the end). Drop
	// the scope only when the subject would otherwise fall below
	// minSubjectWords words. A subject that was already shorter is not held to it.
	startWords := len(strings.Fields(subject))
	if scope != "" {
		kept, keptRepairs := shortenSubject(action, scope, subject, limit)
		if n := len(strings.Fields(kept)); n >= minSubjectWords || n >= startWords {
			return scope, kept, append(repairs, keptRepairs...)
		}
		scope = ""
		repairs = append(repairs, "dropped scope")
	}
	subject, more := shortenSubject(action, scope, subject, limit)
	return scope, subject, append(repairs, more...)
}

// minSubjectWords is the fewest subject words that a cut may leave before the
// scope is given up instead.
const minSubjectWords = 4

// trailingFunctionWords cannot end a subject: a cut that leaves one reads as
// broken ("rebuild the lex steps from").
var trailingFunctionWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "but": true, "nor": true,
	"to": true, "from": true, "for": true, "with": true, "of": true, "in": true, "on": true,
	"at": true, "by": true, "into": true, "onto": true, "via": true, "as": true, "than": true,
	"that": true, "which": true, "while": true, "when": true,
}

// shortenSubject makes the header fit the limit with the given scope. It
// removes articles, then a trailing qualifier, then words from the end, and
// the cut always ends on a content word. If only the leading verb is left, the
// cut keeps that verb alone rather than a dangling function word.
func shortenSubject(action, scope, subject string, limit int) (string, []string) {
	var repairs []string
	fits := func(sub string) bool { return utf8.RuneCountInString(composeHeader(action, scope, sub)) <= limit }
	if fits(subject) {
		return subject, repairs
	}
	originalWords := len(strings.Fields(subject))
	// Remove articles inside the subject, but retain its leading action word.
	words := strings.Fields(subject)
	for i := len(words) - 1; i > 0 && !fits(strings.Join(words, " ")); i-- {
		switch words[i] {
		case "the", "a", "an":
			words = append(words[:i], words[i+1:]...)
		}
	}
	if len(words) < originalWords {
		repairs = append(repairs, "removed filler words")
	}
	subject = strings.Join(words, " ")
	if fits(subject) {
		return subject, repairs
	}
	for _, qualifier := range []string{" for now", " as needed", " where possible", " when needed"} {
		if strings.HasSuffix(subject, qualifier) {
			subject = strings.TrimSuffix(subject, qualifier)
			repairs = append(repairs, "removed trailing qualifier")
			if fits(subject) {
				return subject, repairs
			}
		}
	}
	words = strings.Fields(subject)
	n := len(words)
	for n > 1 && !fits(strings.Join(words[:n], " ")) {
		n--
	}
	for n > 1 && trailingFunctionWords[strings.ToLower(words[n-1])] {
		n--
	}
	subject = strings.Join(words[:n], " ")
	if !fits(subject) {
		// A single token can exceed the entire budget. A rune cut is the only
		// way to retain model text and guarantee that length cannot block a commit.
		budget := limit - utf8.RuneCountInString(composeHeader(action, scope, ""))
		if budget < 1 {
			return subject, repairs
		}
		subject = string([]rune(subject)[:budget])
	}
	subject = strings.TrimRight(subject, " .")
	repairs = append(repairs, fmt.Sprintf("cut the subject from %d to %d words", originalWords, len(strings.Fields(subject))))
	return subject, repairs
}

func nestedHeaderPrefix(subject string) []string {
	prefix := nestedHeaderRE.FindStringSubmatch(subject)
	if prefix != nil && IsAllowedAction(prefix[1]) {
		return prefix
	}
	return nil
}

// danglingWords cannot end a bullet: a cut that leaves one reads as broken.
var danglingWords = map[string]bool{
	"and": true, "or": true, "but": true, "so": true, "then": true, "the": true, "a": true, "an": true,
	"to": true, "of": true, "for": true, "with": true, "in": true, "on": true, "by": true,
	"that": true, "which": true, "because": true, "as": true, "from": true, "into": true,
}

// RepairBody fixes the two body limits without another model call. It shortens
// a bullet of more than MaxBulletWords words at its last sentence end, else its
// last clause break, else at the word limit; it never rewords what it keeps. It
// drops bullets beyond MaxBullets, last first. Blank bullets stay as they are.
// Callers must validate the complete result before use.
func RepairBody(body []string) ([]string, []string) {
	out := make([]string, len(body))
	copy(out, body)
	var repairs []string
	for i, bullet := range out {
		normalized := NormalizeBullet(bullet)
		words := len(strings.Fields(normalized))
		if words <= MaxBulletWords {
			continue
		}
		out[i] = shortenBullet(normalized)
		// Say which bullet lost text and how much. A cut at a clause break can
		// remove a fact ("two heuristics ..., and two more ..." became "two
		// heuristics"), and the author can only check it when told where.
		repairs = append(repairs, fmt.Sprintf("cut bullet %d from %d to %d words; check that it still says what changed", i+1, words, len(strings.Fields(out[i]))))
	}
	kept, nonEmpty := 0, 0
	for _, bullet := range out {
		if NormalizeBullet(bullet) != "" {
			nonEmpty++
		}
	}
	if nonEmpty > MaxBullets {
		trimmed := out[:0:0]
		for _, bullet := range out {
			if NormalizeBullet(bullet) == "" {
				trimmed = append(trimmed, bullet)
				continue
			}
			if kept < MaxBullets {
				trimmed = append(trimmed, bullet)
				kept++
			}
		}
		out = trimmed
		repairs = append(repairs, fmt.Sprintf("dropped %d bullet(s) beyond the limit of %d", nonEmpty-MaxBullets, MaxBullets))
	}
	return out, repairs
}

// shortenBullet cuts a bullet of more than MaxBulletWords words.
func shortenBullet(bullet string) string {
	words := strings.Fields(bullet)[:MaxBulletWords]
	cut := func(minWords int, ends func(word string) bool) int {
		for n := len(words); n >= minWords; n-- {
			if ends(words[n-1]) {
				return n
			}
		}
		return 0
	}
	n := cut(4, func(w string) bool {
		return strings.HasSuffix(w, ".") || strings.HasSuffix(w, "!") || strings.HasSuffix(w, "?")
	})
	if n == 0 {
		n = cut(8, func(w string) bool {
			return strings.HasSuffix(w, ",") || strings.HasSuffix(w, ";") || strings.HasSuffix(w, ":") || strings.HasSuffix(w, "\u2014")
		})
	}
	if n == 0 {
		n = len(words)
		for n > 1 && danglingWords[strings.ToLower(strings.Trim(words[n-1], ",;:."))] {
			n--
		}
	}
	return strings.TrimRight(strings.Join(words[:n], " "), ",;: .")
}
