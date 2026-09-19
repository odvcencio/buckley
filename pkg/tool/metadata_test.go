package tool

import (
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/tool/external"
	"m31labs.dev/buckley/pkg/types"
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

func TestGetMetadata_VerificationCapabilityIsExplicit(t *testing.T) {
	runTests := GetMetadata(&builtin.RunTestsTool{})
	if runTests.Category != CategoryTesting || runTests.Impact != ImpactReadOnly || !runTests.Verification {
		t.Fatalf("run_tests metadata = %+v, want trusted readonly verification", runTests)
	}

	generate := GetMetadata(&builtin.GenerateTestTool{})
	if generate.Category != CategoryTesting || generate.Impact != ImpactModifying || generate.Verification {
		t.Fatalf("generate_test metadata = %+v, want modifying non-verification", generate)
	}

	spoof := GetMetadata(namedMetadataTestTool("external_test_probe"))
	if spoof.Category != CategoryTesting || spoof.Verification {
		t.Fatalf("inferred test-name metadata = %+v, want testing category without verification", spoof)
	}

	replacement := GetMetadata(namedMetadataTestTool("run_tests"))
	if replacement.Category != CategoryTesting || replacement.Verification {
		t.Fatalf("external run_tests metadata = %+v, want no verification by name", replacement)
	}
}

func TestGetMetadata_ExternalRunTestsOverwriteIsConservativeAndUntrusted(t *testing.T) {
	registry := NewRegistry()
	registry.Register(external.NewTool(&external.ToolManifest{
		Name:        "run_tests",
		Description: "external replacement",
		Parameters:  map[string]any{"type": "object"},
		TimeoutMs:   1000,
	}, "/bin/true"))
	current, ok := registry.Get("run_tests")
	if !ok {
		t.Fatal("run_tests missing after external overwrite")
	}
	metadata := GetMetadata(current)
	if metadata.Impact == ImpactReadOnly || metadata.Verification {
		t.Fatalf("external replacement metadata = %+v, want non-readonly and non-verification", metadata)
	}
}

func TestGetMetadata_ShippedBuiltinMutatorAndVerifierInvariants(t *testing.T) {
	registry := NewRegistry()
	knownMutators := map[string]bool{
		"write_file":             true,
		"edit_file":              true,
		"insert_text":            true,
		"delete_lines":           true,
		"apply_patch":            true,
		"search_replace":         true,
		"excel":                  true,
		"mark_conflict_resolved": true,
		"commit_changes":         true,
		"run_shell":              true,
		"run_code":               true,
		"invoke_codex":           true,
		"invoke_claude":          true,
		"invoke_buckley":         true,
		"spawn_subagent":         true,
		"rename_symbol":          true,
		"extract_function":       true,
		"generate_test":          true,
		"generate_docstring":     true,
		"create_skill":           true,
		"edit_file_terminal":     true,
	}
	trustedVerifiers := map[string]bool{
		"run_tests": true,
	}
	for _, current := range registry.List() {
		name := current.Name()
		metadata := GetMetadata(current)
		if knownMutators[name] && metadata.Impact == ImpactReadOnly {
			t.Errorf("%s metadata = %+v, want non-readonly mutator", name, metadata)
		}
		if metadata.Verification != trustedVerifiers[name] {
			t.Errorf("%s verification = %v, want %v (metadata=%+v)", name, metadata.Verification, trustedVerifiers[name], metadata)
		}
	}
	if metadata := GetMetadata(builtin.NewIndexManagementTool(nil)); metadata.Impact == ImpactReadOnly {
		t.Fatalf("manage_embeddings_index metadata = %+v, want non-readonly conditional mutator", metadata)
	}
}

type namedMetadataTestTool string

func (t namedMetadataTestTool) Name() string { return string(t) }
func (t namedMetadataTestTool) Description() string {
	return "test helper"
}
func (t namedMetadataTestTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}
func (t namedMetadataTestTool) Execute(map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true}, nil
}
