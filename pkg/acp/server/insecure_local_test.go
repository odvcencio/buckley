package server

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	acppb "github.com/odvcencio/buckley/pkg/acp/proto"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/coordination/coordinator"
	"github.com/odvcencio/buckley/pkg/coordination/events"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestInsecureLocalAuth_AllowsStreamRPCWithoutMTLS(t *testing.T) {
	tmp := t.TempDir()

	eventStore, err := events.NewSQLiteEventStore(filepath.Join(tmp, "acp-events.db"))
	if err != nil {
		t.Fatalf("NewSQLiteEventStore() error = %v", err)
	}
	t.Cleanup(func() { _ = eventStore.Close() })

	coord, err := coordinator.NewCoordinator(coordinator.DefaultConfig(), eventStore)
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.ACP.AllowInsecureLocal = true

	srv, err := NewServer(coord, nil, cfg, nil)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(srv.UnaryAuthInterceptor),
		grpc.ChainStreamInterceptor(srv.StreamAuthInterceptor),
	)
	acppb.RegisterAgentCommunicationServer(grpcServer, srv)
	t.Cleanup(grpcServer.GracefulStop)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })

	go func() {
		_ = grpcServer.Serve(lis)
	}()

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	conn, err := grpc.DialContext(dialCtx, lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := acppb.NewAgentCommunicationClient(conn)
	callCtx, callCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callCancel()

	stream, err := client.StreamTask(callCtx, &acppb.TaskStreamRequest{
		AgentId: "zed",
		TaskId:  "task-1",
		Query:   "hello",
	})
	if err != nil {
		t.Fatalf("StreamTask() error = %v", err)
	}

	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv() error = %v", err)
	}
	if ev.GetMessage() == "" {
		t.Fatalf("expected streamed message, got %#v", ev)
	}
}
