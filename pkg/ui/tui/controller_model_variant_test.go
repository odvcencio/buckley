package tui

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/fluffyui/backend/sim"
)

func newVariantTestController(t *testing.T) (*Controller, *WidgetApp) {
	t.Helper()
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.backend.Fini)
	ctrl := &Controller{
		app: app,
		cfg: &config.Config{},
		sessions: []*SessionState{
			{ID: "session-1", Conversation: conversation.New("session-1")},
		},
	}
	return ctrl, app
}

func TestCycleModelVariant_AppliesEffortAndContinuation(t *testing.T) {
	ctrl, app := newVariantTestController(t)

	ctrl.cycleModelVariant()
	drainPostedMessage(t, app)

	if ctrl.modelVariant != "fast" {
		t.Fatalf("modelVariant = %q, want fast", ctrl.modelVariant)
	}
	if ctrl.cfg.Models.Reasoning != "minimal" {
		t.Fatalf("Reasoning = %q, want minimal", ctrl.cfg.Models.Reasoning)
	}
	if ctrl.cfg.Models.ProviderContinuation {
		t.Fatal("ProviderContinuation should be false for fast")
	}

	ctrl.cycleModelVariant()
	drainPostedMessage(t, app)
	if ctrl.modelVariant != "balanced" {
		t.Fatalf("modelVariant = %q, want balanced", ctrl.modelVariant)
	}
	if ctrl.cfg.Models.Reasoning != "medium" {
		t.Fatalf("Reasoning = %q, want medium", ctrl.cfg.Models.Reasoning)
	}

	ctrl.cycleModelVariant()
	drainPostedMessage(t, app)
	ctrl.cycleModelVariant()
	drainPostedMessage(t, app)
	if ctrl.modelVariant != "deep+retained" {
		t.Fatalf("modelVariant = %q, want deep+retained", ctrl.modelVariant)
	}
	if !ctrl.cfg.Models.ProviderContinuation {
		t.Fatal("ProviderContinuation should be true for deep+retained")
	}

	ctrl.cycleModelVariant()
	drainPostedMessage(t, app)
	if ctrl.modelVariant != "fast" {
		t.Fatalf("modelVariant = %q, want fast after wrapping", ctrl.modelVariant)
	}
}

func TestRememberRecentModel_CapsAndDedupes(t *testing.T) {
	ctrl, _ := newVariantTestController(t)

	ctrl.rememberRecentModel("openai/gpt-4o")
	ctrl.rememberRecentModel("anthropic/claude-3.5")
	ctrl.rememberRecentModel("moonshotai/kimi-k3")
	ctrl.rememberRecentModel("z-ai/glm-5.2")

	if len(ctrl.recentModels) != maxRecentModels {
		t.Fatalf("recentModels len = %d, want %d", len(ctrl.recentModels), maxRecentModels)
	}
	want := []string{"z-ai/glm-5.2", "moonshotai/kimi-k3", "anthropic/claude-3.5"}
	for i, id := range want {
		if ctrl.recentModels[i] != id {
			t.Fatalf("recentModels[%d] = %q, want %q (%v)", i, ctrl.recentModels[i], id, ctrl.recentModels)
		}
	}

	// Re-selecting an existing entry moves it to the front without growing
	// the list.
	ctrl.rememberRecentModel("anthropic/claude-3.5")
	if len(ctrl.recentModels) != maxRecentModels {
		t.Fatalf("recentModels len after re-select = %d, want %d", len(ctrl.recentModels), maxRecentModels)
	}
	if ctrl.recentModels[0] != "anthropic/claude-3.5" {
		t.Fatalf("recentModels[0] = %q, want anthropic/claude-3.5", ctrl.recentModels[0])
	}
}

