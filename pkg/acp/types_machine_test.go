package acp

import (
	"encoding/json"
	"testing"
)

func TestSpawnAgentParams_JSON(t *testing.T) {
	params := SpawnAgentParams{
		SessionID: "sess-1",
		Task:      "fix auth bug",
		Modality:  "ralph",
		Model:     "gpt-4",
		Spec:      "goal: fix auth\nverify: go test ./...",
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SpawnAgentParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SessionID != "sess-1" {
		t.Errorf("SessionID = %q", got.SessionID)
	}
	if got.Task != "fix auth bug" {
		t.Errorf("Task = %q", got.Task)
	}
	if got.Modality != "ralph" {
		t.Errorf("Modality = %q", got.Modality)
	}
	if got.Spec != "goal: fix auth\nverify: go test ./..." {
		t.Errorf("Spec = %q", got.Spec)
	}
}

func TestSteerAgentParams_JSON(t *testing.T) {
	params := SteerAgentParams{
		SessionID: "sess-1",
		AgentID:   "agent-abc",
		Content:   "focus on error handling",
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SteerAgentParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.AgentID != "agent-abc" {
		t.Errorf("AgentID = %q", got.AgentID)
	}
	if got.Content != "focus on error handling" {
		t.Errorf("Content = %q", got.Content)
	}
}

func TestListAgentsResult_JSON(t *testing.T) {
	result := ListAgentsResult{
		Agents: []AgentInfo{
			{AgentID: "a1", State: "calling_model", Modality: "classic"},
			{AgentID: "a2", State: "executing_tools", Modality: "rlm", ParentID: "a1"},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ListAgentsResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(got.Agents))
	}
	if got.Agents[0].AgentID != "a1" {
		t.Errorf("Agents[0].AgentID = %q", got.Agents[0].AgentID)
	}
	if got.Agents[1].ParentID != "a1" {
		t.Errorf("Agents[1].ParentID = %q", got.Agents[1].ParentID)
	}
}

func TestEscalateModeParams_JSON(t *testing.T) {
	params := EscalateModeParams{
		SessionID: "sess-1",
		AgentID:   "agent-xyz",
		Modality:  "rlm",
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got EscalateModeParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Modality != "rlm" {
		t.Errorf("Modality = %q", got.Modality)
	}
}

func TestSpawnAgentParams_OmitsEmpty(t *testing.T) {
	params := SpawnAgentParams{
		SessionID: "s1",
		Task:      "do something",
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	json.Unmarshal(data, &raw)

	if _, ok := raw["modality"]; ok {
		t.Error("expected modality to be omitted when empty")
	}
	if _, ok := raw["model"]; ok {
		t.Error("expected model to be omitted when empty")
	}
	if _, ok := raw["spec"]; ok {
		t.Error("expected spec to be omitted when empty")
	}
}

func TestMachineSessionUpdateConstants(t *testing.T) {
	if SessionUpdateMachineState != "machine_state" {
		t.Errorf("SessionUpdateMachineState = %q", SessionUpdateMachineState)
	}
	if SessionUpdateMachineLock != "machine_lock" {
		t.Errorf("SessionUpdateMachineLock = %q", SessionUpdateMachineLock)
	}
	if SessionUpdateMachineAgent != "machine_agent" {
		t.Errorf("SessionUpdateMachineAgent = %q", SessionUpdateMachineAgent)
	}
}
