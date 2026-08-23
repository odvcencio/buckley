package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"m31labs.dev/buckley/pkg/launchcontract"
)

const (
	// These bounds apply only to the trusted launch-admission observation. The
	// ordinary catalog path retains its existing compatibility behavior.
	MaxOfficialCatalogResponseBytes = 16 << 20
	MaxOfficialCatalogModels        = 16_384
	MaxOfficialCatalogModelBytes    = 1 << 20
	MaxOfficialCatalogFieldBytes    = 512 << 10
	MaxOfficialCatalogStringBytes   = 256 << 10
)

type officialCatalogObservation struct {
	Catalog           *ModelCatalog
	ObservedAt        time.Time
	ResponseDigest    string
	ModelObjectDigest []string
}

// refreshOfficialOpenRouterCatalog is intentionally unexported. Only the
// concrete OpenRouter adapter can mint an observation accepted by launch
// admission; callers cannot supply a source URL or typed ModelInfo as proof.
func (c *Client) refreshOfficialOpenRouterCatalog(ctx context.Context) (*officialCatalogObservation, error) {
	if c == nil || c.CatalogSourceURL() != launchcontract.OpenRouterCatalogSourceURL || c.httpClient == nil {
		return nil, errors.New("model: official OpenRouter catalog source is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	operationCtx, cancel := context.WithTimeout(ctx, catalogFetchTimeout)
	defer cancel()
	if err := operationCtx.Err(); err != nil {
		return nil, fmt.Errorf("model: official OpenRouter catalog observation canceled: %w", err)
	}

	request, err := http.NewRequestWithContext(operationCtx, http.MethodGet, launchcontract.OpenRouterCatalogSourceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("model: creating official OpenRouter catalog request: %w", err)
	}
	c.setHeaders(request)
	request.Header.Set("Accept", "application/json")
	if c.rateLimiter != nil {
		if err := c.rateLimiter.Wait(operationCtx); err != nil {
			return nil, fmt.Errorf("model: waiting for official OpenRouter catalog request: %w", err)
		}
	}

	// The admission request must not follow a redirect to a caller-controlled
	// host. Clone the client so ordinary request behavior is unchanged.
	httpClient := *c.httpClient
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("model: fetching official OpenRouter catalog: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model: official OpenRouter catalog returned status %d", response.StatusCode)
	}
	if response.ContentLength > MaxOfficialCatalogResponseBytes {
		return nil, errors.New("model: official OpenRouter catalog exceeds its bound")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxOfficialCatalogResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("model: reading official OpenRouter catalog: %w", err)
	}
	if len(body) == 0 || len(body) > MaxOfficialCatalogResponseBytes {
		return nil, errors.New("model: official OpenRouter catalog exceeds its bound")
	}
	if err := operationCtx.Err(); err != nil {
		return nil, fmt.Errorf("model: official OpenRouter catalog observation canceled: %w", err)
	}
	catalog, modelDigests, err := decodeOfficialOpenRouterCatalog(body)
	if err != nil {
		return nil, err
	}
	responseDigest := sha256.Sum256(body)
	return &officialCatalogObservation{
		Catalog:           catalog,
		ObservedAt:        time.Now().UTC().Round(0),
		ResponseDigest:    hex.EncodeToString(responseDigest[:]),
		ModelObjectDigest: modelDigests,
	}, nil
}

func decodeOfficialOpenRouterCatalog(body []byte) (*ModelCatalog, []string, error) {
	if err := launchcontract.RejectDuplicateJSONKeys(body); err != nil {
		return nil, nil, errors.New("model: official OpenRouter catalog contains duplicate fields")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, nil, fmt.Errorf("model: decoding official OpenRouter catalog: %w", err)
	}
	rawData, ok := document["data"]
	if !ok || len(rawData) == 0 {
		return nil, nil, errors.New("model: official OpenRouter catalog has no data array")
	}
	var rawModels []json.RawMessage
	if err := json.Unmarshal(rawData, &rawModels); err != nil || len(rawModels) > MaxOfficialCatalogModels {
		return nil, nil, errors.New("model: official OpenRouter catalog model count exceeds its bound")
	}
	catalog := &ModelCatalog{Data: make([]ModelInfo, 0, len(rawModels))}
	digests := make([]string, 0, len(rawModels))
	for _, rawModel := range rawModels {
		if len(rawModel) == 0 || len(rawModel) > MaxOfficialCatalogModelBytes {
			return nil, nil, errors.New("model: official OpenRouter model object exceeds its bound")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawModel, &fields); err != nil {
			return nil, nil, errors.New("model: official OpenRouter model object is invalid")
		}
		for name, value := range fields {
			if len(name) > MaxOfficialCatalogStringBytes || len(value) > MaxOfficialCatalogFieldBytes {
				return nil, nil, errors.New("model: official OpenRouter model field exceeds its bound")
			}
			if len(value) > 0 && value[0] == '"' {
				var text string
				if json.Unmarshal(value, &text) == nil && len(text) > MaxOfficialCatalogStringBytes {
					return nil, nil, errors.New("model: official OpenRouter model string field exceeds its bound")
				}
			}
		}
		rawPricing, ok := fields["pricing"]
		if !ok || len(rawPricing) == 0 || len(rawPricing) > MaxCatalogPricingBytes {
			return nil, nil, errors.New("model: official OpenRouter model pricing is unavailable")
		}
		prices, err := decodeCatalogPricingStrict(rawPricing)
		if err != nil {
			return nil, nil, err
		}
		var info ModelInfo
		if err := json.Unmarshal(rawModel, &info); err != nil || info.ID == "" {
			return nil, nil, errors.New("model: official OpenRouter model object is invalid")
		}
		info.RawPricing = prices
		info.RawPricingJSON = append(json.RawMessage(nil), rawPricing...)
		info.PricingKnown = validCatalogPrice(prices["prompt"]) && validCatalogPrice(prices["completion"])
		catalog.Data = append(catalog.Data, info)
		digest := sha256.Sum256(bytes.TrimSpace(rawModel))
		digests = append(digests, hex.EncodeToString(digest[:]))
	}
	return catalog, digests, nil
}
