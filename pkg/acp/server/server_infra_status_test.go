package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	acppb "m31labs.dev/buckley/pkg/acp/proto"
	"m31labs.dev/buckley/pkg/bus"
	"m31labs.dev/buckley/pkg/storage"
)

func TestACPServerInfrastructureErrorsUseGenericStatus(t *testing.T) {
	const secret = "infra-secret-sentinel"

	t.Run("get agent info not found hides requested id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		requestedID := "missing-agent-" + secret

		resp, err := srv.GetAgentInfo(context.Background(), &acppb.GetAgentInfoRequest{AgentId: requestedID})
		if resp != nil {
			t.Fatalf("response = %+v, want nil", resp)
		}
		requireStatusMessageWithoutSentinel(t, err, codes.NotFound, "agent not found", secret, requestedID)
	})

	t.Run("subscribe failure hides raw subject and transport detail", func(t *testing.T) {
		srv, _ := newTestServer(t)
		srv.SetMessageBus(failingSubscribeBus{err: fmt.Errorf("raw subscribe failed for %s subject", secret)})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err := srv.SubscribeTaskEvents(&acppb.TaskSubscription{TaskIds: []string{"task-" + secret}}, &captureTaskEventsStream{ctx: ctx})
		requireStatusMessageWithoutSentinel(t, err, codes.Internal, "subscribe failed", secret, "task-"+secret)
	})

	t.Run("editor state backend failure hides storage detail", func(t *testing.T) {
		store, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatalf("storage.New: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		srv := newPartialServer(t, partialServerOptions{store: store})

		resp, err := srv.UpdateEditorState(context.Background(), &acppb.UpdateEditorStateRequest{SessionId: "session-" + secret})
		if resp != nil {
			t.Fatalf("response = %+v, want nil on backend failure", resp)
		}
		requireStatusMessageWithoutSentinel(t, err, codes.Internal, "editor state unavailable", secret, "database", "session-"+secret)
	})
}

func requireStatusMessageWithoutSentinel(t *testing.T, err error, code codes.Code, want string, forbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", code)
	}
	st := status.Convert(err)
	if st.Code() != code || st.Message() != want {
		t.Fatalf("status = %s %q, want %s %q", st.Code(), st.Message(), code, want)
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(st.Message(), value) {
			t.Fatalf("status message leaked %q: %q", value, st.Message())
		}
	}
}

type failingSubscribeBus struct {
	err error
}

func (b failingSubscribeBus) Publish(context.Context, string, []byte) error { return nil }

func (b failingSubscribeBus) Subscribe(context.Context, string, bus.MessageHandler) (bus.Subscription, error) {
	return nil, b.err
}

func (b failingSubscribeBus) Request(context.Context, string, []byte, time.Duration) ([]byte, error) {
	return nil, b.err
}

func (b failingSubscribeBus) QueueSubscribe(context.Context, string, string, bus.MessageHandler) (bus.Subscription, error) {
	return nil, b.err
}

func (b failingSubscribeBus) Queue(string) bus.TaskQueue { return nil }

func (b failingSubscribeBus) Close() error { return nil }

type captureTaskEventsStream struct {
	grpc.ServerStream
	ctx    context.Context
	events []*acppb.TaskEvent
}

func (s *captureTaskEventsStream) Context() context.Context { return s.ctx }

func (s *captureTaskEventsStream) Send(event *acppb.TaskEvent) error {
	s.events = append(s.events, event)
	return nil
}
