package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunWithOutput_PlanOnlyHasNoDockerOrArtifactSideEffects(t *testing.T) {
	parent := t.TempDir()
	gsxmail := createPlanRepo(t, parent, "gsxmail", map[string]string{"go.mod": "module example.com/gsxmail\n\ngo 1.26.0\n", "go.sum": ""})
	gosx := createPlanRepo(t, parent, "gosx", map[string]string{
		"go.mod": "module example.com/gosx\n\ngo 1.26.0\n", "go.sum": "",
		"editor/go.mod": "module example.com/editor\n\ngo 1.26.0\nreplace example.com/gosx => ..\n", "editor/go.sum": "",
		"cmd/buildbootstrap/go.mod": "module example.com/bootstrap\n\ngo 1.26.0\n", "cmd/buildbootstrap/go.sum": "",
	})
	tqwebp := createPlanRepo(t, parent, "tqwebp", map[string]string{
		"go.mod": "module example.com/tqwebp\n\ngo 1.26.0\n", "go.sum": "",
		"bench/deepteams/go.mod": "module example.com/deepteams\n\ngo 1.26.0\nreplace example.com/tqwebp => ../..\n", "bench/deepteams/go.sum": "",
	})
	artifact := filepath.Join(parent, "must-not-exist")
	var output bytes.Buffer
	err := runWithOutput(context.Background(), []string{
		"--assets", planAssetsRoot(t), "--gsxmail", gsxmail, "--gosx", gosx, "--tqwebp", tqwebp,
	}, &output)
	if err != nil {
		t.Fatalf("plan run: %v", err)
	}
	if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plan created artifact path: %v", err)
	}
	var got summary
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("decode plan summary: %v", err)
	}
	if got.Status != "plan_only" || got.NetworkUsed || got.ConfigModified || got.Worker != nil || got.ContextSHA256 == "" || got.ModuleLockSHA256 == "" {
		t.Fatalf("plan summary = %+v", got)
	}
}

func TestRunWithOutput_ExecutionRequiresAllOptInsBeforeMutation(t *testing.T) {
	t.Setenv(provisionOptIn, "")
	output := filepath.Join(t.TempDir(), "artifacts")
	args := []string{"--assets", "/missing", "--gsxmail", "/missing", "--gosx", "/missing", "--tqwebp", "/missing", "--output", output, "--execute", "--allow-network"}
	if err := runWithOutput(context.Background(), args, &bytes.Buffer{}); err == nil {
		t.Fatal("execution without environment opt-in accepted")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected execution mutated output: %v", err)
	}
	if err := runWithOutput(context.Background(), []string{"--assets", "/missing", "--gsxmail", "/missing", "--gosx", "/missing", "--tqwebp", "/missing", "--allow-network"}, &bytes.Buffer{}); err == nil {
		t.Fatal("network authorization without execute accepted")
	}
}

func TestSafeArtifactPath_RejectsSourceOverlap(t *testing.T) {
	root := t.TempDir()
	for _, output := range []string{filepath.Join(root, "artifacts"), filepath.Join(root, "nested", "artifacts")} {
		if _, err := safeArtifactPath(output, []string{root}); err == nil {
			t.Fatalf("source-overlapping output %q accepted", output)
		}
	}
	out := filepath.Join(t.TempDir(), "artifacts")
	if got, err := safeArtifactPath(out, []string{root}); err != nil || got != out {
		t.Fatalf("disjoint artifact path = %q, %v", got, err)
	}
}

func createPlanRepo(t *testing.T, parent, name string, files map[string]string) string {
	t.Helper()
	root := filepath.Join(parent, name)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	files["LICENSE"] = "MIT License\n"
	for path, content := range files {
		absolute := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	planGit(t, root, "init", "-q")
	planGit(t, root, "add", "--", ".")
	planGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
	return root
}

func planGit(t *testing.T, root string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	cmd := exec.Command("/usr/bin/git", commandArgs...)
	cmd.Env = []string{"HOME=/nonexistent", "LC_ALL=C", "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1"}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fixture: %v (%s)", err, output)
	}
}

func planAssetsRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func TestRunWithOutput_RejectsUnknownAndTrailingArguments(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"trailing"}} {
		if err := runWithOutput(context.Background(), args, &strings.Builder{}); err == nil {
			t.Fatalf("arguments %v accepted", args)
		}
	}
}
