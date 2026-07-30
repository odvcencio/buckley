package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const DefaultPRContextEvidencePriority = 50

// PRContextProviderRequest is the immutable snapshot exposed to deterministic
// review-context providers. Providers inspect this request and return evidence;
// they do not mutate the captured pull-request context.
type PRContextProviderRequest struct {
	PR             PRInfo
	RepositoryRoot string
	CheckoutSHA    string
	ChangedFiles   []string
}

// PRContextEvidence is one provider-owned context artifact. Files scopes an
// artifact to matching diff shards. An artifact without Files is shown only to
// the primary shard unless AllShards is true.
type PRContextEvidence struct {
	Provider  string
	Title     string
	Body      string
	Priority  int
	Files     []string
	AllShards bool
}

// PRContextProvider supplies deterministic, model-independent review evidence.
// Implementations should return concise derived facts rather than raw document
// dumps; the shared curation layer applies the final prompt budget.
type PRContextProvider interface {
	Name() string
	Collect(context.Context, PRContextProviderRequest) ([]PRContextEvidence, error)
}

// OptionalPRContextProvider lets enrichment adapters declare that temporary
// unavailability should be visible without blocking approval. Providers are
// required by default so policy-critical adapters remain fail-closed.
type OptionalPRContextProvider interface {
	Required() bool
}

func collectPRContextProviderEvidence(
	parent context.Context,
	prCtx *PRContext,
	providers []PRContextProvider,
) {
	if prCtx == nil || len(providers) == 0 {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	request := PRContextProviderRequest{
		RepositoryRoot: prCtx.localRepoRoot,
		CheckoutSHA:    prCtx.CheckoutSHA,
		ChangedFiles:   append([]string(nil), prCtx.Files...),
	}
	if prCtx.PR != nil {
		request.PR = *prCtx.PR
		request.PR.Labels = append([]string(nil), prCtx.PR.Labels...)
	}

	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		name := strings.TrimSpace(provider.Name())
		if name == "" {
			prCtx.addStatus("Context provider", "failed", "provider name is empty", true)
			continue
		}
		if _, exists := seen[name]; exists {
			prCtx.addStatus("Context provider "+name, "failed", "duplicate provider name", true)
			continue
		}
		seen[name] = struct{}{}

		providerRequest := request
		providerRequest.ChangedFiles = append([]string(nil), request.ChangedFiles...)
		providerRequest.PR.Labels = append([]string(nil), request.PR.Labels...)
		evidence, err := provider.Collect(parent, providerRequest)
		if err != nil {
			required := prContextProviderRequired(provider)
			status := "failed"
			if !required {
				status = "advisory unavailable"
			}
			prCtx.addStatus("Context provider "+name, status, compactPRContextErrorText(err.Error()), required)
			continue
		}
		added := 0
		for _, item := range evidence {
			item.Provider = firstPRString(strings.TrimSpace(item.Provider), name)
			item.Title = strings.TrimSpace(item.Title)
			item.Body = strings.TrimSpace(item.Body)
			if item.Title == "" || item.Body == "" {
				continue
			}
			if item.Priority <= 0 {
				item.Priority = DefaultPRContextEvidencePriority
			}
			item.Files = normalizePRContextEvidenceFiles(item.Files)
			prCtx.ProviderEvidence = append(prCtx.ProviderEvidence, item)
			added++
		}
		prCtx.addStatus("Context provider "+name, "complete", fmt.Sprintf("%d evidence artifacts", added), false)
	}

	sort.SliceStable(prCtx.ProviderEvidence, func(i, j int) bool {
		left, right := prCtx.ProviderEvidence[i], prCtx.ProviderEvidence[j]
		if left.Priority != right.Priority {
			return left.Priority > right.Priority
		}
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		return left.Title < right.Title
	})
}

func prContextProviderRequired(provider PRContextProvider) bool {
	optional, ok := provider.(OptionalPRContextProvider)
	if !ok {
		return true
	}
	return optional.Required()
}

func normalizePRContextEvidenceFiles(files []string) []string {
	if len(files) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(files))
	normalized := make([]string, 0, len(files))
	for _, file := range files {
		file = strings.TrimSpace(strings.ReplaceAll(file, "\\", "/"))
		if file == "" {
			continue
		}
		if _, exists := seen[file]; exists {
			continue
		}
		seen[file] = struct{}{}
		normalized = append(normalized, file)
	}
	sort.Strings(normalized)
	return normalized
}

func prContextEvidenceForShard(evidence []PRContextEvidence, files []string, primary bool) []PRContextEvidence {
	if len(evidence) == 0 {
		return nil
	}
	fileSet := make(map[string]struct{}, len(files))
	for _, file := range files {
		fileSet[file] = struct{}{}
	}
	selected := make([]PRContextEvidence, 0, len(evidence))
	for _, item := range evidence {
		switch {
		case item.AllShards:
			selected = append(selected, item)
		case len(item.Files) == 0 && primary:
			selected = append(selected, item)
		case len(item.Files) > 0 && prContextEvidenceMatchesFiles(item.Files, fileSet):
			selected = append(selected, item)
		}
	}
	return selected
}

func prContextEvidenceMatchesFiles(files []string, shardFiles map[string]struct{}) bool {
	for _, file := range files {
		if _, ok := shardFiles[file]; ok {
			return true
		}
	}
	return false
}
