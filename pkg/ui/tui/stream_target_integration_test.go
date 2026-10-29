package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

func processPresentationEvents(app *WidgetApp) {
	for _, m := range drainFastMessages(app) {
		app.processMessage(m)
	}
	app.sweepPresentations()
}
func containsPresentation(app *WidgetApp, query string) bool {
	app.chatView.Search(query)
	_, count := app.chatView.SearchMatches()
	return count > 0
}

func TestTargetedStream_CorrectionAndLateReasoning(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session")
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Hour}, app.postCoalescerPublication)
	ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "session"}
	ctrl.appendTextProgress(state, sess, "draft ")
	ctrl.appendReasoningProgress(state, sess, model.MessageDelta{Reasoning: "reason"})
	ctrl.appendTextProgress(state, sess, "markup")
	ctrl.closeTextProgress(state, sess, "corrected", true)
	processPresentationEvents(app)
	if !containsPresentation(app, "corrected") || containsPresentation(app, "draft") || !containsPresentation(app, "reason") {
		t.Fatal("corrected assistant text and reasoning were not isolated")
	}
}
func TestTargetedStream_SwitchBackSkipsQueuedUpdates(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("one")
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Hour}, app.postCoalescerPublication)
	ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "one"}
	ctrl.appendTextProgress(state, sess, "first ")
	app.clearScrollback(true)
	app.bindPresentationSession("two")
	ctrl.appendTextProgress(state, sess, "second")
	processPresentationEvents(app)
	if containsPresentation(app, "first") {
		t.Fatal("other session received text")
	}
	app.clearScrollback(true)
	app.bindPresentationSession("one")
	processPresentationEvents(app)
	if !containsPresentation(app, "first second") {
		t.Fatal("active stream was not rehydrated")
	}
	ctrl.appendTextProgress(state, sess, " third")
	ctrl.closeTextProgress(state, sess, "first second third", true)
	processPresentationEvents(app)
	if !containsPresentation(app, "first second third") {
		t.Fatal("stream did not resume after rehydration")
	}
}

func TestTargetedStream_EmptyFinalCorrectionAndCancel(t *testing.T) {
	for _, tc := range []struct {
		name      string
		finalized bool
		wantDraft bool
	}{{"success", true, false}, {"cancel", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			app := newEventDeliveryApp(t)
			app.bindPresentationSession("session")
			app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Hour}, app.postCoalescerPublication)
			ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "session"}
			ctrl.appendTextProgress(state, sess, "draft")
			ctrl.closeTextProgress(state, sess, "", tc.finalized)
			processPresentationEvents(app)
			if containsPresentation(app, "draft") != tc.wantDraft {
				t.Fatalf("draft presence differs from finalized=%v", tc.finalized)
			}
		})
	}
}

func TestTargetedStream_OldGenerationCannotMutateNewBubble(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session")
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Hour}, app.postCoalescerPublication)
	ctrl, sess := &Controller{app: app}, &SessionState{ID: "session"}
	first, second := &toolLoopState{}, &toolLoopState{}
	ctrl.appendTextProgress(first, sess, "old")
	ctrl.closeTextProgress(first, sess, "old", true)
	ctrl.appendTextProgress(second, sess, "new")
	processPresentationEvents(app)
	if !containsPresentation(app, "old") || !containsPresentation(app, "new") {
		t.Fatal("generation messages not isolated")
	}
	app.updatePresentation(first.progress.textID, "old-corrected")
	processPresentationEvents(app)
	if !containsPresentation(app, "old-corrected") || !containsPresentation(app, "new") {
		t.Fatal("old correction changed the new target")
	}
}

