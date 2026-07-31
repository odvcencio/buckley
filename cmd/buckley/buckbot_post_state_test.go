package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/v2/pkg/gitwatcher"
)

func TestReadBuckbotFinalReviewStatePaginatesReviewsAndComments(t *testing.T) {
	original := runBuckbotGitHubFn
	t.Cleanup(func() { runBuckbotGitHubFn = original })

	const head = "1234567890abcdef1234567890abcdef12345678"
	const finalMarker = "<!-- buckbot:final:head:summary -->"
	const inlineMarker = "<!-- buckbot:inline:head:finding -->"
	var calls int
	runBuckbotGitHubFn = func(_ context.Context, args []string, input []byte) ([]byte, error) {
		calls++
		if strings.Join(args, " ") != "api graphql --input -" {
			t.Fatalf("GraphQL args = %v", args)
		}
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(input, &request); err != nil {
			t.Fatal(err)
		}
		switch calls {
		case 1:
			if request.Variables["after"] != nil {
				t.Fatalf("first review cursor = %#v", request.Variables["after"])
			}
			return []byte(`{"data":{"viewer":{"login":"buckbot"},"repository":{"pullRequest":{` +
				`"id":"PR","headRefOid":"` + head + `","reviews":{` +
				`"pageInfo":{"hasNextPage":true,"endCursor":"reviews-2"},` +
				`"nodes":[{"id":"forged","author":{"login":"attacker"},"body":"` + finalMarker + `",` +
				`"commit":{"oid":"` + head + `"},"comments":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}]}` +
				`}}}}`), nil
		case 2:
			if request.Variables["after"] != "reviews-2" {
				t.Fatalf("second review cursor = %#v", request.Variables["after"])
			}
			return []byte(`{"data":{"viewer":{"login":"buckbot"},"repository":{"pullRequest":{` +
				`"id":"PR","headRefOid":"` + head + `","reviews":{` +
				`"pageInfo":{"hasNextPage":false,"endCursor":""},` +
				`"nodes":[{"id":"review","author":{"login":"buckbot"},"body":"` + finalMarker + `\nsummary",` +
				`"commit":{"oid":"` + head + `"},"comments":{"pageInfo":{"hasNextPage":true,"endCursor":"comments-2"},"nodes":[]}}]}` +
				`}}}}`), nil
		case 3:
			if request.Variables["id"] != "review" || request.Variables["after"] != "comments-2" {
				t.Fatalf("comment variables = %#v", request.Variables)
			}
			return []byte(`{"data":{"viewer":{"login":"buckbot"},"node":{` +
				`"id":"review","author":{"login":"buckbot"},"commit":{"oid":"` + head + `"},` +
				`"comments":{"pageInfo":{"hasNextPage":false,"endCursor":""},` +
				`"nodes":[{"body":"` + inlineMarker + `\nfinding"}]}` +
				`}}}`), nil
		default:
			t.Fatalf("calls = %d, want 3", calls)
			return nil, nil
		}
	}

	state, err := readBuckbotFinalReviewState(context.Background(), gitwatcher.PullRequestEvent{
		Repository: "owner/repo",
		Number:     42,
		HeadSHA:    head,
	}, finalMarker, []string{inlineMarker})
	if err != nil {
		t.Fatalf("readBuckbotFinalReviewState() error = %v", err)
	}
	if !state.complete(finalMarker, []string{inlineMarker}) {
		t.Fatalf("state = %#v, want complete", state)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want complete nested pagination", calls)
	}
}

