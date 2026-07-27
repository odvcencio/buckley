package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"m31labs.dev/buckley/pkg/gitwatcher"
)

const (
	buckbotFinalReviewMarkerPrefix  = "<!-- buckbot:final:"
	buckbotInlineReviewMarkerPrefix = "<!-- buckbot:inline:"
)

type buckbotFinalReviewState struct {
	SummaryFound bool
	InlineFound  map[string]struct{}
}

func validateBuckbotReviewEvent(event gitwatcher.PullRequestEvent) error {
	if strings.TrimSpace(event.Repository) == "" || event.Number <= 0 || strings.TrimSpace(event.HeadSHA) == "" {
		return fmt.Errorf("post GitHub review: incomplete pull request identity")
	}
	return nil
}

func (s buckbotFinalReviewState) complete(finalMarker string, inlineMarkers []string) bool {
	if finalMarker != "" && !s.SummaryFound {
		return false
	}
	for _, marker := range inlineMarkers {
		if _, found := s.InlineFound[marker]; !found {
			return false
		}
	}
	return true
}

func buckbotFinalReviewMarker(event gitwatcher.PullRequestEvent, review string, inlineComments []map[string]any) string {
	hash := sha256.New()
	writeBuckbotHashPart(hash, review)
	for _, comment := range inlineComments {
		writeBuckbotHashPart(hash, fmt.Sprint(comment["path"]))
		writeBuckbotHashPart(hash, fmt.Sprint(comment["line"]))
		writeBuckbotHashPart(hash, fmt.Sprint(comment["body"]))
	}
	return fmt.Sprintf("%s%s:%x -->", buckbotFinalReviewMarkerPrefix, event.HeadSHA, hash.Sum(nil)[:12])
}

func buckbotInlineReviewMarker(event gitwatcher.PullRequestEvent, finalMarker string, comment map[string]any) string {
	hash := sha256.New()
	writeBuckbotHashPart(hash, finalMarker)
	writeBuckbotHashPart(hash, fmt.Sprint(comment["path"]))
	writeBuckbotHashPart(hash, fmt.Sprint(comment["line"]))
	writeBuckbotHashPart(hash, fmt.Sprint(comment["body"]))
	return fmt.Sprintf("%s%s:%x -->", buckbotInlineReviewMarkerPrefix, event.HeadSHA, hash.Sum(nil)[:12])
}

func writeBuckbotHashPart(hash interface{ Write([]byte) (int, error) }, value string) {
	_, _ = hash.Write([]byte(strconv.Itoa(len(value))))
	_, _ = hash.Write([]byte{':'})
	_, _ = hash.Write([]byte(value))
}

func leadingBuckbotMarker(body, prefix string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, prefix) && strings.HasSuffix(line, " -->") {
		return line
	}
	return ""
}

func hasExactLeadingBuckbotMarker(body, marker string) bool {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	return strings.TrimSpace(line) == strings.TrimSpace(marker)
}

func buckbotInlineMarkers(inlineComments []map[string]any) []string {
	markers := make([]string, 0, len(inlineComments))
	for _, comment := range inlineComments {
		if body, ok := comment["body"].(string); ok {
			if marker := leadingBuckbotMarker(body, buckbotInlineReviewMarkerPrefix); marker != "" {
				markers = append(markers, marker)
			}
		}
	}
	return markers
}

func filterMissingBuckbotInlineComments(inlineComments []map[string]any, state buckbotFinalReviewState) []map[string]any {
	missing := make([]map[string]any, 0, len(inlineComments))
	for _, comment := range inlineComments {
		body, _ := comment["body"].(string)
		marker := leadingBuckbotMarker(body, buckbotInlineReviewMarkerPrefix)
		if marker != "" {
			if _, found := state.InlineFound[marker]; found {
				continue
			}
		}
		missing = append(missing, comment)
	}
	return missing
}

