package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/policy"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/fluffyui/backend/sim"
	"m31labs.dev/fluffyui/terminal"
)

func TestTUIApprovalBroker_ApproveAndRememberScopedDecision(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.finalizeBackend)
	router := newTUIApprovalRouter()
	broker := newTUIApprovalBroker("session-1", router)
	broker.bindApp(app)
	app.SetApprovalCallback(func(id string, approved, alwaysAllow bool) {
		router.resolve(id, approved, alwaysAllow)
	})
	req := tool.PermissionApprovalRequest{
		ID:   "call-1",
		Tool: "run_shell",
		Permission: policy.PermissionRequest{
			Tool:     "run_shell",
			Category: "shell",
			Arg:      "rm -rf /etc/passwd",
		},
		Decision: policy.PermissionDecision{
			Layer: "built-in defaults",
			Rule:  policy.PermissionRule{Tool: "run_shell", ArgPattern: "*rm -rf*", Action: policy.PermissionAsk},
		},
		Scope: "run_shell\x00shell\x00built-in defaults",
	}

	result := make(chan approvalWaitResult, 1)
	go func() {
		response, err := broker.Request(context.Background(), req)
		result <- approvalWaitResult{response: response, err: err}
	}()

	msg, ok := waitApprovalMessage(app)
	if !ok {
		t.Fatal("approval request was not queued")
	}
	approval, ok := msg.(ApprovalRequestMsg)
	if !ok {
		t.Fatalf("queued message = %T, want ApprovalRequestMsg", msg)
	}
	if !strings.HasPrefix(approval.ID, "approval:v1:") || strings.Contains(approval.ID, "session-1") || strings.Contains(approval.ID, "call-1") || approval.Command != req.Permission.Arg || approval.FilePath != "" {
		t.Fatalf("unexpected structured approval message: %+v", approval)
	}
	if approval.Description == "" {
		t.Fatal("approval description is empty")
	}
	app.resolveApproval(approval.ID, true, true)
	select {
	case got := <-result:
		if got.err != nil || !got.response.Approved || !got.response.AlwaysAllow {
			t.Fatalf("approval result = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("approval waiter did not unblock")
	}

	second, err := broker.Request(context.Background(), req)
	if err != nil || !second.Approved || !second.AlwaysAllow {
		t.Fatalf("remembered approval = %+v err=%v", second, err)
	}
	if _, ok := app.takeCriticalEvent(); ok {
		t.Fatal("remembered approval unexpectedly queued another UI request")
	}
}

func TestTUIApprovalBroker_DenyUnblocksWithoutRemembering(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.finalizeBackend)
	router := newTUIApprovalRouter()
	broker := newTUIApprovalBroker("session-1", router)
	broker.bindApp(app)
	app.SetApprovalCallback(func(id string, approved, alwaysAllow bool) {
		router.resolve(id, approved, alwaysAllow)
	})
	req := tool.PermissionApprovalRequest{
		ID:         "call-deny",
		Tool:       "run_shell",
		Permission: policy.PermissionRequest{Tool: "run_shell", Category: "shell", Arg: "rm -rf /etc/passwd"},
		Decision:   policy.PermissionDecision{Layer: "built-in defaults", Rule: policy.PermissionRule{Action: policy.PermissionAsk}},
		Scope:      "deny-scope",
	}
	result := make(chan approvalWaitResult, 1)
	go func() {
		response, err := broker.Request(context.Background(), req)
		result <- approvalWaitResult{response: response, err: err}
	}()
	msg, ok := waitApprovalMessage(app)
	if !ok {
		t.Fatal("approval request was not queued")
	}
	approval := msg.(ApprovalRequestMsg)
	if !app.resolveApproval(approval.ID, false, false) {
		t.Fatal("approval resolve returned false")
	}
	select {
	case got := <-result:
		if got.err != nil || got.response.Approved {
			t.Fatalf("denial result = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("denied approval waiter did not unblock")
	}

	secondResult := make(chan error, 1)
	go func() {
		_, err := broker.Request(context.Background(), req)
		secondResult <- err
	}()
	if _, ok := waitApprovalMessage(app); !ok {
		t.Fatal("denied scope was incorrectly remembered")
	}
	broker.close()
	select {
	case err := <-secondResult:
		if err == nil {
			t.Fatal("closed broker unexpectedly approved second request")
		}
	case <-time.After(10 * time.Millisecond):
		t.Fatal("second approval waiter did not unblock on broker close")
	}
}

func waitApprovalMessage(app *WidgetApp) (Message, bool) {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if msg, ok := app.takeCriticalEvent(); ok {
			return msg, true
		}
		time.Sleep(time.Millisecond)
	}
	return nil, false
}

func TestTUIApprovalBroker_RejectsClosedUI(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.finalizeBackend)
	broker := newTUIApprovalBroker("session-1", newTUIApprovalRouter())
	broker.bindApp(app)
	app.Quit()
	_, err = broker.Request(context.Background(), tool.PermissionApprovalRequest{ID: "closed", Tool: "run_shell", Permission: policy.PermissionRequest{Tool: "run_shell", Category: "shell", Arg: "rm -rf /"}})
	if err == nil {
		t.Fatal("closed UI approval unexpectedly succeeded")
	}
}

