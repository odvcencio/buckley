package tui

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/ui/widgets"
	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/backend/sim"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/terminal"
)

type deliveryTestMsg struct{ ID int }

func (deliveryTestMsg) isMessage() {}

type blockingDeliveryError struct {
	called  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *blockingDeliveryError) Error() string {
	e.once.Do(func() { close(e.called) })
	<-e.release
	return "blocked"
}

type countingDeliveryBackend struct {
	backend.Backend
	finiCount atomic.Int32
}

func (b *countingDeliveryBackend) Fini() {
	b.finiCount.Add(1)
	b.Backend.Fini()
}

func newEventDeliveryApp(t *testing.T) *WidgetApp {
	t.Helper()
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.finalizeBackend)
	return app
}

func drainFastMessages(app *WidgetApp) []Message {
	var messages []Message
	for {
		select {
		case msg := <-app.messages:
			messages = append(messages, msg)
		default:
			return messages
		}
	}
}

func drainRetainedMessages(app *WidgetApp) []Message {
	messages := drainFastMessages(app)
	for {
		msg, ok := app.takeOverflowEvent()
		if !ok {
			break
		}
		messages = append(messages, msg)
	}
	for {
		msg, ok := app.takeCoalescedEvent()
		if !ok {
			return messages
		}
		messages = append(messages, msg)
	}
}

func fillFastQueue(app *WidgetApp) {
	for i := 0; i < cap(app.messages); i++ {
		app.Post(deliveryTestMsg{ID: i})
	}
}

func TestWidgetApp_PostPreservesCriticalFIFOAfterFastQueueSaturation(t *testing.T) {
	app := newEventDeliveryApp(t)
	fillFastQueue(app)
	for i := 0; i < 3; i++ {
		app.Post(deliveryTestMsg{ID: cap(app.messages) + i})
	}

	messages := drainRetainedMessages(app)
	if len(messages) != cap(app.messages)+3 {
		t.Fatalf("delivered %d messages, want %d", len(messages), cap(app.messages)+3)
	}
	for i, msg := range messages {
		if got := msg.(deliveryTestMsg).ID; got != i {
			t.Fatalf("message %d ID = %d", i, got)
		}
	}
}

func TestWidgetApp_StreamPublicationOrderAcrossConcurrentEnd(t *testing.T) {
	app := newEventDeliveryApp(t)
	started := make(chan struct{})
	release := make(chan struct{})
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1, MaxWait: time.Hour}, func(msg Message) {
		if flush, ok := msg.(StreamFlush); ok && flush.Text == "A" {
			close(started)
			<-release
		}
		app.postCoalescerPublication(msg)
	})

	firstDone := make(chan struct{})
	go func() {
		app.StreamChunk("session-1", "A")
		close(firstDone)
	}()
	<-started
	secondStarted := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		close(secondStarted)
		app.StreamChunk("session-1", "B")
		app.StreamEnd("session-1", "AB")
		close(secondDone)
	}()
	<-secondStarted
	close(release)
	<-firstDone
	<-secondDone

	messages := drainFastMessages(app)
	if len(messages) != 3 {
		t.Fatalf("published %d messages, want two flushes and done", len(messages))
	}
	if got := messages[0].(StreamFlush).Text; got != "A" {
		t.Fatalf("first flush = %q, want A", got)
	}
	if got := messages[1].(StreamFlush).Text; got != "B" {
		t.Fatalf("second flush = %q, want B", got)
	}
	if _, ok := messages[2].(StreamDone); !ok {
		t.Fatalf("last publication = %T, want StreamDone", messages[2])
	}
}

func TestCoalescer_PostRunsOutsideLockAndAllowsReentry(t *testing.T) {
	var c *Coalescer
	var nested atomic.Bool
	done := make(chan struct{})
	c = NewCoalescer(CoalescerConfig{MaxChars: 1, MaxWait: time.Hour}, func(Message) {
		if nested.CompareAndSwap(false, true) {
			c.Add("session-1", "nested")
			close(done)
		}
	})
	c.Add("session-1", "first")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("coalescer callback deadlocked on re-entry")
	}
}

func TestWidgetApp_EventWorkBudgetResignalsOverflow(t *testing.T) {
	app := newEventDeliveryApp(t)
	fillFastQueue(app)
	for i := 0; i < eventWorkBudget*2; i++ {
		app.Post(deliveryTestMsg{ID: 1000 + i})
	}
	for {
		select {
		case <-app.delivery.wake:
		default:
			goto drainedWake
		}
	}

drainedWake:
	app.runState.Store(widgetAppRunning)
	if got := app.drainEventWork(eventWorkBudget); got != eventWorkBudget {
		t.Fatalf("processed %d events, want budget %d", got, eventWorkBudget)
	}
	select {
	case <-app.delivery.wake:
	default:
		t.Fatal("remaining overflow was not re-signaled")
	}
}

func TestWidgetApp_FrameProgressAfterBudgetUnderSustainedProduction(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.runState.Store(widgetAppRunning)
	app.dirty = true
	app.frameTicker = time.NewTicker(time.Millisecond)
	defer app.frameTicker.Stop()

	stop := make(chan struct{})
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				app.Post(StatusMsg{Text: fmt.Sprintf("working-%d", i)})
			}
		}
	}()
	time.Sleep(3 * time.Millisecond)
	app.drainEventWork(eventWorkBudget)
	deadline := time.Now().Add(time.Second)
	for !app.processReadyFrame() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(stop)
	<-producerDone
	if app.Metrics().FrameCount == 0 {
		t.Fatal("frame ticker made no progress after the event work budget")
	}
}

