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
	"regexp"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/launchcontract"
)

const (
	OpenRouterCatalogSourceID  = launchcontract.OpenRouterCatalogSourceID
	OpenRouterCatalogSourceURL = launchcontract.OpenRouterCatalogSourceURL
	FreePriceEvidenceSchema    = launchcontract.FreePriceEvidenceSchema
	MaxFreePriceEvidenceBytes  = launchcontract.MaxFreePriceEvidenceBytes
	MaxCatalogPricingFields    = 32
	MaxCatalogPricingBytes     = 4 << 10
	MaxCatalogPriceValueBytes  = launchcontract.MaxCatalogPriceValueBytes
	MaxFreePriceEvidenceTTL    = launchcontract.MaxFreePriceEvidenceTTL
	OpenRouterLaunchProvider   = "openrouter"
	OpenRouterLaunchModel      = "stealth/ox-alpha"
	OpenRouterLaunchReasoning  = "max"
	OpenRouterLaunchDataPolicy = "deny"
	OpenRouterLaunchMaxOutput  = 32_768
)

type RequestRetryMode string

const (
	RequestRetryDefault       RequestRetryMode = ""
	RequestRetrySingleAttempt RequestRetryMode = "single_attempt"
)

type OpenRouterFreeLaunchContract struct {
	Provider            string
	Model               string
	ReasoningEffort     string
	ZDR                 bool
	DataCollection      string
	AllowFallbacks      bool
	RequireParameters   bool
	MaxOutputPerRequest int64
}

func CanonicalOpenRouterFreeLaunchContract() OpenRouterFreeLaunchContract {
	return OpenRouterFreeLaunchContract{
		Provider: OpenRouterLaunchProvider, Model: OpenRouterLaunchModel,
		ReasoningEffort: OpenRouterLaunchReasoning, ZDR: false,
		DataCollection: OpenRouterLaunchDataPolicy, AllowFallbacks: false,
		RequireParameters: true, MaxOutputPerRequest: OpenRouterLaunchMaxOutput,
	}
}

func (c OpenRouterFreeLaunchContract) Validate() error {
	if c != CanonicalOpenRouterFreeLaunchContract() {
		return errors.New("model: OpenRouter free launch contract is not canonical")
	}
	return nil
}

type RawPriceDimension = launchcontract.RawPriceDimension
type FreePriceEvidence = launchcontract.FreePriceEvidence

// LaunchPriceProof has package-private state and can only be produced by a
// fresh official OpenRouter catalog verification.
type LaunchPriceProof struct {
	evidence launchcontract.FreePriceEvidence
}

func (p LaunchPriceProof) Snapshot() launchcontract.FreePriceEvidence {
	return cloneLaunchPriceEvidence(p.evidence)
}

type freshCatalogProvider interface {
	openRouterCatalogAuthority()
	refreshOfficialOpenRouterCatalog(context.Context) (*officialCatalogObservation, error)
}

var (
	priceDimensionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	priceNumberPattern    = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)
)

// ApplyOpenRouterFreeLaunchRequest applies the exact wire and retry contract
// from an immutable launch profile. It is intentionally not wired into goal
// start until durable reservations are available.
func ApplyOpenRouterFreeLaunchRequest(req ChatRequest, contract OpenRouterFreeLaunchContract) (ChatRequest, error) {
	if err := contract.Validate(); err != nil {
		return req, err
	}
	if strings.TrimSpace(req.Model) != "" && strings.TrimSpace(req.Model) != contract.Model {
		return req, errors.New("model: launch request model conflicts with its profile")
	}
	req.Model = contract.Model
	req.Models = nil
	req.MaxTokens = int(contract.MaxOutputPerRequest)
	req.MaxCompletionTokens = 0
	req.Reasoning = &ReasoningConfig{Effort: contract.ReasoningEffort}
	req.Provider = map[string]any{
		"allow_fallbacks":    false,
		"require_parameters": true,
		"data_collection":    contract.DataCollection,
		"zdr":                false,
		"max_price": map[string]any{
			"prompt": 0, "completion": 0, "request": 0, "image": 0,
		},
	}
	req.RetryMode = RequestRetrySingleAttempt
	return req, nil
}

// ValidateOpenRouterFreeLaunchRequest is the final fail-closed boundary for a
// single-attempt launch request. It is called after Manager transforms and
// immediately before every provider marshal/dispatch path.
func ValidateOpenRouterFreeLaunchRequest(req ChatRequest) error {
	if req.RetryMode != RequestRetrySingleAttempt {
		return nil
	}
	if req.Model != OpenRouterLaunchModel || len(req.Models) != 0 || req.MaxTokens != OpenRouterLaunchMaxOutput || req.MaxCompletionTokens != 0 {
		return errors.New("model: launch request route is not exact OpenRouter Ox-alpha")
	}
	if req.Reasoning == nil || req.Reasoning.Effort != OpenRouterLaunchReasoning || req.Reasoning.MaxTokens != 0 || req.Reasoning.Enabled != nil || req.Reasoning.Exclude != nil {
		return errors.New("model: launch request reasoning contract is not exact")
	}
	if len(req.Provider) != 5 || !exactProviderBool(req.Provider, "allow_fallbacks", false) || !exactProviderBool(req.Provider, "require_parameters", true) || !exactProviderString(req.Provider, "data_collection", OpenRouterLaunchDataPolicy) || !exactProviderBool(req.Provider, "zdr", false) {
		return errors.New("model: launch request provider contract is not exact")
	}
	maxPrice, ok := req.Provider["max_price"].(map[string]any)
	if !ok || len(maxPrice) != 4 {
		return errors.New("model: launch request max price contract is not exact")
	}
	for _, dimension := range []string{"prompt", "completion", "request", "image"} {
		value, ok := maxPrice[dimension].(int)
		if !ok || value != 0 {
			return errors.New("model: launch request max price contract is not exact")
		}
	}
	return nil
}

