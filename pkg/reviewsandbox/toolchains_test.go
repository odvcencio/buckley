package reviewsandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestVerificationPlan_NodeLockfile(t *testing.T) {
	for _, tc := range []struct {
		lock, command string
		args          []string
	}{
		{"package-lock.json", "npm", []string{"--offline", "run", "test", "--", "--testNamePattern", "bird"}},
		{"pnpm-lock.yaml", "pnpm", []string{"run", "test", "--testNamePattern", "bird"}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			root := t.TempDir()
			writeToolchainFixture(t, filepath.Join(root, "package.json"), `{"scripts":{"test":"node --test"}}`)
			writeToolchainFixture(t, filepath.Join(root, tc.lock), "")
			got, err := verificationPlan(KindTest, LanguageNode, "bird", root)
			if err != nil || got.command != tc.command || !reflect.DeepEqual(got.args, tc.args) {
				t.Fatalf("plan = %#v, error = %v", got, err)
			}
		})
	}
}

func TestTrustedLookPath_UserToolchains(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	for _, rel := range []string{".nvm/versions/node/v22.9.0/bin/node", ".nvm/versions/node/v22.10.0/bin/node", ".local/share/buckley/review-python/bin/python3", ".local/bin/pnpm"} {
		path := filepath.Join(home, rel)
		writeToolchainFixture(t, path, "#!/bin/sh\nexit 0\n")
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, rel := range map[string]string{"node": ".nvm/versions/node/v22.10.0/bin/node", "python3": ".local/share/buckley/review-python/bin/python3", "pnpm": ".local/bin/pnpm"} {
		got, err := trustedLookPath(name)
		if err != nil || got != filepath.Join(home, rel) {
			t.Fatalf("%s = %q, error = %v", name, got, err)
		}
	}
	venv := filepath.Join(home, ".local/share/buckley/review-python")
	writeToolchainFixture(t, filepath.Join(venv, "pyvenv.cfg"), "home = /usr/bin\n")
	if roots := strings.Join(reviewReadRoots("true"), "\n"); !strings.Contains(roots, venv) {
		t.Fatalf("Python environment omitted from read roots: %s", roots)
	}
}

func TestProjectNodeDependencies_PnpmLockIdentity(t *testing.T) {
	for _, scenario := range []string{"match", "different install", "source mismatch", "escaping symlink"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newNodeProjectionFixture(t)
			lock := "lockfileVersion: '9.0'\nimporters:\n  .: {}\n"
			for _, root := range []string{filepath.Join(fixture.snapshotRoot, "docs"), fixture.sourcePackage} {
				writeToolchainFixture(t, filepath.Join(root, "pnpm-lock.yaml"), lock)
			}
			installed := filepath.Join(fixture.nodeModules, ".pnpm", "lock.yaml")
			writeToolchainFixture(t, installed, lock)
			switch scenario {
			case "different install":
				writeToolchainFixture(t, installed, "lockfileVersion: '9.0'\nimporters: {}\n")
			case "source mismatch":
				writeToolchainFixture(t, filepath.Join(fixture.sourcePackage, "pnpm-lock.yaml"), "lockfileVersion: '6.0'\n")
			case "escaping symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(fixture.nodeModules, "escape")); err != nil {
					t.Fatal(err)
				}
			}
			_, err := projectNodeDependencies(fixture.snapshotRoot, fixture.sourceRoot, "docs", fixture.writablePackage)
			if (err == nil) != (scenario == "match") {
				t.Fatalf("projection error = %v", err)
			}
		})
	}
}

func TestClassifyVerificationRun_MissingToolchain(t *testing.T) {
	root := t.TempDir()
	writeToolchainFixture(t, filepath.Join(root, "requirements.txt"), "pytest\n")
	for _, tc := range []struct {
		name     string
		language Language
		code     int
		stderr   string
		status   Status
		hint     string
	}{
		{"pytest missing", LanguagePython, 1, "/usr/bin/python3: No module named pytest\n", StatusUnavailable, "-r requirements.txt"},
		{"python assertion", LanguagePython, 1, "FAILED test_bird.py::test_flight - AssertionError", StatusFail, ""},
		{"pnpm missing", LanguageNode, 127, "sh: 1: exec: pnpm: not found", StatusUnavailable, "npm ci"},
		{"node missing UTF-8", LanguageNode, 127, "/usr/bin/env: ‘node’: No such file or directory", StatusUnavailable, "nvm install --lts"},
		{"node assertion", LanguageNode, 1, "AssertionError: expected bird", StatusFail, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyVerificationRun(Result{}, Request{SnapshotRoot: root, Kind: KindTest}, tc.language, time.Minute, "sandbox", commandOutput{ExitCode: tc.code, Stderr: tc.stderr}, nil, nil)
			if got.Status != tc.status || !strings.Contains(got.Error, tc.hint) {
				t.Fatalf("result = %#v", got)
			}
		})
	}
}