func TestTargetedStream_AcceptanceReplayAndSaveFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "failed"}[fail], func(t *testing.T) {
			app := newEventDeliveryApp(t)
			app.bindPresentationSession("session")
			app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Hour}, app.postCoalescerPublication)
			ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "session", Conversation: conversation.New("session")}
			if fail {
				store, err := storage.New(filepath.Join(t.TempDir(), "closed.db"))
				if err != nil {
					t.Fatal(err)
				}
				if err = store.Close(); err != nil {
					t.Fatal(err)
				}
				ctrl.store = store
			}
			ctrl.appendTextProgress(state, sess, "answer")
			ctrl.closeTextProgress(state, sess, "answer", true)
			_, err := ctrl.finishToolLoopResponse(sess, model.Message{Role: "assistant", Content: "answer"}, model.Usage{}, "stop", state)
			if (err != nil) != fail {
				t.Fatalf("save error=%v, fail=%v", err, fail)
			}
			if fail && sess.persistenceErr == nil {
				t.Fatal("save failure not surfaced")
			}
			if !fail && sess.persistenceErr != nil {
				t.Fatal(sess.persistenceErr)
			}
			app.clearScrollback(true)
			renderConversationHistoryImmediately(app, sess.Conversation.Messages)
			app.bindPresentationSession("session")
			processPresentationEvents(app)
			app.chatView.Search("answer")
			_, count := app.chatView.SearchMatches()
			if count != 1 {
				t.Fatalf("answer displayed %d times", count)
			}
			_, exists := app.live.snapshot(state.progress.textID)
			if exists != fail {
				t.Fatalf("live snapshot retained=%v, want %v", exists, fail)
			}
		})
	}
}

func TestTargetedStream_ImmediateAcceptanceStillRenders(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session")
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Hour}, app.postCoalescerPublication)
	ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "session", Conversation: conversation.New("session")}
	ctrl.appendTextProgress(state, sess, "answer")
	ctrl.closeTextProgress(state, sess, "answer", true)
	if _, err := ctrl.finishToolLoopResponse(sess, model.Message{Role: "assistant", Content: "answer"}, model.Usage{}, "stop", state); err != nil {
		t.Fatal(err)
	}
	processPresentationEvents(app)
	if !containsPresentation(app, "answer") {
		t.Fatal("accepted stream never became visible")
	}
}

func TestTargetedStream_RetiredFlushNeverFallsBack(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session")
	ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "session"}
	ctrl.appendTextProgress(state, sess, "old")
	ctrl.closeTextProgress(state, sess, "old", true)
	processPresentationEvents(app)
	id := state.progress.textID
	app.finishPresentation(id)
	app.chatView.AddMessage("new bubble", "assistant")
	app.processMessage(StreamFlush{SessionID: "session", Generation: id, Targeted: true, Text: "stale"})
	if containsPresentation(app, "stale") || !containsPresentation(app, "new bubble") {
		t.Fatal("retired targeted flush mutated current bubble")
	}
}

func TestTargetedStream_InFlightFlushCannotOvertakeCorrectionOrNewBubble(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Nanosecond}, func(m Message) {
		if _, ok := m.(StreamFlush); ok {
			close(entered)
			<-release
		}
		app.postCoalescerPublication(m)
	})
	ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "session"}
	ctrl.appendTextProgress(state, sess, "draft")
	go func() { app.coalescer.Tick(); close(done) }()
	<-entered
	ctrl.closeTextProgress(state, sess, "corrected", true)
	app.AddMessage("new bubble", "system")
	close(release)
	<-done
	processPresentationEvents(app)
	if !containsPresentation(app, "corrected") || containsPresentation(app, "draft") || !containsPresentation(app, "new bubble") {
		t.Fatal("in-flight correction targeted the wrong message")
	}
}

func TestTargetedStream_ToolPreambleReplayOnce(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session")
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Hour}, app.postCoalescerPublication)
	ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "session", Conversation: conversation.New("session")}
	ctrl.appendTextProgress(state, sess, "preamble tool markup")
	ctrl.closeTextProgress(state, sess, "preamble", true)
	calls := []model.ToolCall{{ID: "call", Type: "function"}}
	ctrl.recordToolLoopCalls(sess, calls, model.Message{Role: "assistant", Content: "preamble", ToolCalls: calls}, state)
	app.clearScrollback(true)
	renderConversationHistoryImmediately(app, sess.Conversation.Messages)
	app.bindPresentationSession("session")
	processPresentationEvents(app)
	app.chatView.Search("preamble")
	_, count := app.chatView.SearchMatches()
	if count != 1 {
		t.Fatalf("preamble displayed %d times", count)
	}
}

