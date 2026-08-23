package model

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrOpenRouterOSSAdmissionRequired = errors.New("model: openrouter non-zdr requests require host-minted oss admission")
	ErrOpenRouterRetentionUnspecified = errors.New("model: openrouter retention mode is unspecified")
	ErrOpenRouterPrivacyContract      = errors.New("model: invalid openrouter privacy contract")
	ErrUnsupportedRequestRetryMode    = errors.New("model: unsupported openrouter request retry mode")
)

// OpenRouterPrivacyFallback is retained so existing configuration remains
// parseable. The Manager no longer performs a ZDR-to-non-ZDR retry; this value
// cannot authorize dispatch without a host-minted OSS admission capability.
type OpenRouterPrivacyFallback string

const (
	OpenRouterPrivacyFallbackNone                  OpenRouterPrivacyFallback = ""
	OpenRouterPrivacyFallbackZDRThenDataCollection OpenRouterPrivacyFallback = "zdr_then_data_collection_deny"
)

// ParseOpenRouterPrivacyFallback normalizes the user-facing configuration
// value and rejects unknown policies instead of silently weakening privacy.
func ParseOpenRouterPrivacyFallback(value string) (OpenRouterPrivacyFallback, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none", "off", "disabled":
		return OpenRouterPrivacyFallbackNone, nil
	case string(OpenRouterPrivacyFallbackZDRThenDataCollection):
		return OpenRouterPrivacyFallbackZDRThenDataCollection, nil
	default:
		return OpenRouterPrivacyFallbackNone, fmt.Errorf("unsupported OpenRouter privacy fallback %q", value)
	}
}

func validateOpenRouterRetryMode(mode RequestRetryMode) error {
	switch mode {
	case RequestRetryDefault, RequestRetrySingleAttempt:
		return nil
	default:
		return fmt.Errorf("%w %q", ErrUnsupportedRequestRetryMode, mode)
	}
}

func validateModelDispatch(req ChatRequest, providerID string) error {
	if providerID != "openrouter" {
		return nil
	}
	if err := validateOpenRouterRetryMode(req.RetryMode); err != nil {
		return err
	}
	return validateOpenRouterPrivacy(req)
}

func normalizeOpenRouterStrictZDRRoute(req ChatRequest, providerID string) ChatRequest {
	if providerID != "openrouter" || !openRouterStrictZDR(req) {
		return req
	}
	req.Models = nil
	req.Provider = cloneAnyMap(req.Provider)
	req.Provider["allow_fallbacks"] = false
	return req
}

func openRouterStrictZDR(req ChatRequest) bool {
	raw, ok := req.Provider["zdr"]
	zdr, valid := raw.(bool)
	return ok && valid && zdr
}

// validateOpenRouterPrivacy is the shared pre-provider boundary. Strict ZDR
// is the only currently dispatchable OpenRouter posture. Non-ZDR admission is
// represented by an opaque request capability, but this change deliberately
// provides no way to mint it.
func validateOpenRouterPrivacy(req ChatRequest) error {
	switch req.OpenRouterRetention {
	case OpenRouterRetentionUnspecified, OpenRouterRetentionZDR, OpenRouterRetentionNonZDR:
	default:
		return fmt.Errorf("%w: retention mode %q", ErrOpenRouterPrivacyContract, req.OpenRouterRetention)
	}

	var (
		zdr    bool
		hasZDR bool
	)
	if raw, ok := req.Provider["zdr"]; ok {
		hasZDR = true
		var valid bool
		zdr, valid = raw.(bool)
		if !valid {
			return fmt.Errorf("%w: zdr must be boolean", ErrOpenRouterPrivacyContract)
		}
	}

	collection := ""
	hasCollection := false
	if raw, ok := req.Provider["data_collection"]; ok {
		hasCollection = true
		var valid bool
		collection, valid = raw.(string)
		if !valid || collection != "deny" {
			return fmt.Errorf("%w: data_collection must be deny", ErrOpenRouterPrivacyContract)
		}
	}

	if zdr {
		if hasCollection || req.OpenRouterRetention == OpenRouterRetentionNonZDR {
			return fmt.Errorf("%w: strict zdr conflicts with non-zdr policy", ErrOpenRouterPrivacyContract)
		}
		allowFallbacks, exact := req.Provider["allow_fallbacks"].(bool)
		if !exact || allowFallbacks || len(req.Models) != 0 {
			return fmt.Errorf("%w: strict zdr requires one exact no-fallback route", ErrOpenRouterPrivacyContract)
		}
		return nil
	}
	if req.OpenRouterRetention == OpenRouterRetentionZDR {
		return fmt.Errorf("%w: strict zdr requires provider zdr=true", ErrOpenRouterPrivacyContract)
	}

	explicitNonZDR := req.OpenRouterRetention == OpenRouterRetentionNonZDR || hasZDR || hasCollection
	if !explicitNonZDR {
		return ErrOpenRouterRetentionUnspecified
	}
	if req.openRouterAdmission == nil {
		return ErrOpenRouterOSSAdmissionRequired
	}
	if req.OpenRouterRetention != OpenRouterRetentionNonZDR || !hasZDR || zdr || !hasCollection || collection != "deny" {
		return fmt.Errorf("%w: admitted non-zdr requests require explicit zdr=false and data_collection=deny", ErrOpenRouterPrivacyContract)
	}
	return nil
}

func streamErrorChannels(err error) (<-chan StreamChunk, <-chan error) {
	chunks := make(chan StreamChunk)
	close(chunks)
	errs := make(chan error, 1)
	errs <- err
	close(errs)
	return chunks, errs
}
