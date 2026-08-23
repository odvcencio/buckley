package launchcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	OpenRouterCatalogSourceID  = CatalogSourceOpenRouter
	OpenRouterCatalogSourceURL = "https://openrouter.ai/api/v1/models"
	CatalogReceiptSchema       = "buckley.launch.catalog-receipt.v1"
	FreePriceEvidenceSchema    = "buckley.launch.free-price.v1"
	MaxFreePriceEvidenceBytes  = 8 << 10
	MaxCatalogPricingFields    = 32
	MaxCatalogPriceValueBytes  = 128
	MaxFreePriceEvidenceTTL    = 5 * time.Minute
)

var priceNumberPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

var knownChargeDimensions = map[string]struct{}{
	"prompt": {}, "completion": {}, "request": {}, "image": {},
	"web_search": {}, "internal_reasoning": {}, "input_cache_read": {}, "input_cache_write": {},
}

type RawPriceDimension struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CatalogReceipt binds the launch price decision to the exact bounded bytes
// returned by the trusted official catalog observation. The digests are
// deliberately retained in the durable evidence: a typed ModelInfo alone is
// not proof of which catalog response was observed.
type CatalogReceipt struct {
	Schema            string    `json:"schema"`
	SourceID          string    `json:"source_id"`
	SourceURL         string    `json:"source_url"`
	ObservedAt        time.Time `json:"observed_at"`
	ResponseDigest    string    `json:"response_digest"`
	ModelObjectDigest string    `json:"model_object_digest"`
	Digest            string    `json:"digest"`
}

func newCatalogReceipt(observedAt time.Time, responseDigest, modelObjectDigest string) (CatalogReceipt, error) {
	receipt := CatalogReceipt{
		Schema: CatalogReceiptSchema, SourceID: OpenRouterCatalogSourceID,
		SourceURL: OpenRouterCatalogSourceURL, ObservedAt: observedAt,
		ResponseDigest: responseDigest, ModelObjectDigest: modelObjectDigest,
	}
	digest, err := receipt.expectedDigest()
	if err != nil {
		return CatalogReceipt{}, err
	}
	receipt.Digest = digest
	if err := receipt.ValidateAt(observedAt); err != nil {
		return CatalogReceipt{}, err
	}
	return receipt, nil
}

type FreePriceEvidence struct {
	Schema        string              `json:"schema"`
	ProviderID    string              `json:"provider_id"`
	SourceID      string              `json:"source_id"`
	SourceURL     string              `json:"source_url"`
	CanonicalSlug string              `json:"canonical_slug"`
	ObservedAt    time.Time           `json:"observed_at"`
	ExpiresAt     time.Time           `json:"expires_at"`
	Prices        []RawPriceDimension `json:"prices"`
	Receipt       CatalogReceipt      `json:"receipt"`
	Digest        string              `json:"digest"`
}

func newFreePriceEvidence(canonicalSlug string, observedAt time.Time, ttl time.Duration, prices []RawPriceDimension, receipt CatalogReceipt) (FreePriceEvidence, error) {
	if observedAt != receipt.ObservedAt {
		return FreePriceEvidence{}, errors.New("launchcontract: free-price evidence observation time does not match its receipt")
	}
	evidence := FreePriceEvidence{
		Schema: FreePriceEvidenceSchema, ProviderID: ProviderOpenRouter,
		SourceID: OpenRouterCatalogSourceID, SourceURL: OpenRouterCatalogSourceURL,
		CanonicalSlug: canonicalSlug, ObservedAt: observedAt, ExpiresAt: observedAt.Add(ttl),
		Prices: append([]RawPriceDimension(nil), prices...), Receipt: receipt,
	}
	digest, err := evidence.expectedDigest()
	if err != nil {
		return FreePriceEvidence{}, err
	}
	evidence.Digest = digest
	if err := evidence.ValidateAt(observedAt); err != nil {
		return FreePriceEvidence{}, err
	}
	return evidence, nil
}

func NormalizeZeroPrices(raw map[string]string) ([]RawPriceDimension, error) {
	if len(raw) != len(knownChargeDimensions) {
		return nil, errors.New("launchcontract: OpenRouter pricing shape is invalid")
	}
	prices := make([]RawPriceDimension, 0, len(raw))
	for name, value := range raw {
		if _, ok := knownChargeDimensions[name]; !ok {
			return nil, errors.New("launchcontract: unknown OpenRouter charge dimension")
		}
		if !ExactNumericZero(value) {
			return nil, errors.New("launchcontract: OpenRouter charge dimension is not exact zero")
		}
		prices = append(prices, RawPriceDimension{Name: name, Value: value})
	}
	for name := range knownChargeDimensions {
		if _, ok := raw[name]; !ok {
			return nil, errors.New("launchcontract: OpenRouter charge dimension is unavailable")
		}
	}
	sort.Slice(prices, func(i, j int) bool { return prices[i].Name < prices[j].Name })
	return prices, nil
}

