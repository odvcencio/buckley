package commitmsg

import "testing"

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
