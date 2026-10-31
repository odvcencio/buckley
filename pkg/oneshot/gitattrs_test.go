package oneshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
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

func TestGeneratedByAttrs_SourceTree(t *testing.T) {
	requireAttrSourceGit(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	writeRepoFile(t, dir, "seed.txt", "seed\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "seed")
	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	writeRepoFile(t, dir, ".gitattributes", "f.txt linguist-generated\n")
	writeRepoFile(t, dir, "f.txt", "x\n")
	writeRepoFile(t, dir, "g.txt", "x\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "feature")
	gitIn(t, dir, "checkout", "-q", "-b", "other", "main")
	writeRepoFile(t, dir, ".gitattributes", "g.txt linguist-generated\n")
	t.Chdir(dir)
	paths := []string{"f.txt", "g.txt"}

	fromHead := generatedByAttrs(paths, false, "refs/heads/feature", "")
	if fromHead == nil || !fromHead("f.txt") || fromHead("g.txt") {
		t.Errorf("attributes from the feature tree: f.txt generated = %v, g.txt generated = %v, want true, false",
			fromHead != nil && fromHead("f.txt"), fromHead != nil && fromHead("g.txt"))
	}

	fromWorktree := generatedByAttrs(paths, false, "", "")
	if fromWorktree == nil || fromWorktree("f.txt") || !fromWorktree("g.txt") {
		t.Errorf("without a source the working tree decides: f.txt generated = %v, g.txt generated = %v, want false, true",
			fromWorktree != nil && fromWorktree("f.txt"), fromWorktree != nil && fromWorktree("g.txt"))
	}

	if got := generatedByAttrs(paths, false, "refs/heads/nosuch", ""); got != nil {
		t.Error("an unknown source must leave the path rules alone, not fall back to the working tree")
	}
}

// requireAttrSourceGit skips a test that needs the installed git to read
// gitattributes from a commit. Older git ignores the head and the code under
// test falls back, which TestBuildContext_OldGitFallsBackToTheWorkingTree covers.
func requireAttrSourceGit(t *testing.T) {
	t.Helper()
	if ok, installed := HeadAttributesSupported(); !ok {
		t.Skipf("git %s cannot read gitattributes from a commit (needs 2.40)", installed)
	}
}

// fakeGitScript records every call. With FAKE_GIT_VERSION set it also acts like
// that git release: it reports the version and rejects the options newer
// releases added, as the real older git does with "unknown option". Otherwise
// it hands every call to the real git.
const fakeGitScript = `#!/bin/sh
for a in "$@"; do printf '%s\000' "$a" >> "$FAKE_GIT_LOG"; done
printf '\036' >> "$FAKE_GIT_LOG"
if [ -n "$FAKE_GIT_VERSION" ]; then
	if [ "$1" = version ]; then echo "git version $FAKE_GIT_VERSION"; exit 0; fi
	minor=${FAKE_GIT_VERSION#2.}; minor=${minor%%.*}
	for a in "$@"; do
		case "$a" in
		--attr-source=*|--source=*)
			if [ "$minor" -lt 40 ]; then echo "unknown option: ${a%%=*}" >&2; exit 129; fi ;;
		esac
	done
fi
exec "$FAKE_GIT_REAL" "$@"
`

// installFakeGit puts a git on PATH and returns the log of its calls. A version
// makes it act like that release; "" keeps the real git's behavior.
func installFakeGit(t *testing.T, version string) *fakeGitLog {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("look up git: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(fakeGitScript), 0o755); err != nil {
		t.Fatal(err)
	}
	log := &fakeGitLog{path: filepath.Join(dir, "calls.log")}
	t.Setenv("FAKE_GIT_VERSION", version)
	t.Setenv("FAKE_GIT_REAL", realGit)
	t.Setenv("FAKE_GIT_LOG", log.path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

type fakeGitLog struct{ path string }

// calls returns each git invocation as its argument list.
func (l *fakeGitLog) calls(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var calls [][]string
	for _, record := range strings.Split(string(data), "\x1e") {
		if record != "" {
			calls = append(calls, strings.Split(strings.TrimSuffix(record, "\x00"), "\x00"))
		}
	}
	return calls
}

// subcommand returns the git subcommand of a call: the first argument that is
// not a global option.
func subcommand(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

// attrRepo is headRepo with a .gitattributes in the checked-out worktree that
// marks the head's f1.txt "-diff". Where it lives (uncommitted, or committed on
// the checked-out branch) is the variable.
func attrRepo(t *testing.T, committed bool) string {
	t.Helper()
	dir := headRepo(t)
	writeRepoFile(t, dir, ".gitattributes", "f1.txt -diff\n")
	if committed {
		gitIn(t, dir, "add", ".gitattributes")
		gitIn(t, dir, "commit", "-q", "-m", "mark f1.txt -diff on the checked-out branch")
	}
	return dir
}

func TestGatherGitDiff_HeadAttributesComeFromTheHeadCommit(t *testing.T) {
	requireAttrSourceGit(t)
	for _, tc := range []struct {
		name      string
		committed bool
	}{
		{"an uncommitted .gitattributes in the working tree", false},
		{"a .gitattributes committed on the checked-out branch", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := attrRepo(t, tc.committed)
			t.Chdir(dir)

			ctx, err := BuildContext(headSources("main", "refs/heads/feature"), ContextOpts{MaxDiffBytes: 80_000})
			if err != nil {
				t.Fatalf("BuildContext: %v", err)
			}

			diff := ctx.Sources["git_diff:main"]
			if !strings.Contains(diff, "+feature file") {
				t.Errorf("the head's f1.txt diff was replaced by the checked-out branch's attributes:\n%s", diff)
			}
			if strings.Contains(diff, "binary: content omitted") {
				t.Errorf("a text file reads as binary because of a .gitattributes the head does not have:\n%s", diff)
			}
			if slices.Contains(ctx.Diff.GeneratedPaths, "f1.txt") {
				t.Errorf("generated paths = %v: f1.txt is -diff only in the checked-out worktree", ctx.Diff.GeneratedPaths)
			}
			if !slices.Contains(ctx.Diff.GeneratedPaths, "gen.js") {
				t.Errorf("generated paths = %v: the head's own linguist-generated line must still apply", ctx.Diff.GeneratedPaths)
			}
		})
	}
}

// TestGatherGitDiff_NoMergeBaseFallbackReadsAttributesFromTheHead covers the
// second diff call, the commit-to-commit comparison used when the range has no
// merge base.
func TestGatherGitDiff_NoMergeBaseFallbackReadsAttributesFromTheHead(t *testing.T) {
	requireAttrSourceGit(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	writeRepoFile(t, dir, "seed.txt", "seed\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	writeRepoFile(t, dir, "f1.txt", "feature only\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "feature")
	gitIn(t, dir, "checkout", "-q", "--orphan", "unrel")
	gitIn(t, dir, "rm", "-rf", "-q", ".")
	writeRepoFile(t, dir, "other.txt", "unrelated history\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "unrelated")
	writeRepoFile(t, dir, ".gitattributes", "f1.txt -diff\n")
	t.Chdir(dir)

	diff, _, err := gatherGitDiffStats(map[string]string{"base": "unrel", "head": "refs/heads/feature"}, ContextOpts{MaxDiffBytes: 80_000})
	if err != nil {
		t.Fatalf("gatherGitDiffStats: %v", err)
	}
	if !strings.Contains(diff, "+feature only") || strings.Contains(diff, "binary: content omitted") {
		t.Errorf("the fallback comparison followed the working tree's .gitattributes:\n%s", diff)
	}
}

// TestBuildContext_EveryAttributeReadUsesTheHeadTree watches the git calls a
// head makes. Every call that applies attributes has to name the head's tree,
// so a call added later without it fails here and not in a user's PR text.
func TestBuildContext_EveryAttributeReadUsesTheHeadTree(t *testing.T) {
	requireAttrSourceGit(t)
	dir := attrRepo(t, false)
	t.Chdir(dir)
	log := installFakeGit(t, "")

	const head = "refs/heads/feature"
	if _, err := BuildContext(headSources("main", head), ContextOpts{MaxDiffBytes: 80_000}); err != nil {
		t.Fatalf("BuildContext: %v", err)
	}

	seen := map[string]int{}
	for _, call := range log.calls(t) {
		name := subcommand(call)
		if name != "diff" && name != "check-attr" {
			continue
		}
		kind := name
		if slices.Contains(call, "--name-status") {
			kind = "diff --name-status"
		}
		seen[kind]++
		if !slices.Contains(call, "--attr-source="+head) {
			t.Errorf("git %v reads attributes without --attr-source=%s", call, head)
		}
	}
	for _, kind := range []string{"diff", "diff --name-status", "check-attr"} {
		if seen[kind] == 0 {
			t.Errorf("no git %s call was made, so its attribute source went unchecked (saw %v)", kind, seen)
		}
	}
}

func TestBuildContext_OldGitFallsBackToTheWorkingTree(t *testing.T) {
	dir := attrRepo(t, false)
	t.Chdir(dir)
	log := installFakeGit(t, "2.39.0")

	if ok, installed := HeadAttributesSupported(); ok || installed != "2.39.0" {
		t.Fatalf("HeadAttributesSupported() = %v, %q, want false, 2.39.0", ok, installed)
	}
	ctx, err := BuildContext(headSources("main", "refs/heads/feature"), ContextOpts{MaxDiffBytes: 80_000})
	if err != nil {
		t.Fatalf("BuildContext on a git without --attr-source: %v", err)
	}

	for _, call := range log.calls(t) {
		for _, arg := range call {
			if strings.HasPrefix(arg, "--attr-source") || strings.HasPrefix(arg, "--source") {
				t.Errorf("git %v passes %s to a git that does not have it", call, arg)
			}
		}
	}
	if diff := ctx.Sources["git_diff:main"]; !strings.Contains(diff, "f1.txt") {
		t.Errorf("the diff lost f1.txt:\n%s", diff)
	}
	// Without --attr-source the working tree decides, which is the behavior
	// before the head option existed. The classification follows the same
	// attributes the diff used, so a -diff file is both binary and generated.
	if !slices.Contains(ctx.Diff.GeneratedPaths, "f1.txt") {
		t.Errorf("generated paths = %v, want f1.txt: the diff and the classification must read the same attributes", ctx.Diff.GeneratedPaths)
	}
}

func TestGeneratedByAttrs_RunsFromTheWorkTreeRoot(t *testing.T) {
	requireAttrSourceGit(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	writeRepoFile(t, dir, ".gitattributes", "web/dist/*.js linguist-generated\n")
	writeRepoFile(t, dir, "web/dist/a.js", "var a=1;\n")
	writeRepoFile(t, dir, "pkg/keep.txt", "keeps pkg\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "generated files")
	// A diff names files from the root of the work tree whatever directory the
	// command ran in.
	paths := []string{"web/dist/a.js"}

	for _, tc := range []struct {
		name   string
		cached bool
		source string
	}{
		{"a committed source", false, "HEAD"},
		{"the index", true, ""},
		{"the working tree", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(filepath.Join(dir, "pkg"))
			generated := generatedByAttrs(paths, tc.cached, tc.source, "")
			if generated == nil || !generated("web/dist/a.js") {
				t.Fatalf("generatedByAttrs run from pkg/ lost the mark on web/dist/a.js: git resolves the path against the current directory")
			}
		})
	}

	t.Run("an explicit work directory is a subdirectory too", func(t *testing.T) {
		t.Chdir(t.TempDir())
		generated := generatedByAttrs(paths, false, "HEAD", filepath.Join(dir, "pkg"))
		if generated == nil || !generated("web/dist/a.js") {
			t.Fatal("generatedByAttrs with WorkDir=pkg/ lost the mark on web/dist/a.js")
		}
	})
}