func TestWidgetApp_OverflowCapIsObservableAndPreservesPriority(t *testing.T) {
	app := newEventDeliveryApp(t)
	fillFastQueue(app)
	for i := 0; i < eventOverflowMaxCount+32; i++ {
		app.Post(deliveryTestMsg{ID: 1000 + i})
	}

	snapshot := app.EventDeliverySnapshot()
	if !snapshot.Overloaded || snapshot.OverloadTransitions != 1 {
		t.Fatalf("overload snapshot = %+v", snapshot)
	}
	if snapshot.OverflowQueued > eventOverflowMaxCount || snapshot.OverflowBytes > eventOverflowMaxBytes {
		t.Fatalf("overflow exceeded cap: %+v", snapshot)
	}
	if snapshot.RejectedOverload == 0 || snapshot.DiagnosticsQueued != 1 {
		t.Fatalf("overload was not observable: %+v", snapshot)
	}

	app.Post(ApprovalRequestMsg{ID: "approval-1", Tool: "write_file"})
	app.Post(QuitMsg{})

	messages := drainRetainedMessages(app)
	foundDiagnostic, foundApproval, foundQuit := false, false, false
	for _, msg := range messages {
		switch m := msg.(type) {
		case deliveryOverloadMsg:
			foundDiagnostic = true
			app.processMessage(m)
		case ApprovalRequestMsg:
			foundApproval = m.ID == "approval-1"
		case QuitMsg:
			foundQuit = true
		}
	}
	if !foundDiagnostic || !foundApproval || !foundQuit {
		t.Fatalf("priority survival diagnostic=%v approval=%v quit=%v", foundDiagnostic, foundApproval, foundQuit)
	}
	app.render()
	capture := app.backend.(*sim.Backend).Capture()
	if !strings.Contains(capture, "UI event queue overloaded") || app.EventDeliverySnapshot().DiagnosticsDelivered != 1 {
		t.Fatal("overload diagnostic was not persisted in the transcript")
	}
}

func TestWidgetApp_OversizedOverflowPayloadFailsClosedWithoutRetention(t *testing.T) {
	app := newEventDeliveryApp(t)
	fillFastQueue(app)
	app.Post(PasteMsg{Text: strings.Repeat("x", eventOverflowMaxBytes)})

	snapshot := app.EventDeliverySnapshot()
	if !snapshot.Overloaded || snapshot.RejectedOverload == 0 {
		t.Fatalf("oversized payload did not fail closed: %+v", snapshot)
	}
	if snapshot.OverflowBytes > eventOverflowMaxBytes || snapshot.OverflowQueued != 1 {
		t.Fatalf("oversized payload was retained: %+v", snapshot)
	}
}

func TestWidgetApp_PostAfterQuitReleasesPayload(t *testing.T) {
	app := newEventDeliveryApp(t)
	fillFastQueue(app)
	app.Post(AddMessageMsg{Content: strings.Repeat("x", 4096), Source: "assistant"})
	app.StreamChunk("session-1", "pending")
	app.Quit()

	before := app.EventDeliverySnapshot()
	if !before.Closed || before.FastQueued != 0 || before.OverflowQueued != 0 || before.CoalescedQueued != 0 {
		t.Fatalf("close retained queued payloads: %+v", before)
	}
	app.Post(AddMessageMsg{Content: strings.Repeat("y", 4096), Source: "assistant"})
	app.StreamChunk("session-1", "late")
	after := app.EventDeliverySnapshot()
	if after.RejectedAfterClose < before.RejectedAfterClose+2 {
		t.Fatalf("post-close rejections not counted: before=%+v after=%+v", before, after)
	}
	if app.coalescer.HasPending() {
		t.Fatal("coalescer retained payload after shutdown")
	}
}

func TestWidgetApp_LatestSidebarSnapshotsAreCoalesced(t *testing.T) {
	app := newEventDeliveryApp(t)
	fillFastQueue(app)
	app.Post(SetActivitiesMsg{Records: []widgets.ActivityRecord{{ID: "old"}}})
	app.Post(SetActivitiesMsg{Records: []widgets.ActivityRecord{{ID: "latest"}}})
	app.Post(SessionNavMsg{Nodes: []widgets.SessionNavNode{{ID: "old"}}})
	app.Post(SessionNavMsg{Nodes: []widgets.SessionNavNode{{ID: "latest"}}})

	snapshot := app.EventDeliverySnapshot()
	if snapshot.CoalescedQueued != 2 || snapshot.CoalescedReplacements != 2 {
		t.Fatalf("coalesced snapshot = %+v", snapshot)
	}
	messages := drainRetainedMessages(app)
	for _, msg := range messages {
		switch m := msg.(type) {
		case SetActivitiesMsg:
			if len(m.Records) != 1 || m.Records[0].ID != "latest" {
				t.Fatalf("activities snapshot = %+v", m.Records)
			}
		case SessionNavMsg:
			if len(m.Nodes) != 1 || m.Nodes[0].ID != "latest" {
				t.Fatalf("session snapshot = %+v", m.Nodes)
			}
		}
	}
}

func TestWidgetApp_StreamChunksBypassQueueAndEndFlushesBeforeDone(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.StreamChunk("session-1", "hello")
	app.StreamChunk("session-1", ", ")
	app.StreamChunk("session-1", "world")
	if got := len(app.messages); got != 0 {
		t.Fatalf("stream chunks consumed %d queue slots", got)
	}
	app.StreamEnd("session-1", "hello, world")

	messages := drainFastMessages(app)
	if len(messages) != 2 {
		t.Fatalf("delivered %d messages, want StreamFlush and StreamDone", len(messages))
	}
	if got := messages[0].(StreamFlush).Text; got != "hello, world" {
		t.Fatalf("flushed text = %q", got)
	}
	if _, ok := messages[1].(StreamDone); !ok {
		t.Fatalf("second message = %T, want StreamDone", messages[1])
	}
}

