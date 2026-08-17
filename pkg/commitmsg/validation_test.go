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
