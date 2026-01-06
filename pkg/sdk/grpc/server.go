package grpcsdk

import (
	"context"
	"fmt"
	"net"

	"github.com/odvcencio/buckley/pkg/sdk"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// BuckleyServiceServer describes the RPC surface for automation clients.
type BuckleyServiceServer interface {
	Plan(context.Context, *PlanRequest) (*PlanResponse, error)
	ExecutePlan(context.Context, *ExecutePlanRequest) (*ExecutePlanResponse, error)
	GetPlan(context.Context, *GetPlanRequest) (*GetPlanResponse, error)
	ListPlans(context.Context, *emptypb.Empty) (*ListPlansResponse, error)
}

type service struct {
	agent *sdk.Agent
}

// NewService wires the SDK agent to the gRPC surface.
func NewService(agent *sdk.Agent) *service {
	return &service{agent: agent}
}

// Serve registers the service on the provided gRPC server.
func (s *service) Register(server *grpc.Server) {
	RegisterBuckleyServiceServer(server, s)
}

func (s *service) Plan(ctx context.Context, req *PlanRequest) (*PlanResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("plan request required")
	}
	if req.Feature == "" {
		return nil, fmt.Errorf("feature is required")
	}
	plan, err := s.agent.Plan(ctx, req.Feature, req.Description)
	if err != nil {
		return nil, err
	}
	return &PlanResponse{Plan: plan}, nil
}

func (s *service) ExecutePlan(ctx context.Context, req *ExecutePlanRequest) (*ExecutePlanResponse, error) {
	if req == nil || req.PlanID == "" {
		return nil, fmt.Errorf("plan_id is required")
	}
	if err := s.agent.ExecutePlan(ctx, req.PlanID); err != nil {
		return nil, err
	}
	return &ExecutePlanResponse{PlanID: req.PlanID, Status: "completed"}, nil
}

func (s *service) GetPlan(ctx context.Context, req *GetPlanRequest) (*GetPlanResponse, error) {
	if req == nil || req.PlanID == "" {
		return nil, fmt.Errorf("plan_id is required")
	}
	plan, err := s.agent.GetPlan(ctx, req.PlanID)
	if err != nil {
		return nil, err
	}
	return &GetPlanResponse{Plan: plan}, nil
}

func (s *service) ListPlans(ctx context.Context, _ *emptypb.Empty) (*ListPlansResponse, error) {
	plans, err := s.agent.ListPlans(ctx)
	if err != nil {
		return nil, err
	}
	return &ListPlansResponse{Plans: plans}, nil
}

// BuckleyGRPCServer provides a ready-to-serve gRPC listener.
type BuckleyGRPCServer struct {
	service *service
}

func NewGRPCServer(agent *sdk.Agent) *BuckleyGRPCServer {
	return &BuckleyGRPCServer{
		service: NewService(agent),
	}
}

func (s *BuckleyGRPCServer) Serve(lis net.Listener) error {
	server := grpc.NewServer(grpc.ForceServerCodec(jsonCodec{}))
	s.service.Register(server)
	return server.Serve(lis)
}

// --- Manual service descriptor plumbing (no proto build yet) ---

// RegisterBuckleyServiceServer registers service handlers.
func RegisterBuckleyServiceServer(s *grpc.Server, srv BuckleyServiceServer) {
	s.RegisterService(&_BuckleyService_serviceDesc, srv)
}

func _BuckleyService_Plan_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(PlanRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BuckleyServiceServer).Plan(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/buckley.v1.BuckleyService/Plan",
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(BuckleyServiceServer).Plan(ctx, req.(*PlanRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BuckleyService_ExecutePlan_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(ExecutePlanRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BuckleyServiceServer).ExecutePlan(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/buckley.v1.BuckleyService/ExecutePlan",
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(BuckleyServiceServer).ExecutePlan(ctx, req.(*ExecutePlanRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BuckleyService_GetPlan_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(GetPlanRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BuckleyServiceServer).GetPlan(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/buckley.v1.BuckleyService/GetPlan",
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(BuckleyServiceServer).GetPlan(ctx, req.(*GetPlanRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _BuckleyService_ListPlans_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BuckleyServiceServer).ListPlans(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/buckley.v1.BuckleyService/ListPlans",
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(BuckleyServiceServer).ListPlans(ctx, req.(*emptypb.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

var _BuckleyService_serviceDesc = grpc.ServiceDesc{
	ServiceName: "buckley.v1.BuckleyService",
	HandlerType: (*BuckleyServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Plan",
			Handler:    _BuckleyService_Plan_Handler,
		},
		{
			MethodName: "ExecutePlan",
			Handler:    _BuckleyService_ExecutePlan_Handler,
		},
		{
			MethodName: "GetPlan",
			Handler:    _BuckleyService_GetPlan_Handler,
		},
		{
			MethodName: "ListPlans",
			Handler:    _BuckleyService_ListPlans_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "buckley_sdk",
}
