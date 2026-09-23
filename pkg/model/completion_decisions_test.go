package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/orchestrator/completioncheck"
)

func TestCompletionDecisionClient_Contract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/decisions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		p := body["provider"].(map[string]any)
		if p["zdr"] != true || p["data_collection"] != "deny" || p["allow_fallbacks"] != false || body["model"] != completioncheck.Model {
			t.Errorf("routing/privacy drift: %#v", body)
		}
		if len(body["questions"].(map[string]any)) != 4 {
			t.Error("expected four atomic questions")
		}
		fmt.Fprint(w, `{"model":"typesafe/jev-1.13-20260917","answers":{"action":{"type":"noul","noul":0.99},"unfinished":{"type":"noul","noul":0.99},"authorized":{"type":"noul","noul":0.99},"needs_user":{"type":"noul","noul":0.01}},"usage":{"input_tokens":500,"cost":0.000021}}`)
	}))
	defer srv.Close()
	c := NewCompletionDecisionClient("test-key")
	c.endpoint = srv.URL + "/decisions"
	got, err := completioncheck.Check(context.Background(), c, completioncheck.Input{Request: "Fix it", Response: "I will fix it later"})
	if err != nil || got.Action != "continue" || got.Evidence.InputTokens != 500 {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestCompletionDecisionClient_InvalidResponses(t *testing.T) {
	for _, body := range []string{`{}`, `{"answers":{"action":{"type":"noul"}}}`, `not json`, strings.Repeat("x", 65537)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			c := NewCompletionDecisionClient("test")
			c.endpoint = srv.URL
			if _, err := c.Judge(context.Background(), completioncheck.Input{}); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestCompletionDecisionClient_NoRetryOrRedirect(t *testing.T) {
	for _, status := range []int{302, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/other")
				w.WriteHeader(status)
			}))
			defer srv.Close()
			c := NewCompletionDecisionClient("test")
			c.endpoint = srv.URL
			if _, err := c.Judge(context.Background(), completioncheck.Input{}); err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