const buckbotFinalReviewStateQuery = `query(
  $owner: String!,
  $name: String!,
  $number: Int!,
  $after: String
) {
  viewer { login }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      id
      headRefOid
      reviews(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          author { login }
          body
          commit { oid }
          comments(first: 100) {
            pageInfo { hasNextPage endCursor }
            nodes { body }
          }
        }
      }
    }
  }
}`

const buckbotReviewCommentsQuery = `query($id: ID!, $after: String) {
  viewer { login }
  node(id: $id) {
    ... on PullRequestReview {
      author { login }
      commit { oid }
      comments(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes { body }
      }
    }
  }
}`

type buckbotGraphQLPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type buckbotGraphQLReviewComment struct {
	Body string `json:"body"`
}

type buckbotGraphQLReview struct {
	ID     string `json:"id"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Body   string `json:"body"`
	Commit *struct {
		OID string `json:"oid"`
	} `json:"commit"`
	Comments struct {
		PageInfo buckbotGraphQLPageInfo        `json:"pageInfo"`
		Nodes    []buckbotGraphQLReviewComment `json:"nodes"`
	} `json:"comments"`
}

func readBuckbotFinalReviewState(
	ctx context.Context,
	event gitwatcher.PullRequestEvent,
	finalMarker string,
	inlineMarkers []string,
) (buckbotFinalReviewState, error) {
	if err := validateBuckbotReviewEvent(event); err != nil {
		return buckbotFinalReviewState{}, err
	}
	owner, repo, err := splitBuckbotRepository(event.Repository)
	if err != nil {
		return buckbotFinalReviewState{}, err
	}
	state := buckbotFinalReviewState{InlineFound: make(map[string]struct{})}
	after := ""
	viewerLogin := ""
	pullRequestID := ""
	seenCursors := make(map[string]struct{})
	for {
		output, err := runBuckbotGraphQL(ctx, buckbotFinalReviewStateQuery, map[string]any{
			"owner":  owner,
			"name":   repo,
			"number": event.Number,
			"after":  nullableBuckbotCursor(after),
		})
		if err != nil {
			return state, fmt.Errorf("read posted Buckbot reviews: %w", err)
		}
		var response struct {
			Data struct {
				Viewer struct {
					Login string `json:"login"`
				} `json:"viewer"`
				Repository *struct {
					PullRequest *struct {
						ID         string `json:"id"`
						HeadRefOID string `json:"headRefOid"`
						Reviews    struct {
							PageInfo buckbotGraphQLPageInfo `json:"pageInfo"`
							Nodes    []buckbotGraphQLReview `json:"nodes"`
						} `json:"reviews"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(output, &response); err != nil {
			return state, fmt.Errorf("decode posted Buckbot reviews: %w", err)
		}
		if len(response.Errors) > 0 {
			return state, fmt.Errorf("read posted Buckbot reviews: %s", response.Errors[0].Message)
		}
		if response.Data.Viewer.Login == "" {
			return state, fmt.Errorf("read posted Buckbot reviews: authenticated viewer missing")
		}
		if response.Data.Repository == nil || response.Data.Repository.PullRequest == nil {
			return state, fmt.Errorf("read posted Buckbot reviews: pull request not found")
		}
		pr := response.Data.Repository.PullRequest
		if pr.HeadRefOID != event.HeadSHA {
			return state, fmt.Errorf("read posted Buckbot reviews: head changed from %s to %s", event.HeadSHA, pr.HeadRefOID)
		}
		if viewerLogin == "" {
			viewerLogin = response.Data.Viewer.Login
			pullRequestID = pr.ID
		} else if response.Data.Viewer.Login != viewerLogin || pr.ID != pullRequestID {
			return state, fmt.Errorf("read posted Buckbot reviews: identity changed during pagination")
		}

		for _, review := range pr.Reviews.Nodes {
			if review.Author.Login != viewerLogin || review.Commit == nil || review.Commit.OID != event.HeadSHA {
				continue
			}
			if finalMarker != "" && hasExactLeadingBuckbotMarker(review.Body, finalMarker) {
				state.SummaryFound = true
			}
			if len(inlineMarkers) == 0 {
				continue
			}
			collectBuckbotInlineMarkers(state.InlineFound, inlineMarkers, review.Comments.Nodes)
			if review.Comments.PageInfo.HasNextPage && !allBuckbotInlineMarkersFound(state.InlineFound, inlineMarkers) {
				if err := readRemainingBuckbotReviewComments(
					ctx,
					review,
					viewerLogin,
					event.HeadSHA,
					inlineMarkers,
					state.InlineFound,
				); err != nil {
					return state, err
				}
			}
		}
		if !pr.Reviews.PageInfo.HasNextPage {
			return state, nil
		}
		cursor := pr.Reviews.PageInfo.EndCursor
		if err := advanceBuckbotCursor(seenCursors, cursor, "review"); err != nil {
			return state, err
		}
		after = cursor
	}
}

