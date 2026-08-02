package tool

import (
	"context"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

// stubVetoGate is a PluginVetoGate test double that records every call and
// returns a scripted decision.
type stubVetoGate struct {
	calls   int
	denied  bool
	plugin  string
	reason  string
	lastCtx context.Context
	lastArg map[string]any
}

func (s *stubVetoGate) Veto(ctx context.Context, toolName string, sanitizedArgs map[string]any) (bool, string, string) {
	s.calls++
	s.lastCtx = ctx
	s.lastArg = sanitizedArgs
	return s.denied, s.plugin, s.reason
}

func TestNewPluginVetoMiddleware_NilGatePassesThrough(t *testing.T) {
	called := false
	mw := NewPluginVetoMiddleware(nil)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})
	if _, err := exec(&ExecutionContext{ToolName: "write_file"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected next to be called when gate is nil")
	}
}

func TestNewPluginVetoMiddleware_Allow(t *testing.T) {
	gate := &stubVetoGate{denied: false}
	called := false
	mw := NewPluginVetoMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})
	result, err := exec(&ExecutionContext{ToolName: "read_file", Params: map[string]any{"path": "a.go"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected next to be called when the gate allows")
	}
	if result == nil || !result.Success {
		t.Fatalf("expected the next executor's success result, got %#v", result)
	}
	if gate.calls != 1 {
		t.Fatalf("expected exactly one Veto call, got %d", gate.calls)
	}
}

func TestNewPluginVetoMiddleware_Deny(t *testing.T) {
	gate := &stubVetoGate{denied: true, plugin: "guardian", reason: "not on the allowlist"}
	called := false
	mw := NewPluginVetoMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})
	result, err := exec(&ExecutionContext{ToolName: "marker_tool", Params: map[string]any{"x": 1}})
	if err == nil {
		t.Fatal("expected a non-nil error for a plugin denial")
	}
	if called {
		t.Fatal("expected next NOT to be called when the gate denies")
	}
	if result == nil || result.Success {
		t.Fatalf("expected a failure result, got %#v", result)
	}
	if !strings.Contains(result.Error, "guardian") {
		t.Errorf("expected the error to name the denying plugin, got %q", result.Error)
	}
	if !strings.Contains(result.Error, "not on the allowlist") {
		t.Errorf("expected the error to carry the plugin's reason, got %q", result.Error)
	}
}

func TestNewPluginVetoMiddleware_DenyWithEmptyReason(t *testing.T) {
	gate := &stubVetoGate{denied: true, plugin: "guardian", reason: ""}
	mw := NewPluginVetoMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{Success: true}, nil
	})
	result, err := exec(&ExecutionContext{ToolName: "marker_tool"})
	if err == nil {
		t.Fatal("expected a non-nil error")
	}
	if !strings.Contains(result.Error, "no reason given") {
		t.Errorf("expected a fallback reason, got %q", result.Error)
	}
}

func TestNewPluginVetoMiddleware_SanitizesArgsBeforeSendingToGate(t *testing.T) {
	gate := &stubVetoGate{}
	mw := NewPluginVetoMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{Success: true}, nil
	})
	_, _ = exec(&ExecutionContext{
		ToolName: "run_shell",
		Params: map[string]any{
			"command":       "echo hi",
			"api_key":       "sk-super-secret",
			ToolCallIDParam: "call-123",
		},
	})
	if gate.lastArg == nil {
		t.Fatal("expected sanitized args to be passed to the gate")
	}
	if gate.lastArg["api_key"] != "[REDACTED]" {
		t.Errorf("expected api_key to be redacted, got %v", gate.lastArg["api_key"])
	}
	if _, ok := gate.lastArg[ToolCallIDParam]; ok {
		t.Errorf("expected the internal tool-call-id param to be stripped before sending to the gate")
	}
	if gate.lastArg["command"] != "echo hi" {
		t.Errorf("expected the non-sensitive command field to survive sanitization, got %v", gate.lastArg["command"])
	}
}

