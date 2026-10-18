package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"m31labs.dev/buckley/pkg/agentloop"
	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func printOneShotFailure(responseText string, err error) int {
	var partial *partialStreamTurnError
	var incomplete *agentloop.IncompleteTurnError
	switch {
	case errors.As(err, &partial) && responseText != "":
		fmt.Print(responseText)
		if !strings.HasSuffix(responseText, "\n") {
			fmt.Println()
		}
		fmt.Println("\n[Stream interrupted — the response above is incomplete.]")
		fmt.Fprintf(os.Stderr, "One-shot status: incomplete (exit=1; stop_reason=%q; partial_output_bytes=%d)\n", oneShotFailureStopReason(err), len(responseText))
	case errors.As(err, &incomplete):
		notice := agentloop.PresentIncompleteResult(err)
		if responseText != "" {
			fmt.Print(responseText)
			if !strings.HasSuffix(responseText, "\n") {
				fmt.Println()
			}
		}
		fmt.Printf("\n[%s]\n", notice.Message)
		fmt.Fprintf(os.Stderr, "One-shot status: incomplete (exit=1; code=%s; stop_reason=%q; preserved_output_bytes=%d)\n", notice.Code, oneShotFailureStopReason(err), len(responseText))
		return 1
	default:
		fmt.Fprintf(os.Stderr, "One-shot status: incomplete (exit=1; stop_reason=%q)\n", oneShotFailureStopReason(err))
	}
	fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
	return 1
}

func printOneShotArtifactFailure(submission *builtin.ArtifactSubmission, err error, requiredSourceText ...string) int {
	var incomplete *agentloop.IncompleteTurnError
	isIncomplete := errors.As(err, &incomplete)

	reserve := 0
	if len(requiredSourceText) > 0 {
		reserve = sourceTextCoverageReserve
	}
	artifact := submission.RecoveryArtifactWithReserve(reserve)
	artifact, coverageErr := applySourceTextRequirements(artifact, requiredSourceText)
	if coverageErr != nil {
		fmt.Fprintf(os.Stderr, "Error: checking recovery source requirements: %v\n", coverageErr)
		return 1
	}
	status := artifact.Status
	if artifactJSON, renderErr := artifactv1.RenderJSON(artifact); renderErr != nil {
		fmt.Fprintf(os.Stderr, "Error: rendering recovery artifact: %v\n", renderErr)
	} else {
		fmt.Println(string(artifactJSON))
	}

	if isIncomplete {
		notice := agentloop.PresentIncompleteResult(err)
		fmt.Fprintf(os.Stderr, "One-shot status: %s (exit=1; code=%s; stop_reason=%q)\n", status, notice.Code, oneShotFailureStopReason(err))
		fmt.Fprintln(os.Stderr, "Incomplete turn; captured evidence preserved in recovery artifact.")
		return 1
	}
	fmt.Fprintf(os.Stderr, "One-shot status: %s (exit=1; stop_reason=%q)\nError: %v\n", status, oneShotFailureStopReason(err), err)
	return 1
}

func oneShotFailureStopReason(err error) string {
	var incomplete *agentloop.IncompleteTurnError
	if errors.As(err, &incomplete) && incomplete.StopReason != "" {
		return incomplete.StopReason
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "error"
}

func resolveOneShotArtifact(response string, contract artifactv1.OutputContract, submission *builtin.ArtifactSubmission) (artifactv1.Artifact, error) {
	if submission != nil {
		if artifact, ok := submission.Artifact(); ok {
			return artifact, nil
		}
	}
	raw := []byte(response)
	if len(raw) <= artifactv1.MaxProviderBytes && json.Valid(raw) {
		decoder := json.NewDecoder(strings.NewReader(response))
		decoder.UseNumber()
		var params map[string]any
		if err := decoder.Decode(&params); err == nil {
			if _, hasRefs := params["source_refs"]; hasRefs {
				_, hasArtifact := params["artifact"]
				if len(params) != 2 || !hasArtifact {
					return artifactv1.Artifact{}, fmt.Errorf("final JSON envelope must contain exactly the root keys artifact and source_refs")
				}
				if submission == nil {
					submission = &builtin.ArtifactSubmission{}
				}
				result, err := (&builtin.SubmitArtifactTool{Submission: submission}).Execute(params)
				if err != nil {
					return artifactv1.Artifact{}, err
				}
				if !result.Success {
					return artifactv1.Artifact{}, fmt.Errorf("%s", result.Error)
				}
				detached, _ := submission.Artifact()
				return detached, nil
			}
		}
	}
	artifact, _, err := artifactv1.DecodeProviderOutput(context.Background(), raw, contract.Mode, artifactv1.DecodeOptions{})
	if err != nil {
		if contract.Mode == artifactv1.OutputSubmitArtifact {
			return artifactv1.Artifact{}, fmt.Errorf("required artifact output was not submitted through submit_artifact or returned as valid JSON: %w", err)
		}
		return artifactv1.Artifact{}, fmt.Errorf("required artifact output is invalid: %w", err)
	}
	if submission == nil {
		submission = &builtin.ArtifactSubmission{}
	}
	if err := submission.Submit(artifact); err != nil {
		return artifactv1.Artifact{}, fmt.Errorf("finalize artifact output: %w", err)
	}
	detached, _ := submission.Artifact()
	return detached, nil
}
