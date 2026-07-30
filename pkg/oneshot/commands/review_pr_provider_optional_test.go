package commands

import (
	"context"
	"errors"
	"testing"
)

type optionalFailingPRContextProvider struct{}

func (optionalFailingPRContextProvider) Name() string {
	return "optional"
}

func (optionalFailingPRContextProvider) Required() bool {
	return false
}

func (optionalFailingPRContextProvider) Collect(context.Context, PRContextProviderRequest) ([]PRContextEvidence, error) {
	return nil, errors.New("temporarily unavailable")
}

func TestCollectPRContextProviderEvidence_OptionalFailureDoesNotBlockApproval(t *testing.T) {
	ctx := &PRContext{PR: &PRInfo{Number: 42}}
	collectPRContextProviderEvidence(context.Background(), ctx, []PRContextProvider{
		optionalFailingPRContextProvider{},
	})

	if ctx.HasIncompleteContext() {
		t.Fatal("optional enrichment failure unexpectedly blocked approval")
	}
	if len(ctx.ContextStatus) != 1 || ctx.ContextStatus[0].Status != "advisory unavailable" {
		t.Fatalf("context status = %+v, want visible unavailable status", ctx.ContextStatus)
	}
}
