package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
	"m31labs.dev/buckley/pkg/config"
)

type ossAdmissionRoundTripper func(*http.Request) (*http.Response, error)

func (fn ossAdmissionRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func ossAdmissionResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func newOSSAdmissionTestProvider(t *testing.T, transport http.RoundTripper) (*OpenRouterProvider, *Client) {
	t.Helper()
	client := NewClient("test-key", "https://openrouter.invalid/api/v1")
	client.rateLimiter = nil
	client.ossHTTPClient.Transport = transport
	t.Cleanup(func() { _ = client.Close() })
	return &OpenRouterProvider{client: client}, client
}

func baseOSSAdmissionTestRequest() ChatRequest {
	return ChatRequest{
		Model:      "stealth/ox-alpha",
		Messages:   []Message{{Role: "user", Content: "do useful work"}},
		Transforms: []string{"middle-out"},
		Provider: map[string]any{
			"zdr":             false,
			"data_collection": "deny",
			"allow_fallbacks": false,
		},
		OpenRouterRetention: OpenRouterRetentionNonZDR,
		RetryMode:           RequestRetrySingleAttempt,
	}
}

const (
	ossAdmissionOracleNonStreamWire = `{"model":"stealth/ox-alpha","messages":[{"role":"user","content":"do useful work"}],"stream":false,"transforms":["middle-out"],"provider":{"allow_fallbacks":false,"data_collection":"deny","zdr":false}}`
	ossAdmissionOracleStreamWire    = `{"model":"stealth/ox-alpha","messages":[{"role":"user","content":"do useful work"}],"stream":true,"transforms":["middle-out"],"provider":{"allow_fallbacks":false,"data_collection":"deny","zdr":false}}`
)

type ossAdmissionOracleHeaderSpec struct {
	name    string
	present bool
	value   string
}

func ossAdmissionOracleHeaders(apiKey string, stream bool) http.Header {
	headers := http.Header{
		"Authorization":                      {"Bearer " + apiKey},
		"Content-Type":                       {"application/json"},
		"Http-Referer":                       {"https://github.com/odvcencio/buckley"},
		"User-Agent":                         {"Buckley"},
		"X-Openrouter-Experimental-Metadata": {"enabled"},
		"X-Openrouter-Metadata":              {"enabled"},
		"X-Title":                            {"Buckley"},
	}
	if stream {
		headers["Accept"] = []string{"text/event-stream"}
	}
	return headers
}

func ossAdmissionOracleHeaderSpecs(stream bool) []ossAdmissionOracleHeaderSpec {
	specs := []ossAdmissionOracleHeaderSpec{
		{name: "Accept", present: stream, value: "text/event-stream"},
		{name: "Authorization", present: true, value: "Bearer"},
		{name: "Content-Type", present: true, value: "application/json"},
		{name: "Http-Referer", present: true, value: "https://github.com/odvcencio/buckley"},
		{name: "Idempotency-Key"},
		{name: "User-Agent", present: true, value: "Buckley"},
		{name: "X-Idempotency-Key"},
		{name: "X-Openrouter-Experimental-Metadata", present: true, value: "enabled"},
		{name: "X-Openrouter-Metadata", present: true, value: "enabled"},
		{name: "X-Title", present: true, value: "Buckley"},
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].name < specs[j].name })
	return specs
}

func ossAdmissionOracleWriteValue(buffer *bytes.Buffer, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	buffer.Write(size[:])
	buffer.Write(value)
}

func ossAdmissionOracleHeaderRecord(stream bool) []byte {
	specs := ossAdmissionOracleHeaderSpecs(stream)
	var record bytes.Buffer
	ossAdmissionOracleWriteValue(&record, []byte("buckley.openrouter.oss-admission.headers.v1"))
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(specs)))
	record.Write(count[:])
	for _, spec := range specs {
		ossAdmissionOracleWriteValue(&record, []byte(spec.name))
		if spec.present {
			record.WriteByte(1)
			ossAdmissionOracleWriteValue(&record, []byte(spec.value))
		} else {
			record.WriteByte(0)
			ossAdmissionOracleWriteValue(&record, nil)
		}
	}
	return record.Bytes()
}

func ossAdmissionOracleWriteDigestField(hasher hash.Hash, label string, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(label)))
	_, _ = hasher.Write(size[:])
	_, _ = hasher.Write([]byte(label))
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = hasher.Write(size[:])
	_, _ = hasher.Write(value)
}

func ossAdmissionOracleCredentialFingerprint(apiKey string) [sha256.Size]byte {
	hasher := sha256.New()
	ossAdmissionOracleWriteDigestField(hasher, "domain", []byte("buckley.openrouter.oss-admission.credential.v1"))
	ossAdmissionOracleWriteDigestField(hasher, "credential", []byte(apiKey))
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], hasher.Sum(nil))
	return fingerprint
}

