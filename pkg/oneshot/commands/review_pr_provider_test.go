package commands

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type stubPRContextProvider struct {
	name     string
	evidence []PRContextEvidence
	err      error
	request  PRContextProviderRequest
}

func (p *stubPRContextProvider) Name() string {
	return p.name
}

func (p *stubPRContextProvider) Collect(_ context.Context, request PRContextProviderRequest) ([]PRContextEvidence, error) {
	p.request = request
	return append([]PRContextEvidence(nil), p.evidence...), p.err
}

func TestCollectPRContextProviderEvidence_NormalizesAndRanksArtifacts(t *testing.T) {
	lower := &stubPRContextProvider{
		name: "hyphae",
		evidence: []PRContextEvidence{{
			Title:    "Prior decisions",
			Body:     "  stable architectural decision  ",
			Priority: 10,
			Files:    []string{`pkg\api\handler.go`, "pkg/api/handler.go", " "},
		}},
	}
	higher := &stubPRContextProvider{
		name: "canopy",
		evidence: []PRContextEvidence{{
			Title:    "Changed consumers",
			Body:     "two direct consumers",
			Priority: 90,
		}},
	}
	ctx := &PRContext{
		PR:            &PRInfo{Number: 42, Labels: []string{"runtime"}},
		Files:         []string{"pkg/api/handler.go"},
		CheckoutSHA:   "abc123",
		localRepoRoot: "/work/buckley",
	}

	collectPRContextProviderEvidence(context.Background(), ctx, []PRContextProvider{lower, higher})

	if got, want := []string{ctx.ProviderEvidence[0].Provider, ctx.ProviderEvidence[1].Provider}, []string{"canopy", "hyphae"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("provider order = %v, want %v", got, want)
	}
	if got, want := ctx.ProviderEvidence[1].Files, []string{"pkg/api/handler.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized files = %v, want %v", got, want)
	}
	if ctx.ProviderEvidence[1].Body != "stable architectural decision" {
		t.Fatalf("body was not trimmed: %q", ctx.ProviderEvidence[1].Body)
	}
	if lower.request.RepositoryRoot != "/work/buckley" || lower.request.CheckoutSHA != "abc123" {
		t.Fatalf("provider request lost immutable checkout data: %+v", lower.request)
	}

	lower.request.ChangedFiles[0] = "mutated"
	lower.request.PR.Labels[0] = "mutated"
	if ctx.Files[0] != "pkg/api/handler.go" || ctx.PR.Labels[0] != "runtime" {
		t.Fatal("provider request aliases captured PR context")
	}
}

func TestCollectPRContextProviderEvidence_RecordsProviderFailure(t *testing.T) {
	ctx := &PRContext{PR: &PRInfo{Number: 42}}
	collectPRContextProviderEvidence(context.Background(), ctx, []PRContextProvider{
		&stubPRContextProvider{name: "hyphae", err: errors.New("index unavailable")},
	})

	if len(ctx.ProviderEvidence) != 0 {
		t.Fatalf("evidence = %+v, want none after provider failure", ctx.ProviderEvidence)
	}
	if !ctx.HasIncompleteContext() {
		t.Fatal("provider failure must keep approval fail-closed")
	}
}

func TestPRContextEvidenceForShard_AppliesScopeRules(t *testing.T) {
	evidence := []PRContextEvidence{
		{Title: "global-primary"},
		{Title: "all", AllShards: true},
		{Title: "api", Files: []string{"pkg/api/handler.go"}},
		{Title: "storage", Files: []string{"pkg/storage/db.go"}},
	}

	primary := prContextEvidenceForShard(evidence, []string{"pkg/api/handler.go"}, true)
	if got, want := evidenceTitles(primary), []string{"global-primary", "all", "api"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("primary evidence = %v, want %v", got, want)
	}
	secondary := prContextEvidenceForShard(evidence, []string{"pkg/storage/db.go"}, false)
	if got, want := evidenceTitles(secondary), []string{"all", "storage"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("secondary evidence = %v, want %v", got, want)
	}
}

func evidenceTitles(evidence []PRContextEvidence) []string {
	titles := make([]string, 0, len(evidence))
	for _, item := range evidence {
		titles = append(titles, item.Title)
	}
	return titles
}