func readRemainingBuckbotReviewComments(
	ctx context.Context,
	review buckbotGraphQLReview,
	viewerLogin string,
	headSHA string,
	expected []string,
	found map[string]struct{},
) error {
	after := review.Comments.PageInfo.EndCursor
	seenCursors := make(map[string]struct{})
	for {
		if err := advanceBuckbotCursor(seenCursors, after, "review comment"); err != nil {
			return err
		}
		output, err := runBuckbotGraphQL(ctx, buckbotReviewCommentsQuery, map[string]any{
			"id":    review.ID,
			"after": after,
		})
		if err != nil {
			return fmt.Errorf("read posted Buckbot review comments: %w", err)
		}
		var response struct {
			Data struct {
				Viewer struct {
					Login string `json:"login"`
				} `json:"viewer"`
				Node *buckbotGraphQLReview `json:"node"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(output, &response); err != nil {
			return fmt.Errorf("decode posted Buckbot review comments: %w", err)
		}
		if len(response.Errors) > 0 {
			return fmt.Errorf("read posted Buckbot review comments: %s", response.Errors[0].Message)
		}
		if response.Data.Viewer.Login != viewerLogin || response.Data.Node == nil ||
			response.Data.Node.Author.Login != viewerLogin ||
			response.Data.Node.Commit == nil || response.Data.Node.Commit.OID != headSHA {
			return fmt.Errorf("read posted Buckbot review comments: review identity changed during pagination")
		}
		collectBuckbotInlineMarkers(found, expected, response.Data.Node.Comments.Nodes)
		if !response.Data.Node.Comments.PageInfo.HasNextPage {
			return nil
		}
		after = response.Data.Node.Comments.PageInfo.EndCursor
	}
}

func allBuckbotInlineMarkersFound(found map[string]struct{}, expected []string) bool {
	for _, marker := range expected {
		if _, ok := found[marker]; !ok {
			return false
		}
	}
	return true
}

func collectBuckbotInlineMarkers(
	found map[string]struct{},
	expected []string,
	comments []buckbotGraphQLReviewComment,
) {
	for _, comment := range comments {
		for _, marker := range expected {
			if hasExactLeadingBuckbotMarker(comment.Body, marker) {
				found[marker] = struct{}{}
			}
		}
	}
}

func nullableBuckbotCursor(cursor string) any {
	if cursor == "" {
		return nil
	}
	return cursor
}

func advanceBuckbotCursor(seen map[string]struct{}, cursor, label string) error {
	if cursor == "" {
		return fmt.Errorf("read posted Buckbot reviews: %s pagination omitted the next cursor", label)
	}
	if _, duplicate := seen[cursor]; duplicate {
		return fmt.Errorf("read posted Buckbot reviews: %s pagination repeated cursor %q", label, cursor)
	}
	seen[cursor] = struct{}{}
	return nil
}