func ossAdmissionOracleFinalWireDigest(model, route string, stream bool, headerRecord []byte, credentialFingerprint [sha256.Size]byte, body []byte) [sha256.Size]byte {
	hasher := sha256.New()
	ossAdmissionOracleWriteDigestField(hasher, "domain", []byte("buckley.openrouter.oss-admission.final-wire.v2"))
	ossAdmissionOracleWriteDigestField(hasher, "provider", []byte("openrouter"))
	ossAdmissionOracleWriteDigestField(hasher, "model", []byte(model))
	ossAdmissionOracleWriteDigestField(hasher, "method", []byte("POST"))
	ossAdmissionOracleWriteDigestField(hasher, "route", []byte(route))
	if stream {
		ossAdmissionOracleWriteDigestField(hasher, "stream", []byte{1})
	} else {
		ossAdmissionOracleWriteDigestField(hasher, "stream", []byte{0})
	}
	ossAdmissionOracleWriteDigestField(hasher, "headers", headerRecord)
	ossAdmissionOracleWriteDigestField(hasher, "credential-fingerprint", credentialFingerprint[:])
	ossAdmissionOracleWriteDigestField(hasher, "final-wire", body)
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest
}

// mintOSSAdmissionForTest is deliberately test-only. Production has no
// constructor, mint, accessor, or serialization path for this capability.
func mintOSSAdmissionForTest(t *testing.T, provider *OpenRouterProvider, req ChatRequest, stream bool, fixedWire ...string) (ChatRequest, *openRouterOSSAdmission) {
	t.Helper()
	if provider == nil || provider.client == nil {
		t.Fatal("test admission requires an OpenRouter provider and client")
	}
	client := provider.client
	req.Stream = stream
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal admitted test request: %v", err)
	}
	if len(fixedWire) > 1 {
		t.Fatal("test admission accepts at most one fixed wire body")
	}
	if len(fixedWire) == 1 {
		if !bytes.Equal(body, []byte(fixedWire[0])) {
			t.Fatalf("fixed wire fixture drifted: got %s", body)
		}
		body = []byte(fixedWire[0])
	}
	route := client.baseURL + "/chat/completions"
	headers := ossAdmissionOracleHeaders(client.apiKey, stream)
	headerRecord := ossAdmissionOracleHeaderRecord(stream)
	credentialFingerprint := ossAdmissionOracleCredentialFingerprint(client.apiKey)
	inFlight := &atomic.Bool{}
	consumed := &atomic.Bool{}
	admission := &openRouterOSSAdmission{
		policy:                openRouterAdmissionPolicyOSSNonZDR,
		provider:              provider,
		client:                client,
		httpClient:            client.ossHTTPClient,
		model:                 req.Model,
		route:                 route,
		stream:                stream,
		headers:               headers,
		headerRecord:          headerRecord,
		credentialFingerprint: credentialFingerprint,
		wireDigest:            ossAdmissionOracleFinalWireDigest(req.Model, route, stream, headerRecord, credentialFingerprint, body),
		inFlight:              inFlight,
		consumed:              consumed,
	}
	admission.self = admission
	req.openRouterAdmission = admission
	return req, admission
}

func assertOSSAdmissionOracleRequest(req *http.Request, apiKey string, stream bool, expectedWire string) error {
	if req.Method != http.MethodPost {
		return fmt.Errorf("method = %q, want POST", req.Method)
	}
	if req.URL.String() != "https://openrouter.invalid/api/v1/chat/completions" {
		return fmt.Errorf("route = %q, want exact OpenRouter chat route", req.URL.String())
	}
	expectedHeaders := ossAdmissionOracleHeaders(apiKey, stream)
	if len(req.Header) != len(expectedHeaders) {
		return fmt.Errorf("header count = %d, want %d", len(req.Header), len(expectedHeaders))
	}
	for name, expectedValues := range expectedHeaders {
		actualValues, present := req.Header[name]
		if !present || len(actualValues) != 1 || actualValues[0] != expectedValues[0] {
			return fmt.Errorf("header %q does not match its exact committed value", name)
		}
	}
	for _, forbidden := range []string{"Idempotency-Key", "X-Idempotency-Key"} {
		if _, present := req.Header[forbidden]; present {
			return fmt.Errorf("forbidden retry header %q is present", forbidden)
		}
	}
	if _, present := req.Header["Accept"]; present != stream {
		return fmt.Errorf("Accept presence = %v, want %v", present, stream)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	if !bytes.Equal(body, []byte(expectedWire)) {
		return fmt.Errorf("body does not match fixed literal wire fixture")
	}
	return nil
}

func awaitOSSAdmissionStream(chunks <-chan StreamChunk, errs <-chan error) error {
	for range chunks {
	}
	var result error
	for err := range errs {
		if result == nil && err != nil {
			result = err
		}
	}
	return result
}

func TestOpenRouterOSSAdmissionUsesPrivateNonLoggingNoRedirectClient(t *testing.T) {
	client := NewClient("test-key", "https://openrouter.invalid/api/v1")
	t.Cleanup(func() { _ = client.Close() })
	if client.ossHTTPClient == nil || client.ossHTTPClient == client.httpClient {
		t.Fatal("admitted requests do not have a private HTTP client")
	}
	if client.ossHTTPClient.Transport == client.transport {
		t.Fatal("admitted requests pass through the body-rewriting logging transport")
	}
	if client.ossHTTPClient.CheckRedirect == nil {
		t.Fatal("admitted HTTP client permits default redirects")
	}
	if err := client.ossHTTPClient.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v, want ErrUseLastResponse", err)
	}
}