func TestTUIApprovalBroker_ContextCancelReleasesQueuedApprovalExactlyOnce(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.finalizeBackend)
	router := newTUIApprovalRouter()
	broker := newTUIApprovalBroker("session-cancel", router)
	broker.bindApp(app)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, requestErr := broker.Request(ctx, tool.PermissionApprovalRequest{
			ID:         "request-cancel",
			Tool:       "run_shell",
			Permission: policy.PermissionRequest{Tool: "run_shell", Category: "shell", Arg: "rm -rf /outside"},
			Scope:      "cancel-scope",
		})
		result <- requestErr
	}()

	deadline := time.Now().Add(time.Second)
	for (broker.pendingCount() != 1 || app.EventDeliverySnapshot().OutstandingApprovals != 1) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if broker.pendingCount() != 1 || app.EventDeliverySnapshot().OutstandingApprovals != 1 {
		t.Fatalf("approval was not queued: broker=%d app=%+v", broker.pendingCount(), app.EventDeliverySnapshot())
	}
	cancel()
	select {
	case requestErr := <-result:
		if !errors.Is(requestErr, context.Canceled) {
			t.Fatalf("request error = %v, want context.Canceled", requestErr)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled approval waiter did not unblock")
	}

	snapshot := app.EventDeliverySnapshot()
	if broker.pendingCount() != 0 || router.routeCount() != 0 || snapshot.OutstandingApprovals != 0 || snapshot.ApprovalBytes != 0 || snapshot.ApprovalsCancelled != 1 {
		t.Fatalf("cancel did not release approval exactly once: broker=%d routes=%d app=%+v", broker.pendingCount(), router.routeCount(), snapshot)
	}
	cancelMessage, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("cancelled approval did not enqueue UI cleanup")
	}
	if _, ok := cancelMessage.(approvalCancelledMsg); !ok {
		t.Fatalf("cancellation cleanup message = %T", cancelMessage)
	}
	app.processMessage(cancelMessage)
	if _, ok := app.takeCriticalEvent(); ok {
		t.Fatal("cancelled queued approval remained displayable after cleanup")
	}
	cancel()
	if got := app.EventDeliverySnapshot().ApprovalsCancelled; got != 1 {
		t.Fatalf("duplicate cancellation count = %d, want 1", got)
	}
}

