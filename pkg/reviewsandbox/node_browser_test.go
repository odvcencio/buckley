package reviewsandbox

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNodeBrowserRuntimeOnlyExposesExplicitExecutables(t *testing.T) {
	root, runtimeDir := t.TempDir(), t.TempDir()
	browserDir := filepath.Join(root, "browser")
	if err := os.Mkdir(browserDir, 0o700); err != nil {
		t.Fatal(err)
	}
	browser := filepath.Join(browserDir, "chrome")
	if err := os.WriteFile(browser, []byte("ELF browser"), 0o700); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(root, "chrome-wrapper")
	content := []byte("#!/bin/sh\nexec " + browser + " --no-sandbox \"$@\"\n")
	if err := os.WriteFile(wrapper, content, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLIDES_BROWSER", browser)
	t.Setenv("SLIDES_CHROME", wrapper)
	t.Setenv("SLIDES_PLAYWRIGHT_MODULE", "/outside/unverified-module")
	roots, env, err := prepareNodeBrowserRuntime(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roots, []string{browserDir}) {
		t.Fatalf("read roots=%v", roots)
	}
	if env["SLIDES_BROWSER"] != browser || len(env) != 2 {
		t.Fatalf("environment=%v", env)
	}
	if filepath.Dir(env["SLIDES_CHROME"]) != runtimeDir {
		t.Fatal("wrapper was not copied into private runtime")
	}
	got, err := os.ReadFile(env["SLIDES_CHROME"])
	if err != nil || string(got) != string(content) {
		t.Fatal("wrapper contents changed")
	}
	resources := filepath.Join(root, "chrome-distribution")
	if err := os.Mkdir(resources, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "icudtl.dat"), []byte("ICU"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(resources, "icudtl.dat"), filepath.Join(browserDir, "icudtl.dat")); err != nil {
		t.Fatal(err)
	}
	roots, _, err = prepareNodeBrowserRuntime(runtimeDir)
	if err != nil || !reflect.DeepEqual(roots, canonicalExistingDirectories([]string{browserDir, resources})) {
		t.Fatalf("resource roots=%v error=%v", roots, err)
	}
	t.Setenv("SLIDES_BROWSER", "./relative")
	if _, _, err := prepareNodeBrowserRuntime(runtimeDir); err == nil {
		t.Fatal("relative browser accepted")
	}
	t.Setenv("SLIDES_BROWSER", root)
	if _, _, err := prepareNodeBrowserRuntime(runtimeDir); err == nil {
		t.Fatal("directory browser accepted")
	}
}

func TestNativeNodeLauncherRetainsNetworkAndFilesystemIsolation(t *testing.T) {
	root, runtimeDir, dependencies := t.TempDir(), t.TempDir(), t.TempDir()
	e := NewExecutor()
	var invoked commandInvocation
	e.run = func(_ context.Context, invocation commandInvocation, _ int) (commandOutput, error) {
		invoked = invocation
		return commandOutput{ExitCode: 0}, nil
	}
	_, _, failure := e.runViaNativeGo(context.Background(), "/usr/bin/bwrap", launchParams{
		root: root, workDir: runtimeDir, resolved: "/usr/bin/npm", runtimeDir: runtimeDir,
		plan: plan{command: "npm", args: []string{"--offline", "run", "test"}}, readRoots: []string{dependencies},
		environment: map[string]string{"SLIDES_BROWSER": "/explicit/chrome"},
	})
	if failure != "" {
		t.Fatal(failure)
	}
	joined := strings.Join(invoked.Args, "\n")
	for _, expected := range []string{"--unshare-net", "--clearenv", "--remount-ro\n/tmp", "--ro-bind\n" + root + "\n" + root, "--ro-bind\n" + dependencies + "\n" + dependencies, "--setenv\nSLIDES_BROWSER\n/explicit/chrome", "--offline\nrun\ntest"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("native Node launch omitted %q", expected)
		}
	}
	if strings.Contains(joined, "SLIDES_PLAYWRIGHT_MODULE") {
		t.Fatal("unverified library override inherited")
	}
}

func TestNodeOptionalPackageRequiresExplicitUnsupportedOS(t *testing.T) {
	for _, test := range []struct {
		name string
		pkg  nodeLockPackage
		want bool
	}{
		{"Darwin-only optional on Linux", nodeLockPackage{Optional: true, OS: []string{"darwin"}}, true},
		{"required Darwin package", nodeLockPackage{OS: []string{"darwin"}}, false},
		{"optional current platform", nodeLockPackage{Optional: true, OS: []string{"darwin", "linux"}}, false},
		{"optional no platform restriction", nodeLockPackage{Optional: true}, false},
		{"optional any platform", nodeLockPackage{Optional: true, OS: []string{"any"}}, false},
		{"ambiguous exclusion", nodeLockPackage{Optional: true, OS: []string{"!darwin"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := nodeOptionalPackageExcludesOS(test.pkg, "linux"); got != test.want {
				t.Fatalf("got %v want %v", got, test.want)
			}
		})
	}
}

// Review of #471, finding 5: the distribution launcher finds its browser next
// to itself. Copied into the runtime folder it would run a browser that is not
// there, so it is refused with a clear message.
func TestNodeBrowserRuntimeRefusesSelfLocatingLauncher(t *testing.T) {
	root := t.TempDir()
	launcher := filepath.Join(root, "google-chrome")
	script := "#!/bin/bash\nHERE=\"$(dirname \"$(readlink -f \"$0\")\")\"\nexec \"$HERE/chrome\" \"$@\"\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLIDES_BROWSER", "")
	t.Setenv("SLIDES_CHROME", launcher)
	_, _, err := prepareNodeBrowserRuntime(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "looks for the browser next to itself") {
		t.Fatalf("error = %v, want the launcher refused with a clear message", err)
	}
	// Review of #471, second round: the error reaches the model, so it must not
	// carry the host path of the launcher.
	if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), launcher) || !strings.Contains(err.Error(), "SLIDES_CHROME") {
		t.Fatalf("the error leaks the host path or lacks the variable name: %v", err)
	}
}
