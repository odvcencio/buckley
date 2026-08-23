package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/workspaceevidence"
)

const OxAlphaOpenRouterModelID = "stealth/ox-alpha"

var ErrOpenRouterOSSOneUseSpent = errors.New("model: openrouter oss one-use client is spent")

// OneUseOSSOpenRouterClient sends at most one non-ZDR patch completion for an
// exact Ox Alpha model and a prompt bound by a one-use OSS blob rule.
type OneUseOSSOpenRouterClient struct {
	self       *OneUseOSSOpenRouterClient
	provider   *OpenRouterProvider
	client     *Client
	rule       *workspaceevidence.OSSBlobRule
	boundRule  *workspaceevidence.OSSBlobRule
	prompt     []byte
	clientSeal [sha256.Size]byte
	used       atomic.Bool
}

func NewOneUseOSSOpenRouterClient(apiKey, baseURL, modelID string, rule *workspaceevidence.OSSBlobRule, prompt []byte) (*OneUseOSSOpenRouterClient, error) {
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
	if rule == nil {
		return nil, fmt.Errorf("openrouter oss client requires an OSS blob rule")
	}
	if len(prompt) == 0 || len(prompt) > workspaceevidence.MaxOSSPromptBlobBytes || !utf8.Valid(prompt) || bytes.IndexByte(prompt, 0) >= 0 {
		return nil, fmt.Errorf("openrouter oss client requires a non-empty UTF-8 prompt")
	}

	client := NewClientWithOptions(apiKey, baseURL, ClientOptions{NetworkLogsEnabled: false})
	governed := &OneUseOSSOpenRouterClient{
		provider:  &OpenRouterProvider{client: client},
		client:    client,
		rule:      rule,
		boundRule: rule,
		prompt:    bytes.Clone(prompt),
	}
	governed.self = governed
	governed.clientSeal = ossOneUseClientSeal(governed)
	return governed, nil
}

func (c *OneUseOSSOpenRouterClient) Close() error {
	if c == nil || c.self != c || c.client == nil {
		return nil
	}
	return c.client.Close()
}

func (c *OneUseOSSOpenRouterClient) CompletePatch(ctx context.Context) (*ChatResponse, error) {
	admitted, err := c.admit(ctx)
	if err != nil {
		return nil, err
	}
	return c.provider.ChatCompletion(ctx, admitted)
}

func (c *OneUseOSSOpenRouterClient) admit(ctx context.Context) (ChatRequest, error) {
	if c == nil || c.self != c || c.provider == nil || c.client == nil || c.provider.client != c.client || c.rule == nil || c.rule != c.boundRule || len(c.prompt) == 0 || !utf8.Valid(c.prompt) || c.clientSeal != ossOneUseClientSeal(c) {
		return ChatRequest{}, fmt.Errorf("openrouter oss client is invalid")
	}
	if c.used.Load() {
		return ChatRequest{}, ErrOpenRouterOSSOneUseSpent
	}

	prompt := bytes.Clone(c.prompt)
	prepared := ChatRequest{
		Model:               OxAlphaOpenRouterModelID,
		Messages:            []Message{{Role: "user", Content: string(prompt)}},
		MaxCompletionTokens: 16384,
		Stream:              false,
		Reasoning:           &ReasoningConfig{Effort: "medium"},
		Provider: map[string]any{
			"allow_fallbacks": false,
			"data_collection": "deny",
			"zdr":             false,
		},
		OpenRouterRetention: OpenRouterRetentionNonZDR,
		RetryMode:           RequestRetrySingleAttempt,
	}
	ruleContext, err := c.rule.ClaimForDispatch(ctx, prompt)
	if err != nil {
		return ChatRequest{}, fmt.Errorf("claim openrouter oss blob rule: %w", err)
	}
	if ruleContext == ([sha256.Size]byte{}) {
		return ChatRequest{}, fmt.Errorf("openrouter oss blob rule returned an empty context binding")
	}
	if !c.used.CompareAndSwap(false, true) {
		return ChatRequest{}, ErrOpenRouterOSSOneUseSpent
	}
	prepared.openRouterContext = ruleContext
	return mintOSSNonZDROpenRouterAdmission(c.provider, prepared)
}

func ossOneUseClientSeal(client *OneUseOSSOpenRouterClient) [sha256.Size]byte {
	if client == nil || client.client == nil {
		return [sha256.Size]byte{}
	}
	hasher := sha256.New()
	writeOpenRouterOSSDigestField(hasher, "domain", []byte("buckley.openrouter.oss-one-use-client.v1"))
	writeOpenRouterOSSDigestField(hasher, "model", []byte(OxAlphaOpenRouterModelID))
	writeOpenRouterOSSDigestField(hasher, "route", []byte(openRouterChatRoute(client.client)))
	credential := openRouterOSSCredentialFingerprint(client.client.apiKey)
	writeOpenRouterOSSDigestField(hasher, "credential-fingerprint", credential[:])
	writeOpenRouterOSSDigestField(hasher, "prompt", client.prompt)
	var seal [sha256.Size]byte
	copy(seal[:], hasher.Sum(nil))
	return seal
}

func mintOSSNonZDROpenRouterAdmission(provider *OpenRouterProvider, req ChatRequest) (ChatRequest, error) {
	if provider == nil || provider.client == nil || provider.client.ossHTTPClient == nil {
		return ChatRequest{}, fmt.Errorf("openrouter oss admission requires an exact provider")
	}
	if req.openRouterContext == ([sha256.Size]byte{}) {
		return ChatRequest{}, fmt.Errorf("openrouter oss admission requires a rule context binding")
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
		contextBinding:        req.openRouterContext,
		wireDigest:            openRouterOSSFinalWireDigest(req.openRouterContext, "openrouter", req.Model, route, false, headerRecord, credentialFingerprint, body),
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
