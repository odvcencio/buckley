package model

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
)

const (
	openRouterOSSAdmissionDigestDomain    = "buckley.openrouter.oss-admission.final-wire.v3"
	openRouterOSSCredentialDigestDomain   = "buckley.openrouter.oss-admission.credential.v1"
	openRouterOSSHeaderRecordDomain       = "buckley.openrouter.oss-admission.headers.v1"
	openRouterOSSContentType              = "application/json"
	openRouterOSSAccept                   = "text/event-stream"
	openRouterOSSHTTPReferer              = "https://github.com/odvcencio/buckley"
	openRouterOSSTitle                    = "Buckley"
	openRouterOSSUserAgent                = "Buckley"
	openRouterOSSMetadataEnabled          = "enabled"
	openRouterOSSAuthorizationRecordValue = "Bearer"
)

var (
	errOpenRouterOSSAdmissionInvalid  = errors.New("model: invalid openrouter oss admission")
	errOpenRouterOSSAdmissionInFlight = errors.New("model: openrouter oss admission is already in flight")
	errOpenRouterOSSAdmissionSpent    = errors.New("model: openrouter oss admission is spent")
)

type openRouterAdmissionPolicy uint8

const (
	openRouterAdmissionPolicyInvalid openRouterAdmissionPolicy = iota
	openRouterAdmissionPolicyOSSNonZDR
	openRouterAdmissionPolicyStrictZDR
)

// openRouterOSSAdmission is an opaque, process-local, one-use capability. The
// non-ZDR path mints it only after a host-formed OSS blob rule has been claimed;
// both policies bind the exact final request and a nonzero runtime context.
type openRouterOSSAdmission struct {
	self                  *openRouterOSSAdmission
	policy                openRouterAdmissionPolicy
	provider              *OpenRouterProvider
	client                *Client
	httpClient            *http.Client
	model                 string
	route                 string
	stream                bool
	headers               http.Header
	headerRecord          []byte
	credentialFingerprint [sha256.Size]byte
	contextBinding        [sha256.Size]byte
	wireDigest            [sha256.Size]byte
	inFlight              *atomic.Bool
	consumed              *atomic.Bool
}

type openRouterOSSHeaderSpec struct {
	name    string
	present bool
	value   string
	secret  bool
}

func rejectOpenRouterRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func openRouterChatRoute(client *Client) string {
	if client == nil {
		return ""
	}
	return client.baseURL + "/chat/completions"
}

func openRouterOSSHeaderSpecs(stream bool) []openRouterOSSHeaderSpec {
	specs := []openRouterOSSHeaderSpec{
		{name: "Accept", present: stream, value: openRouterOSSAccept},
		{name: "Authorization", present: true, value: openRouterOSSAuthorizationRecordValue, secret: true},
		{name: "Content-Type", present: true, value: openRouterOSSContentType},
		{name: "Http-Referer", present: true, value: openRouterOSSHTTPReferer},
		{name: "Idempotency-Key"},
		{name: "User-Agent", present: true, value: openRouterOSSUserAgent},
		{name: "X-Idempotency-Key"},
		{name: "X-Openrouter-Experimental-Metadata", present: true, value: openRouterOSSMetadataEnabled},
		{name: "X-Openrouter-Metadata", present: true, value: openRouterOSSMetadataEnabled},
		{name: "X-Title", present: true, value: openRouterOSSTitle},
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].name < specs[j].name })
	return specs
}