func TestWidgetApp_OldStreamDoneCannotClearNextGeneration(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.StreamChunk("shared-session", "round-a")
	app.StreamEnd("shared-session", "round-a")
	firstRound := drainFastMessages(app)
	if len(firstRound) != 2 {
		t.Fatalf("first round delivered %d messages, want flush and done", len(firstRound))
	}
	flushA := firstRound[0].(StreamFlush)
	doneA := firstRound[1].(StreamDone)
	if flushA.Generation != 1 || doneA.Generation != 1 {
		t.Fatalf("first generation flush=%d done=%d, want 1", flushA.Generation, doneA.Generation)
	}

	// The next round buffers before the prior round's Done reaches update.
	app.StreamChunk("shared-session", "round-b")
	if !app.coalescer.HasPendingStream("shared-session", 2) {
		t.Fatal("second generation was not buffered")
	}
	app.processMessage(doneA)
	if !app.coalescer.HasPendingStream("shared-session", 2) {
		t.Fatal("old StreamDone cleared the next generation")
	}

	app.StreamEnd("shared-session", "round-b")
	secondRound := drainFastMessages(app)
	if len(secondRound) != 2 {
		t.Fatalf("second round delivered %d messages, want flush and done", len(secondRound))
	}
	flushB := secondRound[0].(StreamFlush)
	doneB := secondRound[1].(StreamDone)
	if flushB.Text != "round-b" || flushB.Generation != 2 || doneB.Generation != 2 {
		t.Fatalf("second generation flush=%+v done=%+v", flushB, doneB)
	}
}

func TestWidgetApp_RecoveryInputApprovalAndQuitSurviveOverload(t *testing.T) {
	app := newEventDeliveryApp(t)
	fillFastQueue(app)
	for i := 0; i < eventOverflowMaxCount; i++ {
		app.Post(deliveryTestMsg{ID: 10_000 + i})
	}
	if !app.EventDeliverySnapshot().Overloaded {
		t.Fatal("expected overload to latch")
	}

	var approvalCallback atomic.Int32
	approvedID := ""
	approved := false
	app.SetApprovalCallback(func(id string, allow, _ bool) {
		approvedID = id
		approved = allow
		approvalCallback.Add(1)
	})
	app.Post(PasteMsg{Text: "approve"})
	if !app.RequestApproval(ApprovalRequestMsg{ID: "recover-approval", Tool: "shell"}) {
		t.Fatal("approval was rejected despite reserved recovery capacity")
	}
	app.Post(KeyMsg{Key: int(terminal.KeyRune), Rune: 'y'})
	app.Post(QuitMsg{})
	if approvalCallback.Load() != 0 {
		t.Fatal("accepted approval invoked the rejection callback")
	}

	foundKey, foundPaste, foundApproval, foundQuit := false, false, false, false
	for _, msg := range drainRetainedMessages(app) {
		switch m := msg.(type) {
		case KeyMsg:
			foundKey = m.Rune == 'y'
		case PasteMsg:
			foundPaste = m.Text == "approve"
		case ApprovalRequestMsg:
			foundApproval = m.ID == "recover-approval"
		case QuitMsg:
			foundQuit = true
		}
		app.processMessage(msg)
	}
	if !foundKey || !foundPaste || !foundApproval || !foundQuit {
		t.Fatalf("recovery survival key=%v paste=%v approval=%v quit=%v", foundKey, foundPaste, foundApproval, foundQuit)
	}
	if approvalCallback.Load() != 1 || approvedID != "recover-approval" || !approved {
		t.Fatalf("approval answer callback count=%d id=%q approved=%v", approvalCallback.Load(), approvedID, approved)
	}
}

func TestWidgetApp_ApprovalCapRejectionTerminatesWaiter(t *testing.T) {
	app := newEventDeliveryApp(t)
	fillFastQueue(app)
	for i := 0; i < eventOverflowMaxCount+1; i++ {
		if !app.postEvent(ApprovalRequestMsg{ID: fmt.Sprintf("filler-%d", i)}) {
			break
		}
	}

	type decision struct {
		id                    string
		approved, alwaysAllow bool
	}
	decisions := make(chan decision, 1)
	app.SetApprovalCallback(func(id string, approved, alwaysAllow bool) {
		decisions <- decision{id: id, approved: approved, alwaysAllow: alwaysAllow}
	})
	if app.RequestApproval(ApprovalRequestMsg{ID: "must-terminate", Tool: "write"}) {
		t.Fatal("approval unexpectedly fit a queue containing only equal-priority events")
	}
	select {
	case got := <-decisions:
		if got.id != "must-terminate" || got.approved || got.alwaysAllow {
			t.Fatalf("rejection decision = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("rejected approval stranded its waiter")
	}
	if app.EventDeliverySnapshot().RejectedApprovals != 1 {
		t.Fatalf("approval rejection snapshot = %+v", app.EventDeliverySnapshot())
	}
}

func TestWidgetApp_QueuedApprovalIsCancelledOnShutdown(t *testing.T) {
	app := newEventDeliveryApp(t)
	fillFastQueue(app)
	decisions := make(chan bool, 2)
	app.SetApprovalCallback(func(_ string, approved, _ bool) { decisions <- approved })
	if !app.RequestApproval(ApprovalRequestMsg{ID: "queued", Tool: "shell"}) {
		t.Fatal("approval was not admitted")
	}
	if got := app.EventDeliverySnapshot().OutstandingApprovals; got != 1 {
		t.Fatalf("outstanding approvals = %d, want 1", got)
	}
	app.Quit()
	select {
	case approved := <-decisions:
		if approved {
			t.Fatal("shutdown approved queued request")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown stranded queued approval")
	}
	app.Quit()
	select {
	case <-decisions:
		t.Fatal("duplicate shutdown resolved approval twice")
	default:
	}
	snapshot := app.EventDeliverySnapshot()
	if snapshot.OutstandingApprovals != 0 || snapshot.ApprovalsCancelled != 1 {
		t.Fatalf("approval shutdown snapshot = %+v", snapshot)
	}
}

func TestWidgetApp_DisplayedApprovalIsCancelledOnQuit(t *testing.T) {
	app := newEventDeliveryApp(t)
	decisions := make(chan bool, 1)
	app.SetApprovalCallback(func(_ string, approved, _ bool) { decisions <- approved })
	if !app.RequestApproval(ApprovalRequestMsg{ID: "displayed", Tool: "write"}) {
		t.Fatal("approval was not admitted")
	}
	msg, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("approval was not queued")
	}
	app.processMessage(msg)
	if app.screen.LayerCount() < 2 {
		t.Fatal("approval dialog was not displayed")
	}
	app.Quit()
	select {
	case approved := <-decisions:
		if approved {
			t.Fatal("quit approved displayed request")
		}
	case <-time.After(time.Second):
		t.Fatal("quit stranded displayed approval")
	}
}

func TestWidgetApp_ApprovalDecisionShutdownRaceResolvesExactlyOnce(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		app := newEventDeliveryApp(t)
		var callbackCount atomic.Int32
		app.SetApprovalCallback(func(string, bool, bool) { callbackCount.Add(1) })
		if !app.RequestApproval(ApprovalRequestMsg{ID: "race", Tool: "shell"}) {
			t.Fatalf("iteration %d approval was not admitted", iteration)
		}
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			app.resolveApproval("race", true, false)
		}()
		go func() {
			defer wait.Done()
			<-start
			app.Quit()
		}()
		close(start)
		wait.Wait()
		app.resolveApproval("race", false, false)
		app.Quit()
		if got := callbackCount.Load(); got != 1 {
			t.Fatalf("iteration %d callback count = %d, want 1", iteration, got)
		}
		if got := app.EventDeliverySnapshot().OutstandingApprovals; got != 0 {
			t.Fatalf("iteration %d outstanding approvals = %d", iteration, got)
		}
	}
}

