package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"testing"

	acppb "github.com/odvcencio/buckley/pkg/acp/proto"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/coordination/coordinator"
	"github.com/odvcencio/buckley/pkg/coordination/events"
	"github.com/odvcencio/buckley/pkg/coordination/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

func TestUnaryAuthInterceptor(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ACP.AllowInsecureLocal = true
	eventStore := events.NewInMemoryStore()
	coord, err := coordinator.NewCoordinator(coordinator.DefaultConfig(), eventStore)
	require.NoError(t, err)

	srv, err := NewServer(coord, nil, cfg, nil)
	require.NoError(t, err)

	t.Run("no peer info fails", func(t *testing.T) {
		ctx := context.Background()
		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, nil
		}

		_, err := srv.UnaryAuthInterceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)
		assert.Error(t, err)
	})

	t.Run("with loopback peer succeeds", func(t *testing.T) {
		addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:5000")
		require.NoError(t, err)

		peerInfo := &peer.Peer{
			Addr: addr,
		}
		ctx := peer.NewContext(context.Background(), peerInfo)

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			// Verify claims are in context
			claims, ok := security.ClaimsFromContext(ctx)
			assert.True(t, ok)
			assert.NotNil(t, claims)
			return "success", nil
		}

		result, err := srv.UnaryAuthInterceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)
		assert.NoError(t, err)
		assert.Equal(t, "success", result)
	})

	t.Run("with agent id in metadata", func(t *testing.T) {
		addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:5000")
		require.NoError(t, err)

		peerInfo := &peer.Peer{
			Addr: addr,
		}
		ctx := peer.NewContext(context.Background(), peerInfo)
		md := metadata.New(map[string]string{
			insecureAgentIDMetadataKey: "test-agent-id",
		})
		ctx = metadata.NewIncomingContext(ctx, md)

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			claims, ok := security.ClaimsFromContext(ctx)
			assert.True(t, ok)
			assert.Equal(t, "test-agent-id", claims.AgentID)
			return "success", nil
		}

		result, err := srv.UnaryAuthInterceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)
		assert.NoError(t, err)
		assert.Equal(t, "success", result)
	})

	t.Run("agent mismatch", func(t *testing.T) {
		addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:5000")
		require.NoError(t, err)

		peerInfo := &peer.Peer{
			Addr: addr,
		}
		ctx := peer.NewContext(context.Background(), peerInfo)
		md := metadata.New(map[string]string{
			insecureAgentIDMetadataKey: "peer-agent",
		})
		ctx = metadata.NewIncomingContext(ctx, md)

		// Request has different agent ID
		req := &acppb.RegisterAgentRequest{
			AgentId: "different-agent",
		}

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "success", nil
		}

		_, err = srv.UnaryAuthInterceptor(ctx, req, &grpc.UnaryServerInfo{}, handler)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "agent mismatch")
	})
}

func TestStreamAuthInterceptor(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ACP.AllowInsecureLocal = true
	eventStore := events.NewInMemoryStore()
	coord, err := coordinator.NewCoordinator(coordinator.DefaultConfig(), eventStore)
	require.NoError(t, err)

	srv, err := NewServer(coord, nil, cfg, nil)
	require.NoError(t, err)

	t.Run("no peer info fails", func(t *testing.T) {
		mockStream := &mockServerStream{ctx: context.Background()}
		handler := func(srv interface{}, stream grpc.ServerStream) error {
			return nil
		}

		err := srv.StreamAuthInterceptor(nil, mockStream, &grpc.StreamServerInfo{}, handler)
		assert.Error(t, err)
	})

	t.Run("with loopback peer succeeds", func(t *testing.T) {
		addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:5000")
		require.NoError(t, err)

		peerInfo := &peer.Peer{
			Addr: addr,
		}
		ctx := peer.NewContext(context.Background(), peerInfo)
		mockStream := &mockServerStream{ctx: ctx}

		handlerCalled := false
		handler := func(srv interface{}, stream grpc.ServerStream) error {
			handlerCalled = true
			// Verify claims are in stream context
			claims, ok := security.ClaimsFromContext(stream.Context())
			assert.True(t, ok)
			assert.NotNil(t, claims)
			return nil
		}

		err = srv.StreamAuthInterceptor(nil, mockStream, &grpc.StreamServerInfo{}, handler)
		assert.NoError(t, err)
		assert.True(t, handlerCalled)
	})
}

