package orchestrator

import (
	"strings"
	"testing"
)

// TestSTE100MarkerPresentInOrchestratorSystemPrompts asserts the ASD-STE100
// prose block reaches the two orchestrator system prompts that bypass the
// pkg/prompts templates: commitSystemPrompt (used by CommitGenerator) and
// prSystemPrompt (used by PRCreator). This keeps docs/CLI.md's blanket
// "buckley writes commit messages, PR titles, and PR bodies in ASD-STE100"
// claim true for every code path that generates commit/PR prose.
func TestSTE100MarkerPresentInOrchestratorSystemPrompts(t *testing.T) {
	const marker = "ASD-STE100 profile:"

	cases := map[string]string{
		"commitSystemPrompt": commitSystemPrompt,
		"prSystemPrompt":     prSystemPrompt,
	}

	for name, prompt := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(prompt, marker) {
				t.Fatalf("%s missing STE-100 marker %q", name, marker)
			}
		})
	}
}
