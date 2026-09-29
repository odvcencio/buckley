package commitmsg

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const renameDiff = `diff --git a/deploy/app.yaml b/deploy/app.yaml
--- a/deploy/app.yaml
+++ b/deploy/app.yaml
@@ -1,4 +1,4 @@
-namespace: zorblax-prod
-owner: ZorblaxCorp
+namespace: example-prod
+owner: ExampleOrg
 replicas: 2
`

func TestRemovedOnlyHits(t *testing.T) {
	p := Policy{HostPattern: DefaultHostRegexp()}
	cases := []struct {
		name, msg, diff string
		want            int
	}{
		{"renamed identifier named", "rename zorblax-prod to example-prod", renameDiff, 1},
		{"camel and separator insensitive", "drop the ZORBLAX_PROD label", renameDiff, 1},
		{"word part of removed identifier", "rename the Zorblax deployment", renameDiff, 1},
		{"intent only", "rename the deployment; the old name is gone", renameDiff, 0},
		{"token also added", "keep example prod settings", renameDiff, 0},
		{"short token ignored", "fix the old id", "-old id\n+new\n", 0},
		{"stopword ignored", "remove config value", "-config value\n+other\n", 0},
		{"deleted file path allowed", "remove the zorblax helper", "diff --git a/zorblax.go b/zorblax.go\n--- a/zorblax.go\n+++ /dev/null\n-package main\n", 0},
		{"empty diff", "anything zorblax", "", 0},
		{"plain english word only removed", "handle mixed inputs", "-// mixed inputs are rare\n+// none\n", 0},
		{"english part of identifier ignored", "rename the handler", "-func handleRequestMixed() {}\n+func other() {}\n", 0},
		{"removed function name allowed", "drop handleRequestMixed", "-func handleRequestMixed() {}\n+func other() {}\n", 0},
		{"comment word is not a value", "drop the zorblax helper", "-// zorblax helper\n+// none\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.RemovedOnlyHits(tc.msg, tc.diff); got != tc.want {
				t.Fatalf("RemovedOnlyHits = %d, want %d", got, tc.want)
			}
		})
	}
}

const removedValuesDiff = `diff --git a/deploy/app.yaml b/deploy/app.yaml
--- a/deploy/app.yaml
+++ b/deploy/app.yaml
@@ -1,5 +1,5 @@
-host: db-primary.corp.internal
+host: db.example.test
-contact: ops@zorblaxcorp.example
+contact: team@example.test
-data_dir: /home/zorblax/projects/widget
+data_dir: /var/lib/widget
-namespace: zorblax-prod
+namespace: example-prod
-access_key: zorb_live_51HfakeTOKENvalue0000
+access_key: placeholder
`

func hasRule(findings []Finding, rule string) bool {
	for _, f := range findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

// Renamed or deleted code identifiers are ordinary engineering prose: naming
// them must not fail the removed-line check.
func TestRemovedOnlyHitsAllowsCodeIdentifiers(t *testing.T) {
	p := Policy{HostPattern: DefaultHostRegexp()}
	renamed := "diff --git a/store/cache.go b/store/cache.go\n--- a/store/cache.go\n+++ b/store/cache.go\n@@ -4,7 +4,7 @@\n-func handleRequestMixed() {}\n+func processRequest() {}\n"
	for _, msg := range []string{
		"refactor: rename handleRequestMixed to processRequest",
		"refactor: handleRequestMixed is now processRequest",
	} {
		if got := p.RemovedOnlyHits(msg, renamed); got != 0 {
			t.Errorf("removed function named in %q: hits = %d, want 0", msg, got)
		}
		if f := p.Check(msg, renamed); len(f) != 0 {
			t.Errorf("clean message rejected: %+v", f)
		}
	}
	deleted := "diff --git a/store/cache.go b/store/cache.go\n--- a/store/cache.go\n+++ b/store/cache.go\n@@\n-func zorblaxStore() *Store {\n+func sharedStore() *Store {\n"
	if got := p.RemovedOnlyHits("remove the zorblaxStore helper", deleted); got != 0 {
		t.Errorf("deleted helper rejected: hits = %d, want 0", got)
	}
	// A deleted identifier that carries a deny-list name is still a leak.
	q := Policy{HostPattern: DefaultHostRegexp(), DenyTerms: []string{"zorblax"}}
	if got := q.RemovedOnlyHits("remove the zorblaxStore helper", deleted); got == 0 {
		t.Error("deny-list name inside a removed identifier not flagged")
	}
}

// Removed sensitive values must never be echoed by the message.
func TestCheckRejectsRemovedSensitiveValues(t *testing.T) {
	p := Policy{HostPattern: DefaultHostRegexp()}
	cases := []struct{ name, msg, rule string }{
		{"hostname", "update(deploy): point the app at db-primary.corp.internal", RuleRemovedEcho},
		{"email", "update(deploy): cc ops@zorblaxcorp.example instead", RuleRemovedEcho},
		{"home path", "update(deploy): move the config out of /home/zorblax", RuleRemovedEcho},
		{"project id", "update(deploy): rename zorblax-prod namespace", RuleRemovedEcho},
		{"secret value", "update(deploy): rotate zorb_live_51HfakeTOKENvalue0000", RuleRemovedEcho},
		{"host also by pattern", "update(deploy): point the app at db-primary.corp.internal", RuleHost},
		{"email also by pattern", "update(deploy): cc ops@zorblaxcorp.example instead", RuleEmail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := p.Check(tc.msg, removedValuesDiff)
			if len(findings) == 0 {
				t.Fatalf("message accepted: %q", tc.msg)
			}
			if !hasRule(findings, tc.rule) {
				t.Fatalf("findings %+v lack rule %s", findings, tc.rule)
			}
		})
	}
	// A removed owner that is a deny-list name stays blocked when echoed.
	q := Policy{HostPattern: DefaultHostRegexp(), DenyTerms: []string{"zorblaxcorp"}}
	diff := "diff --git a/OWNERS b/OWNERS\n--- a/OWNERS\n+++ b/OWNERS\n@@\n-ZorblaxCorp\n+ExampleOrg\n"
	if findings := q.Check("update(owners): remove the ZorblaxCorp entry\n\n- Use the new owner.\n", diff); !hasRule(findings, RuleDenyList) {
		t.Fatalf("deny-list name not rejected: %+v", findings)
	}
}