func TestWidgetApp_MouseStateIsLossyAndCoalesced(t *testing.T) {
	app := newEventDeliveryApp(t)
	fillFastQueue(app)
	app.Post(MouseMsg{X: 1, Y: 2, Action: MouseMove})
	app.Post(MouseMsg{X: 9, Y: 8, Action: MouseMove})
	if got := app.EventDeliverySnapshot().CoalescedReplacements; got != 1 {
		t.Fatalf("mouse replacements = %d, want 1", got)
	}
	messages := drainRetainedMessages(app)
	var last MouseMsg
	for _, msg := range messages {
		if mouse, ok := msg.(MouseMsg); ok {
			last = mouse
		}
	}
	if last.X != 9 || last.Y != 8 {
		t.Fatalf("retained mouse = %+v, want latest", last)
	}
}

func TestRetainedMessageBytes_BoundsDynamicPayloadGraphs(t *testing.T) {
	type payload struct{ Value any }
	backing := make([]byte, 1, eventOverflowMaxBytes)
	cases := []Message{
		ToolStart{Args: map[string]any{"payload": make([]byte, eventOverflowMaxBytes)}},
		ToolResult{Result: &payload{Value: backing}},
		ModelPickerMsg{Items: []widgets.PaletteItem{{ID: "huge", Data: map[string]any{"payload": make([]byte, eventOverflowMaxBytes)}}}},
	}
	for _, msg := range cases {
		if got := retainedMessageBytes(msg); got <= eventOverflowMaxBytes {
			t.Errorf("%T retained estimate = %d, want over cap", msg, got)
		}
	}

	cycle := map[string]any{}
	cycle["self"] = cycle
	if got := retainedMessageBytes(ToolResult{Result: cycle}); got <= 0 || got > retainedEstimateLimit {
		t.Fatalf("cycle estimate = %d", got)
	}
}

func TestWidgetApp_HugeDynamicPayloadFailsClosedWithoutRetention(t *testing.T) {
	app := newEventDeliveryApp(t)
	fillFastQueue(app)
	app.Post(ToolResult{ToolID: "huge", Result: make([]byte, eventOverflowMaxBytes)})
	snapshot := app.EventDeliverySnapshot()
	if !snapshot.Overloaded || snapshot.RejectedOverload == 0 || snapshot.OverflowQueued != 1 {
		t.Fatalf("huge dynamic payload snapshot = %+v", snapshot)
	}
}

func TestWidgetApp_RetainedSizingDoesNotInvokeBlockingError(t *testing.T) {
	app := newEventDeliveryApp(t)
	errValue := &blockingDeliveryError{called: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		app.Post(ToolResult{ToolID: "blocked-error", Err: errValue})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(errValue.release)
		t.Fatal("Post blocked by error method")
	}
	select {
	case <-errValue.called:
		close(errValue.release)
		t.Fatal("Post invoked Error during retained sizing")
	default:
		close(errValue.release)
	}
}