func TestTargetedStream_BlockedPublicationKeepsTargetIdentityAfterRetirement(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Nanosecond}, func(m Message) {
		if _, ok := m.(StreamFlush); ok {
			close(entered)
			<-release
		}
		app.postCoalescerPublication(m)
	})
	ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "session"}
	ctrl.appendTextProgress(state, sess, "stale")
	go func() { app.coalescer.Tick(); close(done) }()
	<-entered
	app.finishPresentation(state.progress.textID)
	app.chatView.AddMessage("new bubble", "assistant")
	close(release)
	<-done
	processPresentationEvents(app)
	if containsPresentation(app, "stale") || !containsPresentation(app, "new bubble") {
		t.Fatal("late flush lost its target identity")
	}
}

func TestTargetedStream_WrongSessionCannotCorrectTarget(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("one")
	ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "one"}
	ctrl.appendTextProgress(state, sess, "real")
	ctrl.closeTextProgress(state, sess, "real", true)
	processPresentationEvents(app)
	app.processMessage(StreamFlush{SessionID: "other", Generation: state.progress.textID, Targeted: true, Text: "wrong"})
	if !containsPresentation(app, "real") || containsPresentation(app, "wrong") {
		t.Fatal("wrong session modified target")
	}
}

func TestTargetedStream_SaturatedDeliveryReconcilesAndReclaims(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Nanosecond}, func(m Message) {
		if _, ok := m.(StreamFlush); ok {
			select {
			case <-entered:
			default:
				close(entered)
				<-release
			}
		}
		app.postCoalescerPublication(m)
	})
	ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "session", Conversation: conversation.New("session")}
	ctrl.appendTextProgress(state, sess, "draft")
	go func() { app.coalescer.Tick(); close(done) }()
	<-entered
	for i := 0; i < coalescerPendingMaxCount+16; i++ {
		ctrl.appendTextProgress(state, sess, strings.Repeat("x", 1024))
	}
	fillFastQueue(app)
	ctrl.closeTextProgress(state, sess, "final answer", true)
	result, err := ctrl.finishToolLoopResponse(sess, model.Message{Role: "assistant", Content: "final answer"}, model.Usage{}, "stop", state)
	if err != nil || !result.Streamed {
		t.Fatalf("result=%+v, err=%v", result, err)
	}
	app.processFrameTick(time.Now())
	if !containsPresentation(app, "final answer") || containsPresentation(app, "draft") {
		t.Fatal("frame did not reconcile final snapshot")
	}
	if _, ok := app.live.snapshot(state.progress.textID); ok {
		t.Fatal("accepted source not reclaimed on frame")
	}
	close(release)
	<-done
	for _, m := range drainRetainedMessages(app) {
		app.processMessage(m)
	}
	app.chatView.Search("final answer")
	_, count := app.chatView.SearchMatches()
	if count != 1 {
		t.Fatalf("final answer displayed %d times", count)
	}
}

func TestTargetedStream_RepeatedIncompleteReplaysOnceAndReclaims(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session")
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Hour}, app.postCoalescerPublication)
	ctrl, sess := &Controller{app: app}, &SessionState{ID: "session", Conversation: conversation.New("session")}
	for i := 0; i < 3; i++ {
		state := &toolLoopState{}
		ctrl.appendTextProgress(state, sess, "partial")
		ctrl.closeTextProgress(state, sess, "", false)
		ctrl.markIncompleteToolLoopCompletion(state, "interrupted")
		result := toolLoopResult{Text: "partial", presentationTextID: state.progress.textID, presentationReasonID: state.progress.reasoningID}
		ctrl.renderIncompleteStreamResponse(sess, result, errors.New("interrupted"))
	}
	app.clearScrollback(true)
	renderConversationHistoryImmediately(app, sess.Conversation.Messages)
	app.bindPresentationSession("session")
	processPresentationEvents(app)
	app.chatView.Search("Preserved draft")
	_, count := app.chatView.SearchMatches()
	if count != 3 {
		t.Fatalf("draft count=%d, want 3", count)
	}
	if n := app.live.retainedBytes(); n != 0 {
		t.Fatalf("retained bytes=%d", n)
	}
}

