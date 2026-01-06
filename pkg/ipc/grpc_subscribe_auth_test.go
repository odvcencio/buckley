package ipc

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	ipcpb "github.com/odvcencio/buckley/pkg/ipc/proto"
	"github.com/odvcencio/buckley/pkg/ipc/proto/ipcpbconnect"
)

func TestGRPCSubscribeUnauthenticatedUsesConnectError(t *testing.T) {
	svc := NewGRPCService(&Server{})
	svc.subscribeLimiter = nil

	grpcPath, grpcHandler := ipcpbconnect.NewBuckleyIPCHandler(
		svc,
		connect.WithReadMaxBytes(maxConnectReadBytes),
	)

	router := chi.NewRouter()
	router.Mount(grpcPath, grpcHandler)

	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)

	client := ipcpbconnect.NewBuckleyIPCClient(ts.Client(), ts.URL)
	stream, err := client.Subscribe(context.Background(), connect.NewRequest(&ipcpb.SubscribeRequest{}))
	if err != nil {
		assertConnectCode(t, err, connect.CodeUnauthenticated)
		return
	}
	t.Cleanup(func() { _ = stream.Close() })
	if stream.Receive() {
		t.Fatalf("expected auth error, got event=%+v", stream.Msg())
	}
	assertConnectCode(t, stream.Err(), connect.CodeUnauthenticated)
}