func TestWidgetApp_TypedModelPickerRunsProductionActionsWithoutOverload(t *testing.T) {
	app := newEventDeliveryApp(t)
	cfg := config.DefaultConfig()
	ctrl := &Controller{app: app, cfg: cfg}
	app.SetModelPickerActionCallback(ctrl.handleModelPickerAction)

	selectItem := func(action ModelPickerAction, id string) {
		t.Helper()
		app.ShowModelPicker([]widgets.PaletteItem{{ID: id, Label: id, Data: id}}, action)
		msg, ok := app.takeCriticalEvent()
		if !ok {
			t.Fatal("typed model picker was not admitted")
		}
		app.processMessage(msg)
		if got := app.screen.LayerCount(); got != 2 {
			t.Fatalf("model picker layer count = %d, want 2", got)
		}
		app.processMessage(KeyMsg{Key: int(terminal.KeyEnter)})
		if got := app.screen.LayerCount(); got != 1 {
			t.Fatalf("model picker layer count after select = %d, want 1", got)
		}
	}

	selectItem(ModelPickerActionSelectExecution, "provider/execution")
	if got := cfg.Models.Execution; got != "provider/execution" {
		t.Fatalf("execution model = %q", got)
	}
	for _, msg := range drainRetainedMessages(app) {
		app.processMessage(msg)
	}

	selectItem(ModelPickerActionToggleCurated, "provider/curated")
	foundCurated := false
	for _, modelID := range cfg.Models.Curated {
		if modelID == "provider/curated" {
			foundCurated = true
			break
		}
	}
	if !foundCurated {
		t.Fatalf("curated models = %#v", cfg.Models.Curated)
	}
	if snapshot := app.EventDeliverySnapshot(); snapshot.Overloaded || snapshot.RejectedOverload != 0 {
		t.Fatalf("valid typed picker latched overload: %+v", snapshot)
	}
}

func TestWidgetApp_TypedModelPickerReplaceCancelAndCloseReleaseState(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.SetModelPickerActionCallback(func(ModelPickerAction, string, any) {})
	items := []widgets.PaletteItem{{ID: "model", Label: "model", Data: "model"}}

	app.ShowModelPicker(items, ModelPickerActionSelectExecution)
	first, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("first model picker was not admitted")
	}
	app.processMessage(first)
	app.ShowModelPicker(items, ModelPickerActionToggleCurated)
	replacement, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("replacement model picker was not admitted")
	}
	app.processMessage(replacement)
	if got := app.screen.LayerCount(); got != 2 {
		t.Fatalf("replacement left %d layers, want 2", got)
	}
	app.modelPickerMu.Lock()
	action := app.modelPickerAction
	app.modelPickerMu.Unlock()
	if action != ModelPickerActionToggleCurated {
		t.Fatalf("replacement action = %v", action)
	}

	app.processMessage(KeyMsg{Key: int(terminal.KeyEscape)})
	app.modelPickerMu.Lock()
	action = app.modelPickerAction
	app.modelPickerMu.Unlock()
	if action != ModelPickerActionNone || app.screen.LayerCount() != 1 {
		t.Fatalf("cancel retained picker action=%v layers=%d", action, app.screen.LayerCount())
	}

	app.ShowModelPicker(items, ModelPickerActionSelectExecution)
	finalPicker, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("final model picker was not admitted")
	}
	app.processMessage(finalPicker)
	app.processMessage(KeyMsg{Key: int(terminal.KeyCtrlP)})
	app.ShowModelPicker(items, ModelPickerActionToggleCurated)
	pendingPicker, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("pending model picker replacement was not admitted")
	}
	app.processMessage(pendingPicker)
	app.Quit()
	app.modelPickerMu.Lock()
	action = app.modelPickerAction
	token := app.modelPickerToken
	layer := app.modelPickerLayer
	pending := app.modelPickerPending
	callback := app.onModelPickerAction
	app.modelPickerMu.Unlock()
	if action != ModelPickerActionNone || token != 0 || layer != nil || pending != nil || callback != nil {
		t.Fatalf("close retained picker state action=%v token=%d layer=%v pending=%v callbackSet=%v", action, token, layer != nil, pending != nil, callback != nil)
	}
}

func TestWidgetApp_ModelPickerSurvivesCommandPaletteSelectionAboveIt(t *testing.T) {
	app := newEventDeliveryApp(t)
	var submits []string
	app.onSubmit = func(text string) { submits = append(submits, text) }
	selected := make(chan ModelPickerAction, 1)
	app.SetModelPickerActionCallback(func(action ModelPickerAction, _ string, _ any) { selected <- action })
	items := []widgets.PaletteItem{{ID: "model", Label: "model", Data: "model"}}

	app.ShowModelPicker(items, ModelPickerActionSelectExecution)
	picker, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("model picker was not admitted")
	}
	app.processMessage(picker)
	app.modelPickerMu.Lock()
	token := app.modelPickerToken
	layer := app.modelPickerLayer
	app.modelPickerMu.Unlock()

	app.processMessage(KeyMsg{Key: int(terminal.KeyCtrlP)})
	if got := app.screen.LayerCount(); got != 3 {
		t.Fatalf("command palette layer count = %d, want 3", got)
	}
	app.processMessage(KeyMsg{Key: int(terminal.KeyEnter)})
	if len(submits) != 1 || submits[0] != "/new" {
		t.Fatalf("generic command selection = %#v", submits)
	}
	app.modelPickerMu.Lock()
	activeToken := app.modelPickerToken
	activeLayer := app.modelPickerLayer
	app.modelPickerMu.Unlock()
	if activeToken != token || activeLayer != layer || app.screen.LayerCount() != 2 {
		t.Fatalf("generic selection disturbed picker token=%d/%d layerSame=%v layers=%d", activeToken, token, activeLayer == layer, app.screen.LayerCount())
	}

	app.processMessage(KeyMsg{Key: int(terminal.KeyEnter)})
	select {
	case action := <-selected:
		if action != ModelPickerActionSelectExecution {
			t.Fatalf("picker action = %v", action)
		}
	default:
		t.Fatal("picker selection did not invoke its action")
	}
}

