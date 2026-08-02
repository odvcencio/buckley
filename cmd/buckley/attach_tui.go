package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	ipcpb "m31labs.dev/buckley/pkg/ipc/proto"
	"m31labs.dev/buckley/pkg/ipc/proto/ipcpbconnect"
	"m31labs.dev/buckley/pkg/ui/tui"
	"m31labs.dev/buckley/pkg/ui/viewmodel"
)

// runAttachTUI implements `buckley attach <session-id> --tui`: a full-screen
// observation client for a running session. It renders the session
// service's own view model -- the view.patch events broadcastViewPatch
// emits on every storage mutation, plus the initial patch Subscribe sends
// on connect -- so the observer shows the same state every other UI client
// consumes, with no second assembly path. Typed input forwards through
// SendCommand exactly like the line-mode attach when a session token is
// available; without one the session stays observation-only.
func runAttachTUI(ctx context.Context, client ipcpbconnect.BuckleyIPCClient, opts attachOptions) error {
	sessionToken := mintAttachSessionToken(ctx, client, opts, func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// app is declared before NewWidgetApp so the OnSubmit closure can hold
	// it: submissions only fire once app.Run is processing events, well
	// after the assignment below completes.
	var app *tui.WidgetApp
	onSubmit := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" || app == nil {
			return
		}
		if sessionToken == "" {
			app.Post(tui.AddMessageMsg{Content: "Observation only: no session token, so typed input is not sent.", Source: "system"})
			return
		}
		// The accepted input comes back through the session's own
		// view.patch as a stored user message, so it is not echoed
		// locally -- a local echo would duplicate it.
		go func(line string) {
			accepted, reason, err := sendAttachInput(ctx, client, opts, sessionToken, line)
			if err != nil {
				app.Post(tui.AddMessageMsg{Content: "send failed: " + err.Error(), Source: "system"})
				return
			}
			if !accepted {
				app.Post(tui.AddMessageMsg{Content: "send rejected: " + reason, Source: "system"})
			}
		}(text)
	}

	app, err := tui.NewWidgetApp(tui.WidgetAppConfig{
		ModelName: "observing " + opts.SessionID,
		OnSubmit:  onSubmit,
		OnQuit:    cancel,
	})
	if err != nil {
		return fmt.Errorf("start observation TUI: %w", err)
	}

	applier := newAttachViewApplier(app.Post)
	go runAttachTUIStream(ctx, client, opts, applier, app.Post)
	go func() {
		<-ctx.Done()
		app.Post(tui.QuitMsg{})
	}()

	app.Post(tui.StatusMsg{Text: "observing " + opts.SessionID + " at " + opts.Addr})
	return app.Run()
}

// attachViewApplier translates successive viewmodel.SessionState snapshots
// into TUI messages. Each view.patch carries the session's full transcript
// page, so the applier's job is diffing: append only messages it has not
// posted yet, and update status, streaming, and token displays only when
// they change.
type attachViewApplier struct {
	post       func(tui.Message)
	seen       map[string]bool
	lastStatus string
	lastTokens int
	streaming  bool
}

func newAttachViewApplier(post func(tui.Message)) *attachViewApplier {
	return &attachViewApplier{post: post, seen: make(map[string]bool)}
}

