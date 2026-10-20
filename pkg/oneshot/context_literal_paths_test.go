package oneshot

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestCommitContext_LiteralPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX filenames")
	}
	for _, name := range []string{"name.txt ", " leading.txt", "line\nname.txt", "star*.txt", ":(glob)*.txt", "   "} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			gitIn(t, dir, "init", "-q")
			t.Chdir(dir)
			for file, body := range map[string]string{name: "selected fixture\n", "unrelated.txt": "unrelated private fixture\n"} {
				if err := os.WriteFile(file, []byte(body), 0640); err != nil {
					t.Fatal(err)
				}
			}
			gitIn(t, dir, "--literal-pathspecs", "add", "--", name, "unrelated.txt")
			params := map[string]string{"staged": "true", "paths": name, "literal_paths": "true"}
			diff, stats, err := gatherGitDiffStats(params, ContextOpts{})
			if err != nil || !strings.Contains(diff, "selected fixture") || strings.Contains(diff, "unrelated private fixture") || len(stats.SourcePaths) != 1 {
				t.Fatalf("literal diff: stats=%+v error=%v diff=%q", stats, err, diff)
			}
			files, err := gatherGitFiles(params)
			if err != nil || strings.Contains(files, "unrelated.txt") {
				t.Fatalf("unrelated file leaked into context: %q %v", files, err)
			}
			if name == "   " && files != "A\t   " {
				t.Fatalf("whitespace filename lost in file context: %q", files)
			}
			stats, err = StagedDiffStats([]string{name})
			if err != nil || len(stats.SourcePaths) != 1 {
				t.Fatalf("literal generated-file classification: %+v %v", stats, err)
			}
		})
	}
}