func TestOpenRouterOSSAdmissionJSONRoundTripHasNoAuthorityAndCopiedCapabilityFails(t *testing.T) {
	var calls atomic.Int32
	provider, client := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return ossAdmissionResponse(req, http.StatusOK, `{"id":"ok","model":"stealth/ox-alpha","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`), nil
	}))
	req, admission := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), false)
	capabilityBlob, err := json.Marshal(admission)
	if err != nil {
		t.Fatal(err)
	}
	if string(capabilityBlob) != `{}` {
		t.Fatalf("capability serialization exposed state: %s", capabilityBlob)
	}
	var decodedCapability openRouterOSSAdmission
	if err := json.Unmarshal(capabilityBlob, &decodedCapability); err != nil {
		t.Fatal(err)
	}
	decodedRequest := req
	decodedRequest.openRouterAdmission = &decodedCapability
	if _, err := client.ChatCompletion(context.Background(), decodedRequest); !errors.Is(err, errOpenRouterOSSAdmissionInvalid) {
		t.Fatalf("decoded capability error = %v, want invalid admission", err)
	}

	blob, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(blob)), "admission") || strings.Contains(string(blob), "openrouter_oss") {
		t.Fatalf("serialized request exposed authority: %s", blob)
	}
	var roundTripped ChatRequest
	if err := json.Unmarshal(blob, &roundTripped); err != nil {
		t.Fatal(err)
	}
	if roundTripped.openRouterAdmission != nil {
		t.Fatal("JSON round trip recreated process-local authority")
	}
	if _, err := client.ChatCompletion(context.Background(), roundTripped); !errors.Is(err, ErrOpenRouterOSSAdmissionRequired) {
		t.Fatalf("round-tripped request error = %v, want admission required", err)
	}

	copied := *req.openRouterAdmission
	req.openRouterAdmission = &copied
	if _, err := client.ChatCompletion(context.Background(), req); !errors.Is(err, errOpenRouterOSSAdmissionInvalid) {
		t.Fatalf("copied capability error = %v, want invalid admission", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("transport calls = %d, want zero", calls.Load())
	}
}

func TestOpenRouterOSSAdmissionConcurrentCallsDispatchExactlyOnce(t *testing.T) {
	var calls atomic.Int32
	var observedConsumed atomic.Bool
	var oracleFailure atomic.Value
	var admission *openRouterOSSAdmission
	var req ChatRequest
	provider, client := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(httpReq *http.Request) (*http.Response, error) {
		calls.Add(1)
		observedConsumed.Store(admission != nil && admission.consumed.Load())
		if err := assertOSSAdmissionOracleRequest(httpReq, "test-key", false, ossAdmissionOracleNonStreamWire); err != nil {
			oracleFailure.Store(err.Error())
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
		return ossAdmissionResponse(httpReq, http.StatusOK, `{"id":"ok","model":"stealth/ox-alpha","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`), nil
	}))
	req, admission = mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), false, ossAdmissionOracleNonStreamWire)

	const callers = 64
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := client.ChatCompletion(context.Background(), req)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var successes int
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 || calls.Load() != 1 {
		t.Fatalf("successes=%d transport calls=%d, want one each", successes, calls.Load())
	}
	if !observedConsumed.Load() {
		t.Fatal("RoundTripper entered before consumed CAS became visible")
	}
	if failure := oracleFailure.Load(); failure != nil {
		t.Fatalf("independent wire oracle: %s", failure)
	}
	if !admission.consumed.Load() || admission.inFlight.Load() {
		t.Fatalf("terminal state consumed=%v inFlight=%v", admission.consumed.Load(), admission.inFlight.Load())
	}
}