func TestNewPluginVetoMiddleware_NilContextDefaultsToBackground(t *testing.T) {
	gate := &stubVetoGate{}
	mw := NewPluginVetoMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{Success: true}, nil
	})
	if _, err := exec(&ExecutionContext{ToolName: "read_file"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gate.lastCtx == nil {
		t.Fatal("expected a non-nil context to be passed to the gate")
	}
}

// --- Composition ordering: policy beats plugin ----------------------------

func TestChain_PolicyDenyBeatsPluginAllow(t *testing.T) {
	// A built-in default denies reading .env; the plugin veto gate would
	// allow everything. The composed chain must still deny: the plugin
	// never even gets a chance to weigh in, because the outer permission
	// middleware's deny short-circuits before next() reaches the veto
	// middleware.
	permGate := &PermissionGate{Posture: "interactive"}
	vetoGate := &stubVetoGate{denied: false}

	nextCalled := false
	chain := Chain(NewPermissionMiddleware(permGate), NewPluginVetoMiddleware(vetoGate))
	exec := chain(func(ctx *ExecutionContext) (*builtin.Result, error) {
		nextCalled = true
		return &builtin.Result{Success: true}, nil
	})

	result, err := exec(&ExecutionContext{
		ToolName: "read_file",
		Params:   map[string]any{"path": ".env"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected the call to be denied by policy, got %#v", result)
	}
	if !strings.Contains(result.Error, "permission denied") {
		t.Fatalf("expected a permission-denied error, got %q", result.Error)
	}
	if nextCalled {
		t.Error("expected the innermost executor never to run once policy denied")
	}
	if vetoGate.calls != 0 {
		t.Errorf("expected the plugin veto gate never to be consulted once policy denied, got %d calls", vetoGate.calls)
	}
}

func TestChain_PolicyAllowsThenPluginDenies(t *testing.T) {
	// Policy has nothing to say about this call (an ordinary workspace
	// read); the plugin veto gate denies it. The composed chain must
	// reflect the plugin's denial: veto only narrows what policy allows,
	// but it does narrow it.
	permGate := &PermissionGate{Posture: "interactive"}
	vetoGate := &stubVetoGate{denied: true, plugin: "guardian", reason: "blocked for this demo"}

	nextCalled := false
	chain := Chain(NewPermissionMiddleware(permGate), NewPluginVetoMiddleware(vetoGate))
	exec := chain(func(ctx *ExecutionContext) (*builtin.Result, error) {
		nextCalled = true
		return &builtin.Result{Success: true}, nil
	})

	result, err := exec(&ExecutionContext{
		ToolName: "read_file",
		Params:   map[string]any{"path": "main.go"},
	})
	if err == nil {
		t.Fatal("expected a non-nil error for a plugin denial")
	}
	if result == nil || result.Success {
		t.Fatalf("expected the call to be denied by the plugin, got %#v", result)
	}
	if !strings.Contains(result.Error, "guardian") {
		t.Errorf("expected the error to name the denying plugin, got %q", result.Error)
	}
	if nextCalled {
		t.Error("expected the innermost executor never to run once the plugin denied")
	}
	if vetoGate.calls != 1 {
		t.Errorf("expected the plugin veto gate to be consulted exactly once, got %d calls", vetoGate.calls)
	}
}

func TestChain_PolicyAllowsAndPluginAllows(t *testing.T) {
	permGate := &PermissionGate{Posture: "interactive"}
	vetoGate := &stubVetoGate{denied: false}

	nextCalled := false
	chain := Chain(NewPermissionMiddleware(permGate), NewPluginVetoMiddleware(vetoGate))
	exec := chain(func(ctx *ExecutionContext) (*builtin.Result, error) {
		nextCalled = true
		return &builtin.Result{Success: true}, nil
	})

	result, err := exec(&ExecutionContext{
		ToolName: "read_file",
		Params:   map[string]any{"path": "main.go"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected the call to succeed, got %#v", result)
	}
	if !nextCalled {
		t.Error("expected the innermost executor to run once both layers allow")
	}
	if vetoGate.calls != 1 {
		t.Errorf("expected the plugin veto gate to be consulted exactly once, got %d calls", vetoGate.calls)
	}
}
