package tui

import (
	"fmt"
	"testing"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/fluffyui/backend/sim"
)

func historyRenderFixture(turns int) []conversation.Message {
	languages := []struct{ name, code string }{
		{"go", "package main\nfunc answer() string { return \"café 日本語\" }"},
		{"python", "def answer():\n    return 'café 日本語'"},
		{"javascript", "function answer() { return 'café 日本語'; }"},
	}
	var messages []conversation.Message
	for turn := range turns {
		language := languages[turn%len(languages)]
		messages = append(messages,
			conversation.Message{Role: "user", Content: fmt.Sprintf("Verify turn %d.", turn)},
			conversation.Message{Role: "assistant", Reasoning: "I’ll verify café → ready."},
			conversation.Message{Role: "tool", Name: "run_tests", Content: `{"success":true}`},
			conversation.Message{Role: "assistant", Content: fmt.Sprintf("Turn %d complete.\n\n```%s\n%s\n```", turn, language.name, language.code)},
		)
	}
	return messages
}

func BenchmarkController_RerenderTranscript(b *testing.B) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(app.backend.Fini)
	controller := &Controller{app: app}
	session := &SessionState{ID: "history-benchmark"}
	messages := historyRenderFixture(30)
	controller.rerenderTranscript(session, messages)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		controller.rerenderTranscript(session, messages)
	}
}
