package commands

import (
	"strings"
	"testing"
)

func TestSuppliedBodyTemplateNestedAndHeadingless(t *testing.T) {
	t.Run("nested duplicate cannot cover both requirements", func(t *testing.T) {
		tmpl := ParsePRTemplate("## Parent\n- [ ] Required\n### Child\n- [ ] Required\n")
		body := "## Parent\n### Child\n- [x] Required\n"
		if err := tmpl.ValidateSuppliedBody(body); err == nil {
			t.Fatal("child checkbox satisfied missing parent checkbox")
		}
		if err := tmpl.ValidateSuppliedBody("## Parent\n- [ ] Required\n### Child\n- [x] Required\n"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("headingless template with generated headings", func(t *testing.T) {
		tmpl := ParsePRTemplate("- [ ] Required\n")
		if err := tmpl.ValidateSuppliedBody("## Summary\ntext\n## Checks\n- [x] Required\n"); err != nil {
			t.Fatal(err)
		}
		if err := tmpl.ValidateSuppliedBody("## Summary\n```\n- [ ] Required\n```\n"); err == nil {
			t.Fatal("fenced requirement accepted")
		}
	})
}

func TestSuppliedBodyTemplateRawHTMLCannotMoveCheckbox(t *testing.T) {
	tmpl := ParsePRTemplate("## Checks\n- [ ] Required\n")
	if err := tmpl.ValidateSuppliedBody("## Checks\n<h2>Elsewhere</h2>\n- [ ] Required\n"); err == nil {
		t.Fatal("raw HTML heading hid moved checkbox")
	}
}

func TestSuppliedBodyRawHTMLReorderingFailsClosed(t *testing.T) {
	tmpl := ParsePRTemplate("<h2>Intro</h2>\n\n## Checks\n- [ ] A\n")
	body := "## Checks\n\n<h2>Intro</h2>\n\n- [ ] A\n"
	if err := tmpl.ValidateSuppliedBody(body); err == nil {
		t.Fatal("raw heading moved checkbox across sections")
	}
}

func TestSuppliedBodyHeadingSearchLimitFailsClosed(t *testing.T) {
	tmpl := ParsePRTemplate(strings.Repeat("## Same\n", 8) + "- [ ] Required\n")
	body := strings.Repeat("## Same\n", 20)
	if err := tmpl.ValidateSuppliedBody(body); err == nil || !strings.Contains(err.Error(), "validation limit") {
		t.Fatalf("search limit error = %v", err)
	}
	tmpl = ParsePRTemplate(strings.Repeat("## Too many\n", 129))
	if err := tmpl.ValidateSuppliedBody(""); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("heading limit error = %v", err)
	}
}
