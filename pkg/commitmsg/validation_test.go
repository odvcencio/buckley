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

func TestValidateCommitFieldsRejectsRepeatedActionPrefix(t *testing.T) {
	tests := []struct {
		name    string
		action  string
		scope   string
		subject string
	}{
		{name: "canonical action", action: "test", subject: "test: replace unwritable fixture"},
		{name: "legacy action alias with scope", action: "fix", subject: "feat(scope): add guard"},
		{name: "outer scope and breaking marker", action: "add", scope: "core", subject: "fix!: reject duplicate header"},
		{name: "inner scope and breaking marker", action: "refactor", subject: "fix(parser)!: reject duplicate header"},
		{name: "scope with interior space", action: "fix", subject: "test(parser core): preserve header ownership"},
		{name: "case insensitive", action: " FIX ", subject: "Test(ui): preserve header ownership"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateCommitFields(test.action, test.scope, test.subject, []string{"Explain the change"}, nil)
			if err == nil {
				t.Fatal("ValidateCommitFields() returned nil for repeated action prefix")
			}
			for _, want := range []string{"action and scope belong only in the structured header", "subject must not repeat"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestValidateCommitFieldsAllowsColonProseAndMalformedPrefixes(t *testing.T) {
	tests := []struct {
		name    string
		subject string
	}{
		{name: "unknown word", subject: "http: preserve the endpoint"},
		{name: "url", subject: "https://example.com/fixture"},
		{name: "time", subject: "10:30 preserve the scheduled run"},
		{name: "longer word", subject: "testimony: preserve the evidence"},
		{name: "action later", subject: "replace test: fixture references"},
		{name: "space before colon", subject: "fix(scope) : preserve the parser"},
		{name: "unclosed scope", subject: "fix(scope: preserve the parser"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateCommitFields("fix", "", test.subject, []string{"Explain the change"}, nil); err != nil {
				t.Fatalf("ValidateCommitFields() rejected valid colon prose: %v", err)
			}
		})
	}
}

func TestValidateCommitFieldsHeaderLengthRemainsIndependent(t *testing.T) {
	base := "fix: "
	validSubject := strings.Repeat("x", HeaderLimit-len(base))
	if got := len([]rune(base + validSubject)); got != HeaderLimit {
		t.Fatalf("test setup produced %d-rune header, want %d", got, HeaderLimit)
	}
	if err := ValidateCommitFields("fix", "", validSubject, []string{"Explain the change"}, nil); err != nil {
		t.Fatalf("exact-limit header rejected: %v", err)
	}

	tooLong := validSubject + "x"
	err := ValidateCommitFields("fix", "", tooLong, []string{"Explain the change"}, nil)
	if err == nil || !strings.Contains(err.Error(), "header exceeds") {
		t.Fatalf("over-limit header error = %v, want header-length error", err)
	}
}

func TestNormalizeBullet(t *testing.T) {
	if got := NormalizeBullet("  - first line\nsecond line  "); got != "first line second line" {
		t.Fatalf("NormalizeBullet() = %q", got)
	}
}
