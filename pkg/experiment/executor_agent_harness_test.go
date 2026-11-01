package experiment

import (
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/parallel"
)

func TestExperimentExecutor_BuildRegistryCarriesAllowAgentHarness(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, allow := range []bool{false, true} {
		executor := newOpenAIExperimentExecutor(t, "http://unused.invalid", func(cfg *config.Config) {
			cfg.Permissions.AllowAgentHarness = allow
		})
		for name, task := range map[string]*parallel.AgentTask{
			"all tools":          {},
			"explicit allowlist": {Context: map[string]string{"tools_allowed": "read_file,run_shell"}},
		} {
			registry := executor.buildRegistry(task, t.TempDir())
			if got := registry.AllowAgentHarness(); got != allow {
				t.Errorf("%s: AllowAgentHarness() = %t, want %t", name, got, allow)
			}
		}
	}
}
