package builtin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunTestsTool_NpmArguments(t *testing.T) {
	for _, tc := range []struct {
		name, pattern     string
		coverage, verbose bool
		want              []string
	}{
		{name: "default", want: []string{"test", "--"}},
		{name: "coverage", coverage: true, want: []string{"test", "--", "--coverage"}},
		{name: "verbose", verbose: true, want: []string{"test", "--", "--verbose"}},
		{name: "filter with spaces", pattern: "specific case", want: []string{"test", "--", "-t", "specific case"}},
		{name: "all options", coverage: true, verbose: true, pattern: "^specific case$", want: []string{"test", "--", "--coverage", "--verbose", "-t", "^specific case$"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { execCommandContext = exec.CommandContext })
			execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				if name != "npm" || len(args) < 5 || !reflect.DeepEqual(args[:4], []string{"test", "--", "--json", "--outputFile"}) || !filepath.IsAbs(args[4]) {
					t.Fatalf("bad report flags: %s %q", name, args)
				}
				forwarded := append(append([]string{}, args[:2]...), args[5:]...)
				if !reflect.DeepEqual(forwarded, tc.want) {
					t.Fatalf("command=%s %q want npm %q", name, args, tc.want)
				}
				return exec.CommandContext(ctx, "sh", "-c", "exit 0")
			}
			_, code, _, _, err := (&RunTestsTool{}).runTestsForFramework(context.Background(), "jest", ".", tc.pattern, tc.coverage, tc.verbose)
			if err != nil || code != 0 {
				t.Fatalf("exit=%d err=%v", code, err)
			}
		})
	}
}

func TestRunTestsTool_RealNpmArgumentForwarding(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not installed")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	for name, content := range map[string]string{
		"package.json": `{"name":"buckley-npm-args","version":"1.0.0","private":true,"scripts":{"test":"node verify-args.cjs"}}`,
		"verify-args.cjs": `const assert = require('node:assert/strict');
const expected = ['--coverage', '--verbose', '-t', '^specific case$'];
assert.deepEqual(process.argv.slice(2, 4), ['--json', '--outputFile']);
assert.ok(require('node:path').isAbsolute(process.argv[4]));
assert.deepEqual(process.argv.slice(5), expected);
console.log('received exact requested options');
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tool := &RunTestsTool{}
	tool.SetWorkDir(dir)
	output, code, _, _, err := tool.runTestsForFramework(context.Background(), "jest", ".", "^specific case$", true, true)
	if err != nil || code != 0 || !strings.Contains(output, "received exact requested options") {
		t.Fatalf("exit=%d err=%v output=%s", code, err, output)
	}
}
