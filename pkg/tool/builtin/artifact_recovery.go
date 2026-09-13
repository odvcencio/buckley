package builtin

import (
	"fmt"
	"sort"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

// RecoveryArtifact snapshots available evidence without finalizing the sink,
// rereading files, or treating the failed run as completed.
func (s *ArtifactSubmission) RecoveryArtifact() artifactv1.Artifact {
	return s.RecoveryArtifactWithReserve(0)
}

// RecoveryArtifactWithReserve leaves bounded room for caller-owned output checks.
// The reserve is clamped to half the artifact budget; captures are never truncated.
func (s *ArtifactSubmission) RecoveryArtifactWithReserve(reserve int) artifactv1.Artifact {
	reserve = min(max(reserve, 0), artifactv1.MaxProviderBytes/2)
	limit := artifactv1.MaxProviderBytes - reserve
	const reason = "The run ended before a valid final response could be delivered."
	base := artifactv1.New(artifactv1.KindSubagentResult, artifactv1.StatusIncomplete,
		"Partial run evidence", "Available evidence from an unfinished run; not proof of task completion.")
	base.IncompleteReasons = []string{reason, "Task completion and source coverage were not verified."}
	base.ArtifactID = ""
	if s == nil {
		return base.Normalized()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.submitted {
		artifact := s.artifact.Normalized()
		if artifact.Status != artifactv1.StatusFailed && artifact.Status != artifactv1.StatusBlocked {
			artifact.Status = artifactv1.StatusIncomplete
		}
		artifact.IncompleteReasons = append(artifact.IncompleteReasons, reason)
		artifact.ArtifactID = ""
		artifact = artifact.Normalized()
		if body, err := artifactv1.RenderJSON(artifact); err == nil && len(body) < limit {
			return artifact
		}
		base.Status = artifact.Status
		base.IncompleteReasons = append(base.IncompleteReasons, "Previously submitted artifact omitted because it could not fit recovery limits.")
		return base.Normalized()
	}
	ids := make([]string, 0, len(s.sources))
	for id := range s.sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	selected := make([]string, 0, len(ids))
	artifact := base
	for _, id := range ids {
		trial := append(selected, id)
		candidate, err := s.appendCapturedSources(base, trial)
		if err != nil {
			continue
		}
		candidate = candidate.Normalized()
		body, err := artifactv1.RenderJSON(candidate)
		// Leave room for the bounded omission notice added below.
		if err != nil || len(body) > limit-256 {
			continue
		}
		selected = trial
		artifact = candidate
	}
	if omitted := len(ids) - len(selected); omitted > 0 {
		artifact.IncompleteReasons = append(artifact.IncompleteReasons,
			fmt.Sprintf("%d captured source pages omitted because they could not fit recovery limits.", omitted))
	}
	artifact.ArtifactID = ""
	return artifact.Normalized()
}
