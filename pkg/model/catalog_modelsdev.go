package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ModelsDevAPIURL is the models.dev catalog endpoint (models.dev docs:
// "GET https://models.dev/api.json" returns every known provider and
// model, keyed by provider ID).
const ModelsDevAPIURL = "https://models.dev/api.json"

// ModelsDevCatalog is the raw shape of the models.dev API response: a map
// of provider ID to ModelsDevProvider.
type ModelsDevCatalog map[string]ModelsDevProvider

// ModelsDevProvider is one provider entry in the models.dev catalog.
type ModelsDevProvider struct {
	ID     string                    `json:"id"`
	Name   string                    `json:"name"`
	Models map[string]ModelsDevModel `json:"models"`
}

// ModelsDevModel is one model entry under a models.dev provider.
type ModelsDevModel struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Description      string              `json:"description"`
	Reasoning        bool                `json:"reasoning"`
	ToolCall         bool                `json:"tool_call"`
	Attachment       bool                `json:"attachment"`
	Temperature      bool                `json:"temperature"`
	StructuredOutput bool                `json:"structured_output"`
	Limit            ModelsDevLimit      `json:"limit"`
	Cost             ModelsDevCost       `json:"cost"`
	Modalities       ModelsDevModalities `json:"modalities"`
}

// ModelsDevLimit is a model's context/output token limits.
type ModelsDevLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

// ModelsDevCost is a model's pricing, already expressed in USD per one
// million tokens (models.dev docs), matching ModelPricing's convention.
type ModelsDevCost struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

// ModelsDevModalities lists the input/output content types a model
// supports (for example "text", "image", "pdf").
type ModelsDevModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// FetchModelsDevCatalog fetches and decodes the models.dev catalog from
// url (ModelsDevAPIURL when empty) using httpClient (http.DefaultClient
// when nil). Every failure mode — network error, non-200 status, bad JSON
// — returns a wrapped error rather than panicking, so a caller can report
// "offline, try later" and continue.
func FetchModelsDevCatalog(ctx context.Context, httpClient *http.Client, url string) (ModelsDevCatalog, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if url == "" {
		url = ModelsDevAPIURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("models.dev: build request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("models.dev: fetch catalog: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev: unexpected status %s", resp.Status)
	}

	var catalog ModelsDevCatalog
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return nil, fmt.Errorf("models.dev: decode catalog: %w", err)
	}
	return catalog, nil
}

// MergeModelsDevCatalog merges models.dev capability and pricing metadata
// into base (an existing catalog keyed by "provider/model"), returning a
// new map. This is a merge, not a replace: every entry already in base is
// kept, models.dev entries fill in or refresh pricing/context/capability
// fields on matching IDs, and models.dev-only models are added. A model
// curated in base that models.dev does not know about survives untouched.
func MergeModelsDevCatalog(base map[string]ModelInfo, catalog ModelsDevCatalog) map[string]ModelInfo {
	merged := make(map[string]ModelInfo, len(base))
	for id, info := range base {
		merged[id] = info
	}

	for providerID, provider := range catalog {
		for modelID, m := range provider.Models {
			compositeID := providerID + "/" + modelID
			info := merged[compositeID]
			info.ID = compositeID
			if m.Name != "" {
				info.Name = m.Name
			}
			if m.Description != "" {
				info.Description = m.Description
			}
			if m.Limit.Context > 0 {
				info.ContextLength = m.Limit.Context
			}
			if m.Limit.Output > 0 {
				info.MaxCompletionTokens = m.Limit.Output
			}
			if m.Cost.Input > 0 || m.Cost.Output > 0 {
				info.Pricing = ModelPricing{Prompt: m.Cost.Input, Completion: m.Cost.Output}
			}
			info.Architecture.Modality = modelsDevModality(m.Modalities)
			if params := modelsDevSupportedParameters(m); len(params) > 0 {
				info.SupportedParameters = params
			}
			merged[compositeID] = info
		}
	}
	return merged
}

func modelsDevModality(m ModelsDevModalities) string {
	if containsString(m.Input, "image") || containsString(m.Output, "image") {
		return "text+image"
	}
	return "text"
}

func modelsDevSupportedParameters(m ModelsDevModel) []string {
	var params []string
	if m.ToolCall {
		params = append(params, "tools")
	}
	if m.Reasoning {
		params = append(params, "reasoning")
	}
	if m.Attachment {
		params = append(params, "attachment")
	}
	if m.Temperature {
		params = append(params, "temperature")
	}
	if m.StructuredOutput {
		params = append(params, "structured_output")
	}
	return params
}
