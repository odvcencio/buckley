package commands

import "testing"

func TestSuppliedBodyTemplateGuard(t *testing.T) {
	tmpl := ParsePRTemplate("## Summary\n\n## Tests\n- [ ] Unit tests\n- [ ] Race test\n")
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"valid", "## Summary\nChange\n## Tests\n- [x] Unit tests\n- [ ] Race test\n", true},
		{"extra material", "## Notes\n- [ ] Own item\n## Summary\nChange\n## Tests\n- [ ] Before\n- [x] Unit tests\n- [ ] Other\n- [x] Race test\n## More\n- [ ] Extra\n", true},
		{"missing heading", "## Summary\n- [ ] Unit tests\n- [ ] Race test\n", false},
		{"missing task", "## Summary\n## Tests\n- [ ] Unit tests\n", false},
		{"wrong section", "## Summary\n- [ ] Unit tests\n## Tests\n- [ ] Race test\n", false},
		{"fenced heading", "## Summary\n```\n## Tests\n- [ ] Unit tests\n- [ ] Race test\n```\n", false},
		{"commented task", "## Summary\n## Tests\n<!--\n- [ ] Unit tests\n-->\n- [ ] Race test\n", false},
		{"fenced task", "## Summary\n## Tests\n```\n- [ ] Unit tests\n```\n- [ ] Race test\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tmpl.ValidateSuppliedBody(tc.body)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v, want success=%v", err, tc.ok)
			}
		})
	}
}
