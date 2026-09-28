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
		{"whole identifier still flagged", "drop handleRequestMixed", "-func handleRequestMixed() {}\n+func other() {}\n", 1},
		{"unknown plain word still flagged", "drop the zorblax helper", "-// zorblax helper\n+// none\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RemovedOnlyHits(tc.msg, tc.diff); got != tc.want {
				t.Fatalf("RemovedOnlyHits = %d, want %d", got, tc.want)
			}
		})
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
		"email":  "notify ops@corp-example.io on failure",
		"ipv4":   "point at 10.4.7.12 now",
		"ipv6":   "listen on fd00:1234:5678::1",
		"host":   "call db-primary.corp.internal for data",
		"aws":    "use AKIAABCDEFGHIJKLMNOP",
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
