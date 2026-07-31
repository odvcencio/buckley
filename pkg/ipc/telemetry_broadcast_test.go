package ipc

import (
	"testing"

	"m31labs.dev/buckley/v2/pkg/config"
	"m31labs.dev/buckley/v2/pkg/telemetry"
)

type recordingForwarder struct {
	events []Event
}

func (f *recordingForwarder) BroadcastEvent(event Event) {
	f.events = append(f.events, event)
}

func telemetryEventWithPayload() telemetry.Event {
	return telemetry.Event{
		Type:      telemetry.EventToolCompleted,
		SessionID: "sess-1",
		Data: map[string]any{
			"toolName":  "read_file",
			"arguments": `{"path":".env"}`,
			"result":    "SECRET=abc123",
		},
	}
}

func TestBroadcastTelemetry_StripsPayloadsByDefault(t *testing.T) {
	hub := NewHub()
	forwarder := &recordingForwarder{}
	hub.AddForwarder(forwarder)

	s := &Server{hub: hub, appConfig: &config.Config{}}
	s.broadcastTelemetry(telemetryEventWithPayload())

	if len(forwarder.events) != 1 {
		t.Fatalf("expected 1 broadcast event, got %d", len(forwarder.events))
	}
	payload, ok := forwarder.events[0].Payload.(telemetry.Event)
	if !ok {
		t.Fatalf("payload is not a telemetry.Event: %#v", forwarder.events[0].Payload)
	}
	if _, ok := payload.Data["arguments"]; ok {
		t.Fatalf("expected arguments stripped from network broadcast: %+v", payload.Data)
	}
	if _, ok := payload.Data["result"]; ok {
		t.Fatalf("expected result stripped from network broadcast: %+v", payload.Data)
	}
	if payload.Data["toolName"] != "read_file" {
		t.Fatalf("expected non-payload fields preserved: %+v", payload.Data)
	}
}

func TestBroadcastTelemetry_PassesPayloadsWhenFlagEnabled(t *testing.T) {
	hub := NewHub()
	forwarder := &recordingForwarder{}
	hub.AddForwarder(forwarder)

	appCfg := &config.Config{}
	appCfg.Diagnostics.TelemetryPayloadsOverNetwork = true
	s := &Server{hub: hub, appConfig: appCfg}
	s.broadcastTelemetry(telemetryEventWithPayload())

	if len(forwarder.events) != 1 {
		t.Fatalf("expected 1 broadcast event, got %d", len(forwarder.events))
	}
	payload, ok := forwarder.events[0].Payload.(telemetry.Event)
	if !ok {
		t.Fatalf("payload is not a telemetry.Event: %#v", forwarder.events[0].Payload)
	}
	if payload.Data["arguments"] != `{"path":".env"}` || payload.Data["result"] != "SECRET=abc123" {
		t.Fatalf("expected payload fields preserved when flag enabled: %+v", payload.Data)
	}
}

func TestBroadcastTelemetry_NilAppConfigDefaultsToStripped(t *testing.T) {
	hub := NewHub()
	forwarder := &recordingForwarder{}
	hub.AddForwarder(forwarder)

	s := &Server{hub: hub}
	s.broadcastTelemetry(telemetryEventWithPayload())

	payload := forwarder.events[0].Payload.(telemetry.Event)
	if _, ok := payload.Data["arguments"]; ok {
		t.Fatalf("expected arguments stripped when appConfig is nil: %+v", payload.Data)
	}
}