func TestOpenRouterOSSAdmissionCredentialAndCommittedHeaderMutationIsRetryable(t *testing.T) {
	tests := []struct {
		name    string
		stream  bool
		mutate  func(*Client, *openRouterOSSAdmission)
		restore func(*Client, *openRouterOSSAdmission)
	}{
		{
			name: "credential",
			mutate: func(client *Client, _ *openRouterOSSAdmission) {
				client.apiKey = "changed-key"
			},
			restore: func(client *Client, _ *openRouterOSSAdmission) {
				client.apiKey = "test-key"
			},
		},
		{
			name: "routing metadata header",
			mutate: func(_ *Client, admission *openRouterOSSAdmission) {
				admission.headers.Set("X-Title", "Changed")
			},
			restore: func(_ *Client, admission *openRouterOSSAdmission) {
				admission.headers.Set("X-Title", "Buckley")
			},
		},
		{
			name: "nonstream gains stream accept",
			mutate: func(_ *Client, admission *openRouterOSSAdmission) {
				admission.headers.Set("Accept", "text/event-stream")
			},
			restore: func(_ *Client, admission *openRouterOSSAdmission) {
				admission.headers.Del("Accept")
			},
		},
		{
			name:   "stream loses stream accept",
			stream: true,
			mutate: func(_ *Client, admission *openRouterOSSAdmission) {
				admission.headers.Del("Accept")
			},
			restore: func(_ *Client, admission *openRouterOSSAdmission) {
				admission.headers.Set("Accept", "text/event-stream")
			},
		},
		{
			name: "idempotency key",
			mutate: func(_ *Client, admission *openRouterOSSAdmission) {
				admission.headers.Set("Idempotency-Key", "forbidden")
			},
			restore: func(_ *Client, admission *openRouterOSSAdmission) {
				admission.headers.Del("Idempotency-Key")
			},
		},
		{
			name: "x idempotency key",
			mutate: func(_ *Client, admission *openRouterOSSAdmission) {
				admission.headers.Set("X-Idempotency-Key", "forbidden")
			},
			restore: func(_ *Client, admission *openRouterOSSAdmission) {
				admission.headers.Del("X-Idempotency-Key")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			provider, client := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				expectedWire := ossAdmissionOracleNonStreamWire
				if tt.stream {
					expectedWire = ossAdmissionOracleStreamWire
				}
				if err := assertOSSAdmissionOracleRequest(req, "test-key", tt.stream, expectedWire); err != nil {
					return nil, err
				}
				if tt.stream {
					return ossAdmissionResponse(req, http.StatusOK, "data: {\"id\":\"stream\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"), nil
				}
				return ossAdmissionResponse(req, http.StatusOK, `{"id":"ok","model":"stealth/ox-alpha","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`), nil
			}))
			expectedWire := ossAdmissionOracleNonStreamWire
			if tt.stream {
				expectedWire = ossAdmissionOracleStreamWire
			}
			req, admission := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), tt.stream, expectedWire)
			tt.mutate(client, admission)

			var err error
			if tt.stream {
				chunks, errs := client.ChatCompletionStream(context.Background(), req)
				err = awaitOSSAdmissionStream(chunks, errs)
			} else {
				_, err = client.ChatCompletion(context.Background(), req)
			}
			if !errors.Is(err, errOpenRouterOSSAdmissionInvalid) {
				t.Fatalf("mutated admission error = %v, want invalid admission", err)
			}
			if calls.Load() != 0 || admission.consumed.Load() || admission.inFlight.Load() {
				t.Fatalf("pre-CAS mutation calls=%d consumed=%v inFlight=%v", calls.Load(), admission.consumed.Load(), admission.inFlight.Load())
			}

			tt.restore(client, admission)
			if tt.stream {
				chunks, errs := client.ChatCompletionStream(context.Background(), req)
				err = awaitOSSAdmissionStream(chunks, errs)
			} else {
				_, err = client.ChatCompletion(context.Background(), req)
			}
			if err != nil {
				t.Fatalf("restored admission: %v", err)
			}
			if calls.Load() != 1 || !admission.consumed.Load() || admission.inFlight.Load() {
				t.Fatalf("restored calls=%d consumed=%v inFlight=%v", calls.Load(), admission.consumed.Load(), admission.inFlight.Load())
			}
		})
	}
}

func TestOpenRouterOSSAdmissionBindingMismatchesStopBeforeTransport(t *testing.T) {
	tests := []struct {
		name string
		call func(*testing.T, *OpenRouterProvider, *Client, ChatRequest) error
	}{
		{name: "model", call: func(_ *testing.T, _ *OpenRouterProvider, client *Client, req ChatRequest) error {
			req.Model += "-different"
			_, err := client.ChatCompletion(context.Background(), req)
			return err
		}},
		{name: "unnormalized model", call: func(_ *testing.T, _ *OpenRouterProvider, client *Client, req ChatRequest) error {
			req.Model = "gpt-5.4"
			_, err := client.ChatCompletion(context.Background(), req)
			return err
		}},
		{name: "body", call: func(_ *testing.T, _ *OpenRouterProvider, client *Client, req ChatRequest) error {
			req.Messages = append(req.Messages, Message{Role: "user", Content: "changed"})
			_, err := client.ChatCompletion(context.Background(), req)
			return err
		}},
		{name: "route", call: func(_ *testing.T, _ *OpenRouterProvider, client *Client, req ChatRequest) error {
			client.baseURL += "/different"
			_, err := client.ChatCompletion(context.Background(), req)
			return err
		}},
		{name: "private http client", call: func(_ *testing.T, _ *OpenRouterProvider, client *Client, req ChatRequest) error {
			client.ossHTTPClient = &http.Client{Transport: client.httpClient.Transport, CheckRedirect: rejectOpenRouterRedirect}
			_, err := client.ChatCompletion(context.Background(), req)
			return err
		}},
		{name: "client", call: func(t *testing.T, _ *OpenRouterProvider, _ *Client, req ChatRequest) error {
			otherProvider, otherClient := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
				return ossAdmissionResponse(req, http.StatusOK, `{}`), nil
			}))
			_ = otherProvider
			_, err := otherClient.ChatCompletion(context.Background(), req)
			return err
		}},
		{name: "provider", call: func(_ *testing.T, _ *OpenRouterProvider, client *Client, req ChatRequest) error {
			other := &OpenRouterProvider{client: client}
			_, err := other.ChatCompletion(context.Background(), req)
			return err
		}},
		{name: "nonstream capability on stream", call: func(_ *testing.T, _ *OpenRouterProvider, client *Client, req ChatRequest) error {
			chunks, errs := client.ChatCompletionStream(context.Background(), req)
			return awaitOSSAdmissionStream(chunks, errs)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			provider, client := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				return ossAdmissionResponse(req, http.StatusOK, `{"id":"unexpected","choices":[{}]}`), nil
			}))
			req, _ := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), false)
			if err := tt.call(t, provider, client, req); !errors.Is(err, errOpenRouterOSSAdmissionInvalid) {
				t.Fatalf("mismatch error = %v, want invalid admission", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("transport calls = %d, want zero", calls.Load())
			}
		})
	}

	t.Run("stream capability on nonstream", func(t *testing.T) {
		var calls atomic.Int32
		provider, client := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			return ossAdmissionResponse(req, http.StatusOK, `{}`), nil
		}))
		req, _ := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), true)
		if _, err := client.ChatCompletion(context.Background(), req); !errors.Is(err, errOpenRouterOSSAdmissionInvalid) {
			t.Fatalf("cross-mode error = %v, want invalid admission", err)
		}
		if calls.Load() != 0 {
			t.Fatalf("transport calls = %d, want zero", calls.Load())
		}
	})
}

