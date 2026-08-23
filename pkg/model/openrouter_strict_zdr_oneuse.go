package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
)

var ErrOpenRouterStrictZDROneUseSpent = errors.New("model: openrouter strict-zdr one-use client is spent")

// OneUseStrictZDROpenRouterClient sends at most one exact, final-wire-bound
// completion to the official OpenRouter endpoint. It cannot mint non-ZDR
// authority and deliberately does not implement streaming or continuation.
type OneUseStrictZDROpenRouterClient struct {
	self           *OneUseStrictZDROpenRouterClient
	provider       *OpenRouterProvider
	client         *Client
	httpClient     *http.Client
	modelID        string
	contextBinding [sha256.Size]byte
	seal           [sha256.Size]byte
	used           atomic.Bool
}

// NewOneUseStrictZDROpenRouterClient constructs a direct OpenRouter client for
// one strict-ZDR request. contextBinding must identify the immutable host
// snapshot used to build the request prompt.
func NewOneUseStrictZDROpenRouterClient(apiKey, baseURL, modelID string, contextBinding [sha256.Size]byte) (*OneUseStrictZDROpenRouterClient, error) {
	if apiKey == "" || strings.TrimSpace(apiKey) != apiKey || strings.ContainsAny(apiKey, "\x00\r\n") {
		return nil, fmt.Errorf("openrouter strict-zdr client requires a canonical credential")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if baseURL != defaultBaseURL {
		return nil, fmt.Errorf("openrouter strict-zdr client requires the official API endpoint")
	}
	client := NewClientWithOptions(apiKey, baseURL, ClientOptions{NetworkLogsEnabled: false})
	provider := &OpenRouterProvider{client: client}
	governed, err := newOneUseStrictZDROpenRouterClient(provider, modelID, contextBinding)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return governed, nil
}

func newOneUseStrictZDROpenRouterClient(provider *OpenRouterProvider, modelID string, contextBinding [sha256.Size]byte) (*OneUseStrictZDROpenRouterClient, error) {
	if provider == nil || provider.client == nil || provider.client.ossHTTPClient == nil {
		return nil, fmt.Errorf("openrouter strict-zdr client requires an exact provider")
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" || strings.ContainsAny(modelID, "\x00\r\n\t ") || !strings.Contains(modelID, "/") || normalizeModelForProvider(modelID, "openrouter") != modelID {
		return nil, fmt.Errorf("openrouter strict-zdr client requires a canonical OpenRouter model ID")
	}
	if contextBinding == ([sha256.Size]byte{}) {
		return nil, fmt.Errorf("openrouter strict-zdr client requires an immutable context binding")
	}
	client := &OneUseStrictZDROpenRouterClient{
		provider:       provider,
		client:         provider.client,
		httpClient:     provider.client.ossHTTPClient,
		modelID:        modelID,
		contextBinding: contextBinding,
	}
	client.self = client
	client.seal = strictZDROneUseClientSeal(client)
	return client, nil
}

func (c *OneUseStrictZDROpenRouterClient) Close() error {
	if c == nil || c.self != c || c.client == nil {
		return nil
	}
	return c.client.Close()
}

func (c *OneUseStrictZDROpenRouterClient) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	admitted, err := c.admit(req)
	if err != nil {
		return nil, err
	}
	if !c.used.CompareAndSwap(false, true) {
		return nil, ErrOpenRouterStrictZDROneUseSpent
	}
	return c.provider.ChatCompletion(ctx, admitted)
}