func validateOpenRouterLaunchDispatch(req ChatRequest, selectedModel string, provider Provider) error {
	if req.RetryMode != RequestRetrySingleAttempt {
		return nil
	}
	if provider == nil || provider.ID() != OpenRouterLaunchProvider || selectedModel != OpenRouterLaunchModel {
		return errors.New("model: launch request resolved to a non-authoritative OpenRouter route")
	}
	return ValidateOpenRouterFreeLaunchRequest(req)
}

func exactProviderBool(provider map[string]any, key string, expected bool) bool {
	value, ok := provider[key].(bool)
	return ok && value == expected
}

func exactProviderString(provider map[string]any, key, expected string) bool {
	value, ok := provider[key].(string)
	return ok && value == expected
}

// VerifyLaunchPrice performs the official bounded catalog refresh itself;
// callers cannot submit catalog bytes, origin metadata, observation time, or
// prebuilt evidence.
func (m *Manager) VerifyLaunchPrice(ctx context.Context, profile launchcontract.ProfileDescriptor) (LaunchPriceProof, error) {
	if m == nil {
		return LaunchPriceProof{}, errors.New("model: manager is unavailable")
	}
	if profile.Validate() != nil || profile.Provider != OpenRouterLaunchProvider || profile.Model != OpenRouterLaunchModel || profile.ReasoningEffort != OpenRouterLaunchReasoning || profile.Privacy.ZDR || profile.Privacy.DataCollection != OpenRouterLaunchDataPolicy || profile.Privacy.AllowFallbacks || !profile.Privacy.RequireParameters {
		return LaunchPriceProof{}, errors.New("model: launch price request is not canonical")
	}
	contract := CanonicalOpenRouterFreeLaunchContract()
	selectedModel, provider := m.resolveModel(contract.Model)
	if provider == nil || selectedModel != contract.Model || provider.ID() != contract.Provider {
		return LaunchPriceProof{}, errors.New("model: exact OpenRouter route is unavailable")
	}
	refresher, ok := provider.(freshCatalogProvider)
	if !ok {
		return LaunchPriceProof{}, errors.New("model: official OpenRouter catalog source is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	observation, err := refreshCatalogForAdmission(ctx, refresher)
	if err != nil {
		return LaunchPriceProof{}, err
	}
	if observation == nil || observation.Catalog == nil || len(observation.ModelObjectDigest) != len(observation.Catalog.Data) {
		return LaunchPriceProof{}, errors.New("model: official OpenRouter catalog receipt is invalid")
	}
	var matched *ModelInfo
	matchedIndex := -1
	for index := range observation.Catalog.Data {
		if observation.Catalog.Data[index].ID != contract.Model {
			continue
		}
		if matched != nil {
			return LaunchPriceProof{}, errors.New("model: OpenRouter catalog contains duplicate exact model identities")
		}
		copy := observation.Catalog.Data[index]
		matched = &copy
		matchedIndex = index
	}
	if matched == nil || matched.ID != OpenRouterLaunchModel {
		return LaunchPriceProof{}, errors.New("model: exact OpenRouter model is unavailable")
	}
	prices, err := exactZeroPrices(matched.RawPricing)
	if err != nil {
		return LaunchPriceProof{}, err
	}
	now := time.Now().UTC().Round(0)
	if m.launchAdmissionNow != nil {
		now = m.launchAdmissionNow()
	}
	if !canonicalEvidenceTime(now) || !canonicalEvidenceTime(observation.ObservedAt) || observation.ObservedAt.After(now) || now.Sub(observation.ObservedAt) > MaxFreePriceEvidenceTTL {
		return LaunchPriceProof{}, errors.New("model: official OpenRouter catalog observation is stale or future-dated")
	}
	receipt, err := mintCatalogReceipt(observation.ObservedAt, observation.ResponseDigest, observation.ModelObjectDigest[matchedIndex])
	if err != nil {
		return LaunchPriceProof{}, err
	}
	evidence, err := mintFreePriceEvidence(contract.Model, observation.ObservedAt, MaxFreePriceEvidenceTTL, prices, receipt)
	if err != nil || evidence.ValidateAt(now) != nil {
		return LaunchPriceProof{}, errors.New("model: official OpenRouter catalog evidence is not fresh")
	}
	return LaunchPriceProof{evidence: cloneLaunchPriceEvidence(evidence)}, nil
}

func cloneLaunchPriceEvidence(value launchcontract.FreePriceEvidence) launchcontract.FreePriceEvidence {
	copy := value
	copy.Prices = append([]launchcontract.RawPriceDimension(nil), value.Prices...)
	return copy
}

func mintCatalogReceipt(observedAt time.Time, responseDigest, modelObjectDigest string) (launchcontract.CatalogReceipt, error) {
	receipt := launchcontract.CatalogReceipt{
		Schema: launchcontract.CatalogReceiptSchema, SourceID: launchcontract.OpenRouterCatalogSourceID,
		SourceURL: launchcontract.OpenRouterCatalogSourceURL, ObservedAt: observedAt,
		ResponseDigest: responseDigest, ModelObjectDigest: modelObjectDigest,
	}
	digest, err := launchDigestValue(receipt)
	if err != nil {
		return launchcontract.CatalogReceipt{}, err
	}
	receipt.Digest = digest
	if err := receipt.ValidateAt(observedAt); err != nil {
		return launchcontract.CatalogReceipt{}, err
	}
	return receipt, nil
}

func mintFreePriceEvidence(canonicalSlug string, observedAt time.Time, ttl time.Duration, prices []RawPriceDimension, receipt launchcontract.CatalogReceipt) (FreePriceEvidence, error) {
	evidence := FreePriceEvidence{
		Schema: launchcontract.FreePriceEvidenceSchema, ProviderID: launchcontract.ProviderOpenRouter,
		SourceID: launchcontract.OpenRouterCatalogSourceID, SourceURL: launchcontract.OpenRouterCatalogSourceURL,
		CanonicalSlug: canonicalSlug, ObservedAt: observedAt, ExpiresAt: observedAt.Add(ttl),
		Prices: append([]RawPriceDimension(nil), prices...), Receipt: receipt,
	}
	digest, err := launchDigestValue(evidence)
	if err != nil {
		return FreePriceEvidence{}, err
	}
	evidence.Digest = digest
	if err := evidence.ValidateAt(observedAt); err != nil {
		return FreePriceEvidence{}, err
	}
	return evidence, nil
}

func launchDigestValue(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data) == 0 || len(data) > MaxFreePriceEvidenceBytes {
		return "", errors.New("model: launch admission receipt exceeds its bound")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func refreshCatalogForAdmission(ctx context.Context, provider freshCatalogProvider) (*officialCatalogObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	observation, err := provider.refreshOfficialOpenRouterCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("model: fresh OpenRouter catalog observation failed: %w", err)
	}
	return observation, nil
}

func exactZeroPrices(raw map[string]string) ([]RawPriceDimension, error) {
	return launchcontract.NormalizeZeroPrices(raw)
}

func decodeCatalogPricing(data json.RawMessage) (map[string]string, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, nil
	}
	if len(data) > MaxCatalogPricingBytes {
		return nil, errors.New("model catalog pricing exceeds its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("model catalog pricing is invalid")
	}
	prices := make(map[string]string)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || !priceDimensionPattern.MatchString(key) || len(prices) >= MaxCatalogPricingFields {
			return nil, errors.New("model catalog pricing is invalid")
		}
		if _, duplicate := prices[key]; duplicate {
			return nil, errors.New("model catalog pricing contains duplicate dimensions")
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return nil, errors.New("model catalog pricing is invalid")
		}
		value, err := rawCatalogPrice(raw)
		if err != nil {
			return nil, err
		}
		prices[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("model catalog pricing is invalid")
	}
	if err := rejectPricingTrailingJSON(decoder); err != nil {
		return nil, err
	}
	return prices, nil
}

func decodeCatalogPricingStrict(data json.RawMessage) (map[string]string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, errors.New("model: official OpenRouter pricing is null or unavailable")
	}
	prices, err := decodeCatalogPricing(trimmed)
	if err != nil {
		return nil, err
	}
	if len(prices) == 0 {
		return nil, errors.New("model: official OpenRouter pricing is empty")
	}
	return prices, nil
}

func rawCatalogPrice(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > MaxCatalogPriceValueBytes {
		return "", errors.New("model catalog price exceeds its bound")
	}
	if bytes.Equal(raw, []byte("null")) {
		return "null", nil
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		if len(value) == 0 || len(value) > MaxCatalogPriceValueBytes {
			return "", errors.New("model catalog price exceeds its bound")
		}
		return value, nil
	}
	text := string(raw)
	if !priceNumberPattern.MatchString(text) {
		return "", errors.New("model catalog price is not numeric")
	}
	return text, nil
}

func validCatalogPrice(value string) bool {
	return launchcontract.ValidPriceLexeme(value)
}

func exactNumericZero(value string) bool {
	return launchcontract.ExactNumericZero(value)
}

func canonicalEvidenceTime(value time.Time) bool {
	return launchcontract.CanonicalTime(value)
}

func rejectPricingTrailingJSON(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	}
	return errors.New("model catalog pricing has trailing data")
}