func TestOpenRouterOSSAdmissionRejectsFallbacksAndAllCacheShapes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ChatRequest)
	}{
		{name: "fallback models", mutate: func(req *ChatRequest) { req.Models = []string{"other/model"} }},
		{name: "allow fallbacks", mutate: func(req *ChatRequest) { req.Provider["allow_fallbacks"] = true }},
		{name: "missing allow fallbacks", mutate: func(req *ChatRequest) { delete(req.Provider, "allow_fallbacks") }},
		{name: "zdr true", mutate: func(req *ChatRequest) { req.Provider["zdr"] = true }},
		{name: "missing zdr", mutate: func(req *ChatRequest) { delete(req.Provider, "zdr") }},
		{name: "data collection allow", mutate: func(req *ChatRequest) { req.Provider["data_collection"] = "allow" }},
		{name: "missing data collection", mutate: func(req *ChatRequest) { delete(req.Provider, "data_collection") }},
		{name: "typed top level cache", mutate: func(req *ChatRequest) { req.CacheControl = &CacheControl{Type: "ephemeral"} }},
		{name: "prompt cache", mutate: func(req *ChatRequest) { req.PromptCache = &PromptCache{} }},
		{name: "openai prompt cache key", mutate: func(req *ChatRequest) { req.PromptCacheKey = "key" }},
		{name: "openai prompt cache retention", mutate: func(req *ChatRequest) { req.PromptCacheRetention = "24h" }},
		{name: "nested cache control null", mutate: func(req *ChatRequest) {
			req.ResponseFormat = map[string]any{"nested": map[string]any{"cache_control": nil}}
		}},
		{name: "nested cache null", mutate: func(req *ChatRequest) {
			req.Tools = []map[string]any{{"function": map[string]any{"cache": nil}}}
		}},
		{name: "typed content cache", mutate: func(req *ChatRequest) {
			req.Messages[0].Content = []ContentPart{{Type: "text", Text: "hello", CacheControl: &CacheControl{Type: "ephemeral"}}}
		}},
		{name: "default retry", mutate: func(req *ChatRequest) { req.RetryMode = RequestRetryDefault }},
		{name: "zdr retention", mutate: func(req *ChatRequest) { req.OpenRouterRetention = OpenRouterRetentionZDR }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			provider, client := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				return ossAdmissionResponse(req, http.StatusOK, `{}`), nil
			}))
			req, _ := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), false)
			tt.mutate(&req)
			if _, err := client.ChatCompletion(context.Background(), req); err == nil {
				t.Fatal("policy violation was accepted")
			}
			if calls.Load() != 0 {
				t.Fatalf("transport calls = %d, want zero", calls.Load())
			}
		})
	}
}