func (e FreePriceEvidence) ValidateAt(now time.Time) error {
	if e.Schema != FreePriceEvidenceSchema || e.ProviderID != ProviderOpenRouter || e.SourceID != OpenRouterCatalogSourceID || e.SourceURL != OpenRouterCatalogSourceURL || e.CanonicalSlug != ModelOxAlpha {
		return errors.New("launchcontract: free-price evidence identity is invalid")
	}
	if !CanonicalTime(now) || !CanonicalTime(e.ObservedAt) || !CanonicalTime(e.ExpiresAt) || e.ObservedAt.After(now) || !e.ExpiresAt.After(now) || !e.ExpiresAt.After(e.ObservedAt) || e.ExpiresAt.Sub(e.ObservedAt) > MaxFreePriceEvidenceTTL {
		return errors.New("launchcontract: free-price evidence time is invalid or expired")
	}
	if e.Receipt.ObservedAt != e.ObservedAt {
		return errors.New("launchcontract: free-price evidence observation time does not match its receipt")
	}
	if err := e.Receipt.ValidateAt(e.ObservedAt); err != nil {
		return err
	}
	if len(e.Prices) != len(knownChargeDimensions) {
		return errors.New("launchcontract: free-price evidence prices are invalid")
	}
	priceMap := make(map[string]string, len(e.Prices))
	prior := ""
	for _, price := range e.Prices {
		if prior != "" && price.Name <= prior {
			return errors.New("launchcontract: free-price evidence prices are not canonical")
		}
		if _, known := knownChargeDimensions[price.Name]; !known || !ExactNumericZero(price.Value) {
			return errors.New("launchcontract: free-price evidence contains a nonzero or unknown charge")
		}
		priceMap[price.Name] = price.Value
		prior = price.Name
	}
	for name := range knownChargeDimensions {
		if _, ok := priceMap[name]; !ok {
			return errors.New("launchcontract: free-price evidence omits a charge dimension")
		}
	}
	digest, err := e.expectedDigest()
	if err != nil || e.Digest != digest {
		return errors.New("launchcontract: free-price evidence digest is invalid")
	}
	return nil
}

func (r CatalogReceipt) ValidateAt(now time.Time) error {
	if r.Schema != CatalogReceiptSchema || r.SourceID != OpenRouterCatalogSourceID || r.SourceURL != OpenRouterCatalogSourceURL || !CanonicalTime(now) || !CanonicalTime(r.ObservedAt) || r.ObservedAt.After(now) || !validDigest(r.ResponseDigest) || !validDigest(r.ModelObjectDigest) {
		return errors.New("launchcontract: catalog receipt identity or time is invalid")
	}
	digest, err := r.expectedDigest()
	if err != nil || r.Digest != digest {
		return errors.New("launchcontract: catalog receipt digest is invalid")
	}
	return nil
}

func (r CatalogReceipt) expectedDigest() (string, error) {
	copy := r
	copy.Digest = ""
	data, err := json.Marshal(copy)
	if err != nil || len(data) == 0 || len(data) > MaxFreePriceEvidenceBytes {
		return "", errors.New("launchcontract: catalog receipt exceeds its bound")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func (e FreePriceEvidence) CanonicalBytes() ([]byte, error) {
	if err := e.ValidateAt(e.ObservedAt); err != nil {
		return nil, err
	}
	data, err := json.Marshal(e)
	if err != nil || len(data) == 0 || len(data) > MaxFreePriceEvidenceBytes {
		return nil, errors.New("launchcontract: free-price evidence exceeds its bound")
	}
	return data, nil
}

func (e FreePriceEvidence) expectedDigest() (string, error) {
	copy := e
	copy.Digest = ""
	data, err := json.Marshal(copy)
	if err != nil || len(data) == 0 || len(data) > MaxFreePriceEvidenceBytes {
		return "", errors.New("launchcontract: free-price evidence exceeds its bound")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func ValidPriceLexeme(value string) bool {
	return len(value) > 0 && len(value) <= MaxCatalogPriceValueBytes && priceNumberPattern.MatchString(value)
}

func ExactNumericZero(value string) bool {
	if !ValidPriceLexeme(value) {
		return false
	}
	mantissa := value
	if index := strings.IndexAny(mantissa, "eE"); index >= 0 {
		mantissa = mantissa[:index]
	}
	mantissa = strings.TrimPrefix(mantissa, "-")
	seen := false
	for _, char := range mantissa {
		if char == '.' {
			continue
		}
		seen = true
		if char != '0' {
			return false
		}
	}
	return seen
}

func CanonicalTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value == value.Round(0)
}
