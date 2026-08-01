package tool

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/v2/pkg/policy"
	"m31labs.dev/buckley/v2/pkg/tool/builtin"
)

func TestDerivePermissionRequest_RunShell(t *testing.T) {
	req, ok := derivePermissionRequest("run_shell", map[string]any{"command": "rm -rf ./tmp"}, "/workspace", "interactive")
	if !ok {
		t.Fatal("expected run_shell to derive a request")
	}
	if req.Tool != "run_shell" || req.Category != "shell" || req.Arg != "rm -rf ./tmp" {
		t.Fatalf("unexpected request: %+v", req)
	}
	if !req.WorkspaceRelative {
		t.Fatalf("expected a relative-path command to be workspace-relative, got %+v", req)
	}
}

func TestDerivePermissionRequest_RunShellOutsideWorkspace(t *testing.T) {
	req, ok := derivePermissionRequest("run_shell", map[string]any{"command": "rm -rf /etc/passwd"}, "/workspace", "interactive")
	if !ok {
		t.Fatal("expected run_shell to derive a request")
	}
	if req.WorkspaceRelative {
		t.Fatalf("expected an absolute path outside the workspace to be non-relative, got %+v", req)
	}
}

func TestDerivePermissionRequest_RunShellEmptyCommand(t *testing.T) {
	if _, ok := derivePermissionRequest("run_shell", map[string]any{"command": "   "}, "/workspace", ""); ok {
		t.Fatal("expected an empty command not to derive a request")
	}
}

func TestDerivePermissionRequest_FileTool(t *testing.T) {
	req, ok := derivePermissionRequest("read_file", map[string]any{"path": "/workspace/.env"}, "/workspace", "interactive")
	if !ok {
		t.Fatal("expected read_file to derive a request")
	}
	if req.Category != string(policy.CategoryFileRead) {
		t.Fatalf("expected file_read category, got %q", req.Category)
	}
	if !req.WorkspaceRelative {
		t.Fatalf("expected a path under the workspace to be relative, got %+v", req)
	}
}

func TestDerivePermissionRequest_WriteToolCategory(t *testing.T) {
	req, ok := derivePermissionRequest("write_file", map[string]any{"path": "notes.txt"}, "", "")
	if !ok {
		t.Fatal("expected write_file to derive a request")
	}
	if req.Category != string(policy.CategoryFileWrite) {
		t.Fatalf("expected file_write category, got %q", req.Category)
	}
}

func TestDerivePermissionRequest_NoRelevantArg(t *testing.T) {
	if _, ok := derivePermissionRequest("list_directory", map[string]any{}, "", ""); ok {
		t.Fatal("expected a tool with no path/command argument not to derive a request")
	}
}

func TestNewPermissionMiddleware_NilGatePassesThrough(t *testing.T) {
	called := false
	mw := NewPermissionMiddleware(nil)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})
	if _, err := exec(&ExecutionContext{ToolName: "run_shell", Params: map[string]any{"command": "rm -rf /"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected next to be called when gate is nil")
	}
}

func TestNewPermissionMiddleware_DenyBlocksExecution(t *testing.T) {
	called := false
	gate := &PermissionGate{Posture: "interactive"}
	mw := NewPermissionMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})
	result, err := exec(&ExecutionContext{
		ToolName: "read_file",
		Params:   map[string]any{"path": ".env"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("expected next not to be called for a denied .env read")
	}
	if result == nil || result.Success {
		t.Fatalf("expected a denial result, got %#v", result)
	}
	if !strings.Contains(result.Error, "permission denied") {
		t.Fatalf("expected a permission-denied error, got %q", result.Error)
	}
}

func TestNewPermissionMiddleware_AskParksUnderUnattendedPosture(t *testing.T) {
	sink := policy.NewParkedDecisionLog()
	gate := &PermissionGate{
		WorkspaceRoot:    "/workspace",
		Posture:          policy.PostureUnattended,
		ParkAskDecisions: true,
		ParkedSink:       sink,
	}
	called := false
	mw := NewPermissionMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})

	result, err := exec(&ExecutionContext{
		ToolName: "run_shell",
		CallID:   "call-1",
		Params:   map[string]any{"command": "rm -rf /etc/passwd"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("expected next not to be called for a parked ask decision")
	}
	if result == nil || result.Success {
		t.Fatalf("expected a non-success parked result, got %#v", result)
	}
	parked, _ := result.Data["parked"].(bool)
	if !parked {
		t.Fatalf("expected result.Data[parked]=true, got %#v", result.Data)
	}

	items := sink.List()
	if len(items) != 1 {
		t.Fatalf("expected one parked decision, got %d", len(items))
	}
	if items[0].Tool != "run_shell" || items[0].ID != "call-1" {
		t.Fatalf("unexpected parked decision: %+v", items[0])
	}
}

func TestNewPermissionMiddleware_AskPassesThroughWhenNotParking(t *testing.T) {
	gate := &PermissionGate{WorkspaceRoot: "/workspace", Posture: policy.PostureInteractive, ParkAskDecisions: false}
	called := false
	mw := NewPermissionMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})

	// A destructive command outside the workspace triggers the built-in
	// "ask" rule, but the interactive posture (ParkAskDecisions=false) must
	// defer to the existing approval chain rather than blocking here.
	if _, err := exec(&ExecutionContext{
		ToolName: "run_shell",
		Params:   map[string]any{"command": "rm -rf /etc/passwd"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected next to be called when the posture doesn't park ask decisions")
	}
}

func TestNewPermissionMiddleware_AllowPassesThrough(t *testing.T) {
	gate := &PermissionGate{Posture: policy.PostureInteractive}
	called := false
	mw := NewPermissionMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})
	if _, err := exec(&ExecutionContext{
		ToolName: "run_shell",
		Params:   map[string]any{"command": "go test ./..."},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected next to be called for an unmatched command")
	}
}

func TestNewPermissionMiddleware_PostureLayerComposesWithBuiltins(t *testing.T) {
	sink := policy.NewParkedDecisionLog()
	gate := &PermissionGate{
		Layers:           []policy.PermissionLayer{policy.UnattendedPostureLayer()},
		Posture:          policy.PostureUnattended,
		ParkAskDecisions: true,
		ParkedSink:       sink,
	}
	mw := NewPermissionMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{Success: true}, nil
	})

	result, err := exec(&ExecutionContext{
		ToolName: "run_shell",
		Params:   map[string]any{"command": "git push origin main"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected git push to be parked under the unattended posture layer, got %#v", result)
	}
	parked, _ := result.Data["parked"].(bool)
	if !parked {
		t.Fatalf("expected a parked result, got %#v", result)
	}
}

func TestEmptyWorkspaceRootTreatsPathsAsOutside(t *testing.T) {
	if isWorkspaceRelative("/etc/passwd", "") {
		t.Fatal("empty workspace root must not classify paths as workspace-relative")
	}
	if isWorkspaceRelative("relative/file.go", "  ") {
		t.Fatal("blank workspace root must not classify paths as workspace-relative")
	}
}
