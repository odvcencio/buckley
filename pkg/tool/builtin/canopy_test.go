package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newCanopyStub writes an executable "canopy" script into a fresh PATH
// directory and prepends it so exec.LookPath resolves to the stub instead
// of any real canopy install on the test host.
func newCanopyStub(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	stubPath := filepath.Join(dir, "canopy")
	if err := os.WriteFile(stubPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write canopy stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// clearCanopyFromPath points PATH at an empty directory so exec.LookPath
// cannot find any canopy binary, real or stubbed.
func clearCanopyFromPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

const echoArgsStub = "#!/bin/sh\nprintf 'ARGS:%s' \"$*\"\n"

func TestCanopyTools_Metadata(t *testing.T) {
	tools := []struct {
		tool interface {
			Name() string
			Description() string
			Parameters() ParameterSchema
		}
		name string
	}{
		{&CodeCallgraphTool{}, "code_callgraph"},
		{&CodeRefsTool{}, "code_refs"},
		{&CodeImpactTool{}, "code_impact"},
	}

	for _, tt := range tools {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tool.Name(); got != tt.name {
				t.Errorf("Name() = %q, want %q", got, tt.name)
			}
			if tt.tool.Description() == "" {
				t.Error("Description() should not be empty")
			}
			params := tt.tool.Parameters()
			if params.Type != "object" {
				t.Errorf("Parameters().Type = %q, want object", params.Type)
			}
			if _, ok := params.Properties["symbol"]; !ok {
				t.Error("expected a symbol property")
			}
			found := false
			for _, req := range params.Required {
				if req == "symbol" {
					found = true
				}
			}
			if !found {
				t.Error("symbol should be required")
			}
		})
	}
}

func TestCodeCallgraphTool_Execute_InvokesCanopyWithArgs(t *testing.T) {
	newCanopyStub(t, echoArgsStub)

	tool := &CodeCallgraphTool{}
	result, err := tool.Execute(map[string]any{
		"symbol": "NewRegistry",
		"path":   "pkg/tool",
		"depth":  float64(3),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	output, _ := result.Data["output"].(string)
	if !strings.HasPrefix(output, "ARGS:") {
		t.Fatalf("output = %q, want ARGS: prefix", output)
	}
	if !strings.Contains(output, "graph calls NewRegistry pkg/tool") {
		t.Errorf("output = %q, missing expected canopy subcommand/args", output)
	}
	if !strings.Contains(output, "--depth 3") {
		t.Errorf("output = %q, missing --depth 3", output)
	}
}

func TestCodeCallgraphTool_Execute_Reverse(t *testing.T) {
	newCanopyStub(t, echoArgsStub)

	tool := &CodeCallgraphTool{}
	result, err := tool.Execute(map[string]any{
		"symbol":  "Execute",
		"reverse": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output, _ := result.Data["output"].(string)
	if !strings.Contains(output, "--reverse") {
		t.Errorf("output = %q, missing --reverse", output)
	}
}

func TestCodeRefsTool_Execute_InvokesCanopyWithArgs(t *testing.T) {
	newCanopyStub(t, echoArgsStub)

	tool := &CodeRefsTool{}
	result, err := tool.Execute(map[string]any{"symbol": "Registry"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	output, _ := result.Data["output"].(string)
	if !strings.Contains(output, "search refs Registry") {
		t.Errorf("output = %q, missing expected canopy subcommand/args", output)
	}
}

func TestCodeImpactTool_Execute_InvokesCanopyWithArgs(t *testing.T) {
	newCanopyStub(t, echoArgsStub)

	tool := &CodeImpactTool{}
	result, err := tool.Execute(map[string]any{
		"symbol":    "Registry",
		"max_depth": float64(4),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	output, _ := result.Data["output"].(string)
	if !strings.Contains(output, "graph impact Registry") {
		t.Errorf("output = %q, missing expected canopy subcommand/args", output)
	}
	if !strings.Contains(output, "--max-depth 4") {
		t.Errorf("output = %q, missing --max-depth 4", output)
	}
}

func TestCanopyTools_MissingSymbol(t *testing.T) {
	newCanopyStub(t, echoArgsStub)

	for _, tool := range []interface {
		Execute(map[string]any) (*Result, error)
	}{
		&CodeCallgraphTool{}, &CodeRefsTool{}, &CodeImpactTool{},
	} {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing symbol")
		}
	}
}

func TestCanopyTools_MissingBinaryReportsInstallHint(t *testing.T) {
	clearCanopyFromPath(t)

	for name, tool := range map[string]interface {
		Execute(map[string]any) (*Result, error)
	}{
		"code_callgraph": &CodeCallgraphTool{},
		"code_refs":      &CodeRefsTool{},
		"code_impact":    &CodeImpactTool{},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := tool.Execute(map[string]any{"symbol": "Foo"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Success {
				t.Fatal("expected failure when canopy binary is missing")
			}
			if !strings.Contains(result.Error, "go install m31labs.dev/canopy/cmd/canopy@latest") {
				t.Errorf("error = %q, want install hint", result.Error)
			}
		})
	}
}

func TestCanopyTools_OutputIsBoundedAndNotesTruncation(t *testing.T) {
	// Emit far more than canopyMaxOutputBytes (8 KiB) of output.
	newCanopyStub(t, "#!/bin/sh\nhead -c 20000 /dev/zero | tr '\\0' 'A'\n")

	tool := &CodeRefsTool{}
	result, err := tool.Execute(map[string]any{"symbol": "Foo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	output, _ := result.Data["output"].(string)
	if len(output) > canopyMaxOutputBytes {
		t.Fatalf("output length = %d, want <= %d", len(output), canopyMaxOutputBytes)
	}
	truncated, _ := result.Data["truncated"].(bool)
	if !truncated {
		t.Error("expected truncated=true in Data")
	}
	if !result.ShouldAbridge {
		t.Error("expected ShouldAbridge=true when output is truncated")
	}
	if result.DisplayData == nil {
		t.Error("expected DisplayData to be set when output is truncated")
	}
}

func TestCanopyTools_CleanOutputIsNotMarkedTruncated(t *testing.T) {
	newCanopyStub(t, "#!/bin/sh\nprintf 'short output'\n")

	tool := &CodeRefsTool{}
	result, err := tool.Execute(map[string]any{"symbol": "Foo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if _, ok := result.Data["truncated"]; ok {
		t.Error("did not expect truncated key for short output")
	}
	if result.Data["output"] != "short output" {
		t.Errorf("output = %v, want %q", result.Data["output"], "short output")
	}
}

func TestCanopyTools_PathEscapesWorkdirIsRejected(t *testing.T) {
	newCanopyStub(t, echoArgsStub)

	tool := &CodeRefsTool{}
	tool.SetWorkDir(t.TempDir())
	result, err := tool.Execute(map[string]any{
		"symbol": "Foo",
		"path":   "../../etc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for path escaping workdir")
	}
}