// Words inside removed code comments are prose, not values, unless the
// classifier (here: the deny-list) says they are.
func TestRemovedOnlyHitsIgnoresCommentWords(t *testing.T) {
	diff := "diff --git a/store/cache.go b/store/cache.go\n--- a/store/cache.go\n+++ b/store/cache.go\n@@\n-// zorblax was the old vendor name\n+// none\n"
	p := Policy{HostPattern: DefaultHostRegexp()}
	if got := p.RemovedOnlyHits("clean up the zorblax references", diff); got != 0 {
		t.Errorf("comment word flagged: hits = %d, want 0", got)
	}
	q := Policy{HostPattern: DefaultHostRegexp(), DenyTerms: []string{"zorblax"}}
	if got := q.RemovedOnlyHits("clean up the zorblax references", diff); got == 0 {
		t.Error("deny-list word inside a removed comment not flagged")
	}
}

func TestPolicyDenyListIgnoresCaseAndSeparators(t *testing.T) {
	p := Policy{DenyTerms: []string{"Blue Harbor Labs"}}
	for _, msg := range []string{"tune blue-harbor-labs cache", "BLUEHARBORLABS", "blue_harbor_labs"} {
		if p.DenyHits(msg) != 1 {
			t.Errorf("no hit for %q", msg)
		}
	}
	if p.DenyHits("tune the cache") != 0 {
		t.Error("false positive")
	}
}

func TestCheckFindingsNeverEchoPrivateText(t *testing.T) {
	p := Policy{DenyTerms: []string{"quuxcorp"}}
	msg := "rename zorblax-prod for QuuxCorp"
	findings := p.Check(msg, renameDiff)
	if len(findings) != 2 {
		t.Fatalf("findings = %+v", findings)
	}
	err := (&LeakError{Findings: findings}).Error()
	for _, secret := range []string{"quuxcorp", "zorblax"} {
		if strings.Contains(strings.ToLower(err), secret) {
			t.Fatalf("error echoes %q: %s", secret, err)
		}
	}
}

func TestSensitivePatterns(t *testing.T) {
	p := Policy{HostPattern: regexp.MustCompile(DefaultInternalHostPattern)}
	bad := map[string]string{
		"email": "notify ops@corp-example.io on failure",
		"ipv4":  "point at 10.4.7.12 now",
		"ipv6":  "listen on fd00:1234:5678::1",
		"host":  "call db-primary.corp.internal for data",
		// The AWS-style key is built at runtime so the repository never
		// contains a string that GitHub push-protection flags as a real key.
		"aws":    "use AKIA" + strings.Repeat("AB", 8),
		"ghp":    "token ghp_abcdefghijklmnopqrstuvwxyz0123",
		"pem":    "-----BEGIN RSA PRIVATE KEY-----",
		"assign": "set password=hunter2hunter2",
		"b64":    "key QWxhZGRpbjpvcGVuIHNlc2FtZQ1234567890abcdefGHIJKLMN",
	}
	for name, msg := range bad {
		if len(p.sensitivePatterns(msg)) == 0 {
			t.Errorf("%s: not flagged: %q", name, msg)
		}
	}
	good := []string{
		"listen on 192.0.2.10 and 2001:db8::1",
		"bump to v0.56.2",
		"call std::vector helpers and 127.0.0.1",
		"update internal/commit_check_helper_for_long_path_names_tests",
		"add cmd/buckley/commit_command_test.go coverage",
		"ratio 12:30:45 and ::",
	}
	for _, msg := range good {
		if f := p.sensitivePatterns(msg); len(f) != 0 {
			t.Errorf("false positive %+v for %q", f, msg)
		}
	}
}

func TestLoadPolicyReadsPrivateFilesWithoutTracking(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BUCKLEY_INTERNAL_HOST_PATTERN", `\.zorb\.test\b`)
	if err := os.MkdirAll(filepath.Join(home, ".buckley"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, ".buckley", "private-terms"), []byte("# comment\nalpha-term\n\n"), 0o600)

	repo := t.TempDir()
	mustGit(t, repo, "init", "-q")
	os.WriteFile(filepath.Join(repo, ".git", "info", "buckley-private"), []byte("beta-term\n"), 0o600)

	p := LoadPolicy(repo)
	if p.DenyHits("alpha term and beta term") != 2 {
		t.Fatalf("terms not loaded: %d", len(p.DenyTerms))
	}
	if !p.HostPattern.MatchString("x.zorb.test") {
		t.Fatal("host pattern override ignored")
	}
	mustGit(t, repo, "add", "-A")
	if out := mustGit(t, repo, "status", "--porcelain"); strings.Contains(out, "buckley-private") {
		t.Fatalf("private file would be tracked: %s", out)
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
