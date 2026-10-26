package tui

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/ui/widgets"
)

func TestController_RerenderTranscript_MatchesFreshReplay(t *testing.T) {
	controller, app := newSessionNavTestController(t, nil)
	_, reference := newSessionNavTestController(t, nil)
	session := &SessionState{ID: "history-reload"}
	controller.rerenderTranscript(session, historyRenderFixture(3))
	app.unreadCount = 7
	app.dirty = false
	messages := historyRenderFixture(12)
	original := slices.Clone(messages)
	controller.rerenderTranscript(session, messages)
	reference.ClearScrollback()
	reference.WelcomeScreen()
	reference.addMessageImmediately("Session: "+session.ID, "system")
	renderConversationHistoryWith(reference.addMessageImmediately, messages)
	copyText := func(view *widgets.ChatView) string {
		_, total, _ := view.ScrollPosition()
		view.StartSelection(0, 0)
		view.UpdateSelection(total-1, 10000)
		view.EndSelection()
		return view.SelectionText()
	}
	got, want := copyText(app.chatView), copyText(reference.chatView)
	if got != want || !strings.Contains(got, "café 日本語") || !strings.Contains(got, "Thinking") {
		t.Fatal("history redraw changed copied transcript or Unicode source")
	}
	if app.scrollIndicator != reference.scrollIndicator || app.unreadCount != 0 || !app.dirty {
		t.Fatal("history redraw did not publish final scroll or redraw state")
	}
	if !reflect.DeepEqual(messages, original) {
		t.Fatal("history redraw changed caller-owned messages")
	}
}
