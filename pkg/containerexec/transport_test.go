package containerexec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func standaloneToolFixture(t *testing.T, mode string) (*ContainerRunner, string) {
	t.Helper()
	root := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run='^TestContainerToolProcess$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(root, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BUCKLEY_CONTAINER_TEST_MODE", mode)
	t.Setenv("BUCKLEY_CONTAINER_TEST_CAPTURE", filepath.Join(root, "capture.json"))
	return NewContainerRunner(filepath.Join(root, "compose.yml"), "dev-go", root, &builtin.WriteFileTool{}), root
}

func TestContainerToolProcess(t *testing.T) {
	mode := os.Getenv("BUCKLEY_CONTAINER_TEST_MODE")
	if mode == "" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	var params map[string]any
	if err := json.NewDecoder(os.Stdin).Decode(&params); err != nil {
		os.Exit(9)
	}
	cwd, _ := os.Getwd()
	capture, _ := json.Marshal(map[string]any{"args": args, "params": params, "cwd": cwd})
	if err := os.WriteFile(os.Getenv("BUCKLEY_CONTAINER_TEST_CAPTURE"), capture, 0600); err != nil {
		os.Exit(8)
	}
	content, _ := params["content"].(string)
	result := &builtin.Result{Success: true, Data: map[string]any{"content": content, "output": content, "path": "/workspace/note.txt"}}
	switch mode {
	case "stderr":
		os.Stderr.WriteString("diagnostic separate from JSON\n")
	case "raw":
		os.Stdout.WriteString("plain /workspace output\n")
		os.Stderr.WriteString("separate diagnostic\n")
		os.Exit(0)
	case "raw-limit":
		os.Stdout.WriteString(strings.Repeat("x", 64<<10))
		os.Exit(0)
	case "empty":
		os.Exit(0)
	case "invalid":
		os.Stdout.WriteString(`{"success":true,"data":`)
		os.Exit(0)
	case "null":
		os.Stdout.WriteString("null")
		os.Exit(0)
	case "missing-success":
		os.Stdout.WriteString("{}")
		os.Exit(0)
	case "failure":
		result.Success = false
		result.Error = "specific tool failure"
	case "exit":
		json.NewEncoder(os.Stdout).Encode(result)
		os.Exit(7)
	case "output-limit":
		result.Data["content"] = strings.Repeat("x", 64<<10)
	case "diagnostic-limit":
		os.Stderr.WriteString(strings.Repeat("x", 128<<10))
		os.Exit(7)
	case "cancel":
		time.Sleep(20 * time.Second)
	}
	json.NewEncoder(os.Stdout).Encode(result)
	os.Exit(0)
}

func TestContainerRunner_PreservesJSONArguments(t *testing.T) {
	for _, content := range []string{"", "O'Reilly", "first\nsecond", `literal\ntext`, "界🙂", "'; printf unapproved; echo '", "/workspace/source.go"} {
		t.Run(content, func(t *testing.T) {
			runner, root := standaloneToolFixture(t, "stderr")
			params := map[string]any{"path": "note.txt", "content": content}
			result, err := runner.Execute(params)
			if err != nil || result == nil || !result.Success {
				t.Fatalf("execute=%+v err=%v", result, err)
			}
			if result.Data["content"] != content || result.Data["output"] != content {
				t.Fatalf("transport changed data: %+v", result)
			}
			if result.Data["path"] != filepath.Join(root, "note.txt") {
				t.Fatalf("wrong mapped path: %+v", result)
			}
			data, err := os.ReadFile(filepath.Join(root, "capture.json"))
			if err != nil {
				t.Fatal(err)
			}
			var capture struct {
				Args   []string
				Params map[string]any
				Cwd    string
			}
			if err := json.Unmarshal(data, &capture); err != nil {
				t.Fatal(err)
			}
			want := []string{"compose", "-f", filepath.Join(root, "compose.yml"), "exec", "-T", "dev-go", "write_file"}
			if !reflect.DeepEqual(capture.Args, want) || !reflect.DeepEqual(capture.Params, params) || capture.Cwd != root {
				t.Fatalf("unexpected dispatch: %+v", capture)
			}
		})
	}
}

