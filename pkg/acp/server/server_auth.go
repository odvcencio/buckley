package server

import (
	"context"
	"fmt"
	"net"
	"strings"

	acppb "github.com/odvcencio/buckley/pkg/acp/proto"
	"github.com/odvcencio/buckley/pkg/coordination/security"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// UnaryAuthInterceptor enforces mTLS identity and injects claims.
func (s *Server) UnaryAuthInterceptor(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	authCtx, err := s.authorizeContext(ctx, req)
	if err != nil {
		return nil, err
	}
	return handler(authCtx, req)
}

// StreamAuthInterceptor enforces mTLS identity for streaming RPCs.
func (s *Server) StreamAuthInterceptor(srv interface{}, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	authCtx, err := s.authorizeContext(stream.Context(), nil)
	if err != nil {
		return err
	}
	wrapped := &authStream{ServerStream: stream, ctx: authCtx}
	return handler(srv, wrapped)
}

type authStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authStream) Context() context.Context {
	return s.ctx
}

func (s *Server) authorizeContext(ctx context.Context, req interface{}) (context.Context, error) {
	peerID, err := s.peerAgentID(ctx, req)
	if err != nil {
		return nil, statusError(codes.Unauthenticated, err.Error())
	}

	if reqID := requestAgentID(req); reqID != "" && peerID != reqID {
		return nil, statusError(codes.PermissionDenied, fmt.Sprintf("agent mismatch: peer %s cannot act as %s", peerID, reqID))
	}

	caps := s.agentCapabilities(ctx, peerID)
	claims := &security.Claims{
		AgentID:      peerID,
		Capabilities: caps,
	}
	return security.ContextWithClaims(ctx, claims), nil
}

func (s *Server) agentCapabilities(ctx context.Context, agentID string) []string {
	if s.coordinator == nil || strings.TrimSpace(agentID) == "" {
		return nil
	}
	agent, err := s.coordinator.GetAgent(ctx, agentID)
	if err != nil || agent == nil {
		return nil
	}
	return agent.Capabilities
}

func requestAgentID(req interface{}) string {
	switch v := req.(type) {
	case *acppb.RegisterAgentRequest:
		return v.GetAgentId()
	case *acppb.GetAgentInfoRequest:
		return v.GetAgentId()
	case *acppb.TaskStreamRequest:
		return v.GetAgentId()
	case *acppb.ToolExecutionRequest:
		return v.GetAgentId()
	case *acppb.CreateSessionRequest:
		return v.GetAgentId()
	case *acppb.InlineCompletionRequest:
		return v.GetAgentId()
	case *acppb.ProposeEditsRequest:
		return v.GetAgentId()
	case *acppb.ApplyEditsRequest:
		return v.GetAgentId()
	case *acppb.UpdateEditorStateRequest:
		return v.GetAgentId()
	default:
		return ""
	}
}

const insecureAgentIDMetadataKey = "x-buckley-agent-id"

func (s *Server) peerAgentID(ctx context.Context, req interface{}) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("missing context")
	}
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("missing peer info")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if ok && len(tlsInfo.State.PeerCertificates) > 0 {
		return tlsInfo.State.PeerCertificates[0].Subject.CommonName, nil
	}

	if s == nil || s.cfg == nil || !s.cfg.ACP.AllowInsecureLocal {
		return "", fmt.Errorf("client certificate required")
	}
	if !isLoopbackPeer(p.Addr) {
		return "", fmt.Errorf("insecure ACP requires loopback client")
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get(insecureAgentIDMetadataKey); len(vals) > 0 {
			if agentID := strings.TrimSpace(vals[0]); agentID != "" {
				return agentID, nil
			}
		}
	}
	if agentID := strings.TrimSpace(requestAgentID(req)); agentID != "" {
		return agentID, nil
	}
	return "local", nil
}

func isLoopbackPeer(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	if tcp, ok := addr.(*net.TCPAddr); ok && tcp.IP != nil {
		return tcp.IP.IsLoopback()
	}
	host := strings.TrimSpace(addr.String())
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = strings.TrimSpace(h)
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return false
}