func TestTUIApprovalBroker_ContextCancelRemovesExactDisplayedLayer(t *testing.T) {
	for _, tt := range []struct {
		name         string
		overlayAbove bool
	}{
		{name: "top approval"},
		{name: "under unrelated overlay", overlayAbove: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
			if err != nil {
				t.Fatalf("NewWidgetApp: %v", err)
			}
			t.Cleanup(app.finalizeBackend)
			router := newTUIApprovalRouter()
			broker := newTUIApprovalBroker("session-displayed-cancel", router)
			broker.bindApp(app)
			app.SetApprovalCallback(func(id string, approved, alwaysAllow bool) {
				router.resolve(id, approved, alwaysAllow)
			})
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() {
				_, requestErr := broker.Request(ctx, tool.PermissionApprovalRequest{
					ID:         "displayed-cancel",
					Tool:       "run_shell",
					Permission: policy.PermissionRequest{Tool: "run_shell", Category: "shell", Arg: "rm -rf /outside"},
					Scope:      "displayed-cancel-scope",
				})
				result <- requestErr
			}()

			message, ok := waitApprovalMessage(app)
			if !ok {
				t.Fatal("approval request was not queued")
			}
			approval := message.(ApprovalRequestMsg)
			app.processMessage(message)
			if got := app.screen.LayerCount(); got != 2 {
				t.Fatalf("displayed approval layers = %d, want 2", got)
			}
			app.approvalLayerMu.Lock()
			displayedLayer := app.approvalLayers[approval.ID]
			app.approvalLayerMu.Unlock()
			if displayedLayer == nil {
				t.Fatal("displayed approval layer was not tracked")
			}

			var unrelatedLayer any
			if tt.overlayAbove {
				app.showCommandPalette()
				unrelatedLayer = app.screen.TopLayer().Root
				if got := app.screen.LayerCount(); got != 3 {
					t.Fatalf("stacked modal layers = %d, want 3", got)
				}
			}

			cancel()
			select {
			case requestErr := <-result:
				if !errors.Is(requestErr, context.Canceled) {
					t.Fatalf("request error = %v, want context.Canceled", requestErr)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled displayed approval waiter did not unblock")
			}
			cleanup, ok := waitApprovalMessage(app)
			if !ok {
				t.Fatal("displayed cancellation did not enqueue UI cleanup")
			}
			cancelled, ok := cleanup.(approvalCancelledMsg)
			if !ok || cancelled.RequestID != approval.ID {
				t.Fatalf("cleanup message = %#v", cleanup)
			}
			app.processMessage(cleanup)

			wantLayers := 1
			if tt.overlayAbove {
				wantLayers = 2
				if app.screen.TopLayer().Root != unrelatedLayer {
					t.Fatal("approval cancellation removed or replaced the unrelated modal")
				}
			}
			if got := app.screen.LayerCount(); got != wantLayers {
				t.Fatalf("layers after exact cancellation = %d, want %d", got, wantLayers)
			}
			app.approvalLayerMu.Lock()
			_, retainedLayer := app.approvalLayers[approval.ID]
			app.approvalLayerMu.Unlock()
			snapshot := app.EventDeliverySnapshot()
			if retainedLayer || broker.pendingCount() != 0 || router.routeCount() != 0 ||
				snapshot.OutstandingApprovals != 0 || snapshot.ApprovalBytes != 0 || snapshot.ApprovalsCancelled != 1 {
				t.Fatalf("displayed cancellation retained state layer=%v broker=%d routes=%d snapshot=%+v", retainedLayer, broker.pendingCount(), router.routeCount(), snapshot)
			}
			if app.resolveApproval(approval.ID, true, false) {
				t.Fatal("stale response resolved a cancelled approval")
			}
			if app.CancelApproval(approval.ID) {
				t.Fatal("duplicate cancellation released approval twice")
			}
			if got := app.EventDeliverySnapshot().ApprovalsCancelled; got != 1 {
				t.Fatalf("duplicate cancellation count = %d, want 1", got)
			}
		})
	}
}

