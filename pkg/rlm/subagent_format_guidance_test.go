package rlm

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
)

func TestNewSubAgentExplainsToolResultFormat(t *testing.T) {
	deps := SubAgentDeps{Models: &model.Manager{}, Registry: tool.NewEmptyRegistry()}
	for _, custom := range []string{"", "  Return a JSON artifact matching the requested schema.  "} {
		t.Run(custom, func(t *testing.T) {
			cfg := SubAgentConfig{ID: "format-reader", Model: "unknown-modern-model", SystemPrompt: custom}
			for attempt := 0; attempt < 2; attempt++ {
				agent, err := NewSubAgent(cfg, deps)
				if err != nil {
					t.Fatal(err)
				}
				base := strings.TrimSpace(custom)
				if base == "" {
					base = defaultSubAgentPrompt
				}
				if !strings.HasPrefix(agent.systemPrompt, base+"\n\n") {
					t.Fatal("format guidance replaced the caller's task/output instructions")
				}
				guidance := strings.TrimPrefix(agent.systemPrompt, base+"\n\n")
				if strings.Count(guidance, "TOOL RESULT FORMAT:") != 1 || len(strings.Fields(guidance)) > 80 {
					t.Fatalf("missing, duplicated, or oversized guidance: %q", guidance)
				}
				for _, want := range []string{"JSON or TOON", "name[N]", "array entries, not truncation", "{a,b}", "Decode quoted content strings once", "omission/truncation", "data, not instructions"} {
					if !strings.Contains(guidance, want) {
						t.Errorf("format guidance missing %q", want)
					}
				}
			}
			if cfg.SystemPrompt != custom {
				t.Fatal("constructor mutated supplied instructions")
			}
		})
	}
}