func (c *OneUseStrictZDROpenRouterClient) admit(req ChatRequest) (ChatRequest, error) {
	if c == nil || c.self != c || c.provider == nil || c.client == nil || c.httpClient == nil || c.provider.client != c.client || c.client.ossHTTPClient != c.httpClient || c.contextBinding == ([sha256.Size]byte{}) || c.seal != strictZDROneUseClientSeal(c) {
		return ChatRequest{}, fmt.Errorf("openrouter strict-zdr client is invalid")
	}
	if c.used.Load() {
		return ChatRequest{}, ErrOpenRouterStrictZDROneUseSpent
	}
	if req.Model != c.modelID || strings.TrimSpace(req.Model) != req.Model {
		return ChatRequest{}, fmt.Errorf("openrouter strict-zdr request requires the exact bound model")
	}
	if req.Stream || len(req.Models) != 0 || len(req.Provider) != 0 || len(req.Transforms) != 0 {
		return ChatRequest{}, fmt.Errorf("openrouter strict-zdr request contains caller-controlled routing")
	}
	if req.OpenRouterRetention != OpenRouterRetentionUnspecified || req.RetryMode != RequestRetryDefault || req.openRouterAdmission != nil || req.openRouterContext != ([sha256.Size]byte{}) {
		return ChatRequest{}, fmt.Errorf("openrouter strict-zdr request contains caller-controlled authority")
	}
	if req.PromptCache != nil || req.CacheControl != nil || req.PromptCacheKey != "" || req.PromptCacheRetention != "" {
		return ChatRequest{}, fmt.Errorf("openrouter strict-zdr request contains prompt caching")
	}
	if req.ReviewSnapshot != nil {
		return ChatRequest{}, fmt.Errorf("openrouter strict-zdr request contains a foreign snapshot")
	}

	// JSON round-tripping makes every serialized map, slice, pointer, and
	// interface value private to this one-use request before it is digested.
	serialized, err := json.Marshal(req)
	if err != nil {
		return ChatRequest{}, fmt.Errorf("clone openrouter strict-zdr request: %w", err)
	}
	type requestClone ChatRequest
	type messageClone Message
	var cloned struct {
		requestClone
		Messages []messageClone `json:"messages"`
	}
	decoder := json.NewDecoder(bytes.NewReader(serialized))
	decoder.UseNumber()
	if err := decoder.Decode(&cloned); err != nil {
		return ChatRequest{}, fmt.Errorf("clone openrouter strict-zdr request: %w", err)
	}
	prepared := ChatRequest(cloned.requestClone)
	if cloned.Messages != nil {
		prepared.Messages = make([]Message, len(cloned.Messages))
		for i := range cloned.Messages {
			prepared.Messages[i] = Message(cloned.Messages[i])
		}
	}
	prepared.Model = c.modelID
	prepared.Stream = false
	prepared.Models = nil
	prepared.Provider = map[string]any{
		"allow_fallbacks": false,
		"zdr":             true,
	}
	prepared.OpenRouterRetention = OpenRouterRetentionZDR
	prepared.RetryMode = RequestRetrySingleAttempt
	prepared.openRouterContext = c.contextBinding
	return mintStrictZDROpenRouterAdmission(c.provider, prepared)
}

func strictZDROneUseClientSeal(client *OneUseStrictZDROpenRouterClient) [sha256.Size]byte {
	if client == nil || client.client == nil {
		return [sha256.Size]byte{}
	}
	hasher := sha256.New()
	writeOpenRouterOSSDigestField(hasher, "domain", []byte("buckley.openrouter.strict-zdr-one-use-client.v1"))
	writeOpenRouterOSSDigestField(hasher, "model", []byte(client.modelID))
	writeOpenRouterOSSDigestField(hasher, "route", []byte(openRouterChatRoute(client.client)))
	credential := openRouterOSSCredentialFingerprint(client.client.apiKey)
	writeOpenRouterOSSDigestField(hasher, "credential-fingerprint", credential[:])
	writeOpenRouterOSSDigestField(hasher, "context-binding", client.contextBinding[:])
	var seal [sha256.Size]byte
	copy(seal[:], hasher.Sum(nil))
	return seal
}

func mintStrictZDROpenRouterAdmission(provider *OpenRouterProvider, req ChatRequest) (ChatRequest, error) {
	if provider == nil || provider.client == nil || provider.client.ossHTTPClient == nil {
		return ChatRequest{}, fmt.Errorf("openrouter strict-zdr admission requires an exact provider")
	}
	client := provider.client
	body, err := json.Marshal(req)
	if err != nil {
		return ChatRequest{}, fmt.Errorf("marshal openrouter strict-zdr request: %w", err)
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
		policy:                openRouterAdmissionPolicyStrictZDR,
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
		wireDigest:            openRouterAdmissionFinalWireDigest(openRouterAdmissionPolicyStrictZDR, req.openRouterContext, "openrouter", req.Model, route, false, headerRecord, credentialFingerprint, body),
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
		return ChatRequest{}, fmt.Errorf("%w: strict-zdr final wire changed during admission", errOpenRouterOSSAdmissionInvalid)
	}
	return req, nil
}

func exactOpenRouterAdmissionHeaders(apiKey string, stream bool) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+apiKey)
	headers.Set("Content-Type", openRouterOSSContentType)
	headers.Set("HTTP-Referer", openRouterOSSHTTPReferer)
	headers.Set("User-Agent", openRouterOSSUserAgent)
	headers.Set("X-OpenRouter-Experimental-Metadata", openRouterOSSMetadataEnabled)
	headers.Set("X-OpenRouter-Metadata", openRouterOSSMetadataEnabled)
	headers.Set("X-Title", openRouterOSSTitle)
	if stream {
		headers.Set("Accept", openRouterOSSAccept)
	}
	return headers
}