func canonicalOpenRouterOSSHeaderRecord(headers http.Header, apiKey string, stream bool) ([]byte, error) {
	if headers == nil {
		return nil, fmt.Errorf("%w: committed headers are unavailable", errOpenRouterOSSAdmissionInvalid)
	}
	specs := openRouterOSSHeaderSpecs(stream)
	allowed := make(map[string]openRouterOSSHeaderSpec, len(specs))
	for _, spec := range specs {
		allowed[spec.name] = spec
	}
	for rawName := range headers {
		canonicalName := http.CanonicalHeaderKey(rawName)
		spec, ok := allowed[canonicalName]
		if !ok || rawName != canonicalName || !spec.present {
			return nil, fmt.Errorf("%w: unexpected admitted request header %q", errOpenRouterOSSAdmissionInvalid, rawName)
		}
	}

	var record bytes.Buffer
	writeOpenRouterOSSRecordValue(&record, []byte(openRouterOSSHeaderRecordDomain))
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(specs)))
	record.Write(count[:])
	for _, spec := range specs {
		values, present := headers[spec.name]
		if present != spec.present {
			return nil, fmt.Errorf("%w: admitted request header %q presence mismatch", errOpenRouterOSSAdmissionInvalid, spec.name)
		}
		recordValue := ""
		if present {
			if len(values) != 1 {
				return nil, fmt.Errorf("%w: admitted request header %q must have one value", errOpenRouterOSSAdmissionInvalid, spec.name)
			}
			expected := spec.value
			if spec.secret {
				expected = "Bearer " + apiKey
				if subtle.ConstantTimeCompare([]byte(values[0]), []byte(expected)) != 1 {
					return nil, fmt.Errorf("%w: admitted authorization does not match the bound credential", errOpenRouterOSSAdmissionInvalid)
				}
				recordValue = spec.value
			} else {
				if values[0] != expected {
					return nil, fmt.Errorf("%w: admitted request header %q value mismatch", errOpenRouterOSSAdmissionInvalid, spec.name)
				}
				recordValue = values[0]
			}
		}
		writeOpenRouterOSSRecordValue(&record, []byte(spec.name))
		if present {
			record.WriteByte(1)
		} else {
			record.WriteByte(0)
		}
		writeOpenRouterOSSRecordValue(&record, []byte(recordValue))
	}
	return record.Bytes(), nil
}

func writeOpenRouterOSSRecordValue(record *bytes.Buffer, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	record.Write(size[:])
	record.Write(value)
}

func openRouterOSSCredentialFingerprint(apiKey string) [sha256.Size]byte {
	hasher := sha256.New()
	writeOpenRouterOSSDigestField(hasher, "domain", []byte(openRouterOSSCredentialDigestDomain))
	writeOpenRouterOSSDigestField(hasher, "credential", []byte(apiKey))
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], hasher.Sum(nil))
	return fingerprint
}

func validateOpenRouterOSSRequestPolicy(req ChatRequest) error {
	admission := req.openRouterAdmission
	if admission == nil {
		return nil
	}
	if admission.self != admission || admission.inFlight == nil || admission.consumed == nil {
		return fmt.Errorf("%w: invalid self binding", errOpenRouterOSSAdmissionInvalid)
	}
	if admission.consumed.Load() {
		return errOpenRouterOSSAdmissionSpent
	}
	if req.RetryMode != RequestRetrySingleAttempt {
		return fmt.Errorf("%w: single-attempt retry mode is required", errOpenRouterOSSAdmissionInvalid)
	}
	if strings.TrimSpace(req.Model) == "" || req.Model != normalizeModelForProvider(req.Model, "openrouter") {
		return fmt.Errorf("%w: model is not normalized for openrouter", errOpenRouterOSSAdmissionInvalid)
	}
	if len(req.Models) != 0 {
		return fmt.Errorf("%w: fallback models are forbidden", errOpenRouterOSSAdmissionInvalid)
	}
	allowFallbacks, exact := req.Provider["allow_fallbacks"].(bool)
	if !exact || allowFallbacks {
		return fmt.Errorf("%w: provider allow_fallbacks=false is required", errOpenRouterOSSAdmissionInvalid)
	}
	if req.PromptCache != nil || req.CacheControl != nil || req.PromptCacheKey != "" || req.PromptCacheRetention != "" {
		return fmt.Errorf("%w: prompt caching is forbidden", errOpenRouterOSSAdmissionInvalid)
	}
	switch admission.policy {
	case openRouterAdmissionPolicyOSSNonZDR:
		if req.OpenRouterRetention != OpenRouterRetentionNonZDR {
			return fmt.Errorf("%w: non-zdr retention is required", errOpenRouterOSSAdmissionInvalid)
		}
		zdr, exact := req.Provider["zdr"].(bool)
		if !exact || zdr {
			return fmt.Errorf("%w: provider zdr=false is required", errOpenRouterOSSAdmissionInvalid)
		}
		collection, exact := req.Provider["data_collection"].(string)
		if !exact || collection != "deny" {
			return fmt.Errorf("%w: provider data_collection=deny is required", errOpenRouterOSSAdmissionInvalid)
		}
		if admission.contextBinding == ([sha256.Size]byte{}) || req.openRouterContext != admission.contextBinding {
			return fmt.Errorf("%w: oss rule context binding mismatch", errOpenRouterOSSAdmissionInvalid)
		}
	case openRouterAdmissionPolicyStrictZDR:
		if req.OpenRouterRetention != OpenRouterRetentionZDR {
			return fmt.Errorf("%w: strict-zdr retention is required", errOpenRouterOSSAdmissionInvalid)
		}
		zdr, exact := req.Provider["zdr"].(bool)
		if !exact || !zdr {
			return fmt.Errorf("%w: provider zdr=true is required", errOpenRouterOSSAdmissionInvalid)
		}
		if _, present := req.Provider["data_collection"]; present {
			return fmt.Errorf("%w: strict-zdr request cannot carry data_collection", errOpenRouterOSSAdmissionInvalid)
		}
		if admission.contextBinding == ([sha256.Size]byte{}) || req.openRouterContext != admission.contextBinding {
			return fmt.Errorf("%w: strict-zdr context binding mismatch", errOpenRouterOSSAdmissionInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported admission policy", errOpenRouterOSSAdmissionInvalid)
	}
	return nil
}

func marshalOpenRouterOSSFinalWire(req ChatRequest) ([]byte, error) {
	if err := validateOpenRouterOSSRequestPolicy(req); err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling admitted openrouter request: %w", err)
	}
	if err := rejectOpenRouterOSSCacheKeys(body); err != nil {
		return nil, err
	}
	return body, nil
}

func rejectOpenRouterOSSCacheKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: inspect final request json: %v", errOpenRouterOSSAdmissionInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing final request json", errOpenRouterOSSAdmissionInvalid)
	}
	stack := []any{value}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch typed := current.(type) {
		case map[string]any:
			for key, nested := range typed {
				normalized := strings.ToLower(strings.TrimSpace(key))
				if normalized == "cache" || normalized == "cache_control" {
					return fmt.Errorf("%w: cache key %q is forbidden", errOpenRouterOSSAdmissionInvalid, key)
				}
				stack = append(stack, nested)
			}
		case []any:
			stack = append(stack, typed...)
		}
	}
	return nil
}

