package diffsignal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobMatcher(t *testing.T) {
	match := GlobMatcher([]string{"client/js/bootstrap*.js", "**/*.pb.go", "dist/**", "*.snap", ""})
	for path, want := range map[string]bool{
		"client/js/bootstrap-feature.js": true,
		"client/js/other.js":             false,
		"api/v1/api.pb.go":               true,
		"api.pb.go":                      true,
		"dist/a/b.js":                    true,
		"src/dist/a.js":                  false,
		"x/y/z.snap":                     true,
		"main.go":                        false,
	} {
		if got := match(path); got != want {
			t.Errorf("GlobMatcher(%q) = %v, want %v", path, got, want)
		}
	}
	if GlobMatcher(nil) != nil || GlobMatcher([]string{" ", ""}) != nil {
		t.Error("GlobMatcher with no patterns should be nil")
	}
}

func diffFor(path, body string) string {
	return "diff --git a/" + path + " b/" + path + "\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1,2 @@\n+" + body + "\n+more\n"
}

func TestGeneratedResolverFromGlobsMakesFilesSummaryOnly(t *testing.T) {
	SetGeneratedResolver(NewGeneratedResolver(t.TempDir(), []string{"client/js/bootstrap*.js"}))
	t.Cleanup(func() { SetGeneratedResolver(nil) })
	raw := diffFor("client/js/bootstrap-a.js", "var a = 1;") + diffFor("src/main.go", "package main")
	res := Prioritize(raw, 100_000)
	if strings.Contains(res.Context, "var a = 1;") {
		t.Fatalf("glob-matched file body reached the budget:\n%s", res.Context)
	}
	if !strings.Contains(res.Context, "package main") {
		t.Fatalf("hand-written file missing:\n%s", res.Context)
	}
	if res.LowSignal != 1 || !strings.Contains(res.Context, "client/js/bootstrap-a.js") {
		t.Fatalf("LowSignal = %d, want the bootstrap file summarized:\n%s", res.LowSignal, res.Context)
	}
	if shards := PrioritizeShards(raw, 100_000); len(shards.LowSignal) != 1 {
		t.Fatalf("shard LowSignal = %d, want 1", len(shards.LowSignal))
	}
}

func TestGeneratedResolverReadsGitattributes(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("gen/** linguist-generated\nblob.dat -diff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetGeneratedResolver(NewGeneratedResolver(dir, nil))
	t.Cleanup(func() { SetGeneratedResolver(nil) })
	raw := diffFor("gen/a.txt", "generated line") + diffFor("blob.dat", "blob line") + diffFor("keep.txt", "kept line")
	res := Prioritize(raw, 100_000)
	for _, hidden := range []string{"generated line", "blob line"} {
		if strings.Contains(res.Context, hidden) {
			t.Errorf("%q should be summarized:\n%s", hidden, res.Context)
		}
	}
	if !strings.Contains(res.Context, "kept line") {
		t.Errorf("kept.txt missing:\n%s", res.Context)
	}
}
