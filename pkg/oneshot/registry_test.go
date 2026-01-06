package oneshot

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/tools"
)

func TestRegistry(t *testing.T) {
	r := NewRegistry()

	// Register a command
	cmd := &Command{
		Name:        "test",
		Description: "Test command",
		Tool: tools.Definition{
			Name:        "test_tool",
			Description: "Test tool",
		},
		Builtin: true,
	}

	if err := r.Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Get it back
	got, ok := r.Get("test")
	if !ok {
		t.Fatal("Get returned not found")
	}
	if got.Name != "test" {
		t.Errorf("Name = %q, want 'test'", got.Name)
	}

	// List all
	all := r.List()
	if len(all) != 1 {
		t.Errorf("List returned %d, want 1", len(all))
	}

	// List builtins
	builtins := r.ListBuiltin()
	if len(builtins) != 1 {
		t.Errorf("ListBuiltin returned %d, want 1", len(builtins))
	}

	// List plugins (should be empty)
	plugins := r.ListPlugins()
	if len(plugins) != 0 {
		t.Errorf("ListPlugins returned %d, want 0", len(plugins))
	}
}

func TestRegistryEmptyName(t *testing.T) {
	r := NewRegistry()

	err := r.Register(&Command{})
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestRegistryBuiltinPrecedence(t *testing.T) {
	r := NewRegistry()

	// Register builtin
	builtin := &Command{
		Name:    "commit",
		Builtin: true,
	}
	if err := r.Register(builtin); err != nil {
		t.Fatalf("Register builtin: %v", err)
	}

	// Try to override with plugin - should fail
	plugin := &Command{
		Name:    "commit",
		Builtin: false,
	}
	err := r.Register(plugin)
	if err == nil {
		t.Error("expected error when plugin tries to override builtin")
	}
}

func TestRegistryPluginThenBuiltin(t *testing.T) {
	r := NewRegistry()

	// Register plugin first
	plugin := &Command{
		Name:    "custom",
		Builtin: false,
	}
	if err := r.Register(plugin); err != nil {
		t.Fatalf("Register plugin: %v", err)
	}

	// Builtin can override plugin
	builtin := &Command{
		Name:    "custom",
		Builtin: true,
	}
	if err := r.Register(builtin); err != nil {
		t.Fatalf("Register builtin: %v", err)
	}

	// Should get the builtin
	got, _ := r.Get("custom")
	if !got.Builtin {
		t.Error("expected builtin to override plugin")
	}
}

func TestRegisterBuiltinHelper(t *testing.T) {
	// Use a fresh registry for this test
	old := DefaultRegistry
	DefaultRegistry = NewRegistry()
	defer func() { DefaultRegistry = old }()

	err := RegisterBuiltin("helper", "Helper command", tools.Definition{
		Name:        "helper_tool",
		Description: "Helper tool",
	})
	if err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}

	cmd, ok := DefaultRegistry.Get("helper")
	if !ok {
		t.Fatal("helper not found")
	}
	if !cmd.Builtin {
		t.Error("expected Builtin = true")
	}
}