func TestPostBuckbotReviewPayloadTreatsResponseLossAfterSuccessAsComplete(t *testing.T) {
	original := runBuckbotGitHubFn
	t.Cleanup(func() { runBuckbotGitHubFn = original })

	const head = "1234567890abcdef1234567890abcdef12345678"
	event := gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 42, HeadSHA: head}
	inlineComments := []map[string]any{{
		"path": "a.go",
		"line": 12,
		"side": "RIGHT",
		"body": "finding",
	}}
	finalMarker := buckbotFinalReviewMarker(event, "summary", inlineComments)
	inlineMarker := buckbotInlineReviewMarker(event, finalMarker, inlineComments[0])
	review := finalMarker + "\nsummary"
	inlineComments[0]["body"] = inlineMarker + "\nfinding"

	var calls int
	runBuckbotGitHubFn = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		calls++
		switch calls {
		case 1:
			return emptyBuckbotReviewStateResponse(head), nil
		case 2:
			if len(args) < 2 || args[1] != "repos/owner/repo/pulls/42/reviews" {
				t.Fatalf("post args = %v", args)
			}
			return []byte("connection reset after write"), errors.New("connection reset")
		case 3:
			return completeBuckbotReviewStateResponse(head, finalMarker, inlineMarker), nil
		default:
			t.Fatalf("calls = %d, want precheck, post, and postcheck", calls)
			return nil, nil
		}
	}

	if err := postBuckbotReviewPayload(context.Background(), event, review, inlineComments); err != nil {
		t.Fatalf("postBuckbotReviewPayload() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want no duplicate fallback post", calls)
	}
}

func TestPostBuckbotReviewPayloadResumesMissingInlineWithoutSummaryDuplicate(t *testing.T) {
	original := runBuckbotGitHubFn
	t.Cleanup(func() { runBuckbotGitHubFn = original })

	const head = "1234567890abcdef1234567890abcdef12345678"
	event := gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 42, HeadSHA: head}
	inlineComments := []map[string]any{{
		"path": "a.go",
		"line": 12,
		"side": "RIGHT",
		"body": "finding",
	}}
	finalMarker := buckbotFinalReviewMarker(event, "summary", inlineComments)
	inlineMarker := buckbotInlineReviewMarker(event, finalMarker, inlineComments[0])
	review := finalMarker + "\nsummary"
	inlineComments[0]["body"] = inlineMarker + "\nfinding"

	var calls int
	runBuckbotGitHubFn = func(_ context.Context, args []string, input []byte) ([]byte, error) {
		calls++
		switch calls {
		case 1:
			return completeBuckbotReviewStateResponse(head, finalMarker, ""), nil
		case 2:
			if len(args) < 2 || args[1] != "repos/owner/repo/pulls/42/reviews" {
				t.Fatalf("post args = %v", args)
			}
			var payload struct {
				Body     string           `json:"body"`
				Comments []map[string]any `json:"comments"`
			}
			if err := json.Unmarshal(input, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Body != "" || len(payload.Comments) != 1 {
				t.Fatalf("recovery payload = %#v, want missing inline only", payload)
			}
			return []byte(`{"id":123}`), nil
		default:
			t.Fatalf("calls = %d, want state and recovery post", calls)
			return nil, nil
		}
	}

	if err := postBuckbotReviewPayload(context.Background(), event, review, inlineComments); err != nil {
		t.Fatalf("postBuckbotReviewPayload() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want no duplicate summary", calls)
	}
}

func emptyBuckbotReviewStateResponse(head string) []byte {
	return []byte(`{"data":{"viewer":{"login":"buckbot"},"repository":{"pullRequest":{` +
		`"id":"PR","headRefOid":"` + head + `","reviews":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}` +
		`}}}}`)
}

func completeBuckbotReviewStateResponse(head, finalMarker, inlineMarker string) []byte {
	comments := ""
	if inlineMarker != "" {
		comments = `{"body":"` + inlineMarker + `\nfinding"}`
	}
	return []byte(`{"data":{"viewer":{"login":"buckbot"},"repository":{"pullRequest":{` +
		`"id":"PR","headRefOid":"` + head + `","reviews":{` +
		`"pageInfo":{"hasNextPage":false,"endCursor":""},` +
		`"nodes":[{"id":"review","author":{"login":"buckbot"},"body":"` + finalMarker + `\nsummary",` +
		`"commit":{"oid":"` + head + `"},"comments":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[` + comments + `]}}]}` +
		`}}}}`)
}
