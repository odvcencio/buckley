package commitmsg

import (
	"strings"
	"testing"
)

func TestValidateCommitFields(t *testing.T) {
	tests := []struct {
		name    string
		action  string
		scope   string
		subject string
		body    []string
		issues  []string
		wantErr bool
	}{
		{name: "valid", action: "fix", scope: "review", subject: "keep evidence", body: []string{"Preserve the exact staged context"}, issues: []string{"12"}},
		{name: "normalizes action", action: " FIX ", subject: "keep evidence", body: []string{"Preserve context"}},
		{name: "unknown action", action: "ship", subject: "release", body: []string{"Publish the release"}, wantErr: true},
		{name: "empty body", action: "fix", subject: "keep evidence", body: []string{"  -  "}, wantErr: true},
		{name: "long header", action: "fix", subject: "this subject is intentionally long enough to exceed the conventional header length limit", body: []string{"Explain the change"}, wantErr: true},
		{name: "newline subject", action: "fix", subject: "bad\nsubject", body: []string{"Explain the change"}, wantErr: true},
		{name: "unsafe issue", action: "fix", subject: "keep evidence", body: []string{"Explain the change"}, issues: []string{"12\nCloses #99"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateCommitFields(test.action, test.scope, test.subject, test.body, test.issues)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateCommitFields() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestNormalizeBullet(t *testing.T) {
	if got := NormalizeBullet("  - first line\nsecond line  "); got != "first line second line" {
		t.Fatalf("NormalizeBullet() = %q", got)
	}
}

func TestValidateCommitFieldsToolMarkup(t *testing.T) {
	for _, tc := range []struct {
		name, bullet string
		wantErr      bool
	}{
		{"observed artifact", "<arg_value>- Run jest with --json", true},
		{"whitespace", "  <arg_value>Run tests  ", true},
		{"duplicate bullets", "- * • <arg_value>- Run tests", true},
		{"marker only", "<arg_value>", true},
		{"quoted literal", "`<arg_value>` is a literal tag", false},
		{"inline literal", "Preserve <arg_value> in source examples", false},
		{"other markup", "<div> remains valid HTML", false},
		{"escaped literal", "&lt;arg_value&gt; is escaped", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []string{tc.bullet}
			before := NormalizeBullet(tc.bullet)
			err := ValidateCommitFields("fix", "", "preserve message text", body, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tc.wantErr)
			}
			if body[0] != tc.bullet || NormalizeBullet(tc.bullet) != before {
				t.Fatal("validation rewrote body text")
			}
		})
	}
}

func TestValidateCommitFieldsStyle(t *testing.T) {
	words := func(n int) string { return strings.TrimSpace(strings.Repeat("word ", n)) }
	five := []string{"one", "two", "three", "four", "five"}
	tests := []struct {
		name    string
		action  string
		subject string
		body    []string
		wantErr string
	}{
		{name: "ok", action: "fix", subject: "stale cache on reload", body: []string{"Reload no longer serves stale data."}},
		{name: "repeated verb", action: "fix", subject: "fix stale cache", body: []string{"ok"}, wantErr: "repeats the action"},
		{name: "repeated inflection", action: "add", subject: "adds a flag", body: []string{"ok"}, wantErr: "repeats the action"},
		{name: "repeated past form", action: "fix", subject: "fixed stale cache", body: []string{"ok"}, wantErr: "repeats the action"},
		{name: "similar word allowed", action: "add", subject: "address list parsing", body: []string{"ok"}},
		{name: "capital subject", action: "fix", subject: "Stale cache", body: []string{"ok"}, wantErr: "lowercase"},
		{name: "20 words allowed", action: "fix", subject: "cache", body: []string{words(20)}},
		{name: "21 words rejected", action: "fix", subject: "cache", body: []string{words(21)}, wantErr: "21 words"},
		{name: "5 bullets allowed", action: "fix", subject: "cache", body: five},
		{name: "6 bullets rejected", action: "fix", subject: "cache", body: append(append([]string{}, five...), "six"), wantErr: "6 bullets"},
		{name: "empty bullets not counted", action: "fix", subject: "cache", body: append(append([]string{}, five...), " - ")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCommitFields(tc.action, "", tc.subject, tc.body, nil)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateStyleAllowsMissingBody(t *testing.T) {
	if err := ValidateStyle("update", "a", "tune cache", nil); err != nil {
		t.Fatalf("a message without bullets must pass the style check: %v", err)
	}
	for name, tc := range map[string]struct {
		action, subject string
		bullets         []string
	}{
		"unknown action": {"ship", "tune cache", nil},
		"repeated verb":  {"update", "update cache", nil},
		"capital":        {"update", "Tune cache", nil},
		"long bullet":    {"update", "tune cache", []string{"- " + strings.Repeat("word ", 21)}},
	} {
		if err := ValidateStyle(tc.action, "", tc.subject, tc.bullets); err == nil {
			t.Errorf("%s: not rejected", name)
		}
	}
}
