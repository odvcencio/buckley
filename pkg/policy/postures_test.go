package policy

import "testing"

func TestSelectPosture(t *testing.T) {
	t.Run("env overrides configured default", func(t *testing.T) {
		t.Setenv(PostureEnvVar, "unattended")
		if got := SelectPosture("interactive"); got != "unattended" {
			t.Fatalf("got %q, want unattended", got)
		}
	})

	t.Run("configured default used when env unset", func(t *testing.T) {
		t.Setenv(PostureEnvVar, "")
		if got := SelectPosture("unattended"); got != "unattended" {
			t.Fatalf("got %q, want unattended", got)
		}
	})

	t.Run("falls back to interactive", func(t *testing.T) {
		t.Setenv(PostureEnvVar, "")
		if got := SelectPosture(""); got != PostureInteractive {
			t.Fatalf("got %q, want %q", got, PostureInteractive)
		}
	})
}

func TestUnattendedPostureRules_DenyOutwardBash(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{"git push", "git push origin main"},
		{"chained git push", "go build ./... && git push origin main"},
		{"gh cli", "gh pr create --title x"},
		{"chained gh cli", "cd repo && gh issue list"},
		{"curl POST", "curl -X POST https://example.com/hooks"},
		{"curl PUT lowercase", "curl -X put https://example.com/resource/1"},
		{"curl DELETE", "curl -X DELETE https://example.com/resource/1"},
	}
	layer := UnattendedPostureLayer()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := PermissionRequest{Tool: "run_shell", Category: "shell", Arg: tt.cmd, Posture: PostureUnattended}
			dec := EvaluatePermissionLayers(req, layer)
			if dec.Action != PermissionDeny {
				t.Fatalf("expected %q to be denied under unattended posture, got %+v", tt.cmd, dec)
			}
		})
	}
}

func TestUnattendedPostureRules_AllowsSafeCommands(t *testing.T) {
	layer := UnattendedPostureLayer()
	req := PermissionRequest{Tool: "run_shell", Category: "shell", Arg: "go test ./...", Posture: PostureUnattended}
	dec := EvaluatePermissionLayers(req, layer)
	if dec.Matched {
		t.Fatalf("expected go test not to match the unattended deny layer, got %+v", dec)
	}
}

func TestParkedDecisionLog_RecordAndList(t *testing.T) {
	log := NewParkedDecisionLog()
	log.RecordParkedDecision(ParkedDecision{ID: "1", Tool: "run_shell", Arg: "git push", Posture: PostureUnattended})
	log.RecordParkedDecision(ParkedDecision{ID: "2", Tool: "run_shell", Arg: "gh pr create", Posture: PostureUnattended})

	items := log.List()
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].ID != "1" || items[1].ID != "2" {
		t.Fatalf("unexpected order: %+v", items)
	}
}

func TestParkedDecisionLog_NilSafe(t *testing.T) {
	var log *ParkedDecisionLog
	log.RecordParkedDecision(ParkedDecision{ID: "1"})
	if got := log.List(); got != nil {
		t.Fatalf("expected nil list on nil log, got %+v", got)
	}
}
