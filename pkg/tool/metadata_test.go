package tool

import (
	"testing"

	"m31labs.dev/buckley/v2/pkg/tool/builtin"
	"m31labs.dev/buckley/v2/pkg/types"
)

// TestRequiredTierForTool_DynamicCodeMatchesShellExec guards against run_code
// (which executes arbitrary Python/JS/Go/Bash) silently falling back to the
// default read-only tier because its name isn't recognized by the metadata
// inference rules.
func TestRequiredTierForTool_DynamicCodeMatchesShellExec(t *testing.T) {
	shellTier := RequiredTierForTool(&builtin.ShellCommandTool{})
	codeTier := RequiredTierForTool(&builtin.DynamicCodeTool{})

	if shellTier != types.TierShellExec {
		t.Fatalf("run_shell tier = %v, want %v", shellTier, types.TierShellExec)
	}
	if codeTier != types.TierShellExec {
		t.Fatalf("run_code tier = %v, want %v", codeTier, types.TierShellExec)
	}
}

// TestGetMetadata_DynamicCodeMatchesShell asserts run_code carries the same
// category and impact as run_shell instead of the filesystem/read-only
// defaults inferMetadata falls back to for unrecognized tool names.
func TestGetMetadata_DynamicCodeMatchesShell(t *testing.T) {
	shellMeta := GetMetadata(&builtin.ShellCommandTool{})
	codeMeta := GetMetadata(&builtin.DynamicCodeTool{})

	if codeMeta.Category != CategoryShell {
		t.Fatalf("run_code category = %v, want %v", codeMeta.Category, CategoryShell)
	}
	if codeMeta.Impact != ImpactDestructive {
		t.Fatalf("run_code impact = %v, want %v", codeMeta.Impact, ImpactDestructive)
	}
	if codeMeta.Category != shellMeta.Category || codeMeta.Impact != shellMeta.Impact {
		t.Fatalf("run_code metadata %+v does not mirror run_shell metadata %+v", codeMeta, shellMeta)
	}
}
