package tool

import (
	"m31labs.dev/buckley/pkg/tool/builtin"
	"maps"
	"strings"
)

// SetArtifactSourceCapture enables run-local source references on final read
// results, after all approval, middleware and post-hook processing.
func (r *Registry) SetArtifactSourceCapture(submission *builtin.ArtifactSubmission) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.artifactSources = submission
}

func captureArtifactSource(ctx *ExecutionContext, result *builtin.Result, submission *builtin.ArtifactSubmission) *builtin.Result {
	if result == nil || !result.Success || submission == nil || ctx == nil {
		return result
	}
	if _, ok := ctx.Tool.(*builtin.ReadFileTool); !ok {
		return result
	}
	copy := *result
	copy.Data = maps.Clone(result.Data)
	copy.DisplayData = maps.Clone(result.DisplayData)
	if copy.Data == nil {
		return result
	}
	placeholder := "src_" + strings.Repeat("0", 64)
	copy.Data["source_ref"] = placeholder
	if len(copy.DisplayData) > 0 {
		copy.DisplayData["source_ref"] = placeholder
	}
	// Do not issue handles for pages clipped by the model-output byte limit.
	encoded, err := ToModelOutputWithLimit(&copy, 0)
	if err != nil || len(encoded) > DefaultModelOutputBytes {
		return result
	}
	id, err := submission.CaptureReadSource(result)
	delete(copy.Data, "source_ref")
	delete(copy.DisplayData, "source_ref")
	if err != nil {
		copy.Data["source_capture_error"] = err.Error()
		if len(copy.DisplayData) > 0 {
			copy.DisplayData["source_capture_error"] = err.Error()
		}
	} else {
		copy.Data["source_ref"] = id
		if len(copy.DisplayData) > 0 {
			copy.DisplayData["source_ref"] = id
		}
	}
	return &copy
}