func TestWidgetApp_ModelPickerSurvivesSearchCancelAboveIt(t *testing.T) {
	app := newEventDeliveryApp(t)
	items := []widgets.PaletteItem{{ID: "model", Label: "model", Data: "model"}}
	app.ShowModelPicker(items, ModelPickerActionSelectExecution)
	picker, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("model picker was not admitted")
	}
	app.processMessage(picker)
	app.modelPickerMu.Lock()
	token := app.modelPickerToken
	layer := app.modelPickerLayer
	app.modelPickerMu.Unlock()

	app.processMessage(KeyMsg{Key: int(terminal.KeyCtrlF)})
	if got := app.screen.LayerCount(); got != 3 {
		t.Fatalf("search layer count = %d, want 3", got)
	}
	app.processMessage(KeyMsg{Key: int(terminal.KeyEscape)})
	app.modelPickerMu.Lock()
	activeToken := app.modelPickerToken
	activeLayer := app.modelPickerLayer
	app.modelPickerMu.Unlock()
	if activeToken != token || activeLayer != layer || app.screen.LayerCount() != 2 {
		t.Fatalf("search cancel disturbed picker token=%d/%d layerSame=%v layers=%d", activeToken, token, activeLayer == layer, app.screen.LayerCount())
	}

	app.processMessage(KeyMsg{Key: int(terminal.KeyEscape)})
	app.modelPickerMu.Lock()
	activeToken = app.modelPickerToken
	activeLayer = app.modelPickerLayer
	app.modelPickerMu.Unlock()
	if activeToken != 0 || activeLayer != nil || app.screen.LayerCount() != 1 {
		t.Fatalf("picker cancel retained token=%d layer=%v layers=%d", activeToken, activeLayer != nil, app.screen.LayerCount())
	}
}

func TestWidgetApp_ModelPickerReplacementDefersBelowApproval(t *testing.T) {
	app := newEventDeliveryApp(t)
	var approvalDecisions int
	app.SetApprovalCallback(func(_ string, approved, _ bool) {
		if approved {
			t.Fatal("escape unexpectedly approved request")
		}
		approvalDecisions++
	})
	selected := make(chan ModelPickerAction, 1)
	app.SetModelPickerActionCallback(func(action ModelPickerAction, _ string, _ any) { selected <- action })
	items := []widgets.PaletteItem{{ID: "model", Label: "model", Data: "model"}}

	app.ShowModelPicker(items, ModelPickerActionSelectExecution)
	picker, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("model picker was not admitted")
	}
	app.processMessage(picker)
	app.modelPickerMu.Lock()
	oldToken := app.modelPickerToken
	oldLayer := app.modelPickerLayer
	app.modelPickerMu.Unlock()
	if !app.RequestApproval(ApprovalRequestMsg{ID: "picker-approval", Tool: "shell"}) {
		t.Fatal("approval was not admitted")
	}
	approval, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("approval was not queued")
	}
	app.processMessage(approval)
	approvalLayer := app.screen.TopLayer().Root

	app.ShowModelPicker(items, ModelPickerActionToggleCurated)
	replacement, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("replacement picker was not admitted")
	}
	app.processMessage(replacement)
	app.modelPickerMu.Lock()
	activeToken := app.modelPickerToken
	activeLayer := app.modelPickerLayer
	pending := app.modelPickerPending
	app.modelPickerMu.Unlock()
	if app.screen.TopLayer().Root != approvalLayer || app.screen.LayerCount() != 3 ||
		activeToken != oldToken || activeLayer != oldLayer || pending == nil {
		t.Fatalf("replacement disturbed approval/top token=%d/%d layerSame=%v pending=%v layers=%d", activeToken, oldToken, activeLayer == oldLayer, pending != nil, app.screen.LayerCount())
	}

	app.processMessage(KeyMsg{Key: int(terminal.KeyEscape)})
	app.modelPickerMu.Lock()
	newToken := app.modelPickerToken
	newAction := app.modelPickerAction
	newLayer := app.modelPickerLayer
	pending = app.modelPickerPending
	app.modelPickerMu.Unlock()
	if approvalDecisions != 1 || app.EventDeliverySnapshot().OutstandingApprovals != 0 {
		t.Fatalf("approval resolution count=%d snapshot=%+v", approvalDecisions, app.EventDeliverySnapshot())
	}
	if newToken == 0 || newToken == oldToken || newAction != ModelPickerActionToggleCurated ||
		newLayer == nil || newLayer == oldLayer || pending != nil || app.screen.LayerCount() != 2 {
		t.Fatalf("deferred replacement token=%d old=%d action=%v layerChanged=%v pending=%v layers=%d", newToken, oldToken, newAction, newLayer != oldLayer, pending != nil, app.screen.LayerCount())
	}

	app.processMessage(KeyMsg{Key: int(terminal.KeyEnter)})
	select {
	case action := <-selected:
		if action != ModelPickerActionToggleCurated {
			t.Fatalf("replacement picker action = %v", action)
		}
	default:
		t.Fatal("replacement picker did not invoke its action")
	}
}

func TestWidgetApp_StaleModelPickerSelectionCannotConsumeReplacement(t *testing.T) {
	app := newEventDeliveryApp(t)
	var selected []ModelPickerAction
	var genericSubmits int
	app.onSubmit = func(string) { genericSubmits++ }
	app.SetModelPickerActionCallback(func(action ModelPickerAction, _ string, _ any) {
		selected = append(selected, action)
	})
	items := []widgets.PaletteItem{{ID: "model", Label: "model", Data: "model"}}

	app.ShowModelPicker(items, ModelPickerActionSelectExecution)
	first, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("first picker was not admitted")
	}
	app.processMessage(first)
	app.modelPickerMu.Lock()
	oldToken := app.modelPickerToken
	app.modelPickerMu.Unlock()
	app.ShowModelPicker(items, ModelPickerActionToggleCurated)
	replacement, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("replacement picker was not admitted")
	}
	app.processMessage(replacement)
	app.modelPickerMu.Lock()
	newToken := app.modelPickerToken
	newLayer := app.modelPickerLayer
	app.modelPickerMu.Unlock()

	app.handleCommand(runtime.PaletteSelected{
		ID:   "new",
		Data: modelPickerSelectionData{token: oldToken, data: "stale"},
	})
	app.handleCommand(modelPickerPopOverlay{token: oldToken})
	app.modelPickerMu.Lock()
	activeToken := app.modelPickerToken
	activeAction := app.modelPickerAction
	activeLayer := app.modelPickerLayer
	app.modelPickerMu.Unlock()
	if len(selected) != 0 || genericSubmits != 0 || activeToken != newToken ||
		activeAction != ModelPickerActionToggleCurated || activeLayer != newLayer || app.screen.LayerCount() != 2 {
		t.Fatalf("stale selection consumed replacement callbacks=%v generic=%d token=%d/%d action=%v layerSame=%v layers=%d", selected, genericSubmits, activeToken, newToken, activeAction, activeLayer == newLayer, app.screen.LayerCount())
	}

	app.processMessage(KeyMsg{Key: int(terminal.KeyEnter)})
	if len(selected) != 1 || selected[0] != ModelPickerActionToggleCurated {
		t.Fatalf("replacement selection actions = %#v", selected)
	}
}

