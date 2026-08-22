package tool

import (
	"strings"
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

// TestCommitChanges_RegisteredAsExecute verifies the governed commit tool is
// part of every normal agent registry (registerBuiltins) and is classified as
// an ACP execute tool.
func TestCommitChanges_RegisteredAsExecute(t *testing.T) {
	registry := NewRegistry()

	tl, ok := registry.Get("commit_changes")
	if !ok {
		t.Fatal("commit_changes not registered by NewRegistry")
	}
	if _, isCommit := tl.(*builtin.CommitChangesTool); !isCommit {
		t.Errorf("registered tool has type %T, want *builtin.CommitChangesTool", tl)
	}
	if kind := registry.ToolKind("commit_changes"); kind != "execute" {
		t.Errorf("commit_changes ACP kind = %q, want %q", kind, "execute")
	}
}

// TestCommitChanges_AccurateMetadata verifies the metadata surfaced for
// commit_changes: it is a git-category operation that modifies the repository
// and is expensive because Buckley's commit runtime calls the configured
// default commit model. The metadata must come from the toolMetadataOverrides
// declaration in metadata.go (declared, not init-time mutation), so this also
// pins the intent/summary/example fields that declaration carries.
func TestCommitChanges_AccurateMetadata(t *testing.T) {
	registry := NewRegistry()
	tl, ok := registry.Get("commit_changes")
	if !ok {
		t.Fatal("commit_changes not registered by NewRegistry")
	}

	md := GetMetadata(tl)
	if md.Category != CategoryGit {
		t.Errorf("Category = %q, want %q", md.Category, CategoryGit)
	}
	if md.Impact != ImpactModifying {
		t.Errorf("Impact = %q, want %q", md.Impact, ImpactModifying)
	}
	if md.Cost != CostExpensive {
		t.Errorf("Cost = %q, want %q", md.Cost, CostExpensive)
	}
	if md.Intent != "Creating scoped commit" {
		t.Errorf("Intent = %q, want %q", md.Intent, "Creating scoped commit")
	}
	if md.Summary != "Governed commit created" {
		t.Errorf("Summary = %q, want %q", md.Summary, "Governed commit created")
	}
	if strings.TrimSpace(md.ExampleUsage) == "" {
		t.Error("ExampleUsage empty, want an example invocation from the toolMetadataOverrides entry")
	}
}