func TestExecutor_MissingExecutableInstallHint(t *testing.T) {
	root := t.TempDir()
	writeToolchainFixture(t, filepath.Join(root, "pyproject.toml"), "[project]\nname = 'birds'\n")
	e := NewExecutor()
	e.lookPath = func(string) (string, error) { return "", errors.New("missing") }
	got := e.Verify(context.Background(), Request{SnapshotRoot: root, Kind: KindTest, Language: LanguagePython})
	if got.Status != StatusUnavailable || !strings.Contains(got.Error, "-m pip install .") || !strings.Contains(got.Error, "environment limit") {
		t.Fatalf("result = %#v", got)
	}
}

func TestWrapperRemoteCommand_UserPython(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	python := filepath.Join(home, ".local/share/buckley/review-python/bin/python3")
	writeToolchainFixture(t, python, "#!/bin/sh\nprintf 'user-python:%s' \"$1\"\n")
	if err := os.Chmod(python, 0o755); err != nil {
		t.Fatal(err)
	}
	argv := wrapperRemoteCommand(".", "python3", []string{"-m"})
	output, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err != nil || string(output) != "user-python:-m" {
		t.Fatalf("output = %s, error = %v", output, err)
	}
}

func TestWrapperRemoteCommand_UserNode(t *testing.T) {
	for _, manager := range []string{"npm", "pnpm"} {
		t.Run(manager, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			for _, version := range []string{"v22.9.0", "v22.10.0"} {
				bin := filepath.Join(home, ".nvm/versions/node", version, "bin")
				for name, script := range map[string]string{
					"node": "#!/bin/sh\nprintf '%s:%s' '" + version + "' \"$1\"\n",
					"npm":  "#!/bin/sh\nexec node \"$@\"\n",
				} {
					path := filepath.Join(bin, name)
					writeToolchainFixture(t, path, script)
					if err := os.Chmod(path, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			pnpm := filepath.Join(home, ".local/bin/pnpm")
			writeToolchainFixture(t, pnpm, "#!/bin/sh\nexec node \"$@\"\n")
			if err := os.Chmod(pnpm, 0o755); err != nil {
				t.Fatal(err)
			}
			argv := wrapperRemoteCommand(".", manager, []string{"run"})
			output, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
			if err != nil || string(output) != "v22.10.0:run" {
				t.Fatalf("output = %s, error = %v", output, err)
			}
		})
	}
}

func TestCodexSandbox_CommonToolchains(t *testing.T) {
	if os.Getenv("BUCKLEY_TEST_CODEX_SANDBOX") != "1" {
		t.Skip("set BUCKLEY_TEST_CODEX_SANDBOX=1 to exercise installed toolchains")
	}
	for _, language := range []string{"python", "npm", "pnpm"} {
		t.Run(language, func(t *testing.T) {
			root, source := t.TempDir(), t.TempDir()
			request := Request{SnapshotRoot: root, SourceRoot: source, Kind: KindTest, Language: LanguagePython}
			if language == "python" {
				writeToolchainFixture(t, filepath.Join(root, "test_bird.py"), "def test_bird():\n    assert 2 + 2 == 4\n")
			} else {
				request.Language = LanguageNode
				manifest := `{"name":"birds","scripts":{"test":"node --test test.cjs"}}`
				for _, dir := range []string{root, source} {
					writeToolchainFixture(t, filepath.Join(dir, "package.json"), manifest)
					if language == "pnpm" {
						writeToolchainFixture(t, filepath.Join(dir, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\nimporters:\n  .: {}\n")
					} else {
						writeToolchainFixture(t, filepath.Join(dir, "package-lock.json"), `{"lockfileVersion":3,"packages":{}}`)
					}
				}
				if language == "pnpm" {
					writeToolchainFixture(t, filepath.Join(source, "node_modules/.pnpm/lock.yaml"), "lockfileVersion: '9.0'\nimporters:\n  .: {}\n")
				} else {
					writeToolchainFixture(t, filepath.Join(source, "node_modules/.package-lock.json"), `{"lockfileVersion":3,"packages":{}}`)
				}
				writeToolchainFixture(t, filepath.Join(root, "test.cjs"), "const { test } = require('node:test'); const assert = require('node:assert'); test('bird', () => assert.equal(2 + 2, 4));\n")
			}
			got := NewExecutorWithCodexCommand(os.Getenv("BUCKLEY_TEST_CODEX_COMMAND")).Verify(context.Background(), request)
			if got.Status != StatusPass {
				t.Fatalf("verification = %#v", got)
			}
		})
	}
}

func writeToolchainFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
