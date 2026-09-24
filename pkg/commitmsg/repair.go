package commitmsg

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateHeader checks the shared commit and PR header format.
func ValidateHeader(action, scope, subject string, limit int) error {
	if hasCommitControl(scope) || hasCommitControl(subject) {
		return fmt.Errorf("header contains control characters")
	}
	if strings.TrimSpace(subject) == "" {
		return fmt.Errorf("subject is required")
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
	var repairs []string
	cleanScope := strings.Join(strings.Fields(scope), " ")
	cleanSubject := strings.Join(strings.Fields(subject), " ")
	if cleanScope != scope || cleanSubject != subject {
		repairs = append(repairs, "normalized whitespace")
	}
	scope, subject = cleanScope, cleanSubject
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
