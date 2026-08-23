package launchcontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"
)

const (
	ProfileSchema                      = "buckley.launch.profile.v1"
	ProviderOpenRouter                 = "openrouter"
	ModelOxAlpha                       = "stealth/ox-alpha"
	ReasoningMax                       = "max"
	RetentionNonZDR                    = "non_zdr"
	DataCollectionDeny                 = "deny"
	RetryOwnerDapr                     = "dapr"
	CatalogSourceOpenRouter            = "openrouter.models.v1"
	CatalogTTL                         = 5 * time.Minute
	WorkspaceEvidenceSchema            = "buckley.workspace-preflight.v1"
	PricePolicyFreeOnly                = "free_only"
	StateAdmissionPending              = "admission_pending"
	GlobalCapacity                     = 2
	PerRunParallelism                  = 2
	ProviderPostAttempts               = 1
	ManagerAffordabilityAttempts       = 1
	MaxOutputPerRequest          int64 = 32_768
	MaxProfileBytes                    = 16 << 10
)

type PrivacyContract struct {
	RetentionMode     string `json:"retention_mode"`
	ZDR               bool   `json:"zdr"`
	DataCollection    string `json:"data_collection"`
	AllowFallbacks    bool   `json:"allow_fallbacks"`
	RequireParameters bool   `json:"require_parameters"`
}

type LicenseRequirement struct {
	Required       bool     `json:"required"`
	RootOnly       bool     `json:"root_only"`
	EvidenceSchema string   `json:"evidence_schema"`
	AllowedIDs     []string `json:"allowed_ids"`
}

type ProfileLimits struct {
	ModelRequests        int   `json:"model_requests"`
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	TotalTokens          int64 `json:"total_tokens"`
	MaxOutputPerRequest  int64 `json:"max_output_per_request"`
	RequestTimeoutMS     int64 `json:"request_timeout_ms"`
	TurnTimeoutMS        int64 `json:"turn_timeout_ms"`
	AbsoluteRunTimeoutMS int64 `json:"absolute_run_timeout_ms"`
	GlobalCapacity       int   `json:"global_capacity"`
	PerRunParallelism    int   `json:"per_run_parallelism"`
}

type MaxPrice struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
	Request    string `json:"request"`
	Image      string `json:"image"`
}

type PriceGuard struct {
	Policy          string   `json:"policy"`
	CatalogSourceID string   `json:"catalog_source_id"`
	CatalogTTLMS    int64    `json:"catalog_ttl_ms"`
	MaxPrice        MaxPrice `json:"max_price"`
}

// ProfileDescriptor is the dependency-free immutable launch contract. It
// contains values only: observation, persistence, and provider calls belong
// to adapters outside this package.
type ProfileDescriptor struct {
	Schema                       string             `json:"schema"`
	ID                           string             `json:"id"`
	Provider                     string             `json:"provider"`
	Model                        string             `json:"model"`
	ReasoningEffort              string             `json:"reasoning_effort"`
	Privacy                      PrivacyContract    `json:"privacy"`
	License                      LicenseRequirement `json:"license"`
	Limits                       ProfileLimits      `json:"limits"`
	PriceGuard                   PriceGuard         `json:"price_guard"`
	ProviderPostAttempts         int                `json:"provider_post_attempts"`
	ManagerAffordabilityAttempts int                `json:"manager_affordability_attempts"`
	RetryOwner                   string             `json:"retry_owner"`
	Enforced                     bool               `json:"enforced"`
	State                        string             `json:"state"`
}

func ResolveProfile(id string) (ProfileDescriptor, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	var limits ProfileLimits
	switch id {
	case "gsxmail":
		limits = profileLimits(12, 6_000_000, 393_216, 6_393_216, 15*time.Minute, 30*time.Minute, 90*time.Minute)
	case "gosx", "tqwebp":
		limits = profileLimits(24, 12_000_000, 786_432, 12_786_432, 20*time.Minute, 45*time.Minute, 4*time.Hour)
	default:
		return ProfileDescriptor{}, errors.New("launchcontract: unknown launch profile")
	}
	return ProfileDescriptor{
		Schema: ProfileSchema, ID: id, Provider: ProviderOpenRouter,
		Model: ModelOxAlpha, ReasoningEffort: ReasoningMax,
		Privacy: PrivacyContract{
			RetentionMode: RetentionNonZDR, ZDR: false,
			DataCollection: DataCollectionDeny, AllowFallbacks: false, RequireParameters: true,
		},
		License: LicenseRequirement{
			Required: true, RootOnly: true, EvidenceSchema: WorkspaceEvidenceSchema,
			AllowedIDs: []string{"Apache-2.0", "MIT"},
		},
		Limits: limits,
		PriceGuard: PriceGuard{
			Policy: PricePolicyFreeOnly, CatalogSourceID: CatalogSourceOpenRouter,
			CatalogTTLMS: CatalogTTL.Milliseconds(),
			MaxPrice:     MaxPrice{Prompt: "0", Completion: "0", Request: "0", Image: "0"},
		},
		ProviderPostAttempts:         ProviderPostAttempts,
		ManagerAffordabilityAttempts: ManagerAffordabilityAttempts,
		RetryOwner:                   RetryOwnerDapr, Enforced: false, State: StateAdmissionPending,
	}, nil
}

