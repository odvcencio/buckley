package hyphae

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/oneshot/commands"
)

func TestReviewContextProvider_UsesBoundedSummaryAndAnchors(t *testing.T) {
	var binary string
	var args []string
	provider := NewReviewContextProvider()
	provider.Binary = "/opt/hypha"
	provider.MaxTokens = 320
	provider.run = func(_ context.Context, gotBinary string, gotArgs ...string) ([]byte, error) {
		binary = gotBinary
		args = append([]string(nil), gotArgs...)
		return []byte("decision: keep model adapters replaceable\nhypha://m31labs/buckley/object/decision.adapters"), nil
	}
	request := commands.PRContextProviderRequest{
		PR: commands.PRInfo{
			Repository: "m31labs/buckley",
			Title:      "Bound deterministic context",
		},
		ChangedFiles: []string{"pkg/oneshot/commands/review_pr_context.go"},
	}

	evidence, err := provider.Collect(context.Background(), request)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if binary != "/opt/hypha" {
		t.Fatalf("binary = %q, want /opt/hypha", binary)
	}
	wantTail := []string{"-shape", "summary+anchors", "-format", "text", "-max-tokens", strconv.Itoa(320)}
	if len(args) < len(wantTail)+2 || !reflect.DeepEqual(args[len(args)-len(wantTail):], wantTail) {
		t.Fatalf("args = %v, want tail %v", args, wantTail)
	}
	if args[0] != "recall" || !strings.Contains(args[1], "m31labs/buckley") ||
		!strings.Contains(args[1], "review_pr_context.go") {
		t.Fatalf("recall query is not repository/path scoped: %v", args)
	}
	if len(evidence) != 1 || evidence[0].Provider != "hyphae" || evidence[0].Priority != 80 {
		t.Fatalf("evidence = %+v, want one ranked Hyphae artifact", evidence)
	}
	if provider.Required() {
		t.Fatal("Hyphae enrichment should disclose unavailability without blocking approval")
	}
}

func TestReviewContextProvider_PropagatesRecallFailure(t *testing.T) {
	provider := NewReviewContextProvider()
	provider.Timeout = time.Second
	provider.run = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("binary unavailable")
	}

	if _, err := provider.Collect(context.Background(), commands.PRContextProviderRequest{}); err == nil ||
		!strings.Contains(err.Error(), "recalling project knowledge") {
		t.Fatalf("Collect() error = %v, want contextual recall failure", err)
	}
}

func TestBuildReviewQuery_IsBoundedAndValidUTF8(t *testing.T) {
	files := make([]string, 100)
	for i := range files {
		files[i] = strings.Repeat("路径/", 100) + strconv.Itoa(i)
	}
	query := buildReviewQuery(commands.PRContextProviderRequest{
		PR: commands.PRInfo{
			Repository: "m31labs/buckley",
			Title:      strings.Repeat("λ", 500),
		},
		ChangedFiles: files,
	})
	if len(query) > maxQueryBytes {
		t.Fatalf("query bytes = %d, want <= %d", len(query), maxQueryBytes)
	}
	if !strings.Contains(query, "m31labs/buckley") {
		t.Fatal("bounded query lost repository identity")
	}
	if strings.Contains(query, files[20]) {
		t.Fatal("query included changed paths beyond the deterministic cap")
	}
}
