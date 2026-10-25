package skill

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

func TestLoader_SharedContentSurvivesCollection(t *testing.T) {
	for _, format := range []string{"legacy", "agent", "agent-markdown"} {
		t.Run(format, func(t *testing.T) {
			body := strings.Repeat("I’ll inspect → findings ✓ 🐱\n", 128)
			parse := func() (*Skill, error) {
				input := "---\nname: shared\ndescription: Shared content\n---\n" + strings.Clone(body)
				loader := NewLoader()
				switch format {
				case "legacy":
					return loader.parseSkillFile(input)
				case "agent":
					return loader.parseAgentSkillFile(input, "shared")
				default:
					return loader.parseAgentSkillFile(strings.Clone(body), "shared")
				}
			}
			first, err := parse()
			if err != nil {
				t.Fatal(err)
			}
			runtime.GC()
			second, err := parse()
			if err != nil {
				t.Fatal(err)
			}
			if first == second || first.Content != strings.TrimSpace(body) || second.Content != first.Content {
				t.Fatal("independent skill loads changed the guidance")
			}
			if unsafe.StringData(first.Content) != unsafe.StringData(second.Content) {
				t.Fatal("identical guidance retained separate body storage after collection")
			}
			runtime.KeepAlive(first)
		})
	}
}

func TestRegistry_SharedContentKeepsReloadAndActivationIndependent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	path := filepath.Join(dir, ".buckley", "skills", "shared.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		text := "---\nname: shared\ndescription: Shared guidance\nallowed_tools: [read_file]\nmetadata: {owner: original}\n---\n" + body
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	load := func(r *Registry) *Skill {
		t.Helper()
		if err := r.LoadAll(); err != nil {
			t.Fatal(err)
		}
		s := r.GetSkill("shared")
		if s == nil {
			t.Fatal("project skill was not loaded")
		}
		return s
	}
	write("Original guidance → ready ✓")
	first, second := NewRegistry(), NewRegistry()
	one, two := load(first), load(second)
	if one == two || unsafe.StringData(one.Content) != unsafe.StringData(two.Content) {
		t.Fatal("registries must share body storage while owning independent skills")
	}
	one.AllowedTools[0] = "list_directory"
	one.Metadata["owner"] = "first session"
	if two.AllowedTools[0] != "read_file" || two.Metadata["owner"] != "original" {
		t.Fatal("mutable skill fields leaked between registries")
	}
	if err := first.Activate("shared", "first session only", "user"); err != nil {
		t.Fatal(err)
	}
	if second.IsActive("shared") {
		t.Fatal("activation leaked between registries")
	}
	write("Updated guidance → fresh ✓")
	updated := load(second)
	if updated.Content != "Updated guidance → fresh ✓" || one.Content != "Original guidance → ready ✓" || !first.IsActive("shared") {
		t.Fatal("reload reused stale content or changed another session")
	}
	third := load(NewRegistry())
	if unsafe.StringData(updated.Content) != unsafe.StringData(third.Content) {
		t.Fatal("updated guidance was not shared by subsequent loads")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := second.LoadAll(); err != nil {
		t.Fatal(err)
	}
	if second.GetSkill("shared") != nil || !first.IsActive("shared") {
		t.Fatal("deleted content remained discoverable or affected another session")
	}
}