func TestContainerRunner_RejectsUnusableResponses(t *testing.T) {
	for _, mode := range []string{"invalid", "null", "missing-success", "failure", "exit", "output-limit", "diagnostic-limit"} {
		t.Run(mode, func(t *testing.T) {
			runner, _ := standaloneToolFixture(t, mode)
			WithOutputLimit(4096)(runner)
			result, err := runner.Execute(map[string]any{"content": "hello"})
			if err != nil || result == nil || result.Success || result.Error == "" {
				t.Fatalf("expected failure: %+v %v", result, err)
			}
			if mode == "failure" && result.Error != "specific tool failure" {
				t.Fatalf("lost tool failure: %+v", result)
			}
			if mode == "output-limit" && (result.Data["output_truncated"] != true || result.Data["outcome_unknown"] != true) {
				t.Fatalf("lost truncation evidence: %+v", result)
			}
			if len(result.Error) > diagnosticBytes+512 {
				t.Fatalf("unbounded diagnostics: %d", len(result.Error))
			}
		})
	}
}

func TestContainerRunner_ExactOutputBoundary(t *testing.T) {
	runner, _ := standaloneToolFixture(t, "success")
	response, _ := json.Marshal(&builtin.Result{Success: true, Data: map[string]any{"content": "hello", "output": "hello", "path": "/workspace/note.txt"}})
	WithOutputLimit(len(response) + 1)(runner)
	result, err := runner.Execute(map[string]any{"content": "hello"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("exact response budget rejected: %+v %v", result, err)
	}
}

func TestContainerRunner_CancelsClient(t *testing.T) {
	runner, root := standaloneToolFixture(t, "cancel")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan *builtin.Result, 1)
	go func() { res, _ := runner.ExecuteWithContext(ctx, map[string]any{"content": "hello"}); done <- res }()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waiting := true
	for waiting {
		select {
		case <-deadline:
			t.Fatal("client did not start")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(root, "capture.json")); err == nil {
				waiting = false
			}
		}
	}
	cancel()
	select {
	case res := <-done:
		if res == nil || res.Success || res.Data["canceled"] != true || res.Data["outcome_unknown"] != true {
			t.Fatalf("lost cancellation evidence: %+v", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not stop after cancellation")
	}
}

func TestContainerRunner_MapsOnlyWorkspaceMetadata(t *testing.T) {
	runner := NewContainerRunner("unused", "unused", t.TempDir(), &builtin.WriteFileTool{})
	content := "/workspace stays literal"
	res := &builtin.Result{Data: map[string]any{"path": "/workspace-other/file", "output": content, "content": content}, DisplayData: map[string]any{"path": "/workspace/sub/file"}, DiffPreview: &builtin.DiffInfo{FilePath: "/workspace/sub/file", OldContent: content, NewContent: content, UnifiedDiff: content}}
	runner.mapPaths(res)
	if res.Data["path"] != "/workspace-other/file" || res.Data["output"] != content || res.Data["content"] != content || res.DiffPreview.UnifiedDiff != content || res.DiffPreview.OldContent != content {
		t.Fatalf("mapping changed content: %+v", res)
	}
	if res.DiffPreview.FilePath != filepath.Join(runner.workDir, "sub", "file") || res.DisplayData["path"] != res.DiffPreview.FilePath {
		t.Fatalf("metadata not mapped: %+v", res)
	}
}

func TestContainerRunner_PreservesPlainOutput(t *testing.T) {
	for _, mode := range []string{"raw", "empty", "raw-limit"} {
		t.Run(mode, func(t *testing.T) {
			runner, _ := standaloneToolFixture(t, mode)
			WithOutputLimit(4096)(runner)
			result, err := runner.Execute(map[string]any{"content": "hello"})
			if err != nil || result == nil || !result.Success {
				t.Fatalf("raw tool failed: %+v %v", result, err)
			}
			want := ""
			if mode == "raw-limit" {
				want = strings.Repeat("x", 4096)
				if result.Data["output_truncated"] != true {
					t.Fatal("missing raw output truncation flag")
				}
			}
			if mode == "raw" {
				want = "plain /workspace output\n"
			}
			if result.Data["output"] != want {
				t.Fatalf("raw output changed: %+v", result)
			}
			if mode == "raw" && result.Data["stderr"] != "separate diagnostic\n" {
				t.Fatalf("stderr merged into stdout: %+v", result)
			}
		})
	}
}
