package rules

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/types"
)

func TestArbiterSandboxResolver_ShellSubagent(t *testing.T) {
	engine := mustNewTestEngine(t)
	adapter := NewEngineAdapter(engine)
	resolver := NewArbiterSandboxResolver(adapter)

	level := resolver.ForTool("bash", "subagent", 0)
	if level != types.SandboxWorkspace {
		t.Errorf("level = %v, want SandboxWorkspace", level)
	}
}

func TestArbiterSandboxResolver_Browser(t *testing.T) {
	engine := mustNewTestEngine(t)
	adapter := NewEngineAdapter(engine)
	resolver := NewArbiterSandboxResolver(adapter)

	level := resolver.ForTool("web_fetch", "interactive", 0)
	if level != types.SandboxNone {
		t.Errorf("level = %v, want SandboxNone", level)
	}
}

func TestArbiterSandboxResolver_DefaultNone(t *testing.T) {
	engine := mustNewTestEngine(t)
	adapter := NewEngineAdapter(engine)
	resolver := NewArbiterSandboxResolver(adapter)

	level := resolver.ForTool("read_file", "subagent", 0)
	if level != types.SandboxNone {
		t.Errorf("level = %v, want SandboxNone for read_file", level)
	}
}
