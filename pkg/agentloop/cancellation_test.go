package agentloop

import (
	"context"
	"errors"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
)

func TestController_PartialCancellationRetainsEvidenceAndStopReason(t *testing.T) {
	for _, kind := range []string{"cancelled", "deadline_exceeded", "provider_timeout"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			requests := 0
			var stopped Termination
			ctrl, err := NewController(ControllerConfig{
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return model.ChatRequest{Model: "test"}, nil
				},
				CallModel: ModelCallerFunc(func(ctx context.Context, _ model.ChatRequest, _ bool) (*model.ChatResponse, error) {
					requests++
					cause := context.DeadlineExceeded
					if kind == "cancelled" {
						cancel()
					}
					if kind != "provider_timeout" {
						<-ctx.Done()
						cause = ctx.Err()
					}
					return textResponse("partial evidence", model.Usage{TotalTokens: 123}), cause
				}),
				DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
					t.Fatal("interrupted model response dispatched tools")
					return nil, nil
				}),
				OnStop: func(stop Termination) { stopped = stop },
			})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "deadline_exceeded" {
				var cancelDeadline context.CancelFunc
				ctx, cancelDeadline = context.WithTimeout(ctx, time.Second)
				defer cancelDeadline()
			}
			result, err := ctrl.Run(ctx)
			var incomplete *IncompleteTurnError
			wantCause, wantKind := error(context.DeadlineExceeded), kind
			if kind == "cancelled" {
				wantCause = context.Canceled
			} else if kind == "provider_timeout" {
				wantKind = FinishReasonModelError
			}
			if !errors.Is(err, wantCause) || !errors.As(err, &incomplete) {
				t.Fatalf("lost cancellation or incomplete error: %v", err)
			}
			if requests != 1 || result.Content != "partial evidence" || result.Usage.TotalTokens != 123 || result.CompletionStatus != CompletionIncomplete {
				t.Fatalf("partial response was lost or retried: requests=%d result=%+v", requests, result)
			}
			if result.Termination.Kind != wantKind || stopped.Kind != wantKind || incomplete.StopReason != wantKind {
				t.Fatalf("inconsistent stop classification: result=%+v stop=%+v error=%+v", result.Termination, stopped, incomplete)
			}
		})
	}
}