func TestOpenRouterOSSAdmissionPostCASFailuresAreSpentWithoutRetry(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		transport error
	}{
		{name: "payment required", status: http.StatusPaymentRequired, body: `{"error":{"message":"lower tokens"}}`},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `{"error":{"message":"rate limited"}}`},
		{name: "server error", status: http.StatusInternalServerError, body: `{"error":{"message":"upstream"}}`},
		{name: "network", transport: errors.New("transport failed")},
		{name: "decode", status: http.StatusOK, body: `{`},
		{name: "zero choices", status: http.StatusOK, body: `{"id":"empty","choices":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			provider, client := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if tt.transport != nil {
					return nil, tt.transport
				}
				return ossAdmissionResponse(req, tt.status, tt.body), nil
			}))
			req, admission := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), false)
			if _, err := client.ChatCompletion(context.Background(), req); err == nil {
				t.Fatal("first call error = nil")
			}
			if _, err := client.ChatCompletion(context.Background(), req); !errors.Is(err, errOpenRouterOSSAdmissionSpent) {
				t.Fatalf("second call error = %v, want spent", err)
			}
			if calls.Load() != 1 || !admission.consumed.Load() || admission.inFlight.Load() {
				t.Fatalf("calls=%d consumed=%v inFlight=%v", calls.Load(), admission.consumed.Load(), admission.inFlight.Load())
			}
		})
	}

	t.Run("zero event stream", func(t *testing.T) {
		var calls atomic.Int32
		provider, client := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			return ossAdmissionResponse(req, http.StatusOK, "data: [DONE]\n\n"), nil
		}))
		req, admission := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), true)
		chunks, errs := client.ChatCompletionStream(context.Background(), req)
		if err := awaitOSSAdmissionStream(chunks, errs); err == nil || !strings.Contains(err.Error(), "no events") {
			t.Fatalf("stream error = %v, want zero-event error", err)
		}
		chunks, errs = client.ChatCompletionStream(context.Background(), req)
		if err := awaitOSSAdmissionStream(chunks, errs); !errors.Is(err, errOpenRouterOSSAdmissionSpent) {
			t.Fatalf("second stream error = %v, want spent", err)
		}
		if calls.Load() != 1 || !admission.consumed.Load() {
			t.Fatalf("calls=%d consumed=%v", calls.Load(), admission.consumed.Load())
		}
	})
}

func TestOpenRouterOSSAdmissionRedirectDoesNotReachTarget(t *testing.T) {
	var sourceCalls atomic.Int32
	var targetCalls atomic.Int32
	provider, client := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "redirect-target.invalid" {
			targetCalls.Add(1)
			return ossAdmissionResponse(req, http.StatusOK, `{"id":"leaked","choices":[{}]}`), nil
		}
		sourceCalls.Add(1)
		resp := ossAdmissionResponse(req, http.StatusFound, `{"error":{"message":"redirect"}}`)
		resp.Header.Set("Location", "https://redirect-target.invalid/leak")
		return resp, nil
	}))
	req, admission := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), false)
	if _, err := client.ChatCompletion(context.Background(), req); err == nil {
		t.Fatal("redirect response unexpectedly succeeded")
	}
	if sourceCalls.Load() != 1 || targetCalls.Load() != 0 || !admission.consumed.Load() {
		t.Fatalf("source=%d target=%d consumed=%v", sourceCalls.Load(), targetCalls.Load(), admission.consumed.Load())
	}
}

func TestOpenRouterOSSAdmissionPreCASCircuitAndLimiterFailuresRemainUnused(t *testing.T) {
	t.Run("open circuit", func(t *testing.T) {
		var calls atomic.Int32
		provider, client := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			return ossAdmissionResponse(req, http.StatusOK, `{}`), nil
		}))
		req, admission := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), false)
		client.circuitBreaker.mu.Lock()
		client.circuitBreaker.state = CircuitOpen
		client.circuitBreaker.lastFailureTime = time.Now()
		client.circuitBreaker.config.ResetTimeout = time.Hour
		client.circuitBreaker.mu.Unlock()
		if _, err := client.ChatCompletion(context.Background(), req); err == nil || !strings.Contains(err.Error(), "circuit breaker is open") {
			t.Fatalf("open-circuit error = %v", err)
		}
		if calls.Load() != 0 || admission.consumed.Load() || admission.inFlight.Load() {
			t.Fatalf("calls=%d consumed=%v inFlight=%v", calls.Load(), admission.consumed.Load(), admission.inFlight.Load())
		}
	})

	t.Run("rate limiter deadline", func(t *testing.T) {
		var calls atomic.Int32
		provider, client := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			return ossAdmissionResponse(req, http.StatusOK, `{}`), nil
		}))
		client.rateLimiter = rate.NewLimiter(rate.Every(time.Hour), 1)
		if !client.rateLimiter.Allow() {
			t.Fatal("failed to consume initial limiter token")
		}
		req, admission := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), false)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if _, err := client.ChatCompletion(ctx, req); err == nil || !strings.Contains(err.Error(), "rate limit wait") {
			t.Fatalf("limiter error = %v", err)
		}
		if calls.Load() != 0 || admission.consumed.Load() || admission.inFlight.Load() {
			t.Fatalf("calls=%d consumed=%v inFlight=%v", calls.Load(), admission.consumed.Load(), admission.inFlight.Load())
		}
	})

	t.Run("canceled context with limiter", func(t *testing.T) {
		var calls atomic.Int32
		provider, client := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			return ossAdmissionResponse(req, http.StatusOK, `{}`), nil
		}))
		client.rateLimiter = rate.NewLimiter(rate.Every(time.Hour), 1)
		req, admission := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), false)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := client.ChatCompletion(ctx, req); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled request error = %v, want context canceled", err)
		}
		if calls.Load() != 0 || admission.consumed.Load() || admission.inFlight.Load() {
			t.Fatalf("calls=%d consumed=%v inFlight=%v", calls.Load(), admission.consumed.Load(), admission.inFlight.Load())
		}
	})
}

func TestOpenRouterOSSAdmissionManagerBindingAndRouting(t *testing.T) {
	t.Run("exact openrouter manager bypasses cache and retries", func(t *testing.T) {
		var calls atomic.Int32
		provider, _ := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			return ossAdmissionResponse(req, http.StatusOK, `{"id":"ok","model":"openai/gpt-5.4","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`), nil
		}))
		finalRequest := baseOSSAdmissionTestRequest()
		finalRequest.Model = "openai/gpt-5.4"
		req, _ := mintOSSAdmissionForTest(t, provider, finalRequest, false)
		// Admission binds the post-transform request. The Manager must normalize
		// this caller shape and inject the deterministic transform before gating.
		req.Model = "gpt-5.4"
		req.Transforms = nil
		cfg := config.DefaultConfig()
		cfg.Models.DefaultProvider = "openrouter"
		cfg.Models.FallbackChains = map[string][]string{req.Model: {"other/model"}}
		cfg.PromptCache = config.PromptCacheConfig{Enabled: true, Providers: []string{"openrouter"}, TailMessages: 1}
		manager := &Manager{
			config:         cfg,
			providers:      map[string]Provider{"openrouter": provider},
			providerOrder:  []string{"openrouter"},
			catalog:        map[string]ModelInfo{req.Model: {ID: req.Model}},
			providerModels: map[string][]string{"openrouter": {req.Model}},
			modelProviders: map[string]string{req.Model: "openrouter"},
		}
		if _, err := manager.ChatCompletion(context.Background(), req); err != nil {
			t.Fatalf("manager completion: %v", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("transport calls = %d, want one", calls.Load())
		}
	})

	t.Run("exact stream dispatch", func(t *testing.T) {
		var calls atomic.Int32
		provider, _ := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(httpReq *http.Request) (*http.Response, error) {
			calls.Add(1)
			body, err := io.ReadAll(httpReq.Body)
			if err != nil {
				t.Fatalf("read stream request: %v", err)
			}
			var wire struct {
				Stream bool `json:"stream"`
			}
			if err := json.Unmarshal(body, &wire); err != nil || !wire.Stream {
				t.Fatalf("stream wire = %s, error=%v", body, err)
			}
			return ossAdmissionResponse(httpReq, http.StatusOK, "data: {\"id\":\"stream\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"), nil
		}))
		req, admission := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), true)
		cfg := config.DefaultConfig()
		cfg.Models.DefaultProvider = "openrouter"
		cfg.PromptCache = config.PromptCacheConfig{Enabled: true, Providers: []string{"openrouter"}, TailMessages: 1}
		manager := &Manager{
			config:         cfg,
			providers:      map[string]Provider{"openrouter": provider},
			providerOrder:  []string{"openrouter"},
			catalog:        map[string]ModelInfo{req.Model: {ID: req.Model}},
			providerModels: map[string][]string{"openrouter": {req.Model}},
			modelProviders: map[string]string{req.Model: "openrouter"},
		}
		chunks, errs := manager.ChatCompletionStream(context.Background(), req)
		if err := awaitOSSAdmissionStream(chunks, errs); err != nil {
			t.Fatalf("manager stream: %v", err)
		}
		if calls.Load() != 1 || !admission.consumed.Load() {
			t.Fatalf("stream calls=%d consumed=%v", calls.Load(), admission.consumed.Load())
		}
	})

	for _, providerID := range []string{"openai", "openrouter"} {
		t.Run("diversion/"+providerID, func(t *testing.T) {
			var calls atomic.Int32
			boundProvider, _ := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				return ossAdmissionResponse(req, http.StatusOK, `{}`), nil
			}))
			req, _ := mintOSSAdmissionForTest(t, boundProvider, baseOSSAdmissionTestRequest(), false)
			diverted := &stubProvider{id: providerID, catalog: ModelCatalog{Data: []ModelInfo{{ID: req.Model}}}}
			cfg := config.DefaultConfig()
			cfg.Models.DefaultProvider = providerID
			manager := &Manager{
				config:         cfg,
				providers:      map[string]Provider{providerID: diverted},
				providerOrder:  []string{providerID},
				catalog:        map[string]ModelInfo{req.Model: {ID: req.Model}},
				providerModels: map[string][]string{providerID: {req.Model}},
				modelProviders: map[string]string{req.Model: providerID},
			}
			if _, err := manager.ChatCompletion(context.Background(), req); !errors.Is(err, errOpenRouterOSSAdmissionInvalid) {
				t.Fatalf("routing diversion error = %v, want invalid admission", err)
			}
			if len(diverted.requests) != 0 || calls.Load() != 0 {
				t.Fatalf("diverted calls=%d bound transport=%d, want zero", len(diverted.requests), calls.Load())
			}
		})
	}
}

