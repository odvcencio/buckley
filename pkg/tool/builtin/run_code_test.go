package builtin

import (
	"os/exec"
	"strings"
	"testing"
)

func TestDynamicCodeToolMetadata(t *testing.T) {
	tool := &DynamicCodeTool{}
	if tool.Name() != "run_code" {
		t.Fatalf("Name() = %q", tool.Name())
	}
	params := tool.Parameters()
	if params.Type != "object" || len(params.Required) != 2 {
		t.Fatalf("unexpected parameter schema: %+v", params)
	}
}

func TestDynamicCodeCommandEncodesSourceAndEscapesArguments(t *testing.T) {
	command, err := dynamicCodeCommand("python", "print('sensitive source')", []string{"hello world", "it's safe"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(command, "sensitive source") {
		t.Fatalf("command exposes raw source: %q", command)
	}
	if !strings.Contains(command, "base64 -d") || !strings.Contains(command, "'hello world'") {
		t.Fatalf("unexpected command: %q", command)
	}
}

func TestDynamicCodeArgsRejectsNonStrings(t *testing.T) {
	if _, err := dynamicCodeArgs([]any{"ok", 42}); err == nil {
		t.Fatal("expected non-string argument error")
	}
}

func TestDynamicCodeToolRejectsUnsupportedLanguage(t *testing.T) {
	result, err := (&DynamicCodeTool{}).Execute(map[string]any{
		"language": "brainfuck",
		"code":     "+.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "language") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDynamicCodeToolRunsPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	tool := &DynamicCodeTool{}
	result, err := tool.Execute(map[string]any{
		"language": "python",
		"code":     "import sys\nprint(int(sys.argv[1]) + int(sys.argv[2]))",
		"args":     []any{"20", "22"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("execution failed: %+v", result)
	}
	stdout, _ := result.Data["stdout"].(string)
	if strings.TrimSpace(stdout) != "42" {
		t.Fatalf("stdout = %q, want 42", stdout)
	}
	if result.Data["language"] != "python" {
		t.Fatalf("language metadata = %#v", result.Data["language"])
	}
}

func TestDynamicCodeToolRunsGo(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	tool := &DynamicCodeTool{}
	result, err := tool.Execute(map[string]any{
		"language":        "go",
		"code":            "package main\nimport \"fmt\"\nfunc main(){fmt.Println(6*7)}",
		"timeout_seconds": 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("execution failed: %+v", result)
	}
	stdout, _ := result.Data["stdout"].(string)
	if strings.TrimSpace(stdout) != "42" {
		t.Fatalf("stdout = %q, want 42", stdout)
	}
}
