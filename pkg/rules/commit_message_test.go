package rules

import "testing"

func TestCommitMessagePolicy(t *testing.T) {
	engine, err := NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	base := func() map[string]any {
		return map[string]any{
			"deny_hits": 0, "removed_echo": 0, "sensitive_hits": 0,
			"max_bullet_words": 12, "bullet_count": 3, "generated_ratio": 0.0, "diff_files": 4,
		}
	}
	tests := []struct {
		name        string
		mutate      func(map[string]any)
		action, why string
	}{
		{"clean", func(map[string]any) {}, "allow", "ok"},
		{"deny hit", func(f map[string]any) { f["deny_hits"] = 1 }, "repair", "deny_list"},
		{"repeated deny hits", func(f map[string]any) { f["deny_hits"] = 3 }, "block", "repeated_private_content"},
		{"sensitive", func(f map[string]any) { f["sensitive_hits"] = 1 }, "repair", "sensitive_pattern"},
		{"removed echo", func(f map[string]any) { f["removed_echo"] = 2 }, "repair", "removed_echo"},
		{"removed echo in generated diff", func(f map[string]any) { f["removed_echo"] = 2; f["generated_ratio"] = 0.9 }, "allow", "ok"},
		{"long bullet", func(f map[string]any) { f["max_bullet_words"] = 21 }, "repair", "bullet_too_long"},
		{"20 word bullet", func(f map[string]any) { f["max_bullet_words"] = 20 }, "allow", "ok"},
		{"too many bullets", func(f map[string]any) { f["bullet_count"] = 6 }, "repair", "too_many_bullets"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			facts := base()
			tc.mutate(facts)
			res, err := engine.EvalStrategy("commit_message", "commit_message_policy", facts)
			if err != nil {
				t.Fatal(err)
			}
			if res.Params["action"] != tc.action || res.Params["reason"] != tc.why {
				t.Fatalf("got %v, want %s/%s", res.Params, tc.action, tc.why)
			}
		})
	}
}
