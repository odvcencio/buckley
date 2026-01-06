package server

import (
	"context"

	acppb "github.com/odvcencio/buckley/pkg/acp/proto"
	"github.com/odvcencio/buckley/pkg/coordination/coordinator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// RegisterAgent handles agent registration requests
func (s *Server) RegisterAgent(ctx context.Context, req *acppb.RegisterAgentRequest) (*acppb.RegisterAgentResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.Endpoint == "" {
		return nil, status.Error(codes.InvalidArgument, "endpoint is required")
	}

	agent := &coordinator.AgentInfo{
		ID:           req.AgentId,
		Type:         req.Type,
		Endpoint:     req.Endpoint,
		Capabilities: req.Capabilities,
		Metadata:     req.Metadata,
	}

	token, err := s.coordinator.RegisterAgent(ctx, agent)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "registration failed: %v", err)
	}

	return &acppb.RegisterAgentResponse{
		Agent: &acppb.AgentInfo{
			Id:           agent.ID,
			Type:         agent.Type,
			Endpoint:     agent.Endpoint,
			Capabilities: agent.Capabilities,
			Metadata:     agent.Metadata,
		},
		SessionToken: token,
	}, nil
}

// UnregisterAgent handles agent unregistration
func (s *Server) UnregisterAgent(ctx context.Context, req *acppb.UnregisterAgentRequest) (*emptypb.Empty, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	if err := s.coordinator.UnregisterAgent(ctx, req.AgentId, req.Reason); err != nil {
		return nil, status.Errorf(codes.Internal, "unregistration failed: %v", err)
	}

	return &emptypb.Empty{}, nil
}
