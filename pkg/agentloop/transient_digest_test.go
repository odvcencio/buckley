package agentloop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

type digestCountingSchema struct{ calls *int }

func (s digestCountingSchema) MarshalJSON() ([]byte, error) {
	*s.calls += 1
	return []byte(`{"type":"object"}`), nil
}

type digestCapturingJournal struct {
	DurableStepJournal
	digest  string
	failure error
}

func (j *digestCapturingJournal) GetStep(context.Context, string, string) (runledger.ExecutionStep, error) {
	return runledger.ExecutionStep{}, runledger.ErrStepNotFound
}

func (j *digestCapturingJournal) BeginStep(_ context.Context, step runledger.ExecutionStep) (runledger.ExecutionStep, bool, error) {
	j.digest = step.InputDigest
	return step, false, j.failure
}

func TestController_ModelDigestRequiresConsumer(t *testing.T) {
	for _, name := range []string{"transient", "lifecycle", "ledger", "evidence", "journal"} {
		t.Run(name, func(t *testing.T) {
			encoded, providerCalls, events := 0, 0, 0
			schema := digestCountingSchema{calls: &encoded}
			req := model.ChatRequest{Model: "test-model", Messages: []model.Message{{Role: "user", Content: "Continue."}}, Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": schema}}}}
			cfg := ControllerConfig{
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) { return req, nil },
				CallModel: ModelCallerFunc(func(_ context.Context, got model.ChatRequest, _ bool) (*model.ChatResponse, error) {
					providerCalls++
					parameters := got.Tools[0]["function"].(map[string]any)["parameters"].(digestCountingSchema)
					if parameters.calls != &encoded {
						t.Fatal("provider request changed")
					}
					return textResponse("done", model.Usage{}), nil
				}),
			}
			expectedEncodes := 0
			var journal *digestCapturingJournal
			switch name {
			case "lifecycle":
				cfg.LifecycleObserver = func(LifecycleEvent) { events++ }
				expectedEncodes = 1
			case "ledger":
				ledger, _, runID := newDurableControllerStores(t)
				cfg.RunLedger = ledger
				cfg.RunID = runID
				expectedEncodes = 1
			case "evidence":
				_, ev, _ := newDurableControllerStores(t)
				cfg.Evidence = ev
				expectedEncodes = 2
			case "journal":
				journal = &digestCapturingJournal{failure: errors.New("stop before dispatch")}
				cfg.StepJournal = journal
				expectedEncodes = 1
			}
			ctrl, err := NewController(cfg)
			if err != nil {
				t.Fatal(err)
			}
			result, err := ctrl.executePreparedModelRound(t.Context(), &Result{}, req, 1, "model", false)
			controllerEncodes := encoded
			if name == "journal" {
				if !errors.Is(err, journal.failure) || providerCalls != 0 {
					t.Fatalf("journal path: provider=%d err=%v", providerCalls, err)
				}
				legacy, marshalErr := json.Marshal(req)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				// The independent legacy codec must still produce the retained digest.
				sum := sha256.Sum256(legacy)
				want := hex.EncodeToString(sum[:])
				if journal.digest != want {
					t.Fatalf("digest=%s want=%s", journal.digest, want)
				}
			} else if err != nil || result == nil || result.response.Choices[0].Message.Content != "done" || providerCalls != 1 {
				t.Fatalf("model result=%+v provider=%d err=%v", result, providerCalls, err)
			}
			if controllerEncodes != expectedEncodes {
				t.Fatalf("request encodes=%d want=%d", controllerEncodes, expectedEncodes)
			}
			if name == "lifecycle" && events == 0 {
				t.Fatal("lifecycle events lost")
			}
		})
	}
}
