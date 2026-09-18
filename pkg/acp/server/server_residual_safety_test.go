package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	acppb "m31labs.dev/buckley/pkg/acp/proto"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/coordination/coordinator"
	"m31labs.dev/buckley/pkg/coordination/security"
	"m31labs.dev/buckley/pkg/model"
)

func TestACPRequestToolExecutionDenialAndFailureUseSafeOutput(t *testing.T) {
	const secret = "tool-secret-sentinel"
	t.Run("approval denial", func(t *testing.T) {
		srv := newToolSafetyServer(t)
		registerToolSafetyAgent(t, srv, "agent-denied", []string{secret})
		stream := newCaptureToolExecutionStream(security.ContextWithClaims(context.Background(), &security.Claims{
			AgentID:      "agent-denied",
			Capabilities: []string{secret},
		}))

		err := srv.RequestToolExecution(&acppb.ToolExecutionRequest{AgentId: "agent-denied", Tool: "read_file"}, stream)
		if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "tool execution denied" {
			t.Fatalf("error = %v, want generic denied status", err)
		}
		if got := toolEventStatuses(stream.events); strings.Join(got, ",") != "started,denied" {
			t.Fatalf("events = %v, want started,denied", got)
		}
		if stream.events[1].GetOutput() != "tool execution denied" {
			t.Fatalf("denied output = %q", stream.events[1].GetOutput())
		}
		if strings.Contains(status.Convert(err).Message()+stream.events[1].String(), secret) {
			t.Fatalf("denial leaked secret in status/event")
		}
	})

	t.Run("execution failure", func(t *testing.T) {
		srv := newToolSafetyServer(t)
		srv.toolApprover = nil
		registerToolSafetyAgent(t, srv, "agent-failed", []string{"admin"})
		stream := newCaptureToolExecutionStream(security.ContextWithClaims(context.Background(), &security.Claims{
			AgentID:      "agent-failed",
			Capabilities: []string{"admin"},
		}))

		err := srv.RequestToolExecution(&acppb.ToolExecutionRequest{
			AgentId:    "agent-failed",
			Tool:       "missing_tool",
			Parameters: map[string]string{"secret": secret},
		}, stream)
		if status.Code(err) != codes.Internal || status.Convert(err).Message() != "tool execution failed" {
			t.Fatalf("error = %v, want generic failed status", err)
		}
		if got := toolEventStatuses(stream.events); strings.Join(got, ",") != "started,failed" {
			t.Fatalf("events = %v, want started,failed", got)
		}
		if stream.events[1].GetOutput() != "tool execution failed" {
			t.Fatalf("failed output = %q", stream.events[1].GetOutput())
		}
		if strings.Contains(status.Convert(err).Message()+stream.events[1].String(), secret) {
			t.Fatalf("execution failure leaked secret in status/event")
		}
	})
}

func TestACPRequestToolExecutionSendErrorPrecedesTerminalStatus(t *testing.T) {
	srv := newToolSafetyServer(t)
	registerToolSafetyAgent(t, srv, "agent-denied", []string{"limited"})
	sendErr := errors.New("client send failed")
	stream := newCaptureToolExecutionStream(security.ContextWithClaims(context.Background(), &security.Claims{
		AgentID:      "agent-denied",
		Capabilities: []string{"limited"},
	}))
	stream.failOnSend = 2
	stream.sendErr = sendErr

	err := srv.RequestToolExecution(&acppb.ToolExecutionRequest{AgentId: "agent-denied", Tool: "read_file"}, stream)
	if !errors.Is(err, sendErr) {
		t.Fatalf("error = %v, want send error precedence", err)
	}
	if got := toolEventStatuses(stream.events); strings.Join(got, ",") != "started" {
		t.Fatalf("events = %v, want only sent started event", got)
	}
}