func TestPeerAgentID(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ACP.AllowInsecureLocal = true
	eventStore := events.NewInMemoryStore()
	coord, err := coordinator.NewCoordinator(coordinator.DefaultConfig(), eventStore)
	require.NoError(t, err)

	srv, err := NewServer(coord, nil, cfg, nil)
	require.NoError(t, err)

	t.Run("nil context", func(t *testing.T) {
		_, err := srv.peerAgentID(nil, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing context")
	})

	t.Run("no peer in context", func(t *testing.T) {
		_, err := srv.peerAgentID(context.Background(), nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing peer info")
	})

	t.Run("non-loopback without mTLS fails when not allowed", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.ACP.AllowInsecureLocal = false
		eventStore := events.NewInMemoryStore()
		coord, err := coordinator.NewCoordinator(coordinator.DefaultConfig(), eventStore)
		require.NoError(t, err)

		srvNoInsecure, err := NewServer(coord, nil, cfg, nil)
		require.NoError(t, err)

		addr, err := net.ResolveTCPAddr("tcp", "192.168.1.1:5000")
		require.NoError(t, err)

		peerInfo := &peer.Peer{
			Addr: addr,
		}
		ctx := peer.NewContext(context.Background(), peerInfo)

		_, err = srvNoInsecure.peerAgentID(ctx, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "client certificate required")
	})

	t.Run("non-loopback without mTLS fails even with insecure allowed", func(t *testing.T) {
		addr, err := net.ResolveTCPAddr("tcp", "192.168.1.1:5000")
		require.NoError(t, err)

		peerInfo := &peer.Peer{
			Addr: addr,
		}
		ctx := peer.NewContext(context.Background(), peerInfo)

		_, err = srv.peerAgentID(ctx, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "insecure ACP requires loopback client")
	})

	t.Run("loopback returns local by default", func(t *testing.T) {
		addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:5000")
		require.NoError(t, err)

		peerInfo := &peer.Peer{
			Addr: addr,
		}
		ctx := peer.NewContext(context.Background(), peerInfo)

		agentID, err := srv.peerAgentID(ctx, nil)
		assert.NoError(t, err)
		assert.Equal(t, "local", agentID)
	})

	t.Run("loopback with metadata agent ID", func(t *testing.T) {
		addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:5000")
		require.NoError(t, err)

		peerInfo := &peer.Peer{
			Addr: addr,
		}
		ctx := peer.NewContext(context.Background(), peerInfo)
		md := metadata.New(map[string]string{
			insecureAgentIDMetadataKey: "custom-agent",
		})
		ctx = metadata.NewIncomingContext(ctx, md)

		agentID, err := srv.peerAgentID(ctx, nil)
		assert.NoError(t, err)
		assert.Equal(t, "custom-agent", agentID)
	})

	t.Run("loopback with request agent ID fallback", func(t *testing.T) {
		addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:5000")
		require.NoError(t, err)

		peerInfo := &peer.Peer{
			Addr: addr,
		}
		ctx := peer.NewContext(context.Background(), peerInfo)

		req := &acppb.RegisterAgentRequest{
			AgentId: "request-agent",
		}

		agentID, err := srv.peerAgentID(ctx, req)
		assert.NoError(t, err)
		assert.Equal(t, "request-agent", agentID)
	})

	t.Run("with mTLS cert", func(t *testing.T) {
		// Create a mock TLS peer with certificate
		cert := &x509.Certificate{
			Subject: pkix.Name{
				CommonName: "mtls-agent",
			},
		}

		tlsInfo := credentials.TLSInfo{
			State: tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{cert},
			},
		}

		addr, err := net.ResolveTCPAddr("tcp", "192.168.1.1:5000")
		require.NoError(t, err)

		peerInfo := &peer.Peer{
			Addr:     addr,
			AuthInfo: tlsInfo,
		}
		ctx := peer.NewContext(context.Background(), peerInfo)

		agentID, err := srv.peerAgentID(ctx, nil)
		assert.NoError(t, err)
		assert.Equal(t, "mtls-agent", agentID)
	})
}

func TestIsLoopbackPeer(t *testing.T) {
	t.Run("nil addr", func(t *testing.T) {
		assert.False(t, isLoopbackPeer(nil))
	})

	t.Run("loopback IPv4", func(t *testing.T) {
		addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:5000")
		assert.True(t, isLoopbackPeer(addr))
	})

	t.Run("loopback IPv6", func(t *testing.T) {
		addr, _ := net.ResolveTCPAddr("tcp", "[::1]:5000")
		assert.True(t, isLoopbackPeer(addr))
	})

	t.Run("external IPv4", func(t *testing.T) {
		addr, _ := net.ResolveTCPAddr("tcp", "192.168.1.1:5000")
		assert.False(t, isLoopbackPeer(addr))
	})

	t.Run("localhost string", func(t *testing.T) {
		mockAddr := &mockNetAddr{addr: "localhost:5000"}
		assert.True(t, isLoopbackPeer(mockAddr))
	})

	t.Run("localhost uppercase", func(t *testing.T) {
		mockAddr := &mockNetAddr{addr: "LOCALHOST:5000"}
		assert.True(t, isLoopbackPeer(mockAddr))
	})

	t.Run("empty address", func(t *testing.T) {
		mockAddr := &mockNetAddr{addr: ""}
		assert.False(t, isLoopbackPeer(mockAddr))
	})
}