func TestWidgetApp_OutstandingApprovalCapBoundsDisplayedDialogs(t *testing.T) {
	app := newEventDeliveryApp(t)
	total := eventApprovalMaxCount + 7
	callbacks := make(map[string]int, total)
	app.SetApprovalCallback(func(id string, _ bool, _ bool) {
		callbacks[id]++
	})

	accepted := 0
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("approval-cap-%02d", i)
		if app.RequestApproval(ApprovalRequestMsg{ID: id, Tool: "shell", Description: "bounded"}) {
			accepted++
			msg, ok := app.takeCriticalEvent()
			if !ok {
				t.Fatalf("accepted approval %q was not queued", id)
			}
			app.processMessage(msg)
		}
	}
	if accepted != eventApprovalMaxCount {
		t.Fatalf("accepted approvals = %d, want %d", accepted, eventApprovalMaxCount)
	}
	if got := app.screen.LayerCount(); got != 1+eventApprovalMaxCount {
		t.Fatalf("approval layers = %d, want %d", got, 1+eventApprovalMaxCount)
	}
	snapshot := app.EventDeliverySnapshot()
	if snapshot.OutstandingApprovals != eventApprovalMaxCount || snapshot.ApprovalBytes <= 0 ||
		snapshot.ApprovalBytes > eventApprovalMaxBytes || snapshot.ApprovalCapRejections != 7 {
		t.Fatalf("approval cap snapshot = %+v", snapshot)
	}

	app.Quit()
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("approval-cap-%02d", i)
		if callbacks[id] != 1 {
			t.Fatalf("approval %q callback count = %d, want 1", id, callbacks[id])
		}
	}
	snapshot = app.EventDeliverySnapshot()
	if snapshot.OutstandingApprovals != 0 || snapshot.ApprovalBytes != 0 ||
		snapshot.ApprovalsCancelled != eventApprovalMaxCount || snapshot.RejectedApprovals != 7 {
		t.Fatalf("approval close snapshot = %+v", snapshot)
	}
}

func TestWidgetApp_RejectedStreamBoundaryIsCorrelatedVisibleAndGenerationSafe(t *testing.T) {
	app := newEventDeliveryApp(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var blockFirst sync.Once
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 128, MaxWait: time.Hour}, func(msg Message) {
		blockFirst.Do(func() {
			close(started)
			<-release
		})
		app.postCoalescerPublication(msg)
	})

	payload := strings.Repeat("x", 128)
	publisherDone := make(chan struct{})
	go func() {
		app.coalescer.AddStream("fill-000", 1, payload)
		close(publisherDone)
	}()
	<-started
	ordinaryLimit := coalescerPendingMaxCount - coalescerBoundaryReserve
	for i := 1; i < ordinaryLimit; i++ {
		app.coalescer.AddStream(fmt.Sprintf("fill-%03d", i), 1, payload)
	}

	const target = "rejected-target"
	app.StreamChunk(target, "")
	app.streamMu.Lock()
	rejectedGeneration := app.streamGenerations[target]
	app.streamMu.Unlock()
	if rejectedGeneration == 0 {
		t.Fatal("target stream did not receive a generation")
	}
	app.coalescer.AddStream(target, rejectedGeneration, "pending-final")
	app.StreamEnd(target, "")
	// Model a late old-generation chunk and a new round arriving before the
	// rejected terminal marker reaches the UI loop.
	app.coalescer.AddStream(target, rejectedGeneration, "late-old")
	app.StreamChunk(target, "next-round")
	app.streamMu.Lock()
	nextGeneration := app.streamGenerations[target]
	app.streamMu.Unlock()
	if nextGeneration == 0 || nextGeneration == rejectedGeneration {
		t.Fatalf("next generation = %d after rejected generation %d", nextGeneration, rejectedGeneration)
	}

	close(release)
	select {
	case <-publisherDone:
	case <-time.After(time.Second):
		t.Fatal("blocked stream publisher did not drain")
	}

	var marker streamBoundaryRejectedMsg
	foundMarker := false
	for {
		msg, ok := app.takeCriticalEvent()
		if !ok {
			break
		}
		if candidate, ok := msg.(streamBoundaryRejectedMsg); ok {
			marker = candidate
			foundMarker = true
		}
		app.processMessage(msg)
	}
	if !foundMarker || marker.Fingerprint != streamFingerprint(target) || marker.Generation != rejectedGeneration {
		t.Fatalf("correlated boundary marker = %+v found=%v", marker, foundMarker)
	}
	if app.coalescer.HasPendingStream(target, rejectedGeneration) {
		t.Fatal("rejected generation retained late buffered text")
	}
	if !app.coalescer.HasPendingStream(target, nextGeneration) {
		t.Fatal("rejected boundary cleared the next generation")
	}
	app.streamMu.Lock()
	activeGeneration := app.streamGenerations[target]
	app.streamMu.Unlock()
	if activeGeneration != nextGeneration {
		t.Fatalf("active generation = %d, want next generation %d", activeGeneration, nextGeneration)
	}
	app.render()
	if capture := app.backend.(*sim.Backend).Capture(); !strings.Contains(capture, "A streamed response ended") {
		t.Fatalf("terminal diagnostic was not visible:\n%s", capture)
	}
}