func TestTargetedStream_OuterSaveFailureRetainsSourceAndReplaysOnce(t *testing.T) {
	var ctrl *Controller
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := ctrl.store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"save-failure","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"answer after save failure"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	var sess *SessionState
	var app *WidgetApp
	ctrl, sess, app = newTerminalPartialController(t, server.URL, nil)
	app.bindPresentationSession(sess.ID)
	ctrl.streamResponse(context.Background(), "question", sess)
	if sess.persistenceErr == nil {
		t.Fatal("expected actual save failure")
	}
	processPresentationEvents(app)
	if app.live.retainedBytes() == 0 {
		t.Fatal("failed save lost its bounded live source")
	}
	app.clearScrollback(true)
	renderConversationHistoryImmediately(app, sess.Conversation.Messages)
	app.bindPresentationSession(sess.ID)
	processPresentationEvents(app)
	app.chatView.Search("answer after save failure")
	_, count := app.chatView.SearchMatches()
	if count != 1 {
		t.Fatalf("answer displayed %d times", count)
	}
	for _, msg := range sess.Conversation.Messages {
		if strings.Contains(conversation.GetContentAsString(msg.Content), "Preserved draft") {
			t.Fatal("accepted in-memory answer duplicated as incomplete draft")
		}
	}
}

func TestTargetedStream_OuterCancelPersistsIncompleteAndReclaims(t *testing.T) {
	observed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"cancel-target","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"cancel partial","reasoning":"late reason"},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()
		close(observed)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctrl, sess, app := newTerminalPartialController(t, server.URL, nil)
	app.bindPresentationSession(sess.ID)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); ctrl.streamResponse(ctx, "question", sess) }()
	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("no stream")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		found := false
		for _, item := range app.live.sessionEntries(sess.ID) {
			if strings.Contains(item.snapshot.content, "cancel partial") {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("partial not observed")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not finish")
	}
	app.clearScrollback(true)
	renderConversationHistoryImmediately(app, sess.Conversation.Messages)
	app.bindPresentationSession(sess.ID)
	processPresentationEvents(app)
	app.chatView.Search("Preserved draft")
	_, count := app.chatView.SearchMatches()
	if count != 1 {
		t.Fatalf("incomplete draft count=%d", count)
	}
	if n := app.live.retainedBytes(); n != 0 {
		t.Fatalf("retained bytes=%d", n)
	}
}

func TestTargetedStream_ToolSummariesSurviveDelayedFlush(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Nanosecond}, func(m Message) {
		if _, ok := m.(StreamFlush); ok {
			close(entered)
			<-release
		}
		app.postCoalescerPublication(m)
	})
	ctrl, state, sess := &Controller{app: app}, &toolLoopState{sessionID: "session"}, &SessionState{ID: "session"}
	ctrl.appendTextProgress(state, sess, "assistant answer")
	go func() { app.coalescer.Tick(); close(done) }()
	<-entered
	ctrl.closeTextProgress(state, sess, "assistant answer", true)
	tc := model.ToolCall{ID: "call", Type: "function"}
	tc.Function.Name = "echo"
	ctrl.appendToolCallProgress(state, tc)
	ctrl.appendToolResultProgress(state, "echo", nil, errors.New("tool failed"))
	ctrl.appendCodeModeRecoveryProgress(state, "inspect repository")
	close(release)
	<-done
	processPresentationEvents(app)
	if !containsPresentation(app, "assistant answer") || !containsPresentation(app, "inspect repository") || !containsPresentation(app, "tool failed") {
		t.Fatal("assistant or tool progress was lost")
	}
	app.chatView.Search("assistant answer")
	_, count := app.chatView.SearchMatches()
	if count != 1 {
		t.Fatalf("assistant count=%d", count)
	}
}

func TestTargetedStream_BackgroundToolProgressReplaysOnlyInItsSession(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session-b")
	ctrl, state := &Controller{app: app}, &toolLoopState{sessionID: "session-a"}
	sess := &SessionState{ID: "session-a", Conversation: conversation.New("session-a")}
	tc := model.ToolCall{ID: "call", Type: "function"}
	tc.Function.Name = "echo"
	ctrl.appendToolCallProgress(state, tc)
	ctrl.appendToolResultProgress(state, "echo", nil, errors.New("tool failed"))
	ctrl.appendCodeModeRecoveryProgress(state, "inspect repository")
	ctrl.recordToolLoopCalls(sess, []model.ToolCall{tc}, model.Message{Role: "assistant", ToolCalls: []model.ToolCall{tc}}, state)
	sess.Conversation.AddToolResponseMessage("call", "echo", formatToolResultForModel(nil, errors.New("tool failed"))+"\n\nCODE MODE RECOVERY: inspect repository")
	processPresentationEvents(app)
	if containsPresentation(app, "echo") || containsPresentation(app, "inspect repository") {
		t.Fatal("background progress appeared in other session")
	}
	app.clearScrollback(true)
	renderConversationHistoryImmediately(app, sess.Conversation.Messages)
	app.bindPresentationSession(sess.ID)
	processPresentationEvents(app)
	app.chatView.Search("→ echo")
	_, count := app.chatView.SearchMatches()
	if count != 1 {
		t.Fatalf("replayed tool call count=%d", count)
	}
	for _, want := range []string{"✗ echo", "inspect repository"} {
		app.chatView.Search(want)
		_, n := app.chatView.SearchMatches()
		if n != 1 {
			t.Fatalf("replayed %q count=%d", want, n)
		}
	}
}