func TestNextRecentModel_CyclesAndWraps(t *testing.T) {
	recents := []string{"c", "b", "a"}

	if got := nextRecentModel(recents, "c"); got != "b" {
		t.Fatalf("nextRecentModel(c) = %q, want b", got)
	}
	if got := nextRecentModel(recents, "a"); got != "c" {
		t.Fatalf("nextRecentModel(a) = %q, want c (wrap)", got)
	}
	if got := nextRecentModel(recents, "not-present"); got != "c" {
		t.Fatalf("nextRecentModel(unknown) = %q, want c (most recent)", got)
	}
}

func TestCycleRecentModel_NoHistoryWarns(t *testing.T) {
	ctrl, app := newVariantTestController(t)

	ctrl.cycleRecentModel()

	select {
	case msg := <-app.messages:
		add, ok := msg.(AddMessageMsg)
		if !ok {
			t.Fatalf("expected AddMessageMsg, got %T", msg)
		}
		if add.Content == "" {
			t.Fatal("expected a non-empty warning message")
		}
	default:
		t.Fatal("expected a message to be queued")
	}
}

func TestCycleRecentModel_SwitchesToNextEntry(t *testing.T) {
	ctrl, app := newVariantTestController(t)
	ctrl.recentModels = []string{"z-ai/glm-5.2", "openai/gpt-4o"}
	ctrl.modelOverride = "z-ai/glm-5.2"
	ctrl.cfg.Models.Curated = nil

	ctrl.cycleRecentModel()
	drainPostedMessage(t, app) // ModelMsg from setExecutionModelLocked
	drainPostedMessage(t, app) // AddMessageMsg notice

	if ctrl.modelOverride != "openai/gpt-4o" {
		t.Fatalf("modelOverride = %q, want openai/gpt-4o", ctrl.modelOverride)
	}
}

func TestSetReasoningEffort_AppliesAndValidates(t *testing.T) {
	tests := []struct {
		name        string
		start       string
		args        []string
		wantEffort  string
		wantVariant bool
		wantHeader  string
		wantMessage string
	}{
		{name: "sets level", start: "medium", args: []string{"HIGH"}, wantEffort: "high", wantHeader: "effort high", wantMessage: "set to high"},
		{name: "rejects off", start: "medium", args: []string{"off"}, wantEffort: "medium", wantVariant: true, wantMessage: "Unknown effort"},
		{name: "auto clears header", start: "high", args: []string{"auto"}, wantEffort: "auto", wantHeader: "", wantMessage: "set to auto"},
		{name: "rejects unknown", start: "medium", args: []string{"turbo"}, wantEffort: "medium", wantVariant: true, wantMessage: "Unknown effort"},
		{name: "shows current", start: "low", wantEffort: "low", wantVariant: true, wantMessage: "Reasoning effort: low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl, app := newVariantTestController(t)
			ctrl.cfg.Models.Reasoning = tt.start
			ctrl.modelVariant = "balanced"

			ctrl.setReasoningEffort(tt.args)

			if ctrl.cfg.Models.Reasoning != tt.wantEffort {
				t.Fatalf("Reasoning = %q, want %q", ctrl.cfg.Models.Reasoning, tt.wantEffort)
			}
			if got := ctrl.modelVariant == "balanced"; got != tt.wantVariant {
				t.Fatalf("variant kept = %v, want %v", got, tt.wantVariant)
			}
			if !tt.wantVariant {
				select {
				case msg := <-app.messages:
					variant, ok := msg.(ModelVariantMsg)
					if !ok || variant.Name != tt.wantHeader {
						t.Fatalf("header message = %#v, want ModelVariantMsg %q", msg, tt.wantHeader)
					}
				default:
					t.Fatal("expected a header update")
				}
			}
			select {
			case msg := <-app.messages:
				add, ok := msg.(AddMessageMsg)
				if !ok || !strings.Contains(add.Content, tt.wantMessage) {
					t.Fatalf("message = %#v, want it to contain %q", msg, tt.wantMessage)
				}
			default:
				t.Fatal("expected a system message")
			}
		})
	}
}