func TestACPStreamInlineCompletionsErrorAndLengthFailClosed(t *testing.T) {
	const secret = "inline-provider-secret"
	t.Run("provider error after chunk", func(t *testing.T) {
		provider := &partialServerProvider{
			stream: func(context.Context, model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
				chunks := make(chan model.StreamChunk)
				errs := make(chan error)
				go func() {
					chunks <- model.StreamChunk{Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: "public"}}}}
					close(chunks)
					errs <- fmt.Errorf("provider failed: %s", secret)
					close(errs)
				}()
				return chunks, errs
			},
		}
		srv := newPartialServer(t, partialServerOptions{models: newPartialModelManager(t, provider)})
		stream := newCaptureInlineCompletionStream(context.Background())

		err := srv.StreamInlineCompletions(inlineSafetyRequest(), stream)
		if status.Code(err) != codes.Internal || status.Convert(err).Message() != "model response incomplete" {
			t.Fatalf("error = %v, want generic model error", err)
		}
		if len(stream.events) != 1 || stream.events[0].GetText() != "public" || stream.events[0].GetIsFinal() {
			t.Fatalf("events = %+v, want one nonfinal public chunk", stream.events)
		}
		if strings.Contains(status.Convert(err).Message(), secret) || strings.Contains(stream.events[0].String(), secret) {
			t.Fatalf("inline provider error leaked secret")
		}
	})

	t.Run("length finish is not final accepted", func(t *testing.T) {
		length := "length"
		provider := &partialServerProvider{
			stream: func(context.Context, model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
				chunks := make(chan model.StreamChunk, 2)
				errs := make(chan error)
				chunks <- model.StreamChunk{Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: "partial"}}}}
				chunks <- model.StreamChunk{Choices: []model.StreamChoice{{FinishReason: &length}}}
				close(chunks)
				close(errs)
				return chunks, errs
			},
		}
		srv := newPartialServer(t, partialServerOptions{models: newPartialModelManager(t, provider)})
		stream := newCaptureInlineCompletionStream(context.Background())

		err := srv.StreamInlineCompletions(inlineSafetyRequest(), stream)
		if status.Code(err) != codes.Internal || status.Convert(err).Message() != "model response incomplete" {
			t.Fatalf("error = %v, want incomplete model status", err)
		}
		for _, event := range stream.events {
			if event.GetIsFinal() {
				t.Fatalf("event = %+v, length finish must not be final accepted", event)
			}
		}
	})

	t.Run("in-band provider error after chunk", func(t *testing.T) {
		provider := &partialServerProvider{
			stream: func(context.Context, model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
				chunks := make(chan model.StreamChunk, 1)
				errs := make(chan error)
				chunks <- model.StreamChunk{
					Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: "safe-prefix"}}},
					Error:   &model.ErrorDetail{Message: "raw " + secret},
				}
				close(chunks)
				close(errs)
				return chunks, errs
			},
		}
		srv := newPartialServer(t, partialServerOptions{models: newPartialModelManager(t, provider)})
		stream := newCaptureInlineCompletionStream(context.Background())

		err := srv.StreamInlineCompletions(inlineSafetyRequest(), stream)
		if status.Code(err) != codes.Internal || status.Convert(err).Message() != "model response incomplete" {
			t.Fatalf("error = %v, want generic in-band model error", err)
		}
		if len(stream.events) != 1 || stream.events[0].GetText() != "safe-prefix" || stream.events[0].GetIsFinal() {
			t.Fatalf("events = %+v, want one nonfinal public chunk", stream.events)
		}
		if strings.Contains(status.Convert(err).Message()+stream.events[0].String(), secret) {
			t.Fatalf("in-band provider error leaked secret")
		}
	})
}