func TestValidateAPIKeyUsesExplicitStrictZDRSingleAttemptRequest(t *testing.T) {
	var catalogCalls atomic.Int32
	var completionCalls atomic.Int32
	client := NewClient("test-key", "https://openrouter.invalid/api/v1")
	client.httpClient.Transport = ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/models"):
			catalogCalls.Add(1)
			return ossAdmissionResponse(req, http.StatusOK, `{"data":[]}`), nil
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/chat/completions"):
			completionCalls.Add(1)
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read completion request: %v", err)
			}
			var wire struct {
				Provider map[string]any `json:"provider"`
				Models   []string       `json:"models"`
			}
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatalf("decode completion request: %v", err)
			}
			if wire.Provider["zdr"] != true || wire.Provider["allow_fallbacks"] != false || len(wire.Models) != 0 {
				t.Fatalf("ValidateAPIKey request is not exact strict ZDR: %#v", wire)
			}
			return ossAdmissionResponse(req, http.StatusTooManyRequests, `{"error":{"message":"one attempt"}}`), nil
		default:
			t.Fatalf("unexpected ValidateAPIKey request: %s %s", req.Method, req.URL)
			return nil, errors.New("unexpected request")
		}
	})
	client.SetRetryConfig(RetryConfig{MaxRetries: 5, MaxRateLimitRetries: 5, InitialInterval: time.Millisecond, MaxInterval: time.Millisecond, Multiplier: 1})
	if err := client.ValidateAPIKey(); err == nil {
		t.Fatal("ValidateAPIKey error = nil, want terminal 429")
	}
	if catalogCalls.Load() != 1 || completionCalls.Load() != 1 {
		t.Fatalf("catalog calls=%d completion calls=%d, want one each", catalogCalls.Load(), completionCalls.Load())
	}
}