func TestAgentCapabilities(t *testing.T) {
	srv, coord := newTestServer(t)
	ctx := context.Background()

	t.Run("nil coordinator", func(t *testing.T) {
		srv2 := &Server{}
		caps := srv2.agentCapabilities(ctx, "agent-1")
		assert.Nil(t, caps)
	})

	t.Run("empty agent ID", func(t *testing.T) {
		caps := srv.agentCapabilities(ctx, "")
		assert.Nil(t, caps)
	})

	t.Run("whitespace agent ID", func(t *testing.T) {
		caps := srv.agentCapabilities(ctx, "   ")
		assert.Nil(t, caps)
	})

	t.Run("nonexistent agent", func(t *testing.T) {
		caps := srv.agentCapabilities(ctx, "nonexistent")
		assert.Nil(t, caps)
	})

	t.Run("existing agent", func(t *testing.T) {
		_, err := coord.RegisterAgent(ctx, &coordinator.AgentInfo{
			ID:           "caps-test-agent",
			Type:         "builder",
			Endpoint:     "localhost:5000",
			Capabilities: []string{"read", "write", "execute"},
		})
		require.NoError(t, err)

		caps := srv.agentCapabilities(ctx, "caps-test-agent")
		assert.Equal(t, []string{"read", "write", "execute"}, caps)
	})
}

func TestRequestAgentIDTypes(t *testing.T) {
	tests := []struct {
		name     string
		req      interface{}
		expected string
	}{
		{"RegisterAgentRequest", &acppb.RegisterAgentRequest{AgentId: "reg-agent"}, "reg-agent"},
		{"GetAgentInfoRequest", &acppb.GetAgentInfoRequest{AgentId: "info-agent"}, "info-agent"},
		{"TaskStreamRequest", &acppb.TaskStreamRequest{AgentId: "task-agent"}, "task-agent"},
		{"ToolExecutionRequest", &acppb.ToolExecutionRequest{AgentId: "tool-agent"}, "tool-agent"},
		{"CreateSessionRequest", &acppb.CreateSessionRequest{AgentId: "session-agent"}, "session-agent"},
		{"InlineCompletionRequest", &acppb.InlineCompletionRequest{AgentId: "inline-agent"}, "inline-agent"},
		{"ProposeEditsRequest", &acppb.ProposeEditsRequest{AgentId: "propose-agent"}, "propose-agent"},
		{"ApplyEditsRequest", &acppb.ApplyEditsRequest{AgentId: "apply-agent"}, "apply-agent"},
		{"UpdateEditorStateRequest", &acppb.UpdateEditorStateRequest{AgentId: "state-agent"}, "state-agent"},
		{"int", 42, ""},
		{"string", "hello", ""},
		{"slice", []string{"a"}, ""},
		{"map", map[string]string{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := requestAgentID(tt.req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAuthorizeContext(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ACP.AllowInsecureLocal = true
	eventStore := events.NewInMemoryStore()
	coord, err := coordinator.NewCoordinator(coordinator.DefaultConfig(), eventStore)
	require.NoError(t, err)

	srv, err := NewServer(coord, nil, cfg, nil)
	require.NoError(t, err)

	t.Run("adds claims to context", func(t *testing.T) {
		addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:5000")
		require.NoError(t, err)

		peerInfo := &peer.Peer{
			Addr: addr,
		}
		ctx := peer.NewContext(context.Background(), peerInfo)

		authCtx, err := srv.authorizeContext(ctx, nil)
		require.NoError(t, err)

		claims, ok := security.ClaimsFromContext(authCtx)
		assert.True(t, ok)
		assert.NotNil(t, claims)
		assert.Equal(t, "local", claims.AgentID)
	})
}

// Mock types for testing

type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	return m.ctx
}

type mockNetAddr struct {
	addr string
}

func (m *mockNetAddr) Network() string {
	return "tcp"
}

func (m *mockNetAddr) String() string {
	return m.addr
}