func TestWidgetApp_DetachesSubstringAndSliceBackingBeforeRetention(t *testing.T) {
	app := newEventDeliveryApp(t)
	largeText := strings.Repeat("x", eventOverflowMaxBytes)
	substring := largeText[:1]
	app.Post(AddMessageMsg{Content: substring, Source: "assistant"})
	msg := (<-app.messages).(AddMessageMsg)
	if unsafe.StringData(msg.Content) == unsafe.StringData(substring) {
		t.Fatal("retained substring still references the large backing string")
	}

	largeBacking := make([]byte, 1, eventOverflowMaxBytes)
	largeBacking[0] = 7
	app.Post(ToolResult{ToolID: "slice", Result: largeBacking})
	result := (<-app.messages).(ToolResult).Result.([]byte)
	if len(result) != 1 || cap(result) != 1 || unsafe.SliceData(result) == unsafe.SliceData(largeBacking) {
		t.Fatalf("retained slice len=%d cap=%d detached=%v", len(result), cap(result), unsafe.SliceData(result) != unsafe.SliceData(largeBacking))
	}
}

func TestWidgetApp_PostQuitStreamFloodDoesNotRetainGenerationsOrBuffers(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.StreamChunk("active", "pending")
	app.Quit()
	for i := 0; i < coalescerBufferMaxCount*4; i++ {
		sessionID := fmt.Sprintf("post-quit-%d", i)
		app.StreamChunk(sessionID, "payload")
		app.StreamEnd(sessionID, "payload")
	}
	app.streamMu.Lock()
	generations := len(app.streamGenerations)
	app.streamMu.Unlock()
	coalescerSnapshot := app.coalescer.Snapshot()
	if generations != 0 || coalescerSnapshot.BufferedStreams != 0 || coalescerSnapshot.BufferBytes != 0 || coalescerSnapshot.PendingCount != 0 {
		t.Fatalf("post-quit stream retention generations=%d coalescer=%+v", generations, coalescerSnapshot)
	}
	if app.EventDeliverySnapshot().RejectedAfterClose < coalescerBufferMaxCount*8 {
		t.Fatalf("post-quit rejections = %+v", app.EventDeliverySnapshot())
	}
}

func TestWidgetApp_RunIsSingleUseAndFinalizesOnceUnderConcurrentQuit(t *testing.T) {
	be := &countingDeliveryBackend{Backend: sim.New(80, 24)}
	app, err := NewWidgetApp(WidgetAppConfig{Backend: be})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.finalizeBackend)

	runDone := make(chan error, 1)
	go func() { runDone <- app.Run() }()
	deadline := time.Now().Add(time.Second)
	for app.runState.Load() != widgetAppRunning && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if app.runState.Load() != widgetAppRunning {
		t.Fatal("Run did not acquire lifecycle ownership")
	}

	var producers sync.WaitGroup
	for i := 0; i < 4; i++ {
		producers.Add(1)
		go func(id int) {
			defer producers.Done()
			for n := 0; n < 500; n++ {
				app.Post(KeyMsg{Rune: rune('a' + id)})
			}
		}(i)
	}
	app.Quit()
	producers.Wait()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("first Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after Quit")
	}
	if err := app.Run(); err == nil {
		t.Fatal("second Run unexpectedly succeeded")
	}
	if got := be.finiCount.Load(); got != 1 {
		t.Fatalf("backend Fini count = %d, want 1", got)
	}
	app.Post(PasteMsg{Text: strings.Repeat("released", 1024)})
	if !app.EventDeliverySnapshot().Closed {
		t.Fatal("post-Run delivery was not closed")
	}
}

func TestWidgetApp_RunQuitPostStartRaceFinalizesOnce(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		be := &countingDeliveryBackend{Backend: sim.New(40, 12)}
		app, err := NewWidgetApp(WidgetAppConfig{Backend: be})
		if err != nil {
			t.Fatalf("iteration %d NewWidgetApp: %v", iteration, err)
		}
		start := make(chan struct{})
		runDone := make(chan error, 1)
		quitDone := make(chan struct{})
		postDone := make(chan struct{})
		go func() {
			<-start
			runDone <- app.Run()
		}()
		go func() {
			<-start
			app.Quit()
			close(quitDone)
		}()
		go func() {
			<-start
			for i := 0; i < 100; i++ {
				app.Post(KeyMsg{Key: int(terminal.KeyRune), Rune: 'x'})
			}
			close(postDone)
		}()
		close(start)
		select {
		case <-runDone:
		case <-time.After(time.Second):
			app.finalizeBackend()
			t.Fatalf("iteration %d Run did not terminate", iteration)
		}
		<-quitDone
		<-postDone
		app.finalizeBackend()
		if got := be.finiCount.Load(); got != 1 {
			t.Fatalf("iteration %d backend Fini count = %d, want 1", iteration, got)
		}
		if err := app.Run(); err == nil {
			t.Fatalf("iteration %d second Run unexpectedly succeeded", iteration)
		}
	}
}

func TestWidgetApp_PostDoesNotBlockAtHardCap(t *testing.T) {
	app := newEventDeliveryApp(t)
	fillFastQueue(app)
	done := make(chan struct{})
	go func() {
		for i := 0; i < eventOverflowMaxCount*4; i++ {
			app.Post(deliveryTestMsg{ID: i})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Post blocked at the overflow cap")
	}
}
