package decisions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New(Config{Model: "typesafe/jev-1.13"}); err == nil {
		t.Fatal("expected an error for a missing API key")
	}
	if _, err := New(Config{APIKey: "k"}); err == nil {
		t.Fatal("expected an error for a missing model")
	}
	if _, err := New(Config{APIKey: "k", Model: "typesafe/jev-1.13", Pricing: Pricing{InputPerMillion: -1}}); err == nil {
		t.Fatal("expected an error for negative pricing")
	}
	c, err := New(Config{APIKey: "k", Model: "typesafe/jev-1.13"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Model() != "typesafe/jev-1.13" {
		t.Fatalf("unexpected model: %s", c.Model())
	}
}

// captureServer records the last request body it received alongside a
// canned response, so tests can assert on exactly what Ask sent.
func captureServer(t *testing.T, status int, response string) (*httptest.Server, *[]byte) {
	t.Helper()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = body
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func newTestClient(t *testing.T, endpoint string, pricing Pricing) *Client {
	t.Helper()
	c, err := New(Config{
		APIKey: "test-key", Model: "typesafe/jev-1.13", Endpoint: endpoint,
		Timeout: 5 * time.Second, Pricing: pricing,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestAskEncodesNoulQuestion(t *testing.T) {
	srv, captured := captureServer(t, http.StatusOK, `{
		"model":"typesafe/jev-1.13-20260917",
		"answers":{"unfinished":{"type":"noul","noul":0.91}},
		"usage":{"input_tokens":100,"output_tokens":10,"cost":0.0001},
		"id":"gen-1"
	}`)
	c := newTestClient(t, srv.URL, Pricing{})
	resp, err := c.Ask(context.Background(), "state text", map[string]Question{
		"unfinished": {Type: TypeNoul, Instructions: "Is the work unfinished?"},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if resp.Answers["unfinished"].Noul != 0.91 {
		t.Fatalf("unexpected noul answer: %+v", resp.Answers["unfinished"])
	}

	var wire map[string]any
	if err := json.Unmarshal(*captured, &wire); err != nil {
		t.Fatalf("invalid request JSON: %v", err)
	}
	if wire["model"] != "typesafe/jev-1.13" {
		t.Fatalf("unexpected model in request: %v", wire["model"])
	}
	questions, ok := wire["questions"].(map[string]any)
	if !ok {
		t.Fatalf("request did not carry questions: %v", wire)
	}
	q, ok := questions["unfinished"].(map[string]any)
	if !ok {
		t.Fatalf("request question missing: %v", questions)
	}
	if q["type"] != "noul" {
		t.Fatalf("unexpected question type: %v", q["type"])
	}
	if _, hasCriteria := q["criteria"]; hasCriteria {
		t.Fatalf("noul question must not carry criteria: %v", q)
	}
	instructions, _ := q["instructions"].(string)
	if !strings.Contains(instructions, "Is the work unfinished?") {
		t.Fatalf("instructions dropped the caller's text: %q", instructions)
	}

	provider, ok := wire["provider"].(map[string]any)
	if !ok {
		t.Fatalf("request did not carry a provider block: %v", wire)
	}
	if provider["zdr"] != true {
		t.Fatalf("zdr must always be true: %v", provider)
	}
	if provider["data_collection"] != "deny" {
		t.Fatalf("data_collection must always be deny: %v", provider)
	}
	if provider["allow_fallbacks"] != false {
		t.Fatalf("allow_fallbacks must always be false: %v", provider)
	}
}

func TestAskEncodesChoiceQuestion(t *testing.T) {
	srv, captured := captureServer(t, http.StatusOK, `{
		"model":"typesafe/jev-1.13-20260917",
		"answers":{"effort":{"type":"choice","choice":"medium","probabilities":{"low":0.25,"medium":0.61,"high":0.14},"confidence":0.42}},
		"usage":{"input_tokens":200,"output_tokens":20,"cost":0.00002},
		"id":"gen-2"
	}`)
	c := newTestClient(t, srv.URL, Pricing{})
	resp, err := c.Ask(context.Background(), "state text", map[string]Question{
		"effort": {
			Type: TypeChoice, Instructions: "What reasoning effort should the reviewer use?",
			Options: map[string]string{"low": "minimal", "medium": "moderate", "high": "maximum"},
		},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	answer := resp.Answers["effort"]
	if answer.Choice != "medium" {
		t.Fatalf("unexpected choice: %+v", answer)
	}
	if answer.ChoiceProbabilities["high"] != 0.14 {
		t.Fatalf("unexpected choice probabilities: %+v", answer.ChoiceProbabilities)
	}
	if answer.Confidence != 0.42 {
		t.Fatalf("unexpected confidence: %+v", answer)
	}

	var wire map[string]any
	if err := json.Unmarshal(*captured, &wire); err != nil {
		t.Fatalf("invalid request JSON: %v", err)
	}
	questions := wire["questions"].(map[string]any)
	q := questions["effort"].(map[string]any)
	criteria, ok := q["criteria"].(map[string]any)
	if !ok {
		t.Fatalf("choice question must carry a criteria record: %v", q)
	}
	if criteria["low"] != "minimal" {
		t.Fatalf("unexpected criteria: %v", criteria)
	}
}

func TestAskEncodesScoreQuestion(t *testing.T) {
	srv, captured := captureServer(t, http.StatusOK, `{
		"model":"typesafe/jev-1.13-20260917",
		"answers":{"depth":{"type":"score","score":1.49,"legend":{"0":"trivial","1":"light","2":"standard","3":"deep"},"probabilities":{"0":0.43,"1":0.05,"2":0.13,"3":0.39},"confidence":0}},
		"usage":{"input_tokens":429,"output_tokens":69,"cost":0.000018018},
		"id":"gen-3"
	}`)
	c := newTestClient(t, srv.URL, Pricing{})
	resp, err := c.Ask(context.Background(), "state text", map[string]Question{
		"depth": {
			Type: TypeScore, Instructions: "How deep should this review be?",
			Scale: []string{"trivial", "light", "standard", "deep"},
		},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	answer := resp.Answers["depth"]
	// probability mass is 0.43 trivial vs a max of 0.39 deep, so trivial wins.
	if answer.Score != "trivial" {
		t.Fatalf("unexpected score label: %+v", answer)
	}
	if answer.ScoreProbabilities["trivial"] != 0.43 || answer.ScoreProbabilities["deep"] != 0.39 {
		t.Fatalf("unexpected score probabilities: %+v", answer.ScoreProbabilities)
	}
	if answer.Raw != 1.49 {
		t.Fatalf("unexpected raw score: %+v", answer)
	}

	var wire map[string]any
	if err := json.Unmarshal(*captured, &wire); err != nil {
		t.Fatalf("invalid request JSON: %v", err)
	}
	questions := wire["questions"].(map[string]any)
	q := questions["depth"].(map[string]any)
	criteria, ok := q["criteria"].([]any)
	if !ok || len(criteria) != 4 {
		t.Fatalf("score question must carry a criteria array of scale labels: %v", q)
	}
}

func TestUsagePrefersAPICostOverConfigPricing(t *testing.T) {
	srv, _ := captureServer(t, http.StatusOK, `{
		"model":"typesafe/jev-1.13-20260917",
		"answers":{"a":{"type":"noul","noul":0.5}},
		"usage":{"input_tokens":1000000,"output_tokens":0,"cost":0.5},
		"id":"gen-4"
	}`)
	// Configured pricing would compute a very different cost (0.042); the
	// API's own usage.cost (0.5) must win.
	c := newTestClient(t, srv.URL, Pricing{InputPerMillion: 0.042})
	resp, err := c.Ask(context.Background(), "state", map[string]Question{"a": {Type: TypeNoul, Instructions: "?"}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if resp.Usage.Cost != 0.5 || resp.Usage.CostSource != "api" {
		t.Fatalf("expected authoritative API cost, got %+v", resp.Usage)
	}
}

func TestUsageFallsBackToConfigPricingWhenCostIsAbsent(t *testing.T) {
	srv, _ := captureServer(t, http.StatusOK, `{
		"model":"typesafe/jev-1.13-20260917",
		"answers":{"a":{"type":"noul","noul":0.5}},
		"usage":{"input_tokens":1000000,"output_tokens":1000000}
	}`)
	c := newTestClient(t, srv.URL, Pricing{InputPerMillion: 0.042, OutputPerMillion: 1.5})
	resp, err := c.Ask(context.Background(), "state", map[string]Question{"a": {Type: TypeNoul, Instructions: "?"}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	wantCost := 0.042 + 1.5
	if resp.Usage.Cost != wantCost || resp.Usage.CostSource != "config" {
		t.Fatalf("expected config-derived cost %v, got %+v", wantCost, resp.Usage)
	}
}

func TestUsageZeroAPICostIsAuthoritative(t *testing.T) {
	// An explicit "cost":0 in the response (a genuinely free call) must
	// not be overridden by nonzero configured pricing.
	srv, _ := captureServer(t, http.StatusOK, `{
		"model":"typesafe/jev-1.13-20260917",
		"answers":{"a":{"type":"noul","noul":0.5}},
		"usage":{"input_tokens":100,"output_tokens":100,"cost":0}
	}`)
	c := newTestClient(t, srv.URL, Pricing{InputPerMillion: 99, OutputPerMillion: 99})
	resp, err := c.Ask(context.Background(), "state", map[string]Question{"a": {Type: TypeNoul, Instructions: "?"}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if resp.Usage.Cost != 0 || resp.Usage.CostSource != "api" {
		t.Fatalf("expected authoritative zero API cost, got %+v", resp.Usage)
	}
}

func TestAskRejectsUnexpectedModel(t *testing.T) {
	srv, _ := captureServer(t, http.StatusOK, `{
		"model":"some-other-model",
		"answers":{"a":{"type":"noul","noul":0.5}},
		"usage":{"input_tokens":1,"output_tokens":1,"cost":0.001}
	}`)
	c := newTestClient(t, srv.URL, Pricing{})
	if _, err := c.Ask(context.Background(), "state", map[string]Question{"a": {Type: TypeNoul, Instructions: "?"}}); err == nil {
		t.Fatal("expected an error for an unexpected model")
	}
}

func TestAskAcceptsDatedModelVariant(t *testing.T) {
	srv, _ := captureServer(t, http.StatusOK, `{
		"model":"typesafe/jev-1.13-20260917",
		"answers":{"a":{"type":"noul","noul":0.5}},
		"usage":{"input_tokens":1,"output_tokens":1,"cost":0.001}
	}`)
	c := newTestClient(t, srv.URL, Pricing{})
	if _, err := c.Ask(context.Background(), "state", map[string]Question{"a": {Type: TypeNoul, Instructions: "?"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAskRejectsOutOfRangeProbability(t *testing.T) {
	srv, _ := captureServer(t, http.StatusOK, `{
		"model":"typesafe/jev-1.13",
		"answers":{"a":{"type":"noul","noul":1.5}},
		"usage":{"input_tokens":1,"output_tokens":1,"cost":0.001}
	}`)
	c := newTestClient(t, srv.URL, Pricing{})
	if _, err := c.Ask(context.Background(), "state", map[string]Question{"a": {Type: TypeNoul, Instructions: "?"}}); err == nil {
		t.Fatal("expected an error for an out-of-range probability")
	}
}

func TestAskRejectsUnofferedChoice(t *testing.T) {
	srv, _ := captureServer(t, http.StatusOK, `{
		"model":"typesafe/jev-1.13",
		"answers":{"a":{"type":"choice","choice":"extreme","probabilities":{"low":0.5,"high":0.5}}},
		"usage":{"input_tokens":1,"output_tokens":1,"cost":0.001}
	}`)
	c := newTestClient(t, srv.URL, Pricing{})
	_, err := c.Ask(context.Background(), "state", map[string]Question{
		"a": {Type: TypeChoice, Instructions: "?", Options: map[string]string{"low": "l", "high": "h"}},
	})
	if err == nil {
		t.Fatal("expected an error for a choice not offered")
	}
}

func TestAskRejectsMissingAnswer(t *testing.T) {
	srv, _ := captureServer(t, http.StatusOK, `{
		"model":"typesafe/jev-1.13",
		"answers":{},
		"usage":{"input_tokens":1,"output_tokens":1,"cost":0.001}
	}`)
	c := newTestClient(t, srv.URL, Pricing{})
	if _, err := c.Ask(context.Background(), "state", map[string]Question{"a": {Type: TypeNoul, Instructions: "?"}}); err == nil {
		t.Fatal("expected an error for a missing answer")
	}
}

func TestAskRejectsHTTPError(t *testing.T) {
	srv, _ := captureServer(t, http.StatusBadRequest, `{"error":{"message":"bad request"}}`)
	c := newTestClient(t, srv.URL, Pricing{})
	_, err := c.Ask(context.Background(), "state", map[string]Question{"a": {Type: TypeNoul, Instructions: "?"}})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected an HTTP 400 error, got %v", err)
	}
}

func TestAskRequiresQuestions(t *testing.T) {
	c := newTestClient(t, "http://unused.invalid", Pricing{})
	if _, err := c.Ask(context.Background(), "state", map[string]Question{}); err == nil {
		t.Fatal("expected an error for zero questions")
	}
}

func TestAskRequiresChoiceOptions(t *testing.T) {
	c := newTestClient(t, "http://unused.invalid", Pricing{})
	_, err := c.Ask(context.Background(), "state", map[string]Question{"a": {Type: TypeChoice, Instructions: "?"}})
	if err == nil {
		t.Fatal("expected an error for a choice question without options")
	}
}

func TestAskRequiresScoreScale(t *testing.T) {
	c := newTestClient(t, "http://unused.invalid", Pricing{})
	_, err := c.Ask(context.Background(), "state", map[string]Question{"a": {Type: TypeScore, Instructions: "?", Scale: []string{"only-one"}}})
	if err == nil {
		t.Fatal("expected an error for a score question with fewer than two scale labels")
	}
}

// TestAskTimesOut confirms a slow server produces an error within the
// configured timeout, so callers (the gates) can fall back to their
// non-gated default behavior instead of hanging.
func TestAskTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, err := New(Config{APIKey: "k", Model: "typesafe/jev-1.13", Endpoint: srv.URL, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	start := time.Now()
	_, err = c.Ask(context.Background(), "state", map[string]Question{"a": {Type: TypeNoul, Instructions: "?"}})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Ask did not respect its timeout: took %s", elapsed)
	}
}

func TestAskRespectsCallerContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, err := New(Config{APIKey: "k", Model: "typesafe/jev-1.13", Endpoint: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Ask(ctx, "state", map[string]Question{"a": {Type: TypeNoul, Instructions: "?"}}); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
}