func TestToolRecoveryReplay_SeparatesOnlyValidSuffix(t *testing.T) {
	for _, tc := range []struct{ name, content, wantError, wantRecovery string }{
		{"absent", "Error: ordinary failure", "ordinary failure", ""},
		{"valid", "Error: ordinary failure\n\nCODE MODE RECOVERY: inspect repository", "ordinary failure", "CODE MODE RECOVERY: inspect repository"},
		{"malformed", "Error: ordinary failure\n\nCODE MODE RECOVERY inspect repository", "ordinary failure CODE MODE RECOVERY inspect repository", ""},
		{"inline", "Error: text CODE MODE RECOVERY: literal detail", "text CODE MODE RECOVERY: literal detail", ""},
		{"escaped", `Error: ordinary failure\n\nCODE MODE RECOVERY: inspect repository`, "ordinary failure", "CODE MODE RECOVERY: inspect repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := storedToolResultProgressSummary("echo", tc.content); !strings.Contains(strings.Join(strings.Fields(got), " "), tc.wantError) {
				t.Fatalf("summary=%q, want detail %q", got, tc.wantError)
			}
			if got := storedCodeModeRecovery(tc.content); got != tc.wantRecovery {
				t.Fatalf("recovery=%q, want %q", got, tc.wantRecovery)
			}
		})
	}
}

func TestTargetedStream_LateReasoningPrecedesAnswerAcrossRebind(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session")
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Hour}, app.postCoalescerPublication)
	ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "session"}
	ctrl.appendTextProgress(state, sess, "ANSWER_SENTINEL")
	ctrl.appendReasoningProgress(state, sess, model.MessageDelta{Reasoning: strings.Repeat("REASON_SENTINEL\n\n", 50)})
	processPresentationEvents(app)
	check := func() {
		app.chatView.StartSelection(0, 0)
		app.chatView.UpdateSelection(10000, 0)
		app.chatView.EndSelection()
		text := app.chatView.SelectionText()
		app.chatView.ClearSelection()
		reason, answer := strings.Index(text, "REASON_SENTINEL"), strings.Index(text, "ANSWER_SENTINEL")
		if reason < 0 || answer < 0 || reason >= answer {
			t.Fatalf("reasoning/answer order wrong: reason=%d answer=%d", reason, answer)
		}
	}
	check()
	app.clearScrollback(true)
	app.bindPresentationSession("other")
	app.clearScrollback(true)
	app.bindPresentationSession("session")
	check()
	ctrl.closeTextProgress(state, sess, "ANSWER_SENTINEL", true)
	processPresentationEvents(app)
	check()
}

func TestTargetedStream_ReasoningBeforeTextSurvivesRebind(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session")
	ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "session"}
	ctrl.appendReasoningProgress(state, sess, model.MessageDelta{Reasoning: "REASON_FIRST"})
	ctrl.appendTextProgress(state, sess, "ANSWER_AFTER")
	processPresentationEvents(app)
	app.clearScrollback(true)
	app.bindPresentationSession("other")
	app.clearScrollback(true)
	app.bindPresentationSession("session")
	app.chatView.StartSelection(0, 0)
	app.chatView.UpdateSelection(1000, 0)
	app.chatView.EndSelection()
	text := app.chatView.SelectionText()
	if r, a := strings.Index(text, "REASON_FIRST"), strings.Index(text, "ANSWER_AFTER"); r < 0 || a < 0 || r >= a {
		t.Fatalf("wrong order: %q", text)
	}
}