func TestWidgetApp_CancelDisplayedApprovalBypassesSaturatedInteractiveQueue(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.finalizeBackend)
	callbackCount := 0
	app.SetApprovalCallback(func(string, bool, bool) { callbackCount++ })
	const requestID = "saturated-displayed-approval"
	if !app.RequestApproval(ApprovalRequestMsg{ID: requestID, Tool: "run_shell", Description: "saturated cancellation"}) {
		t.Fatal("approval was not admitted")
	}
	message, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("approval was not queued")
	}
	app.processMessage(message)
	app.showCommandPalette()
	unrelatedLayer := app.screen.TopLayer().Root
	if got := app.screen.LayerCount(); got != 3 {
		t.Fatalf("stacked layer count = %d, want 3", got)
	}

	accepted := 0
	for i := 0; i < cap(app.messages)+eventOverflowMaxCount*2; i++ {
		if !app.postEvent(KeyMsg{Key: int(terminal.KeyRune), Rune: 'x'}) {
			break
		}
		accepted++
	}
	saturated := app.EventDeliverySnapshot()
	if accepted == 0 || saturated.FastQueued != cap(app.messages) || saturated.OverflowQueued == 0 ||
		!saturated.Overloaded || saturated.RejectedInteractive == 0 {
		t.Fatalf("interactive queue did not saturate: accepted=%d snapshot=%+v", accepted, saturated)
	}

	if !app.CancelApproval(requestID) {
		t.Fatal("displayed approval cancellation was not accepted")
	}
	if app.CancelApproval(requestID) {
		t.Fatal("duplicate cancellation was accepted")
	}
	queued := app.EventDeliverySnapshot()
	if queued.OutstandingApprovals != 0 || queued.ApprovalBytes != 0 ||
		queued.ApprovalCancellations != 1 || queued.ApprovalCancellationBytes == 0 ||
		queued.ApprovalCancellations > eventApprovalMaxCount || queued.ApprovalCancellationBytes > eventApprovalMaxBytes {
		t.Fatalf("bounded cancellation lane snapshot = %+v", queued)
	}

	cleanup, ok := app.takeCriticalEvent()
	if !ok {
		t.Fatal("saturated queue lost approval cancellation")
	}
	cancelled, ok := cleanup.(approvalCancelledMsg)
	if !ok || cancelled.RequestID != requestID {
		t.Fatalf("priority cleanup = %#v", cleanup)
	}
	app.processMessage(cleanup)
	if app.screen.TopLayer().Root != unrelatedLayer || app.screen.LayerCount() != 2 {
		t.Fatalf("cleanup disturbed unrelated overlay layers=%d sameTop=%v", app.screen.LayerCount(), app.screen.TopLayer().Root == unrelatedLayer)
	}
	if callbackCount != 0 || app.resolveApproval(requestID, true, false) {
		t.Fatalf("cancelled approval accepted stale response callbackCount=%d", callbackCount)
	}
	app.approvalLayerMu.Lock()
	_, retainedLayer := app.approvalLayers[requestID]
	app.approvalLayerMu.Unlock()
	final := app.EventDeliverySnapshot()
	if retainedLayer || final.ApprovalCancellations != 0 || final.ApprovalCancellationBytes != 0 ||
		final.ApprovalsCancelled != 1 || final.FastQueued != cap(app.messages) {
		t.Fatalf("cancellation cleanup state layer=%v snapshot=%+v", retainedLayer, final)
	}
}

func TestTUIApprovalRouter_RoutesSameRequestIDToExactBroker(t *testing.T) {
	router := newTUIApprovalRouter()
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.finalizeBackend)
	first := newTUIApprovalBroker("session-a", router)
	second := newTUIApprovalBroker("session-b", router)
	first.bindApp(app)
	second.bindApp(app)
	app.SetApprovalCallback(func(id string, approved, alwaysAllow bool) {
		router.resolve(id, approved, alwaysAllow)
	})
	req := tool.PermissionApprovalRequest{ID: "same-call", Tool: "run_shell", Permission: policy.PermissionRequest{Tool: "run_shell", Category: "shell", Arg: "rm -rf /outside"}, Scope: "same-scope"}
	results := make(chan approvalWaitResult, 2)
	go func() {
		response, requestErr := first.Request(context.Background(), req)
		results <- approvalWaitResult{response: response, err: requestErr}
	}()
	go func() {
		response, requestErr := second.Request(context.Background(), req)
		results <- approvalWaitResult{response: response, err: requestErr}
	}()
	one, ok := waitApprovalMessage(app)
	if !ok {
		t.Fatal("first routed approval was not queued")
	}
	two, ok := waitApprovalMessage(app)
	if !ok {
		t.Fatal("second routed approval was not queued")
	}
	firstID := one.(ApprovalRequestMsg).ID
	secondID := two.(ApprovalRequestMsg).ID
	if firstID == secondID {
		t.Fatalf("structured approval IDs collided: %q", firstID)
	}
	if !app.resolveApproval(secondID, false, false) || !app.resolveApproval(firstID, true, false) {
		t.Fatal("exact router failed to resolve an owned approval")
	}
	var approved, denied int
	for i := 0; i < 2; i++ {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("routed approval error: %v", got.err)
			}
			if got.response.Approved {
				approved++
			} else {
				denied++
			}
		case <-time.After(time.Second):
			t.Fatal("routed approval waiter did not unblock")
		}
	}
	if approved != 1 || denied != 1 || router.routeCount() != 0 {
		t.Fatalf("routed outcomes approved=%d denied=%d routes=%d", approved, denied, router.routeCount())
	}
}
