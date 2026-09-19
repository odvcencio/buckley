package model

import (
	"bytes"
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

	capabilityFields map[string]struct{}
}

// UnmarshalJSON records which models.dev capability booleans were explicitly
// present, preserving the public bool fields for existing Go literal callers.
func (m *ModelsDevModel) UnmarshalJSON(data []byte) error {
	type modelsDevModelAlias ModelsDevModel
	var decoded modelsDevModelAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*m = ModelsDevModel(decoded)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, field := range modelsDevCapabilityFieldNames {
		value, ok := raw[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		var present bool
		if err := json.Unmarshal(value, &present); err != nil {
			return fmt.Errorf("models.dev: decode %s capability: %w", field, err)
		}
		if m.capabilityFields == nil {
			m.capabilityFields = make(map[string]struct{})
		}
		m.capabilityFields[field] = struct{}{}
	}
	return nil
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
		info.SupportedParameters = append([]string(nil), info.SupportedParameters...)
		info.supportedParameterEvidence = cloneStringSet(info.supportedParameterEvidence)
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
			info.SupportedParameters = mergeModelsDevSupportedParameters(info.SupportedParameters, m)
			markModelsDevSupportedParameterEvidence(&info, m)
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

type modelsDevCapabilityUpdate struct {
	field  string
	add    string
	remove []string
}

var modelsDevCapabilityUpdates = []modelsDevCapabilityUpdate{
	{field: "tool_call", add: "tools", remove: []string{"tools", "functions"}},
	{field: "reasoning", add: "reasoning", remove: []string{"reasoning", "reasoning_effort"}},
	{field: "attachment", add: "attachment", remove: []string{"attachment"}},
	{field: "temperature", add: "temperature", remove: []string{"temperature"}},
	{field: "structured_output", add: "structured_output", remove: []string{"structured_output"}},
}

var modelsDevCapabilityFieldNames = []string{
	"tool_call",
	"reasoning",
	"attachment",
	"temperature",
	"structured_output",
}

func mergeModelsDevSupportedParameters(existing []string, m ModelsDevModel) []string {
	params := append([]string(nil), existing...)
	for _, update := range modelsDevCapabilityUpdates {
		value, known := modelsDevCapabilityValue(m, update)
		if !known {
			continue
		}
		if value {
			params = addModelParameter(params, update.add)
			continue
		}
		params = removeModelParameters(params, update.remove)
	}
	return params
}

func markModelsDevSupportedParameterEvidence(info *ModelInfo, m ModelsDevModel) {
	for _, update := range modelsDevCapabilityUpdates {
		if _, known := modelsDevCapabilityValue(m, update); !known {
			continue
		}
		info.markSupportedParameterEvidence(append([]string{update.add}, update.remove...)...)
	}
}

func cloneStringSet(in map[string]struct{}) map[string]struct{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(in))
	for key := range in {
		out[key] = struct{}{}
	}
	return out
}

func modelsDevCapabilityValue(m ModelsDevModel, update modelsDevCapabilityUpdate) (bool, bool) {
	if m.capabilityFields != nil {
		if _, ok := m.capabilityFields[update.field]; !ok {
			return false, false
		}
		return modelsDevCapabilityBool(m, update.field), true
	}
	if modelsDevCapabilityBool(m, update.field) {
		return true, true
	}
	return false, false
}

func modelsDevCapabilityBool(m ModelsDevModel, field string) bool {
	switch field {
	case "tool_call":
		return m.ToolCall
	case "reasoning":
		return m.Reasoning
	case "attachment":
		return m.Attachment
	case "temperature":
		return m.Temperature
	case "structured_output":
		return m.StructuredOutput
	default:
		return false
	}
}

func addModelParameter(params []string, param string) []string {
	if containsString(params, param) {
		return params
	}
	return append(params, param)
}

func removeModelParameters(params []string, remove []string) []string {
	out := params[:0]
	for _, param := range params {
		if containsString(remove, param) {
			continue
		}
		out = append(out, param)
	}
	return out
}
