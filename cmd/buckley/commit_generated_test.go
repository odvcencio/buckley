package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func stageRepo(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(dir)
}

func TestGeneratedCommitRuntime(t *testing.T) {
	attrs := ".gitattributes"
	t.Run("generated only skips the model", func(t *testing.T) {
		stageRepo(t, map[string]string{
			attrs:                 "client/js/*.js linguist-generated\n",
			"client/js/bundle.js": "var zorblax=1;\n",
		})
		// .gitattributes itself is a source file, so stage only the bundle.
		rt := generatedCommitRuntime(commitCommandOptions{paths: []string{"client/js/bundle.js"}}, repoOpState{Kind: opNone})
		if rt == nil {
			t.Fatal("expected a fixed runtime")
		}
		res, err := rt.runner.Run(context.Background())
		if err != nil || res.Commit == nil {
			t.Fatalf("run: %v %v", res, err)
		}
		if got := res.Commit.Header(); got != "update(client): regenerate bundle.js" {
			t.Fatalf("header = %q", got)
		}
	})
	t.Run("mixed commit calls the model", func(t *testing.T) {
		stageRepo(t, map[string]string{
			attrs:                 "client/js/*.js linguist-generated\n",
			"client/js/bundle.js": "var a=1;\n",
			"main.go":             "package main\n",
		})
		if rt := generatedCommitRuntime(commitCommandOptions{}, repoOpState{Kind: opNone}); rt != nil {
			t.Fatal("mixed commit must use the model")
		}
	})
	t.Run("merge in progress calls the model", func(t *testing.T) {
		stageRepo(t, map[string]string{attrs: "*.js linguist-generated\n", "a.js": "x\n"})
		if rt := generatedCommitRuntime(commitCommandOptions{paths: []string{"a.js"}}, repoOpState{Kind: opMerge}); rt != nil {
			t.Fatal("merge commits must not use the template")
		}
	})
}