func (a *attachViewApplier) Apply(state *viewmodel.SessionState) {
	if a == nil || state == nil || a.post == nil {
		return
	}
	for _, msg := range state.Transcript.Messages {
		key := msg.ID
		if key == "" {
			key = fmt.Sprintf("%s|%d|%s", msg.Role, msg.Timestamp.UnixNano(), msg.Content)
		}
		if a.seen[key] {
			continue
		}
		a.seen[key] = true
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		a.post(tui.AddMessageMsg{Content: msg.Content, Source: attachMessageSource(msg.Role)})
	}

	if status := attachStatusLine(state); status != a.lastStatus {
		a.lastStatus = status
		a.post(tui.StatusMsg{Text: status})
	}

	if state.IsStreaming != a.streaming {
		a.streaming = state.IsStreaming
		a.post(tui.ThinkingMsg{Show: state.IsStreaming})
		if state.IsStreaming {
			a.post(tui.ProcessStatusMsg{Text: "Working…", Active: true, ResetElapsed: true})
		} else {
			a.post(tui.ProcessStatusMsg{Active: false})
		}
	}

	if state.Metrics.TotalTokens != a.lastTokens {
		a.lastTokens = state.Metrics.TotalTokens
		a.post(tui.TokensMsg{Tokens: state.Metrics.TotalTokens, CostCent: state.Metrics.TotalCost * 100})
	}
}

// attachMessageSource maps a viewmodel role onto the sources the chat view
// renders; anything unrecognized lands as a system line rather than being
// dropped.
func attachMessageSource(role string) string {
	switch role {
	case "user", "assistant", "system", "tool", "thinking":
		return role
	default:
		return "system"
	}
}

// attachStatusLine condenses a session state into the observer's status
// bar: session state, pause/awaiting flags, and the first running tool.
func attachStatusLine(state *viewmodel.SessionState) string {
	var parts []string
	if s := strings.TrimSpace(state.Status.State); s != "" {
		parts = append(parts, s)
	}
	if state.Status.Paused {
		parts = append(parts, "paused")
	}
	if state.Status.AwaitingUser {
		parts = append(parts, "awaiting user")
	}
	for _, call := range state.ActiveToolCalls {
		if call.Status == "running" {
			parts = append(parts, "tool: "+call.Name)
			break
		}
	}
	if len(parts) == 0 {
		return "observing"
	}
	return strings.Join(parts, " · ")
}

// decodeAttachViewPatch extracts the viewmodel.SessionState from a
// view.patch event. The server encodes viewmodel.Patch through its JSON
// field names (payloadToStruct), so the struct payload round-trips back
// through JSON here.
func decodeAttachViewPatch(evt *ipcpb.Event) (*viewmodel.SessionState, bool) {
	if evt == nil || evt.GetType() != "view.patch" {
		return nil, false
	}
	payload := evt.GetPayload()
	if payload == nil {
		return nil, false
	}
	raw, err := payload.MarshalJSON()
	if err != nil {
		return nil, false
	}
	var patch viewmodel.Patch
	if err := json.Unmarshal(raw, &patch); err != nil || patch.Session == nil {
		return nil, false
	}
	return patch.Session, true
}

// runAttachTUIStream is the observation loop behind runAttachTUI: the same
// subscribe-with-backoff shape as runAttachStream, but view.patch events
// feed the applier and the few user-facing alerts (approval requests,
// errors) land as system lines instead of stdout prints.
func runAttachTUIStream(ctx context.Context, client ipcpbconnect.BuckleyIPCClient, opts attachOptions, applier *attachViewApplier, post func(tui.Message)) {
	backoff := 500 * time.Millisecond
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		req := connect.NewRequest(&ipcpb.SubscribeRequest{SessionId: opts.SessionID})
		attachAuthHeader(req, opts.Token)
		stream, err := client.Subscribe(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			post(tui.StatusMsg{Text: fmt.Sprintf("stream connect failed; retrying in %s", backoff)})
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = 500 * time.Millisecond
		for stream.Receive() {
			evt := stream.Msg()
			if state, ok := decodeAttachViewPatch(evt); ok {
				applier.Apply(state)
				continue
			}
			switch evt.GetType() {
			case "approval.required", "error":
				if line := formatAttachEvent(evt); line != "" {
					post(tui.AddMessageMsg{Content: line, Source: "system"})
				}
			}
		}
		_ = stream.Close()
		if ctx.Err() != nil {
			return
		}
		post(tui.StatusMsg{Text: fmt.Sprintf("stream disconnected; reconnecting in %s", backoff)})
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
