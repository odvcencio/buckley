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
		return fmt.Errorf("header exceeds %d characters (%d)", limit, n)
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
	if scope != "" {
		scope = ""
		repairs = append(repairs, "dropped scope")
	}
	if fits() {
		return scope, subject, repairs
	}
	// Remove articles inside the subject, but retain its leading action word.
	words := strings.Fields(subject)
	for i := len(words) - 1; i > 0 && !fits(); i-- {
		switch words[i] {
		case "the", "a", "an":
			words = append(words[:i], words[i+1:]...)
			subject = strings.Join(words, " ")
		}
	}
	if subject != cleanSubject && len(words) < len(strings.Fields(cleanSubject)) {
		repairs = append(repairs, "removed filler words")
	}
	if fits() {
		return scope, subject, repairs
	}
	for _, qualifier := range []string{" for now", " as needed", " where possible", " when needed"} {
		if strings.HasSuffix(subject, qualifier) {
			subject = strings.TrimSuffix(subject, qualifier)
			repairs = append(repairs, "removed trailing qualifier")
			if fits() {
				return scope, subject, repairs
			}
		}
	}
	budget := limit - utf8.RuneCountInString(action) - 2
	if budget < 1 {
		return scope, subject, repairs
	}
	words = strings.Fields(subject)
	for len(words) > 1 && !fits() {
		words = words[:len(words)-1]
		// A cut before the next list item must not leave a dangling conjunction.
		for len(words) > 1 && (words[len(words)-1] == "and" || words[len(words)-1] == "or") {
			words = words[:len(words)-1]
		}
		subject = strings.Join(words, " ")
	}
	if !fits() {
		// A single token can exceed the entire budget. A rune cut is the only
		// way to retain model text and guarantee that length cannot block a commit.
		subject = string([]rune(subject)[:budget])
	}
	subject = strings.TrimRight(subject, " .")
	repairs = append(repairs, "shortened subject")
	return scope, subject, repairs
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
	shortened := false
	for i, bullet := range out {
		normalized := NormalizeBullet(bullet)
		if len(strings.Fields(normalized)) <= MaxBulletWords {
			continue
		}
		out[i] = shortenBullet(normalized)
		shortened = true
	}
	if shortened {
		repairs = append(repairs, "shortened long bullets")
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
		repairs = append(repairs, "dropped extra bullets")
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
