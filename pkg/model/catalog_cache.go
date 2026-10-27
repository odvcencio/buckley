package model

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"
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
	ID                          string       `json:"id"`
	Name                        string       `json:"name"`
	Description                 string       `json:"description"`
	ContextLength               int          `json:"context_length"`
	MaxCompletionTokens         int          `json:"max_completion_tokens,omitempty"`
	Pricing                     cachePricing `json:"pricing"`
	PricingKnown                bool         `json:"pricing_known,omitempty"`
	Created                     int64        `json:"created"`
	Architecture                Architecture `json:"architecture,omitempty"`
	SupportedParameters         []string     `json:"supported_parameters,omitempty"`
	SupportedParametersComplete bool         `json:"supported_parameters_complete,omitempty"`
	SupportedParameterEvidence  []string     `json:"supported_parameter_evidence,omitempty"`
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
		info := ModelInfo{
			ID:                  entry.ID,
			Name:                entry.Name,
			Description:         entry.Description,
			ContextLength:       entry.ContextLength,
			MaxCompletionTokens: entry.MaxCompletionTokens,
			Pricing:             ModelPricing{Prompt: entry.Pricing.Prompt, Completion: entry.Pricing.Completion},
			PricingKnown:        entry.PricingKnown,
			Created:             entry.Created,
			Architecture:        entry.Architecture,
			SupportedParameters: entry.SupportedParameters,
		}
		info.supportedParametersComplete = entry.SupportedParametersComplete
		info.setSupportedParameterEvidence(entry.SupportedParameterEvidence...)
		out[entry.ID] = info
	}
	return out, nil
}

// SaveCatalogCache publishes compact catalog JSON sorted by ID without
// modifying an open reader's file. Existing permissions and symbolic links
// are preserved, and parent directories are created as needed.
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
			ID:                          info.ID,
			Name:                        info.Name,
			Description:                 info.Description,
			ContextLength:               info.ContextLength,
			MaxCompletionTokens:         info.MaxCompletionTokens,
			Pricing:                     cachePricing{Prompt: info.Pricing.Prompt, Completion: info.Pricing.Completion},
			PricingKnown:                info.PricingKnown,
			Created:                     info.Created,
			Architecture:                info.Architecture,
			SupportedParameters:         info.SupportedParameters,
			SupportedParametersComplete: info.supportedParametersComplete,
			SupportedParameterEvidence:  info.supportedParameterEvidenceList(),
		})
	}

	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode catalog cache: %w", err)
	}
	var existing os.FileInfo
	for links := 0; ; links++ {
		existing, err = os.Lstat(path)
		if os.IsNotExist(err) {
			existing = nil
			break
		}
		if err != nil {
			return fmt.Errorf("inspect catalog cache: %w", err)
		}
		if existing.Mode()&os.ModeSymlink == 0 {
			break
		}
		if links >= 255 {
			return fmt.Errorf("resolve catalog cache: too many symbolic links")
		}
		target, err := os.Readlink(path)
		if err != nil {
			return fmt.Errorf("resolve catalog cache: %w", err)
		}
		if filepath.IsAbs(target) {
			path = target
		} else {
			parent, err := filepath.EvalSymlinks(filepath.Dir(path))
			if err != nil {
				return fmt.Errorf("resolve catalog cache directory: %w", err)
			}
			path = filepath.Join(parent, target)
		}
	}
	if existing != nil {
		if !existing.Mode().IsRegular() {
			return fmt.Errorf("catalog cache must be a regular file")
		}
		if existing.Mode().Perm()&0o222 == 0 {
			return fmt.Errorf("write catalog cache: %w", os.ErrPermission)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create catalog cache directory: %w", err)
	}
	// Keep staging beside the destination for same-filesystem publication.
	// OpenFile preserves umask behavior when creating a new cache.
	tempPath := filepath.Join(filepath.Dir(path), ".model-catalog-"+rand.Text()+".tmp")
	file, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create catalog cache staging file: %w", err)
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(tempPath)
	}()
	if existing != nil {
		if err := file.Chmod(existing.Mode().Perm()); err != nil {
			return fmt.Errorf("preserve catalog cache permissions: %w", err)
		}
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write catalog cache: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close catalog cache staging file: %w", err)
	}
	for attempt := 0; ; attempt++ {
		err := os.Rename(tempPath, path)
		if err == nil {
			return nil
		}
		if runtime.GOOS != "windows" || attempt >= 3 {
			return fmt.Errorf("publish catalog cache: %w", err)
		}
		// Windows readers can briefly prevent replacement while their file
		// handle is open. The previous cache stays intact if retries expire.
		time.Sleep(10 * time.Millisecond)
	}
}
