package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

// ArtifactSubmission is a bounded, in-memory handoff for the final artifact
// submitted through SubmitArtifactTool. It is deliberately a control-plane
// object: evidence persistence remains the responsibility of the caller that
// owns the run lifecycle.
type ArtifactSubmission struct {
	mu        sync.RWMutex
	artifact  artifactv1.Artifact
	submitted bool
}

// Submit records a validated artifact. Repeated submissions fail closed so a
// provider cannot silently replace its final result after the fact.
func (s *ArtifactSubmission) Submit(artifact artifactv1.Artifact) error {
	if s == nil {
		return fmt.Errorf("artifact submission sink is required")
	}
	artifact, err := artifactv1.NormalizeAndValidate(artifact)
	if err != nil {
		return fmt.Errorf("validate submitted artifact: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.submitted {
		return fmt.Errorf("artifact was already submitted")
	}
	s.artifact = artifact
	s.submitted = true
	return nil
}

// Artifact returns a detached submitted artifact, if one exists.
func (s *ArtifactSubmission) Artifact() (artifactv1.Artifact, bool) {
	if s == nil {
		return artifactv1.Artifact{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.submitted {
		return artifactv1.Artifact{}, false
	}
	return s.artifact.Normalized(), true
}

// SubmitArtifactTool is the provider-neutral forced-tool fallback for
// buckley.artifact/v1. It has no workspace effect and is only registered for
// an execution that explicitly requires the Artifact v1 output contract.
type SubmitArtifactTool struct {
	Submission *ArtifactSubmission
}

func (t *SubmitArtifactTool) Name() string {
	return "submit_artifact"
}

func (t *SubmitArtifactTool) Description() string {
	return "Submit the final buckley.artifact/v1 result exactly once after completing the assigned task."
}

func (t *SubmitArtifactTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"artifact": {
				Type:        "object",
				Description: "A complete buckley.artifact/v1 object",
				Properties: map[string]PropertySchema{
					"schema_version": {Type: "string", Description: "Must be buckley.artifact/v1"},
					"artifact_id":    {Type: "string", Description: "Stable artifact identifier"},
					"kind":           {Type: "string", Description: "Artifact kind"},
					"status":         {Type: "string", Description: "Artifact lifecycle status"},
					"title":          {Type: "string", Description: "Short result title"},
					"summary":        {Type: "string", Description: "Bounded result summary"},
				},
				Required: []string{"schema_version", "artifact_id", "kind", "status", "title", "summary"},
			},
		},
		Required:             []string{"artifact"},
		AdditionalProperties: false,
	}
}

func (t *SubmitArtifactTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

// ExecuteWithContext validates provider-decoded arguments through the same
// Artifact v1 decoder used by adapters. A malformed submission is returned as
// a normal tool failure so the model can correct it within its bounded turn.
func (t *SubmitArtifactTool) ExecuteWithContext(_ context.Context, params map[string]any) (*Result, error) {
	if t == nil || t.Submission == nil {
		return nil, fmt.Errorf("submit_artifact requires an artifact submission sink")
	}
	raw, err := json.Marshal(map[string]any{"artifact": params["artifact"]})
	if err != nil {
		return &Result{Success: false, Error: "artifact parameter is not JSON-serializable"}, nil
	}
	artifact, err := artifactv1.DecodeSubmitArtifact(raw)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("invalid buckley.artifact/v1 submission: %v", err)}, nil
	}
	if err := t.Submission.Submit(artifact); err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	return &Result{
		Success: true,
		Data: map[string]any{
			"artifact_id":    artifact.ArtifactID,
			"schema_version": artifact.SchemaVersion,
			"status":         artifact.Status,
		},
		DisplayData: map[string]any{
			"summary": fmt.Sprintf("Artifact %s accepted", artifact.ArtifactID),
		},
		ShouldAbridge: true,
	}, nil
}
