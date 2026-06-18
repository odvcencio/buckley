package tui

import (
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/ui/backend/sim"
)

func TestModelProcessStatus(t *testing.T) {
	got := modelProcessStatus("z-ai/glm-5.2", 0, 50, 12, nil)
	if got != "Thinking with z-ai/glm-5.2 - round 1/50, 12 tools" {
		t.Fatalf("modelProcessStatus() = %q", got)
	}

	got = modelProcessStatus("very/long-model-name-that-will-not-fit-in-the-status-bar", 1, 50, 0, nil)
	if !strings.HasPrefix(got, "Thinking after tools with very/long-model-name") {
		t.Fatalf("unexpected model process status: %q", got)
	}
	if !strings.Contains(got, "round 2/50") {
		t.Fatalf("expected round indicator in status: %q", got)
	}
}

func TestMaxToolIterationsCheckpoint(t *testing.T) {
	got := maxToolIterationsCheckpoint(50)
	if !strings.Contains(got, "50 model/tool rounds") {
		t.Fatalf("expected model/tool round count, got %q", got)
	}
	if !strings.Contains(got, "continue without tools") {
		t.Fatalf("expected no-tools continuation option, got %q", got)
	}
	if strings.Contains(got, "Error:") || strings.Contains(got, "max tool calling iterations") {
		t.Fatalf("checkpoint should not read like an internal error, got %q", got)
	}
}

func TestModelFinishReasonNotice(t *testing.T) {
	if got := modelFinishReasonNotice("stop"); got != "" {
		t.Fatalf("stop should not produce a notice, got %q", got)
	}

	got := modelFinishReasonNotice("length")
	if !strings.Contains(got, "output token limit") {
		t.Fatalf("length notice should mention output token limit, got %q", got)
	}
	if status := readyStatusForFinishReason("length"); status != "Ready - output token limit reached" {
		t.Fatalf("ready status for length = %q", status)
	}

	got = modelFinishReasonNotice("content_filter")
	if !strings.Contains(got, "content_filter") {
		t.Fatalf("content_filter notice should include reason, got %q", got)
	}
}

func TestToolLoopCheckpointFinishReason(t *testing.T) {
	if got := modelFinishReasonNotice(toolLoopCheckpointFinishReason); got != "" {
		t.Fatalf("checkpoint should not add a provider notice, got %q", got)
	}
	if got := readyStatusForFinishReason(toolLoopCheckpointFinishReason); got != "Ready - needs direction" {
		t.Fatalf("checkpoint ready status = %q", got)
	}
}

func TestShouldDisableToolsForPrompt(t *testing.T) {
	tests := []struct {
		prompt string
		want   bool
	}{
		{prompt: "continue without tools", want: true},
		{prompt: "please do a no tools follow-up", want: true},
		{prompt: "tools off for this one", want: true},
		{prompt: "continue with tools", want: false},
		{prompt: "inspect the tool registry", want: false},
		{prompt: "why do we want no tools runs?", want: false},
	}

	for _, tt := range tests {
		if got := shouldDisableToolsForPrompt(tt.prompt); got != tt.want {
			t.Fatalf("shouldDisableToolsForPrompt(%q) = %v, want %v", tt.prompt, got, tt.want)
		}
	}
}

func TestConsumeDisableToolsNextTurn(t *testing.T) {
	ctrl := &Controller{}
	sess := &SessionState{DisableToolsNextTurn: true}

	if !ctrl.consumeDisableToolsNextTurn(sess) {
		t.Fatal("expected first consume to disable tools")
	}
	if ctrl.consumeDisableToolsNextTurn(sess) {
		t.Fatal("expected second consume to be false")
	}
}

func TestHandleToolLoopModelError_RetriesWithoutTools(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	ctrl := &Controller{app: app}
	state := &toolLoopState{useTools: true}

	if err := ctrl.handleToolLoopModelError(fmt.Errorf("model does not support tool calling"), state); err != nil {
		t.Fatalf("expected unsupported tools error to be consumed, got %v", err)
	}
	if state.useTools {
		t.Fatal("useTools should be disabled after unsupported tools error")
	}
	select {
	case msg := <-app.messages:
		status, ok := msg.(StatusMsg)
		if !ok || status.Text != "Retrying without tools" {
			t.Fatalf("posted message = %#v, want retrying status", msg)
		}
	default:
		t.Fatal("expected retrying status message to be posted")
	}

	if err := ctrl.handleToolLoopModelError(fmt.Errorf("provider unavailable"), state); err == nil {
		t.Fatal("non-tool errors should be returned")
	}
}
