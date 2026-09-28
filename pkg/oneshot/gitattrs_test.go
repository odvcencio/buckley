package oneshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	return dir
}

func TestParseCheckAttr(t *testing.T) {
	out := []byte("a.js\x00linguist-generated\x00set\x00a.js\x00diff\x00unspecified\x00" +
		"b.bin\x00linguist-generated\x00unspecified\x00b.bin\x00diff\x00unset\x00" +
		"c.go\x00linguist-generated\x00false\x00c.go\x00diff\x00unspecified\x00")
	got := parseCheckAttr(out)
	want := map[string]bool{"a.js": true, "b.bin": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestStagedDiffStatsHonorsGitattributes(t *testing.T) {
	bundle := "var a=1;\nvar b=2;\n"
	dir := gitRepo(t, map[string]string{
		".gitattributes":       "client/js/*.js linguist-generated\nfixtures/*.dat -diff\n",
		"client/js/bundle.js":  bundle,
		"client/js/runtime.js": bundle,
		"fixtures/golden.dat":  "row 1\n",
		"cmd/main.go":          "package main\n",
	})
	t.Chdir(dir)

	all, err := StagedDiffStats(nil)
	if err != nil {
		t.Fatal(err)
	}
	if all.Files != 5 || len(all.GeneratedPaths) != 3 || len(all.SourcePaths) != 2 {
		t.Fatalf("stats = %+v", all)
	}
	if !all.Mixed() || all.GeneratedOnly() {
		t.Fatalf("Mixed=%v GeneratedOnly=%v", all.Mixed(), all.GeneratedOnly())
	}

	only, err := StagedDiffStats([]string{"client/js/bundle.js", "client/js/runtime.js", "fixtures/golden.dat"})
	if err != nil {
		t.Fatal(err)
	}
	if !only.GeneratedOnly() || only.Mixed() {
		t.Fatalf("stats = %+v, want generated-only", only)
	}
}

func TestGeneratedDiffRendersAsSummaryLine(t *testing.T) {
	dir := gitRepo(t, map[string]string{
		".gitattributes": "client/js/*.js linguist-generated\n",
		"client/js/a.js": "zorblaxHelper()\n",
		"cmd/main.go":    "package main\n",
	})
	t.Chdir(dir)
	got, stats, err := gatherGitDiffStats(map[string]string{"staged": "true"}, ContextOpts{MaxDiffBytes: 80_000})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "zorblaxHelper") {
		t.Fatalf("generated content reached the model context:\n%s", got)
	}
	if !strings.Contains(got, "client/js/a.js") || !strings.Contains(got, "cmd/main.go") {
		t.Fatalf("expected both files in context:\n%s", got)
	}
	if stats.LowSignal != 1 {
		t.Fatalf("LowSignal = %d, want 1", stats.LowSignal)
	}
}
