package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/storage"
)

func TestNewSessionState_RestoresSkillsIndependentlyOfTranscript(t *testing.T) {
	cfg, store, workDir := newControllerSessionTestConfig(t)
	dir := filepath.Join(workDir, ".buckley", "skills")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resume.md"), []byte("---\nname: resume\ndescription: Resume skill\nallowed_tools: [activate_skill, list_directory]\n---\nretain-active-instruction-marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	createControllerTestSession(t, store, "resume", workDir, storage.SessionStatusActive, time.Now())
	load := func(history bool) *SessionState {
		t.Helper()
		sess, err := newSessionState(cfg.Config, store, workDir, nil, "resume", history, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = sess.ToolRegistry.Close()
			if sess.HookCloser != nil {
				_ = sess.HookCloser.Close()
			}
		})
		return sess
	}
	sess := load(false)
	if _, err := activateSessionSkill(sess, "resume"); err != nil {
		t.Fatal(err)
	}
	if len(sess.Conversation.Messages) != 0 {
		t.Fatal("activation duplicated instructions into transcript history")
	}
	active, err := store.GetActiveSessionSkills("resume")
	if err != nil || len(active) != 1 {
		t.Fatalf("durable skills=%v err=%v", active, err)
	}
	for _, history := range []bool{true, false} {
		resumed := load(history)
		if !resumed.SkillRegistry.IsActive("resume") || !reflect.DeepEqual(resumed.SkillState.ToolFilter(), []string{"activate_skill", "list_directory"}) {
			t.Fatal("resume lost active skill or restrictions")
		}
		// Empty history also models a retained working set that has evicted activation.
		resumed.Conversation.Messages = nil
		ctrl := &Controller{cfg: cfg.Config, workDir: workDir}
		req, enabled := ctrl.buildToolLoopRequest(resumed, "gpt-4o", true, toolLoopAllowedTools(resumed))
		if !enabled || !reflect.DeepEqual(skillDiscoveryToolNames(req.Tools), []string{"activate_skill", "list_directory"}) {
			t.Fatal("resume request exposed tools outside the restored skill")
		}
		if text := ctrl.buildSystemPrompt(resumed); strings.Count(text, "retain-active-instruction-marker") != 1 {
			t.Fatal("active guidance missing or duplicated in the system prompt")
		}
		if !resumed.SkillRegistry.GetActiveSkill("resume").ActivatedAt.Equal(active[0].ActivatedAt) {
			t.Fatal("resume changed the activation timestamp")
		}
	}
	resumed := load(true)
	result, err := resumed.ToolRegistry.ExecuteWithContext(context.Background(), "activate_skill", map[string]any{"action": "deactivate", "skill": "resume"})
	if err != nil || !result.Success {
		t.Fatalf("deactivate: result=%+v err=%v", result, err)
	}
	after := load(true)
	if after.SkillRegistry.IsActive("resume") || after.SkillState.ToolFilter() != nil || strings.Contains((&Controller{}).buildSystemPrompt(after), "retain-active-instruction-marker") {
		t.Fatal("deactivation did not survive the next resume")
	}
	rows, err := store.GetSessionSkillHistory("resume")
	if err != nil || len(rows) != 1 || rows[0].IsActive {
		t.Fatalf("resume grew durable activation history: rows=%v err=%v", rows, err)
	}
}

func TestNewSessionState_MissingActiveSkillFailsResume(t *testing.T) {
	cfg, store, workDir := newControllerSessionTestConfig(t)
	createControllerTestSession(t, store, "missing-skill", workDir, storage.SessionStatusActive, time.Now())
	if err := store.SaveSessionSkill("missing-skill", "removed-playbook", "model", "test"); err != nil {
		t.Fatal(err)
	}
	_, err := newSessionState(cfg.Config, store, workDir, nil, "missing-skill", true, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "removed-playbook") {
		t.Fatalf("missing skill silently widened permissions: err=%v", err)
	}
}
