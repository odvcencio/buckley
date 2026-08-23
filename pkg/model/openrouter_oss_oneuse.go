package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"m31labs.dev/buckley/pkg/workspaceevidence"
)

const OxAlphaOpenRouterModelID = "stealth/ox-alpha"

var ErrOpenRouterOSSOneUseSpent = errors.New("model: openrouter oss one-use client is spent")

// OneUseOSSOpenRouterClient sends at most one non-ZDR completion for an exact
// Ox Alpha model after revalidating recognized root-license evidence.
type OneUseOSSOpenRouterClient struct {
	self     *OneUseOSSOpenRouterClient
	provider *OpenRouterProvider
	client   *Client
	evidence workspaceevidence.RootLicenseBlobEvidence
	used     atomic.Bool
}

func NewOneUseOSSOpenRouterClient(ctx context.Context, apiKey, baseURL, modelID string, evidence workspaceevidence.RootLicenseBlobEvidence) (*OneUseOSSOpenRouterClient, error) {
	if apiKey == "" || strings.TrimSpace(apiKey) != apiKey || strings.ContainsAny(apiKey, "\x00\r\n") {
		return nil, fmt.Errorf("openrouter oss client requires a canonical credential")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if baseURL != defaultBaseURL {
		return nil, fmt.Errorf("openrouter oss client requires the official API endpoint")
	}
	if modelID != OxAlphaOpenRouterModelID {
		return nil, fmt.Errorf("openrouter oss client only allows %s", OxAlphaOpenRouterModelID)
	}
	if !recognizedOSSRootLicense(evidence.DetectedSPDXHint()) {
		return nil, fmt.Errorf("openrouter oss client requires a recognized root OSS license")
	}
	if err := evidence.Revalidate(ctx); err != nil {
		return nil, fmt.Errorf("revalidate root OSS license: %w", err)
	}

	client := NewClientWithOptions(apiKey, baseURL, ClientOptions{NetworkLogsEnabled: false})
	governed := &OneUseOSSOpenRouterClient{
		provider: &OpenRouterProvider{client: client},
		client:   client,
		evidence: evidence,
	}
	governed.self = governed
	return governed, nil
}

func recognizedOSSRootLicense(spdx string) bool {
	switch spdx {
	case "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC", "MIT", "MPL-2.0":
		return true
	default:
		return false
	}
}

func (c *OneUseOSSOpenRouterClient) Close() error {
	if c == nil || c.self != c || c.client == nil {
		return nil
	}
	return c.client.Close()
}

func (c *OneUseOSSOpenRouterClient) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if c == nil || c.self != c || c.provider == nil || c.client == nil || c.provider.client != c.client {
		return nil, fmt.Errorf("openrouter oss client is invalid")
	}
	if c.used.Load() {
		return nil, ErrOpenRouterOSSOneUseSpent
	}
	if req.Model != OxAlphaOpenRouterModelID || strings.TrimSpace(req.Model) != req.Model {
		return nil, fmt.Errorf("openrouter oss request requires the exact Ox Alpha model")
	}
	if req.Stream || len(req.Models) != 0 || len(req.Provider) != 0 || len(req.Transforms) != 0 {
		return nil, fmt.Errorf("openrouter oss request contains caller-controlled routing")
	}
	if req.OpenRouterRetention != OpenRouterRetentionUnspecified || req.RetryMode != RequestRetryDefault || req.openRouterAdmission != nil || req.openRouterContext != ([32]byte{}) {
		return nil, fmt.Errorf("openrouter oss request contains caller-controlled authority")
	}
	if req.PromptCache != nil || req.CacheControl != nil || req.PromptCacheKey != "" || req.PromptCacheRetention != "" {
		return nil, fmt.Errorf("openrouter oss request contains prompt caching")
	}
	if req.ReviewSnapshot != nil || len(req.Tools) != 0 || req.ToolChoice != "" || req.ParallelToolCalls != nil {
		return nil, fmt.Errorf("openrouter oss request must be patch-text only")
	}
	if !recognizedOSSRootLicense(c.evidence.DetectedSPDXHint()) {
		return nil, fmt.Errorf("openrouter oss client lost recognized root-license evidence")
	}
	if err := c.evidence.Revalidate(ctx); err != nil {
		return nil, fmt.Errorf("revalidate root OSS license before dispatch: %w", err)
	}
	if !c.used.CompareAndSwap(false, true) {
		return nil, ErrOpenRouterOSSOneUseSpent
	}

	serialized, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("clone openrouter oss request: %w", err)
	}
	var prepared ChatRequest
	if err := json.Unmarshal(serialized, &prepared); err != nil {
		return nil, fmt.Errorf("clone openrouter oss request: %w", err)
	}
	prepared.Model = OxAlphaOpenRouterModelID
	prepared.Stream = false
	prepared.Models = nil
	prepared.Provider = map[string]any{
		"allow_fallbacks": false,
		"data_collection": "deny",
		"zdr":             false,
	}
	prepared.OpenRouterRetention = OpenRouterRetentionNonZDR
	prepared.RetryMode = RequestRetrySingleAttempt

	admitted, err := mintOSSNonZDROpenRouterAdmission(c.provider, prepared)
	if err != nil {
		return nil, err
	}
	return c.provider.ChatCompletion(ctx, admitted)
}

func mintOSSNonZDROpenRouterAdmission(provider *OpenRouterProvider, req ChatRequest) (ChatRequest, error) {
	if provider == nil || provider.client == nil || provider.client.ossHTTPClient == nil {
		return ChatRequest{}, fmt.Errorf("openrouter oss admission requires an exact provider")
	}
	client := provider.client
	body, err := json.Marshal(req)
	if err != nil {
		return ChatRequest{}, fmt.Errorf("marshal openrouter oss request: %w", err)
	}
	if err := rejectOpenRouterOSSCacheKeys(body); err != nil {
		return ChatRequest{}, err
	}
	headers := exactOpenRouterAdmissionHeaders(client.apiKey, false)
	headerRecord, err := canonicalOpenRouterOSSHeaderRecord(headers, client.apiKey, false)
	if err != nil {
		return ChatRequest{}, err
	}
	credentialFingerprint := openRouterOSSCredentialFingerprint(client.apiKey)
	route := openRouterChatRoute(client)
	inFlight := &atomic.Bool{}
	consumed := &atomic.Bool{}
	admission := &openRouterOSSAdmission{
		policy:                openRouterAdmissionPolicyOSSNonZDR,
		provider:              provider,
		client:                client,
		httpClient:            client.ossHTTPClient,
		model:                 req.Model,
		route:                 route,
		stream:                false,
		headers:               headers,
		headerRecord:          headerRecord,
		credentialFingerprint: credentialFingerprint,
		wireDigest:            openRouterOSSFinalWireDigest("openrouter", req.Model, route, false, headerRecord, credentialFingerprint, body),
		inFlight:              inFlight,
		consumed:              consumed,
	}
	admission.self = admission
	req.openRouterAdmission = admission
	validated, err := marshalOpenRouterOSSFinalWire(req)
	if err != nil {
		return ChatRequest{}, err
	}
	if !bytes.Equal(body, validated) {
		return ChatRequest{}, fmt.Errorf("%w: oss final wire changed during admission", errOpenRouterOSSAdmissionInvalid)
	}
	return req, nil
}
