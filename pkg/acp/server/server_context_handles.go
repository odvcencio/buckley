package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	acppb "github.com/odvcencio/buckley/pkg/acp/proto"
	"github.com/oklog/ulid/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CreateContextHandle stores context data and returns a handle for later retrieval.
func (s *Server) CreateContextHandle(_ context.Context, req *acppb.ContextHandleRequest) (*acppb.ContextHandle, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "request required")
	}
	if strings.TrimSpace(req.Type) == "" {
		return nil, statusError(codes.InvalidArgument, "type required")
	}

	handleID := ulid.Make().String()
	now := time.Now()

	s.contextHandleMux.Lock()
	s.contextHandles[handleID] = &ContextHandleData{
		HandleID:  handleID,
		Type:      req.Type,
		Data:      req.Data,
		CreatedAt: now,
	}
	s.contextHandleMux.Unlock()

	return &acppb.ContextHandle{
		HandleId:  handleID,
		Type:      req.Type,
		SizeBytes: int64(len(req.Data)),
		CreatedAt: timestamppb.New(now),
	}, nil
}

// ResolveContextHandle retrieves the stored data for a context handle.
func (s *Server) ResolveContextHandle(_ context.Context, req *acppb.ContextHandle) (*acppb.ContextData, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "request required")
	}
	if strings.TrimSpace(req.HandleId) == "" {
		return nil, statusError(codes.InvalidArgument, "handle_id required")
	}

	s.contextHandleMux.RLock()
	handle, exists := s.contextHandles[req.HandleId]
	s.contextHandleMux.RUnlock()

	if !exists {
		return nil, statusError(codes.NotFound, fmt.Sprintf("context handle %s not found", req.HandleId))
	}

	return &acppb.ContextData{
		Type: handle.Type,
		Data: handle.Data,
	}, nil
}

// DeleteContextHandle removes a context handle from storage.
func (s *Server) DeleteContextHandle(handleID string) bool {
	s.contextHandleMux.Lock()
	defer s.contextHandleMux.Unlock()

	if _, exists := s.contextHandles[handleID]; !exists {
		return false
	}
	delete(s.contextHandles, handleID)
	return true
}