func profileLimits(requests int, input, output, total int64, request, turn, run time.Duration) ProfileLimits {
	return ProfileLimits{
		ModelRequests: requests, InputTokens: input, OutputTokens: output, TotalTokens: total,
		MaxOutputPerRequest: MaxOutputPerRequest, RequestTimeoutMS: request.Milliseconds(),
		TurnTimeoutMS: turn.Milliseconds(), AbsoluteRunTimeoutMS: run.Milliseconds(),
		GlobalCapacity: GlobalCapacity, PerRunParallelism: PerRunParallelism,
	}
}

func (d ProfileDescriptor) Validate() error {
	want, err := ResolveProfile(d.ID)
	if err != nil || !reflect.DeepEqual(d, want) {
		return errors.New("launchcontract: launch profile descriptor is not canonical")
	}
	return nil
}

func (d ProfileDescriptor) CanonicalBytes() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(d)
	if err != nil || len(data) == 0 || len(data) > MaxProfileBytes {
		return nil, errors.New("launchcontract: launch profile descriptor exceeds its bound")
	}
	return data, nil
}

func (d ProfileDescriptor) Digest() (string, error) {
	data, err := d.CanonicalBytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func DecodeProfile(data []byte) (ProfileDescriptor, error) {
	if len(data) == 0 || len(data) > MaxProfileBytes {
		return ProfileDescriptor{}, errors.New("launchcontract: launch profile descriptor exceeds its bound")
	}
	if err := RejectDuplicateJSONKeys(data); err != nil {
		return ProfileDescriptor{}, err
	}
	if err := validateKeys(data); err != nil {
		return ProfileDescriptor{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var descriptor ProfileDescriptor
	if err := decoder.Decode(&descriptor); err != nil {
		return ProfileDescriptor{}, errors.New("launchcontract: launch profile descriptor is invalid")
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return ProfileDescriptor{}, err
	}
	if err := descriptor.Validate(); err != nil {
		return ProfileDescriptor{}, err
	}
	return descriptor, nil
}

// RejectDuplicateJSONKeys performs a bounded caller-owned JSON scan and
// rejects duplicate object keys at every nesting level.
func RejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok {
					return errors.New("launchcontract: launch profile descriptor is invalid")
				}
				if _, exists := seen[key]; exists {
					return errors.New("launchcontract: launch profile descriptor contains duplicate fields")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("launchcontract: launch profile descriptor is invalid")
		}
	}
	if err := walk(); err != nil {
		return errors.New("launchcontract: launch profile descriptor is invalid")
	}
	return nil
}

func validateKeys(data []byte) error {
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil || !exactKeys(top, "schema", "id", "provider", "model", "reasoning_effort", "privacy", "license", "limits", "price_guard", "provider_post_attempts", "manager_affordability_attempts", "retry_owner", "enforced", "state") {
		return errors.New("launchcontract: launch profile descriptor fields are invalid")
	}
	for key, expected := range map[string][]string{
		"privacy":     {"retention_mode", "zdr", "data_collection", "allow_fallbacks", "require_parameters"},
		"license":     {"required", "root_only", "evidence_schema", "allowed_ids"},
		"limits":      {"model_requests", "input_tokens", "output_tokens", "total_tokens", "max_output_per_request", "request_timeout_ms", "turn_timeout_ms", "absolute_run_timeout_ms", "global_capacity", "per_run_parallelism"},
		"price_guard": {"policy", "catalog_source_id", "catalog_ttl_ms", "max_price"},
	} {
		var object map[string]json.RawMessage
		if json.Unmarshal(top[key], &object) != nil || !exactKeys(object, expected...) {
			return errors.New("launchcontract: launch profile descriptor fields are invalid")
		}
	}
	var guard map[string]json.RawMessage
	if json.Unmarshal(top["price_guard"], &guard) != nil {
		return errors.New("launchcontract: launch profile descriptor fields are invalid")
	}
	var maxPrice map[string]json.RawMessage
	if json.Unmarshal(guard["max_price"], &maxPrice) != nil || !exactKeys(maxPrice, "prompt", "completion", "request", "image") {
		return errors.New("launchcontract: launch profile descriptor fields are invalid")
	}
	return nil
}

func exactKeys(values map[string]json.RawMessage, expected ...string) bool {
	if len(values) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, ok := values[key]; !ok {
			return false
		}
	}
	return true
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	}
	return errors.New("launchcontract: launch profile descriptor has trailing data")
}
