package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

// ArtifactSubmission is a bounded, in-memory handoff for the final artifact
// submitted through SubmitArtifactTool. It is deliberately a control-plane
// object: evidence persistence remains the responsibility of the caller that
// owns the run lifecycle.
type ArtifactSubmission struct {
	mu                  sync.RWMutex
	artifact            artifactv1.Artifact
	submitted           bool
	toolFailures        []artifactv1.Diagnostic
	omittedToolFailures int
	sources             map[string]capturedSource
	sourceBytes         int
}

// Submit records a validated artifact. Repeated submissions fail closed so a
// provider cannot silently replace its final result after the fact.
func (s *ArtifactSubmission) Submit(artifact artifactv1.Artifact) error {
	return s.SubmitWithSources(artifact, nil)
}

// SubmitWithSources materializes selected read snapshots before validation.
// It neither upgrades the model's completion status nor verifies its summary.
func (s *ArtifactSubmission) SubmitWithSources(artifact artifactv1.Artifact, refs []string) error {
	if s == nil {
		return fmt.Errorf("artifact submission sink is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.submitted {
		return fmt.Errorf("artifact was already submitted")
	}
	artifact, err := s.appendCapturedSources(artifact, refs)
	if err != nil {
		return err
	}
	artifact, err = artifactv1.NormalizeAndValidate(artifact)
	if err != nil {
		return fmt.Errorf("validate submitted artifact: %w", err)
	}
	if len(refs) > 0 {
		encoded, err := json.Marshal(artifact)
		if err != nil {
			return err
		}
		if len(encoded) > artifactv1.MaxProviderBytes {
			return fmt.Errorf("captured artifact exceeds %d bytes; select fewer source_refs", artifactv1.MaxProviderBytes)
		}
	}
	s.artifact = artifact
	s.submitted = true
	s.sources = nil
	s.sourceBytes = 0
	s.toolFailures = nil
	s.omittedToolFailures = 0
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
			"source_refs": {
				Type: "array", Description: "Use [\"all\"] to include every page already captured in this run without copying IDs, or list source_ref IDs from read_file for a subset. No files are read. Leave artifact blocks and evidence_refs empty. Output limits still apply; summary accuracy and coverage are not verified.",
				Items: &PropertySchema{Type: "string", Description: "all (alone) or a source_ref from a successful read"},
			},
			"artifact": {
				RawSchema:   artifactv1.SubmissionJSONSchema(),
				Type:        "object",
				Description: "A complete buckley.artifact/v1 object; schema_version and artifact_id may be omitted because Buckley supplies them, kind, status, title, and summary are required",
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
	artifactParam := params["artifact"]
	rawRefs, refsPresent := params["source_refs"]
	if artifactMap, ok := artifactParam.(map[string]any); ok {
		if nested, nestedPresent := artifactMap["source_refs"]; nestedPresent {
			if refsPresent {
				return &Result{Success: false, Error: "source_refs supplied in two locations: provide it either beside artifact or inside artifact, not both"}, nil
			}
			clone := maps.Clone(artifactMap)
			delete(clone, "source_refs")
			artifactParam = clone
			rawRefs = nested
			refsPresent = true
		}
	}
	raw, err := json.Marshal(map[string]any{"artifact": artifactParam})
	if err != nil {
		return &Result{Success: false, Error: "artifact parameter is not JSON-serializable"}, nil
	}
	artifact, err := artifactv1.DecodeSubmitArtifact(raw)
	if err != nil {
		var validation *artifactv1.ValidationError
		if errors.As(err, &validation) && len(validation.Diagnostics) > 0 {
			diagnostics := validation.Diagnostics
			limit := min(len(diagnostics), 8)
			parts := make([]string, 0, limit)
			for _, d := range diagnostics[:limit] {
				parts = append(parts, d.Code+": "+d.Message)
			}
			message := "invalid buckley.artifact/v1 submission; correct these fields: " + strings.Join(parts, "; ")
			if omitted := len(diagnostics) - limit; omitted > 0 {
				message += fmt.Sprintf(" (+%d more omitted)", omitted)
			}
			return &Result{Success: false, Error: message}, nil
		}
		return &Result{Success: false, Error: fmt.Sprintf("invalid buckley.artifact/v1 submission: %v", err)}, nil
	}
	var refs []string
	if refsPresent {
		refs, err = parseSourceRefs(rawRefs)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
	}
	if err := t.Submission.SubmitWithSources(artifact, refs); err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	artifact, _ = t.Submission.Artifact()
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