func openRouterOSSFinalWireDigest(contextBinding [sha256.Size]byte, providerID, model, route string, stream bool, headerRecord []byte, credentialFingerprint [sha256.Size]byte, body []byte) [sha256.Size]byte {
	return openRouterAdmissionFinalWireDigest(openRouterAdmissionPolicyOSSNonZDR, contextBinding, providerID, model, route, stream, headerRecord, credentialFingerprint, body)
}

func openRouterAdmissionFinalWireDigest(policy openRouterAdmissionPolicy, contextBinding [sha256.Size]byte, providerID, model, route string, stream bool, headerRecord []byte, credentialFingerprint [sha256.Size]byte, body []byte) [sha256.Size]byte {
	hasher := sha256.New()
	writeOpenRouterOSSDigestField(hasher, "domain", []byte(openRouterOSSAdmissionDigestDomain))
	writeOpenRouterOSSDigestField(hasher, "provider", []byte(providerID))
	writeOpenRouterOSSDigestField(hasher, "model", []byte(model))
	writeOpenRouterOSSDigestField(hasher, "method", []byte(http.MethodPost))
	writeOpenRouterOSSDigestField(hasher, "route", []byte(route))
	if stream {
		writeOpenRouterOSSDigestField(hasher, "stream", []byte{1})
	} else {
		writeOpenRouterOSSDigestField(hasher, "stream", []byte{0})
	}
	writeOpenRouterOSSDigestField(hasher, "headers", headerRecord)
	writeOpenRouterOSSDigestField(hasher, "credential-fingerprint", credentialFingerprint[:])
	switch policy {
	case openRouterAdmissionPolicyOSSNonZDR:
		writeOpenRouterOSSDigestField(hasher, "policy", []byte("oss-non-zdr"))
		writeOpenRouterOSSDigestField(hasher, "context-binding", contextBinding[:])
	case openRouterAdmissionPolicyStrictZDR:
		writeOpenRouterOSSDigestField(hasher, "policy", []byte("strict-zdr"))
		writeOpenRouterOSSDigestField(hasher, "context-binding", contextBinding[:])
	}
	writeOpenRouterOSSDigestField(hasher, "final-wire", body)
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest
}

func writeOpenRouterOSSDigestField(hasher hash.Hash, label string, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(label)))
	_, _ = hasher.Write(size[:])
	_, _ = hasher.Write([]byte(label))
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = hasher.Write(size[:])
	_, _ = hasher.Write(value)
}

