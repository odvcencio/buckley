package grpcsdk

import (
	"context"
	"testing"

	"github.com/odvcencio/buckley/pkg/sdk"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestNewService(t *testing.T) {
	agent := &sdk.Agent{}
	svc := NewService(agent)

	if svc == nil {
		t.Fatal("NewService returned nil")
	}
	if svc.agent != agent {
		t.Error("agent not set correctly")
	}
}

func TestService_Plan_NilRequest(t *testing.T) {
	agent := &sdk.Agent{}
	svc := NewService(agent)

	_, err := svc.Plan(context.Background(), nil)
	if err == nil {
		t.Error("expected error for nil request")
	}
}

func TestService_Plan_EmptyFeature(t *testing.T) {
	agent := &sdk.Agent{}
	svc := NewService(agent)

	req := &PlanRequest{Feature: ""}
	_, err := svc.Plan(context.Background(), req)
	if err == nil {
		t.Error("expected error for empty feature")
	}
}

func TestService_ExecutePlan_NilRequest(t *testing.T) {
	agent := &sdk.Agent{}
	svc := NewService(agent)

	_, err := svc.ExecutePlan(context.Background(), nil)
	if err == nil {
		t.Error("expected error for nil request")
	}
}

func TestService_ExecutePlan_EmptyPlanID(t *testing.T) {
	agent := &sdk.Agent{}
	svc := NewService(agent)

	req := &ExecutePlanRequest{PlanID: ""}
	_, err := svc.ExecutePlan(context.Background(), req)
	if err == nil {
		t.Error("expected error for empty plan ID")
	}
}

func TestService_GetPlan_NilRequest(t *testing.T) {
	agent := &sdk.Agent{}
	svc := NewService(agent)

	_, err := svc.GetPlan(context.Background(), nil)
	if err == nil {
		t.Error("expected error for nil request")
	}
}

func TestService_GetPlan_EmptyPlanID(t *testing.T) {
	agent := &sdk.Agent{}
	svc := NewService(agent)

	req := &GetPlanRequest{PlanID: ""}
	_, err := svc.GetPlan(context.Background(), req)
	if err == nil {
		t.Error("expected error for empty plan ID")
	}
}

func TestService_ListPlans_NilAgent(t *testing.T) {
	// Test with nil agent orchestrator
	agent := &sdk.Agent{}
	svc := NewService(agent)

	_, err := svc.ListPlans(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Error("expected error for nil orchestrator")
	}
}

func TestNewGRPCServer(t *testing.T) {
	agent := &sdk.Agent{}
	server := NewGRPCServer(agent)

	if server == nil {
		t.Fatal("NewGRPCServer returned nil")
	}
	if server.service == nil {
		t.Error("service not set")
	}
}
