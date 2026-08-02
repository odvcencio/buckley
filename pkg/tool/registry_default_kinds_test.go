package tool

import (
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

// TestApplyDefaultKinds_KeysMatchRegisteredTools guards against
// defaultToolKinds() drifting from the real Tool.Name() values.
// applyDefaultKinds silently skips any key that does not match a
// registered tool (see the `if _, exists := r.tools[name]` guard), so a
// stale or renamed key never fails a build -- it just quietly makes that
// tool render as ACP kind "other" in every client. This test builds the
// real registry, plus the two tools NewRegistry does not register itself
// (lookup_context, todo -- both wired later via EnableCodeIndex and
// SetTodoStore), and asserts every defaultToolKinds() key names a tool
// that actually exists.
func TestApplyDefaultKinds_KeysMatchRegisteredTools(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&builtin.LookupContextTool{})
	registry.Register(&builtin.TodoTool{})

	for name, kind := range defaultToolKinds() {
		if _, exists := registry.Get(name); !exists {
			t.Errorf("defaultToolKinds()[%q] = %q, but no registered tool has that name (stale/renamed key)", name, kind)
		}
	}
}

// TestApplyDefaultKinds_NoToolRendersAsOther asserts that every built-in
// tool NewRegistry registers ends up with a non-"other" ACP kind. A tool
// falling through to "other" almost always means its name changed and
// defaultToolKinds() was not updated to match.
func TestApplyDefaultKinds_NoToolRendersAsOther(t *testing.T) {
	registry := NewRegistry()
	for _, tl := range registry.List() {
		name := tl.Name()
		kind := registry.ToolKind(name)
		if kind == "" || kind == "other" {
			t.Errorf("tool %q has ACP kind %q, want a real kind from defaultToolKinds()", name, kind)
		}
	}
}