type countingOSSContinuationStore struct {
	loads   atomic.Int32
	saves   atomic.Int32
	deletes atomic.Int32
}

func (s *countingOSSContinuationStore) LoadProviderContinuation(string, string, string) (string, error) {
	s.loads.Add(1)
	return "", nil
}

func (s *countingOSSContinuationStore) SaveProviderContinuation(string, string, string, string) error {
	s.saves.Add(1)
	return nil
}

func (s *countingOSSContinuationStore) DeleteProviderContinuation(string, string) error {
	s.deletes.Add(1)
	return nil
}

func TestOpenRouterOSSAdmissionContinuationRejectedBeforeStateMutation(t *testing.T) {
	provider, _ := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
		return ossAdmissionResponse(req, http.StatusOK, `{}`), nil
	}))
	req, _ := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), false)

	manager := &Manager{}
	if _, err := manager.ChatCompletionWithContinuation(context.Background(), ContinuationRequest{Request: req}); !errors.Is(err, errOpenRouterOSSAdmissionInvalid) {
		t.Fatalf("manager continuation error = %v, want invalid admission", err)
	}

	store := &countingOSSContinuationStore{}
	coordinator := NewContinuationCoordinator(manager, store, "session")
	coordinator.requestModel = "unchanged"
	coordinator.hit = true
	coordinator.cursor.continuation = &ProviderContinuation{
		ProviderID: "openai",
		ModelID:    "gpt-5.4",
		State:      json.RawMessage(`{"item":"preserve"}`),
	}
	coordinator.cursor.represented = append(coordinator.cursor.represented, [32]byte{1})
	if _, err := coordinator.Call(context.Background(), req); !errors.Is(err, errOpenRouterOSSAdmissionInvalid) {
		t.Fatalf("coordinator continuation error = %v, want invalid admission", err)
	}
	if coordinator.requestModel != "unchanged" || !coordinator.hit || !coordinator.cursor.Active() || len(coordinator.cursor.represented) != 1 || string(coordinator.cursor.continuation.State) != `{"item":"preserve"}` {
		t.Fatalf("coordinator mutated: model=%q hit=%v active=%v", coordinator.requestModel, coordinator.hit, coordinator.cursor.Active())
	}
	if store.loads.Load() != 0 || store.saves.Load() != 0 || store.deletes.Load() != 0 {
		t.Fatalf("store mutations load=%d save=%d delete=%d", store.loads.Load(), store.saves.Load(), store.deletes.Load())
	}
}

func TestOpenRouterOSSAdmissionDirectClientAndNonOpenRouterProviderCannotBypass(t *testing.T) {
	var calls atomic.Int32
	provider, client := newOSSAdmissionTestProvider(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return ossAdmissionResponse(req, http.StatusOK, `{}`), nil
	}))
	withoutCapability := baseOSSAdmissionTestRequest()
	if _, err := client.ChatCompletion(context.Background(), withoutCapability); !errors.Is(err, ErrOpenRouterOSSAdmissionRequired) {
		t.Fatalf("direct client error = %v, want admission required", err)
	}

	admitted, _ := mintOSSAdmissionForTest(t, provider, baseOSSAdmissionTestRequest(), false)
	openAI := NewOpenAIProvider("test-key", "https://openai.invalid", false)
	if _, err := openAI.ChatCompletion(context.Background(), admitted); !errors.Is(err, errOpenRouterOSSAdmissionInvalid) {
		t.Fatalf("direct non-openrouter error = %v, want invalid admission", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("transport calls = %d, want zero", calls.Load())
	}
}
