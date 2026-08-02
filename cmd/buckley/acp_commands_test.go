package main

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/skill"
)

// TestBuildACPAvailableCommands_ListsSkillsSorted locks S6: every
// registered skill becomes an available command, sorted by name, named
// after the skill itself so a client's command palette can invoke it
// directly.
func TestBuildACPAvailableCommands_ListsSkillsSorted(t *testing.T) {
	t.Parallel()

	registry := skill.NewRegistry()
	mustRegisterSkill(t, registry, &skill.Skill{Name: "release-notes", Description: "Draft release notes"})
	mustRegisterSkill(t, registry, &skill.Skill{Name: "code-review", Description: "Review code"})

	commands := buildACPAvailableCommands(registry)
	if len(commands) != 2 {
		t.Fatalf("commands = %+v, want 2 entries", commands)
	}
	if commands[0].Name != "code-review" || commands[1].Name != "release-notes" {
		t.Fatalf("commands = %+v, want sorted [code-review, release-notes]", commands)
	}
	if commands[0].Description != "Review code" {
		t.Fatalf("commands[0].Description = %q, want %q", commands[0].Description, "Review code")
	}
}

// TestBuildACPAvailableCommands_EmptyRegistryReturnsNil matches the
// pattern buildACPModelModes/buildACPModelConfigOptions use: nothing to
// advertise means nil, not an empty-but-present list.
func TestBuildACPAvailableCommands_EmptyRegistryReturnsNil(t *testing.T) {
	t.Parallel()

	if got := buildACPAvailableCommands(skill.NewRegistry()); got != nil {
		t.Fatalf("commands = %+v, want nil", got)
	}
	if got := buildACPAvailableCommands(nil); got != nil {
		t.Fatalf("commands = %+v, want nil for nil registry", got)
	}
}

// TestSendACPAvailableCommandsUpdate_SendsCurrentList locks the wire shape
// sendACPAvailableCommandsUpdate produces: an available_commands_update
// session update carrying the current skill set.
func TestSendACPAvailableCommandsUpdate_SendsCurrentList(t *testing.T) {
	t.Parallel()

	registry := skill.NewRegistry()
	mustRegisterSkill(t, registry, &skill.Skill{Name: "code-review", Description: "Review code"})

	var got []acp.SessionUpdate
	sendACPAvailableCommandsUpdate(func(update acp.SessionUpdate) error {
		got = append(got, update)
		return nil
	}, registry)

	if len(got) != 1 {
		t.Fatalf("updates = %+v, want 1", got)
	}
	if got[0].SessionUpdate != acp.SessionUpdateAvailableCommands {
		t.Fatalf("sessionUpdate = %q, want %q", got[0].SessionUpdate, acp.SessionUpdateAvailableCommands)
	}
	if len(got[0].AvailableCommands) != 1 || got[0].AvailableCommands[0].Name != "code-review" {
		t.Fatalf("AvailableCommands = %+v, want [code-review]", got[0].AvailableCommands)
	}
}

// TestHandleACPSkillNameCommand_ActivatesByBareName locks S6's other half:
// an advertised "/<skill-name>" command must actually work when a client's
// command palette invokes it -- otherwise available_commands_update would
// advertise commands that silently do nothing.
func TestHandleACPSkillNameCommand_ActivatesByBareName(t *testing.T) {
	t.Parallel()

	registry := skill.NewRegistry()
	mustRegisterSkill(t, registry, &skill.Skill{
		Name:        "code-review",
		Description: "Review code",
		Content:     "# Review\nInspect the diff.",
	})
	runtime := skill.NewRuntimeState(func(string) {})
	state := &acpSessionState{skills: registry, skillState: runtime}

	handled, text := handleACPUserSkillCommand("/code-review", state)
	if !handled {
		t.Fatal("expected /code-review to be handled as a skill activation")
	}
	if !strings.Contains(text, "Skill 'code-review' activated") {
		t.Fatalf("activation response missing message: %q", text)
	}
	if !registry.IsActive("code-review") {
		t.Fatal("expected code-review to be active")
	}
}

// TestHandleACPSkillNameCommand_IgnoresUnknownAndGenericCommands ensures
// the bare-name shortcut only intercepts commands that actually name a
// registered skill, leaving ordinary "/" prose and the generic
// "/skill"/"/skills" commands to parseACPUserSkillCommand.
func TestHandleACPSkillNameCommand_IgnoresUnknownAndGenericCommands(t *testing.T) {
	t.Parallel()

	registry := skill.NewRegistry()
	mustRegisterSkill(t, registry, &skill.Skill{Name: "code-review", Description: "Review code"})
	state := &acpSessionState{skills: registry, skillState: skill.NewRuntimeState(func(string) {})}

	cases := []string{"/unknown-command", "/skill", "/skills", "please review /code-review later"}
	for _, prompt := range cases {
		if handled, _ := handleACPSkillNameCommand(prompt, state); handled {
			t.Errorf("handleACPSkillNameCommand(%q) = handled, want unhandled", prompt)
		}
	}
}
