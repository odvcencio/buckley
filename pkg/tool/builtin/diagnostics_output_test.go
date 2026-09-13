package builtin

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPostEditDiagnostics_PreservesMainPackageFiles(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go toolchain unavailable")
	}
	t.Setenv(postEditDiagnosticsEnv, "on")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOOS", runtime.GOOS)
	t.Setenv("GOARCH", runtime.GOARCH)
	for _, tc := range []struct {
		name                         string
		existing, executable, broken bool
	}{
		{name: "no new executable"},
		{name: "preserve existing output", existing: true},
		{name: "preserve existing executable", existing: true, executable: true},
		{name: "retain compiler errors", broken: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/diagnostics\n\ngo 1.22\n"), 0644); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "cmd", "probe")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "main.go")
			code := "package main\nfunc main() {}\n"
			if tc.broken {
				code = "package main\nfunc main() { missingValue() }\n"
			}
			if err := os.WriteFile(path, []byte(code), 0644); err != nil {
				t.Fatal(err)
			}
			outputName := "probe"
			if runtime.GOOS == "windows" {
				outputName += ".exe"
			}
			sentinel := []byte("keep this existing user artifact\n")
			if tc.executable {
				if err := os.WriteFile(path, []byte("package main\nfunc main() { println(\"prior build\") }\n"), 0644); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("go", "build", "-o", filepath.Join(dir, outputName), ".")
				cmd.Dir = dir
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("build original executable: %v %s", err, output)
				}
				var err error
				sentinel, err = os.ReadFile(filepath.Join(dir, outputName))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(code), 0644); err != nil {
					t.Fatal(err)
				}
			} else if tc.existing {
				if err := os.WriteFile(filepath.Join(dir, outputName), sentinel, 0644); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			got := postEditDiagnostics(path)
			if tc.broken {
				if !strings.Contains(got, "missingValue") {
					t.Fatalf("compiler error lost: %q", got)
				}
			} else if got != "" {
				t.Fatalf("valid package diagnostic: %q", got)
			}
			after, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			names := func(entries []os.DirEntry) []string {
				result := make([]string, len(entries))
				for i, e := range entries {
					result[i] = e.Name()
				}
				return result
			}
			if !reflect.DeepEqual(names(before), names(after)) {
				t.Fatalf("probe changed package files: before=%v after=%v", names(before), names(after))
			}
			if tc.existing {
				data, err := os.ReadFile(filepath.Join(dir, outputName))
				if err != nil || string(data) != string(sentinel) {
					t.Fatalf("probe overwrote existing artifact: size=%d err=%v", len(data), err)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != code {
				t.Fatalf("source changed: %v", err)
			}
		})
	}
}