func TestACPApplyEditsPreflightsAllFilesBeforeWriting(t *testing.T) {
	root := t.TempDir()
	srv := newApplySafetyServer(t, root)
	first := filepath.Join(root, "a.txt")
	second := filepath.Join(root, "b.txt")
	if err := os.WriteFile(first, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write first fixture: %v", err)
	}
	if err := os.WriteFile(second, []byte("beta\n"), 0o644); err != nil {
		t.Fatalf("write second fixture: %v", err)
	}

	_, err := srv.ApplyEdits(context.Background(), &acppb.ApplyEditsRequest{
		AgentId: "zed",
		Edits: []*acppb.TextEdit{
			{
				Uri:     first,
				Range:   &acppb.Range{Start: &acppb.Position{Line: 0, Character: 0}, End: &acppb.Position{Line: 0, Character: 5}},
				NewText: "changed",
			},
			{
				Uri:     second,
				Range:   &acppb.Range{Start: &acppb.Position{Line: 9, Character: 0}, End: &acppb.Position{Line: 9, Character: 1}},
				NewText: "invalid",
			},
		},
	})
	if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "invalid text edit" {
		t.Fatalf("ApplyEdits error = %v, want sanitized invalid edit", err)
	}
	if strings.Contains(status.Convert(err).Message(), root) || strings.Contains(status.Convert(err).Message(), second) {
		t.Fatalf("ApplyEdits leaked local path in status: %q", status.Convert(err).Message())
	}
	data, readErr := os.ReadFile(first)
	if readErr != nil {
		t.Fatalf("read first after failed edit: %v", readErr)
	}
	if string(data) != "alpha\n" {
		t.Fatalf("first file = %q, want unchanged after later invalid edit", string(data))
	}
}

func newToolSafetyServer(t *testing.T) *Server {
	t.Helper()
	cfg := configForSafetyServer(t.TempDir())
	return newPartialServer(t, partialServerOptions{cfg: cfg, store: newPartialStore(t)})
}

func newApplySafetyServer(t *testing.T, root string) *Server {
	t.Helper()
	return newPartialServer(t, partialServerOptions{cfg: configForSafetyServer(root), store: nil})
}

func configForSafetyServer(root string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Worktrees.RootPath = root
	cfg.Artifacts.PlanningDir = filepath.Join(root, "plans")
	return cfg
}

func registerToolSafetyAgent(t *testing.T, srv *Server, id string, capabilities []string) {
	t.Helper()
	if _, err := srv.coordinator.RegisterAgent(context.Background(), &coordinator.AgentInfo{
		ID:           id,
		Type:         "test",
		Endpoint:     "local",
		Capabilities: capabilities,
	}); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
}

func newCaptureToolExecutionStream(ctx context.Context) *captureToolExecutionStream {
	return &captureToolExecutionStream{ctx: ctx}
}

type captureToolExecutionStream struct {
	grpc.ServerStream
	ctx        context.Context
	events     []*acppb.ToolExecutionEvent
	failOnSend int
	sendErr    error
}

func (s *captureToolExecutionStream) Context() context.Context { return s.ctx }

func (s *captureToolExecutionStream) Send(event *acppb.ToolExecutionEvent) error {
	if s.failOnSend > 0 && len(s.events)+1 == s.failOnSend {
		return s.sendErr
	}
	s.events = append(s.events, event)
	return nil
}

func newCaptureInlineCompletionStream(ctx context.Context) *captureInlineCompletionStream {
	return &captureInlineCompletionStream{ctx: ctx}
}

type captureInlineCompletionStream struct {
	grpc.ServerStream
	ctx    context.Context
	events []*acppb.InlineCompletionEvent
}

func (s *captureInlineCompletionStream) Context() context.Context { return s.ctx }

func (s *captureInlineCompletionStream) Send(event *acppb.InlineCompletionEvent) error {
	s.events = append(s.events, event)
	return nil
}

func toolEventStatuses(events []*acppb.ToolExecutionEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.GetStatus())
	}
	return out
}

func inlineSafetyRequest() *acppb.InlineCompletionRequest {
	return &acppb.InlineCompletionRequest{
		AgentId:   "zed",
		SessionId: "inline-safety",
		Prompt:    "complete",
		Context: &acppb.EditorContext{Document: &acppb.DocumentSnapshot{
			Uri:        "file:///tmp/example.go",
			LanguageId: "go",
			Content:    "package main\n",
		}},
	}
}
