package commands

import (
	"strings"
	"testing"
)

func TestSuppliedBodyCheckboxAnnotations(t *testing.T) {
	tmpl := ParsePRTemplate("## Checks\n- [ ] Tests\n- [ ] Tests — integration\n- [ ] Docs\n")
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"annotated", "## Checks\n- [x] Tests — unit passed\n- [ ] Tests — integration — pending\n- [x] Docs — updated\n", true},
		{"annotation contains separator", "## Checks\n- [x] Tests — unit — passed\n- [x] Tests — integration — passed\n- [ ] Docs\n", true},
		{"longer task cannot replace shorter", "## Checks\n- [x] Tests — integration\n- [x] Tests — integration — passed\n- [ ] Docs\n", false},
		{"malformed longer task cannot fall back", "## Checks\n- [x] Tests — integration-more\n- [x] Tests — integration\n- [ ] Docs\n", false},
		{"missing separator", "## Checks\n- [x] Tests passed\n- [x] Tests — integration\n- [ ] Docs\n", false},
		{"empty annotation", "## Checks\n- [x] Tests — \n- [x] Tests — integration\n- [ ] Docs\n", false},
		{"changed wording", "## Checks\n- [x] Test — passed\n- [x] Tests — integration\n- [ ] Docs\n", false},
		{"wrong order", "## Checks\n- [x] Tests — integration\n- [x] Tests — passed\n- [ ] Docs\n", false},
		{"fenced annotation", "## Checks\n```\n- [x] Tests — passed\n```\n- [ ] Tests — integration\n- [ ] Docs\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tmpl.ValidateSuppliedBody(tc.body)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v, want success=%v", err, tc.ok)
			}
		})
	}
}

func TestSuppliedBodyDuplicateAnnotatedRequirements(t *testing.T) {
	tmpl := ParsePRTemplate("## Checks\n- [ ] Tests\n- [ ] Tests\n")
	if err := tmpl.ValidateSuppliedBody("## Checks\n- [x] Tests — passed\n"); err == nil {
		t.Fatal("one annotated checkbox satisfied two requirements")
	}
	if err := tmpl.ValidateSuppliedBody("## Checks\n- [x] Tests — passed\n- [ ] Tests — pending\n"); err != nil {
		t.Fatal(err)
	}
}

func TestSuppliedBodyAnnotationUsesWholeTemplateInventory(t *testing.T) {
	tmpl := ParsePRTemplate("## First\n- [ ] Tests\n## Second\n- [ ] Tests — integration\n")
	body := "## First\n- [x] Tests — integration — passed\n## Second\n- [x] Tests — integration\n"
	if err := tmpl.ValidateSuppliedBody(body); err == nil {
		t.Fatal("longer item in another section satisfied the shorter requirement")
	}
}

func TestSuppliedBodyAnnotatedGotreesitterTemplate(t *testing.T) {
	raw := readGotreesitterTemplate(t)
	original := "- [ ] Affected parity validation ran in Docker, one language or grammar at a time"
	if !strings.Contains(raw, original) {
		t.Fatal("template fixture changed")
	}
	body := strings.Replace(raw, original, strings.Replace(original, "[ ]", "[x]", 1)+" — JSON and Python passed", 1)
	if err := ParsePRTemplate(raw).ValidateSuppliedBody(body); err != nil {
		t.Fatal(err)
	}
}
