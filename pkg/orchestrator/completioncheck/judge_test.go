package completioncheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model/decisions"
)

func newTestJudge(t *testing.T, endpoint string) Judge {
	t.Helper()
	client, err := decisions.New(decisions.Config{
		APIKey: "test-key", Model: Model, Endpoint: endpoint,
	})
	if err != nil {
		t.Fatalf("decisions.New: %v", err)
	}
	return decisionsJudge{client: client}
}

func TestOpenRouterJudge_Contract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		p := body["provider"].(map[string]any)
		if p["zdr"] != true || p["data_collection"] != "deny" || p["allow_fallbacks"] != false || body["model"] != Model {
			t.Errorf("routing/privacy drift: %#v", body)
		}
		if len(body["questions"].(map[string]any)) != 4 {
			t.Error("expected four atomic questions")
		}
		fmt.Fprint(w, `{"model":"typesafe/jev-1.13-20260917","answers":{"action":{"type":"noul","noul":0.99},"unfinished":{"type":"noul","noul":0.99},"authorized":{"type":"noul","noul":0.99},"needs_user":{"type":"noul","noul":0.01}},"usage":{"input_tokens":500,"output_tokens":40,"cost":0.000021}}`)
	}))
	defer srv.Close()

	j := newTestJudge(t, srv.URL)
	got, err := Check(context.Background(), j, Input{Request: "Fix it", Response: "I will fix it later"})
	if err != nil || got.Action != "continue" || got.Evidence.InputTokens != 500 || got.Evidence.Cost != 0.000021 {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestOpenRouterJudge_InvalidResponses(t *testing.T) {
	for _, body := range []string{`{}`, `{"model":"typesafe/jev-1.13","answers":{"action":{"type":"noul"}}}`, `not json`, strings.Repeat("x", 65537)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			j := newTestJudge(t, srv.URL)
			if _, err := j.Judge(context.Background(), Input{Request: "r", Response: "s"}); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestOpenRouterJudge_NoRetryOrRedirect(t *testing.T) {
	for _, status := range []int{302, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/other")
				w.WriteHeader(status)
			}))
			defer srv.Close()
			j := newTestJudge(t, srv.URL)
			if _, err := j.Judge(context.Background(), Input{Request: "r", Response: "s"}); err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestNewOpenRouterJudgeRequiresAPIKey(t *testing.T) {
	if _, err := NewOpenRouterJudge("", decisions.Pricing{}); err == nil {
		t.Fatal("expected an error for a missing API key")
	}
}