func validateOpenRouterOSSAdmission(
	req ChatRequest,
	provider *OpenRouterProvider,
	client *Client,
	httpClient *http.Client,
	route string,
	stream bool,
	headers http.Header,
	body []byte,
) error {
	admission := req.openRouterAdmission
	if admission == nil {
		return nil
	}
	if err := validateOpenRouterOSSRequestPolicy(req); err != nil {
		return err
	}
	if provider == nil || client == nil || httpClient == nil ||
		admission.provider != provider || admission.client != client || admission.httpClient != httpClient ||
		provider.client != client || client.ossHTTPClient != httpClient {
		return fmt.Errorf("%w: provider or client identity mismatch", errOpenRouterOSSAdmissionInvalid)
	}
	if httpClient.CheckRedirect == nil {
		return fmt.Errorf("%w: no-redirect client is not configured", errOpenRouterOSSAdmissionInvalid)
	}
	if route == "" || admission.route != route || admission.model != req.Model || admission.stream != stream || req.Stream != stream {
		return fmt.Errorf("%w: request binding mismatch", errOpenRouterOSSAdmissionInvalid)
	}
	if len(body) == 0 {
		return fmt.Errorf("%w: empty final wire request", errOpenRouterOSSAdmissionInvalid)
	}
	credentialFingerprint := openRouterOSSCredentialFingerprint(client.apiKey)
	if subtle.ConstantTimeCompare(credentialFingerprint[:], admission.credentialFingerprint[:]) != 1 {
		return fmt.Errorf("%w: credential fingerprint mismatch", errOpenRouterOSSAdmissionInvalid)
	}
	headerRecord, err := canonicalOpenRouterOSSHeaderRecord(headers, client.apiKey, stream)
	if err != nil {
		return err
	}
	committedRecord, err := canonicalOpenRouterOSSHeaderRecord(admission.headers, client.apiKey, stream)
	if err != nil {
		return err
	}
	if !bytes.Equal(headerRecord, admission.headerRecord) || !bytes.Equal(committedRecord, admission.headerRecord) {
		return fmt.Errorf("%w: committed header record mismatch", errOpenRouterOSSAdmissionInvalid)
	}
	digest := openRouterAdmissionFinalWireDigest(admission.policy, admission.contextBinding, "openrouter", req.Model, route, stream, headerRecord, credentialFingerprint, body)
	if digest != admission.wireDigest {
		return fmt.Errorf("%w: final wire digest mismatch", errOpenRouterOSSAdmissionInvalid)
	}
	return nil
}

func validateOpenRouterOSSProviderRequest(req ChatRequest, provider *OpenRouterProvider, stream bool) ([]byte, error) {
	if req.openRouterAdmission == nil {
		return nil, validateModelDispatch(req, "openrouter")
	}
	if provider == nil || provider.client == nil {
		return nil, fmt.Errorf("%w: openrouter provider is unavailable", errOpenRouterOSSAdmissionInvalid)
	}
	req.Stream = stream
	body, err := marshalOpenRouterOSSFinalWire(req)
	if err != nil {
		return nil, err
	}
	client := provider.client
	route := openRouterChatRoute(client)
	headers := req.openRouterAdmission.headers.Clone()
	if err := validateOpenRouterOSSAdmission(req, provider, client, client.ossHTTPClient, route, stream, headers, body); err != nil {
		return nil, err
	}
	return body, nil
}

func validateOpenRouterOSSManagerDispatch(req ChatRequest, provider Provider, stream bool) error {
	if req.openRouterAdmission == nil {
		if provider == nil {
			return fmt.Errorf("model: provider is unavailable")
		}
		return validateModelDispatch(req, provider.ID())
	}
	openRouterProvider, exact := provider.(*OpenRouterProvider)
	if !exact || openRouterProvider == nil || openRouterProvider.ID() != "openrouter" {
		providerID := "<nil>"
		if provider != nil {
			providerID = provider.ID()
		}
		return fmt.Errorf("%w: capability cannot dispatch through provider %q", errOpenRouterOSSAdmissionInvalid, providerID)
	}
	_, err := validateOpenRouterOSSProviderRequest(req, openRouterProvider, stream)
	return err
}

func rejectOpenRouterOSSAdmissionForProvider(req ChatRequest, providerID string) error {
	if req.openRouterAdmission == nil {
		return nil
	}
	return fmt.Errorf("%w: capability cannot dispatch through provider %q", errOpenRouterOSSAdmissionInvalid, providerID)
}
