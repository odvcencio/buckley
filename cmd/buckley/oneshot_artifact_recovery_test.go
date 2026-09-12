package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestPrintOneShotArtifactFailure(t *testing.T) {
	for _, withCapture := range []bool{false, true} {
		for _, cause := range []error{fmt.Errorf("invalid final artifact"), &agentloop.IncompleteTurnError{FinishReason: agentloop.FinishReasonStepCap, Reason: "PRIVATE_REASONING", FinalizationError: "PRIVATE_REASONING"}} {
			t.Run(fmt.Sprintf("capture=%t/%T", withCapture, cause), func(t *testing.T) {
				var sink *builtin.ArtifactSubmission
				if withCapture {
					sink = &builtin.ArtifactSubmission{}
					_, err := sink.CaptureReadSource(&builtin.Result{Success: true, Data: map[string]any{"path": "/never-reread", "content": "\texact\r\n", "page": map[string]any{"start_line": 1, "end_line": 1}}})
					if err != nil {
						t.Fatal(err)
					}
				}
				code := 0
				var stdout string
				stderr := captureStderr(t, func() { stdout = captureStdout(t, func() { code = printOneShotArtifactFailure(sink, cause) }) })
				if code != 1 || !strings.Contains(stderr, "One-shot status: incomplete") || strings.Contains(stdout+stderr, "PRIVATE_REASONING") {
					t.Fatalf("bad failure boundary: code=%d stderr=%q", code, stderr)
				}
				got, _, err := artifactv1.DecodeProviderOutput(context.Background(), []byte(stdout), artifactv1.OutputPromptJSON, artifactv1.DecodeOptions{})
				if err != nil || got.Status != artifactv1.StatusIncomplete {
					t.Fatalf("not strict incomplete JSON: %v %q", err, stdout)
				}
				if withCapture {
					if len(got.Blocks) != 1 || got.Blocks[0].Table.Rows[0][4] != "\texact\r\n" {
						t.Fatal("captured bytes lost")
					}
				} else if len(got.Blocks) != 0 {
					t.Fatal("invented source evidence")
				}
			})
		}
	}
}
