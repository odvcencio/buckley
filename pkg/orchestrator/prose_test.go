package orchestrator

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/prompts"
)

func TestSystemPrompts_RegisterGuidance(t *testing.T) {
	for _, tc := range []struct{ name, prompt, block string }{
		{"commit", commitSystemPrompt, prompts.CommitProseBlock()},
		{"pr", prSystemPrompt, prompts.PRProseBlock()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.prompt, tc.block) {
				t.Fatal("missing register guidance")
			}
			if strings.Contains(tc.prompt, "ASD-STE100") {
				t.Fatal("prompt contains superseded standard")
			}
		})
	}
}
