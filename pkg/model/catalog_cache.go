package model

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// cachePricing mirrors ModelPricing's wire shape (dollars per one million
// tokens) without ModelPricing's custom UnmarshalJSON, which exists to
// convert OpenRouter's raw per-token API values on the way in. The catalog
// cache is Buckley's own document, already in per-million-token units, so
// round-tripping it through that OpenRouter-specific conversion would
// silently inflate every price by 1e6 on load.
type cachePricing struct {
	Prompt     float64 `json:"prompt"`
	Completion float64 `json:"completion"`
}

// cacheModelInfo mirrors ModelInfo's JSON field names exactly, substituting
// cachePricing for Pricing, so the cache file's shape matches the existing
// ModelCatalog/ModelInfo document that OpenRouter's catalog client already
// produces.
type cacheModelInfo struct {
	ID                  string       `json:"id"`
	Name                string       `json:"name"`
	Description         string       `json:"description"`
	ContextLength       int          `json:"context_length"`
	Pricing             cachePricing `json:"pricing"`
	Created             int64        `json:"created"`
	Architecture        Architecture `json:"architecture,omitempty"`
	SupportedParameters []string     `json:"supported_parameters,omitempty"`
}

type cacheCatalogDoc struct {
	Data []cacheModelInfo `json:"data"`
}

// LoadCatalogCache reads a persisted catalog from path. A missing file
// returns an empty catalog rather than an error, since a first-ever
// refresh has nothing to merge against yet.
func LoadCatalogCache(path string) (map[string]ModelInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]ModelInfo{}, nil
		}
		return nil, fmt.Errorf("read catalog cache: %w", err)
	}

	var doc cacheCatalogDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse catalog cache: %w", err)
	}

	out := make(map[string]ModelInfo, len(doc.Data))
	for _, entry := range doc.Data {
		out[entry.ID] = ModelInfo{
			ID:                  entry.ID,
			Name:                entry.Name,
			Description:         entry.Description,
			ContextLength:       entry.ContextLength,
			Pricing:             ModelPricing{Prompt: entry.Pricing.Prompt, Completion: entry.Pricing.Completion},
			Created:             entry.Created,
			Architecture:        entry.Architecture,
			SupportedParameters: entry.SupportedParameters,
		}
	}
	return out, nil
}

// SaveCatalogCache writes catalog to path, sorted by ID, creating parent
// directories as needed.
func SaveCatalogCache(path string, catalog map[string]ModelInfo) error {
	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	doc := cacheCatalogDoc{Data: make([]cacheModelInfo, 0, len(ids))}
	for _, id := range ids {
		info := catalog[id]
		doc.Data = append(doc.Data, cacheModelInfo{
			ID:                  info.ID,
			Name:                info.Name,
			Description:         info.Description,
			ContextLength:       info.ContextLength,
			Pricing:             cachePricing{Prompt: info.Pricing.Prompt, Completion: info.Pricing.Completion},
			Created:             info.Created,
			Architecture:        info.Architecture,
			SupportedParameters: info.SupportedParameters,
		})
	}

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode catalog cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create catalog cache directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write catalog cache: %w", err)
	}
	return nil
}
