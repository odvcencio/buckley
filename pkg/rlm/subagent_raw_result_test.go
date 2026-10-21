package rlm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
)

func TestSubAgentExecute_RawResultPolicy(t *testing.T) {
	for _, mode := range []struct {
		name       string
		omit       bool
		scratchpad bool
		wantRaw    bool
	}{
		{"default", false, false, true},
		{"structured only", true, false, false},
		{"scratchpad overrides omission", true, true, true},
	} {
		for _, finish := range []string{"stop", "length"} {
			t.Run(mode.name+"/"+finish, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"id":"raw-policy-response","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"public summary","reasoning":"private-sentinel"},"finish_reason":%q}],"usage":{"prompt_tokens":7,"completion_tokens":11,"total_tokens":18}}`, finish)
				}))
				t.Cleanup(server.Close)
				registry := tool.NewEmptyRegistry()
				t.Cleanup(func() { _ = registry.Close() })
				deps := SubAgentDeps{Models: newSubAgentTestManager(t, server), Registry: registry}
				var pad *Scratchpad
				if mode.scratchpad {
					pad = NewScratchpad(nil, nil, ScratchpadConfig{})
					deps.Scratchpad = pad
				}
				agent, err := NewSubAgent(SubAgentConfig{ID: "raw-policy", Model: "gpt-4o", MaxIterations: 1, OmitRawResult: mode.omit}, deps)
				if err != nil {
					t.Fatal(err)
				}
				result, err := agent.Execute(context.Background(), "report the observed result")
				if (err != nil) != (finish == "length") {
					t.Fatalf("finish=%s err=%v", finish, err)
				}
				if result == nil || result.Summary != "public summary" || result.FinishReason != finish || result.Duration <= 0 {
					t.Fatalf("result=%+v", result)
				}
				if result.InputTokens != 7 || result.OutputTokens != 11 || result.TokensUsed != 18 ||
					len(result.ModelExecutions) != 1 || result.ModelExecutions[0].ResponseID != "raw-policy-response" {
					t.Fatalf("structured usage/identity changed: %+v", result)
				}
				if (len(result.Raw) > 0) != mode.wantRaw {
					t.Fatalf("raw bytes=%d wantRaw=%v", len(result.Raw), mode.wantRaw)
				}
				if mode.wantRaw && (!json.Valid(result.Raw) || bytes.Contains(result.Raw, []byte("private-sentinel"))) {
					t.Fatal("raw result is invalid or includes private reasoning")
				}
				if !mode.scratchpad || finish == "length" {
					if result.RawKey != "" {
						t.Fatal("unexpected scratchpad key")
					}
					return
				}
				entry, err := pad.InspectRaw(context.Background(), result.RawKey)
				if err != nil || entry == nil || !bytes.Equal(entry.Raw, result.Raw) {
					t.Fatalf("scratchpad raw changed: entry=%+v err=%v", entry, err)
				}

				// Analysis entries stay in memory; explicit artifact export is durable.
				path := filepath.Join(t.TempDir(), "scratchpad.db")
				store, err := storage.New(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				cfg := ScratchpadConfig{PersistArtifacts: true}
				key, err := NewScratchpad(store, nil, cfg).Write(context.Background(), WriteRequest{
					Type: EntryTypeArtifact, Raw: result.Raw, Summary: result.Summary,
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := storage.New(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = reopened.Close() })
				persisted, err := NewScratchpad(reopened, nil, cfg).InspectRaw(context.Background(), key)
				if err != nil || persisted == nil || !bytes.Equal(persisted.Raw, result.Raw) {
					t.Fatalf("reopened raw artifact changed: entry=%+v err=%v", persisted, err)
				}
				t.Logf("raw_bytes=%d scratchpad_exact=true artifact_reopen_exact=true", len(result.Raw))
			})
		}
	}
}
