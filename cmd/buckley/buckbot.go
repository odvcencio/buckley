package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/gitwatcher"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/transparency"
)

var postBuckbotReviewPayloadFn = postBuckbotReviewPayload
var runBuckbotGitHubFn = runBuckbotGitHub

type buckbotReviewer func(context.Context, gitwatcher.PullRequestEvent) (string, float64, error)
type buckbotPoster func(context.Context, gitwatcher.PullRequestEvent, string) error
type buckbotSalvager func(gitwatcher.PullRequestEvent, string, error) (string, error)

type buckbotReviewIntake struct {
	Model           string
	ReasoningEffort string
	SizeClass       string
	BudgetUSD       float64
}

const (
	buckbotReviewAttemptTimeout = defaultReviewTimeout
	buckbotPostAttemptTimeout   = time.Minute
	buckbotInitialRetryDelay    = 15 * time.Second
	buckbotMaxRetryDelay        = 5 * time.Minute
	buckbotMaxReviewAttempts    = 6
	buckbotMaxPostAttempts      = 8
)

type buckbotActiveReview struct {
	revision string
	cancel   context.CancelFunc
}

type buckbotService struct {
	cfg      config.BuckbotConfig
	review   buckbotReviewer
	post     buckbotPoster
	announce buckbotPoster
	mu       sync.Mutex
	seen     map[string]struct{}
	active   map[string]buckbotActiveReview
	spentUSD float64
	reserved float64
	sleep    func(context.Context, time.Duration) error
	salvage  buckbotSalvager
}

func newBuckbotService(cfg config.BuckbotConfig, review buckbotReviewer, post buckbotPoster) *buckbotService {
	return &buckbotService{
		cfg:     cfg,
		review:  review,
		post:    post,
		seen:    make(map[string]struct{}),
		active:  make(map[string]buckbotActiveReview),
		sleep:   waitForBuckbotRetry,
		salvage: saveBuckbotSalvage,
	}
}

func (s *buckbotService) handle(event gitwatcher.PullRequestEvent) {
	key := fmt.Sprintf("%s#%d@%s", event.Repository, event.Number, event.HeadSHA)
	pullRequestKey := fmt.Sprintf("%s#%d", event.Repository, event.Number)
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	_, completed := s.seen[key]
	if completed {
		s.mu.Unlock()
		cancel()
		return
	}
	if current, running := s.active[pullRequestKey]; running {
		if current.revision == key {
			s.mu.Unlock()
			cancel()
			return
		}
		current.cancel()
	}
	s.active[pullRequestKey] = buckbotActiveReview{revision: key, cancel: cancel}
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		if current, ok := s.active[pullRequestKey]; ok && current.revision == key {
			delete(s.active, pullRequestKey)
		}
		s.mu.Unlock()
	}()

	for attempt := 0; attempt < buckbotMaxReviewAttempts; attempt++ {
		if !s.reserveReviewBudget() {
			slog.Warn("Buckbot monthly budget cannot reserve another review", "repository", event.Repository, "pr", event.Number)
			return
		}
		if attempt == 0 && s.announce != nil {
			announceCtx, announceCancel := context.WithTimeout(ctx, buckbotPostAttemptTimeout)
			if err := s.announce(announceCtx, event, ""); err != nil {
				slog.Warn("Buckbot could not post review-started comment", "repository", event.Repository, "pr", event.Number, "head", event.HeadSHA, "error", err)
			}
			announceCancel()
		}
		attemptCtx, attemptCancel := context.WithTimeout(ctx, buckbotReviewAttemptTimeout)
		review, cost, err := s.review(attemptCtx, event)
		attemptCancel()
		s.settleReviewBudget(cost)

		if err != nil {
			if strings.TrimSpace(review) != "" {
				path, salvageErr := s.salvage(event, review, err)
				if salvageErr != nil {
					slog.Error("Buckbot could not persist incomplete review", "repository", event.Repository, "pr", event.Number, "head", event.HeadSHA, "error", salvageErr)
				} else {
					slog.Warn("Buckbot persisted incomplete review", "repository", event.Repository, "pr", event.Number, "head", event.HeadSHA, "path", path)
				}
			}
			if cost == 0 && isRetryableBuckbotError(err) && attempt+1 < buckbotMaxReviewAttempts {
				delay := buckbotRetryDelay(attempt, err)
				slog.Warn("Buckbot review retry scheduled", "repository", event.Repository, "pr", event.Number, "head", event.HeadSHA, "delay", delay, "error", err)
				if s.sleep(ctx, delay) == nil {
					continue
				}
				return
			}
			slog.Warn("Buckbot review failed", "repository", event.Repository, "pr", event.Number, "head", event.HeadSHA, "cost_usd", cost, "error", err)
			return
		}
		if strings.TrimSpace(review) == "" {
			slog.Warn("Buckbot review was empty", "repository", event.Repository, "pr", event.Number, "head", event.HeadSHA)
			return
		}
		if cost > s.cfg.PerReviewBudgetUSD {
			slog.Warn("Buckbot review exceeded per-review budget", "repository", event.Repository, "pr", event.Number, "cost_usd", cost, "budget_usd", s.cfg.PerReviewBudgetUSD)
			return
		}
		if !s.isCurrentRevision(pullRequestKey, key) {
			slog.Info("Buckbot discarded stale review", "repository", event.Repository, "pr", event.Number, "head", event.HeadSHA)
			return
		}

		for postAttempt := 0; postAttempt < buckbotMaxPostAttempts; postAttempt++ {
			if !s.isCurrentRevision(pullRequestKey, key) {
				return
			}
			postCtx, postCancel := context.WithTimeout(ctx, buckbotPostAttemptTimeout)
			err := s.post(postCtx, event, review)
			postCancel()
			if err == nil {
				s.mu.Lock()
				if current, ok := s.active[pullRequestKey]; ok && current.revision == key {
					s.seen[key] = struct{}{}
				}
				s.mu.Unlock()
				return
			}
			if postAttempt+1 == buckbotMaxPostAttempts {
				slog.Warn("Buckbot post retries exhausted", "repository", event.Repository, "pr", event.Number, "head", event.HeadSHA, "attempts", buckbotMaxPostAttempts, "error", err)
				return
			}
			delay := buckbotRetryDelay(postAttempt, err)
			slog.Warn("Buckbot post retry scheduled", "repository", event.Repository, "pr", event.Number, "head", event.HeadSHA, "delay", delay, "error", err)
			if s.sleep(ctx, delay) != nil {
				return
			}
		}
	}
	slog.Warn("Buckbot review retries exhausted", "repository", event.Repository, "pr", event.Number, "head", event.HeadSHA, "attempts", buckbotMaxReviewAttempts)
}

func (s *buckbotService) isCurrentRevision(pullRequestKey, revision string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.active[pullRequestKey]
	return ok && current.revision == revision
}

func (s *buckbotService) reserveReviewBudget() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	reservation := s.cfg.PerReviewBudgetUSD
	if s.spentUSD+s.reserved+reservation > s.cfg.MonthlyBudgetUSD {
		return false
	}
	s.reserved += reservation
	return true
}

func (s *buckbotService) settleReviewBudget(cost float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserved -= s.cfg.PerReviewBudgetUSD
	if s.reserved < 0 {
		s.reserved = 0
	}
	if cost > 0 {
		s.spentUSD += cost
	}
}

func isRetryableBuckbotError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var apiErr *model.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

func buckbotRetryDelay(attempt int, err error) time.Duration {
	delay := buckbotInitialRetryDelay
	for i := 0; i < attempt && delay < buckbotMaxRetryDelay; i++ {
		delay = min(delay*2, buckbotMaxRetryDelay)
	}
	var apiErr *model.APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > delay {
		return apiErr.RetryAfter
	}
	return delay
}

func waitForBuckbotRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func runBuckbotCommand(args []string) error {
	_ = args
	return withExitCode(fmt.Errorf(
		"automatic Buckbot webhook posting is retired; use `buckley review-pr <PR> -post` for an on-demand GitHub review",
	), 2)
}

func newBuckbotReviewer(botCfg config.BuckbotConfig, costStore *storage.Store) buckbotReviewer {
	return func(ctx context.Context, event gitwatcher.PullRequestEvent) (string, float64, error) {
		cfg, mgr, store, err := initDependenciesFn()
		if store != nil {
			defer func() { _ = store.Close() }()
		}
		if err != nil {
			return "", 0, fmt.Errorf("init dependencies: %w", err)
		}
		cfgCopy := *cfg
		cfgCopy.Buckbot = botCfg
		cfgCopy.Models.Review = botCfg.Model
		runtime, err := newReviewCommandRuntime(&cfgCopy, mgr)
		if err != nil {
			return "", 0, err
		}
		criticModel := strings.TrimSpace(botCfg.CriticModel)
		ref := fmt.Sprintf("https://github.com/%s/pull/%d", event.Repository, event.Number)
		result, _, reviewErr := runPRReviewWithOptions(ctx, ref, runtime.framework, runtime.policy)
		entries := runtime.ledger.Entries()
		cost := runtime.ledger.SessionTotal()
		partialReview := ""
		if result != nil {
			partialReview = result.reviewText
		}
		if len(entries) > 0 {
			if err := saveBuckbotSpend(costStore, event, botCfg.Model, entries); err != nil {
				return partialReview, cost, err
			}
		}
		if reviewErr != nil {
			return partialReview, cost, reviewErr
		}
		modelLabel := botCfg.Model
		if criticModel != "" && criticModel != botCfg.Model {
			modelLabel += " + " + criticModel + " critic"
		}
		return appendBuckbotCostFooter(partialReview, modelLabel, runtime.ledger.Summary(), botCfg.PerReviewBudgetUSD), cost, nil
	}
}

func appendBuckbotCostFooter(review, modelID string, summary transparency.CostSummary, budgetUSD float64) string {
	review = strings.TrimSpace(review)
	if review == "" {
		return ""
	}
	return fmt.Sprintf(
		"%s\n\n---\n_Buckbot · `%s` · $%.4f / $%.2f · %s input + %s output tokens · %d model call%s_",
		review,
		modelID,
		summary.SessionCost,
		budgetUSD,
		formatBuckbotCount(summary.SessionTokens.Input),
		formatBuckbotCount(summary.SessionTokens.Output),
		summary.InvocationCount,
		func() string {
			if summary.InvocationCount == 1 {
				return ""
			}
			return "s"
		}(),
	)
}

func formatBuckbotCount(value int) string {
	if value < 1_000 {
		return fmt.Sprintf("%d", value)
	}
	if value < 999_950 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(value)/1_000), ".0") + "k"
	}
	return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
}

func saveBuckbotSalvage(event gitwatcher.PullRequestEvent, review string, cause error) (string, error) {
	dir := filepath.Join(".buckley", "buckbot", "salvage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create Buckbot salvage directory: %w", err)
	}
	sha := strings.TrimSpace(event.HeadSHA)
	if len(sha) > 12 {
		sha = sha[:12]
	}
	name := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(event.Repository)
	filename := fmt.Sprintf("%s-pr-%d-%s-%s.md", name, event.Number, sha, time.Now().UTC().Format("20060102T150405.000000000Z"))
	path := filepath.Join(dir, filename)
	body := strings.TrimSpace(review)
	if cause != nil && !strings.Contains(body, "Incomplete review") {
		body = fmt.Sprintf("> [!WARNING]\n> **Incomplete review — salvaged from completed work.**\n> Cause: %s\n\n%s", cause, body)
	}
	tmp, err := os.CreateTemp(dir, ".salvage-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create Buckbot salvage temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("secure Buckbot salvage temp file: %w", err)
	}
	if _, err := tmp.WriteString(body + "\n"); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write Buckbot salvage: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("sync Buckbot salvage: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close Buckbot salvage: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("publish Buckbot salvage: %w", err)
	}
	return path, nil
}

func saveBuckbotSpend(store *storage.Store, event gitwatcher.PullRequestEvent, modelID string, entries []transparency.CostEntry) error {
	if store == nil {
		return fmt.Errorf("buckbot cost store required")
	}
	now := time.Now().UTC()
	sessionID := fmt.Sprintf("buckbot-%d", now.UnixNano())
	if err := store.CreateSession(&storage.Session{
		ID: sessionID, Principal: "buckbot", GitRepo: event.Repository, Model: modelID,
		CreatedAt: now, LastActive: now, Status: storage.SessionStatusCompleted,
	}); err != nil {
		return fmt.Errorf("create Buckbot cost session: %w", err)
	}
	for _, entry := range entries {
		if err := store.SaveAPICall(&storage.APICall{
			SessionID: sessionID, Model: entry.Model, PromptTokens: entry.Tokens.Input,
			CompletionTokens: entry.Tokens.Output, Cost: entry.Cost, Timestamp: now,
		}); err != nil {
			return fmt.Errorf("save Buckbot API cost: %w", err)
		}
	}
	return nil
}

func postBuckbotReview(ctx context.Context, event gitwatcher.PullRequestEvent, review string) error {
	if err := validateBuckbotReviewEvent(event); err != nil {
		return err
	}
	parsed := commands.ParseReview(review)
	review = formatBuckbotGitHubReview(parsed, review, event.HeadSHA)
	inlineComments := make([]map[string]any, 0, len(parsed.Findings))
	for _, finding := range parsed.Findings {
		if finding.Line <= 0 || strings.TrimSpace(finding.File) == "" {
			continue
		}
		inlineComments = append(inlineComments, map[string]any{
			"path": finding.File,
			"line": finding.Line,
			"side": "RIGHT",
			"body": formatBuckbotInlineFinding(finding),
		})
	}
	finalMarker := buckbotFinalReviewMarker(event, review, inlineComments)
	review = finalMarker + "\n" + review
	for _, comment := range inlineComments {
		marker := buckbotInlineReviewMarker(event, finalMarker, comment)
		comment["body"] = marker + "\n" + fmt.Sprint(comment["body"])
	}
	if err := postBuckbotReviewPayloadFn(ctx, event, review, inlineComments); err != nil {
		if len(inlineComments) == 0 {
			return err
		}
		slog.Warn("Buckbot inline review batch was rejected; preserving the summary and retrying each line comment", "repository", event.Repository, "pr", event.Number, "error", err)
		if fallbackErr := postBuckbotReviewPayloadFn(ctx, event, review, nil); fallbackErr != nil {
			return fmt.Errorf("%v; summary fallback: %w", err, fallbackErr)
		}
		for _, comment := range inlineComments {
			if commentErr := postBuckbotReviewPayloadFn(ctx, event, "", []map[string]any{comment}); commentErr != nil {
				slog.Warn("Buckbot skipped one rejected inline comment", "repository", event.Repository, "pr", event.Number, "path", comment["path"], "line", comment["line"], "error", commentErr)
			}
		}
	}
	return nil
}

func formatBuckbotGitHubReview(parsed *commands.ParsedReview, review, headSHA string) string {
	review = strings.TrimSpace(review)
	if review == "" || parsed == nil {
		return review
	}
	revision := strings.TrimSpace(headSHA)
	if len(revision) > 12 {
		revision = revision[:12]
	}
	grade := string(parsed.Grade)
	if grade == "" {
		grade = "ungraded"
	}
	verdict := "NEEDS FOLLOW-UP"
	noteType := "NOTE"
	if parsed.Approved {
		verdict = "READY TO MERGE"
		noteType = "TIP"
	} else if parsed.HasBlockers() {
		verdict = "CHANGES REQUESTED"
		noteType = "CAUTION"
	}
	review = collapseBuckbotEvidence(review)
	return fmt.Sprintf(
		"> [!%s]\n> **Buckbot · Grade %s · %s**\n> Reviewed commit `%s`. Line comments contain only demonstrated findings.\n\n%s",
		noteType,
		grade,
		verdict,
		revision,
		review,
	)
}

func collapseBuckbotEvidence(review string) string {
	for _, section := range []struct {
		heading string
		label   string
	}{
		{heading: "Coverage", label: "Full changed-file coverage ledger"},
		{heading: "Invariant Audit", label: "Cross-file invariant audit"},
		{heading: "Falsification", label: "Strongest-failure falsification"},
	} {
		review = collapseBuckbotSection(review, section.heading, section.label)
	}
	return review
}

func collapseBuckbotSection(review, heading, label string) string {
	marker := "## " + heading
	start := strings.Index(review, marker)
	if start < 0 || (start > 0 && review[start-1] != '\n') {
		return review
	}
	contentStart := start + len(marker)
	end := len(review)
	if next := strings.Index(review[contentStart:], "\n## "); next >= 0 {
		end = contentStart + next
	}
	section := strings.TrimSpace(review[start:end])
	replacement := fmt.Sprintf("<details>\n<summary><strong>%s</strong></summary>\n\n%s\n\n</details>", label, section)
	return review[:start] + replacement + review[end:]
}

func postBuckbotReviewPayload(ctx context.Context, event gitwatcher.PullRequestEvent, review string, inlineComments []map[string]any) error {
	if err := validateBuckbotReviewEvent(event); err != nil {
		return err
	}
	finalMarker := leadingBuckbotMarker(review, buckbotFinalReviewMarkerPrefix)
	inlineMarkers := buckbotInlineMarkers(inlineComments)
	effectiveReview := review
	effectiveComments := inlineComments
	if finalMarker != "" || len(inlineMarkers) > 0 {
		state, err := readBuckbotFinalReviewState(ctx, event, finalMarker, inlineMarkers)
		if err != nil {
			return fmt.Errorf("check existing GitHub review: %w", err)
		}
		if state.complete(finalMarker, inlineMarkers) {
			return nil
		}
		if state.SummaryFound {
			effectiveReview = ""
		}
		effectiveComments = filterMissingBuckbotInlineComments(inlineComments, state)
	}

	restErr := postBuckbotReviewPayloadREST(ctx, event, effectiveReview, effectiveComments)
	if restErr == nil {
		return nil
	}
	if finalMarker != "" || len(inlineMarkers) > 0 {
		state, stateErr := readBuckbotFinalReviewState(ctx, event, finalMarker, inlineMarkers)
		if stateErr != nil {
			return fmt.Errorf("%v; post-state check: %w", restErr, stateErr)
		}
		if state.complete(finalMarker, inlineMarkers) {
			return nil
		}
		if state.SummaryFound {
			effectiveReview = ""
		}
		effectiveComments = filterMissingBuckbotInlineComments(inlineComments, state)
	}
	if !isGitHubRESTThrottleError(restErr) {
		return restErr
	}

	slog.Warn("Buckbot REST post was throttled; retrying through GraphQL", "repository", event.Repository, "pr", event.Number, "error", restErr)
	graphErr := postBuckbotReviewPayloadGraphQL(ctx, event, effectiveReview, effectiveComments)
	if graphErr == nil {
		return nil
	}
	if finalMarker != "" || len(inlineMarkers) > 0 {
		state, stateErr := readBuckbotFinalReviewState(ctx, event, finalMarker, inlineMarkers)
		if stateErr != nil {
			return fmt.Errorf("%v; GraphQL fallback: %v; post-state check: %w", restErr, graphErr, stateErr)
		}
		if state.complete(finalMarker, inlineMarkers) {
			return nil
		}
	}
	return fmt.Errorf("%v; GraphQL fallback: %w", restErr, graphErr)
}

func postBuckbotReviewPayloadREST(ctx context.Context, event gitwatcher.PullRequestEvent, review string, inlineComments []map[string]any) error {
	if err := validateBuckbotReviewEvent(event); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"body":      review,
		"commit_id": event.HeadSHA,
		"event":     "COMMENT",
		"comments":  inlineComments,
	})
	if err != nil {
		return fmt.Errorf("encode GitHub review: %w", err)
	}
	output, err := runBuckbotGitHubFn(ctx, []string{
		"api",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", event.Repository, event.Number),
		"--method", "POST",
		"--input", "-",
	}, payload)
	if err != nil {
		return fmt.Errorf("post GitHub review: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

const buckbotReviewPostIdentityQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) { id headRefOid }
  }
}`

const buckbotReviewPostMutation = `mutation(
  $pullRequestId: ID!,
  $commitOID: GitObjectID!,
  $body: String,
  $threads: [DraftPullRequestReviewThread!]
) {
  addPullRequestReview(input: {
    pullRequestId: $pullRequestId,
    commitOID: $commitOID,
    body: $body,
    event: COMMENT,
    threads: $threads
  }) {
    pullRequestReview { id url }
  }
}`

func postBuckbotReviewPayloadGraphQL(ctx context.Context, event gitwatcher.PullRequestEvent, review string, inlineComments []map[string]any) error {
	if err := validateBuckbotReviewEvent(event); err != nil {
		return err
	}
	owner, repo, err := splitBuckbotRepository(event.Repository)
	if err != nil {
		return err
	}
	output, err := runBuckbotGraphQL(ctx, buckbotReviewPostIdentityQuery, map[string]any{
		"owner":  owner,
		"name":   repo,
		"number": event.Number,
	})
	if err != nil {
		return fmt.Errorf("read GitHub review target: %w", err)
	}
	var identityResponse struct {
		Data struct {
			Repository *struct {
				PullRequest *struct {
					ID         string `json:"id"`
					HeadRefOID string `json:"headRefOid"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(output, &identityResponse); err != nil {
		return fmt.Errorf("decode GitHub review target: %w", err)
	}
	if len(identityResponse.Errors) > 0 {
		return fmt.Errorf("read GitHub review target: %s", identityResponse.Errors[0].Message)
	}
	if identityResponse.Data.Repository == nil || identityResponse.Data.Repository.PullRequest == nil {
		return fmt.Errorf("read GitHub review target: pull request not found")
	}
	pr := identityResponse.Data.Repository.PullRequest
	if pr.HeadRefOID != event.HeadSHA {
		return fmt.Errorf("post GitHub review: head changed from %s to %s", event.HeadSHA, pr.HeadRefOID)
	}

	threads, err := buckbotGraphQLReviewThreads(inlineComments)
	if err != nil {
		return err
	}
	output, err = runBuckbotGraphQL(ctx, buckbotReviewPostMutation, map[string]any{
		"pullRequestId": pr.ID,
		"commitOID":     event.HeadSHA,
		"body":          review,
		"threads":       threads,
	})
	if err != nil {
		return fmt.Errorf("post GitHub review through GraphQL: %w", err)
	}
	var mutationResponse struct {
		Data struct {
			AddPullRequestReview *struct {
				PullRequestReview *struct {
					ID string `json:"id"`
				} `json:"pullRequestReview"`
			} `json:"addPullRequestReview"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(output, &mutationResponse); err != nil {
		return fmt.Errorf("decode GitHub review response: %w", err)
	}
	if len(mutationResponse.Errors) > 0 {
		return fmt.Errorf("post GitHub review through GraphQL: %s", mutationResponse.Errors[0].Message)
	}
	if mutationResponse.Data.AddPullRequestReview == nil ||
		mutationResponse.Data.AddPullRequestReview.PullRequestReview == nil ||
		strings.TrimSpace(mutationResponse.Data.AddPullRequestReview.PullRequestReview.ID) == "" {
		return fmt.Errorf("post GitHub review through GraphQL: response omitted the review ID")
	}
	return nil
}

func buckbotGraphQLReviewThreads(inlineComments []map[string]any) ([]map[string]any, error) {
	threads := make([]map[string]any, 0, len(inlineComments))
	for _, comment := range inlineComments {
		path, pathOK := comment["path"].(string)
		body, bodyOK := comment["body"].(string)
		line, lineOK := comment["line"].(int)
		side, sideOK := comment["side"].(string)
		if !pathOK || !bodyOK || !lineOK || !sideOK || path == "" || body == "" || line <= 0 {
			return nil, fmt.Errorf("post GitHub review through GraphQL: invalid inline comment")
		}
		threads = append(threads, map[string]any{
			"path": path,
			"line": line,
			"side": side,
			"body": body,
		})
	}
	return threads, nil
}

func isGitHubRESTThrottleError(err error) bool {
	if err == nil {
		return false
	}
	detail := strings.ToLower(err.Error())
	rateLimited := strings.Contains(detail, "rate limit") ||
		strings.Contains(detail, "secondary rate") ||
		strings.Contains(detail, "abuse detection")
	return rateLimited && (strings.Contains(detail, "http 403") || strings.Contains(detail, "http 429"))
}

func isRetryableBuckbotPostError(err error) bool {
	if err == nil {
		return false
	}
	if isRetryableBuckbotError(err) || isGitHubRESTThrottleError(err) {
		return true
	}
	detail := strings.ToLower(err.Error())
	for _, status := range []string{"http 429", "http 500", "http 502", "http 503", "http 504"} {
		if strings.Contains(detail, status) {
			return true
		}
	}
	return strings.Contains(detail, "temporarily unavailable")
}

func postBuckbotReviewStarted(cfg config.BuckbotConfig) buckbotPoster {
	return func(ctx context.Context, event gitwatcher.PullRequestEvent, _ string) error {
		return postBuckbotReviewLifecycle(ctx, event, buckbotReviewIntake{
			Model:     cfg.Model,
			BudgetUSD: cfg.PerReviewBudgetUSD,
		})
	}
}

const buckbotReviewLifecycleQuery = `query($owner: String!, $name: String!, $number: Int!, $after: String) {
  viewer { login }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      id
      headRefOid
      comments(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          author { login }
          body
        }
      }
    }
  }
}`

const buckbotReviewLifecycleMutation = `mutation($subjectId: ID!, $body: String!, $postIntake: Boolean!) {
  eyes: addReaction(input: {subjectId: $subjectId, content: EYES}) {
    reaction { content }
  }
  intake: addComment(input: {subjectId: $subjectId, body: $body}) @include(if: $postIntake) {
    commentEdge { node { id } }
  }
}`

func postBuckbotReviewLifecycle(ctx context.Context, event gitwatcher.PullRequestEvent, intake buckbotReviewIntake) error {
	if err := validateBuckbotReviewEvent(event); err != nil {
		return fmt.Errorf("post GitHub review intake: %w", err)
	}
	owner, repo, err := splitBuckbotRepository(event.Repository)
	if err != nil {
		return err
	}

	marker := commands.BuckbotReviewIntakeMarker(event.HeadSHA)
	prID, postIntake, err := readBuckbotReviewLifecycleState(ctx, event, owner, repo, marker)
	if err != nil {
		return err
	}
	output, err := runBuckbotGraphQL(ctx, buckbotReviewLifecycleMutation, map[string]any{
		"subjectId":  prID,
		"body":       formatBuckbotReviewIntake(marker, event.HeadSHA, intake),
		"postIntake": postIntake,
	})
	if err != nil {
		return fmt.Errorf("post GitHub review intake: %w", err)
	}
	var mutationResponse struct {
		Data struct {
			Eyes *struct {
				Reaction *struct {
					Content string `json:"content"`
				} `json:"reaction"`
			} `json:"eyes"`
			Intake *struct {
				CommentEdge *struct {
					Node *struct {
						ID string `json:"id"`
					} `json:"node"`
				} `json:"commentEdge"`
			} `json:"intake"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(output, &mutationResponse); err != nil {
		return fmt.Errorf("decode GitHub review intake response: %w", err)
	}
	if len(mutationResponse.Errors) > 0 {
		return fmt.Errorf("post GitHub review intake: %s", mutationResponse.Errors[0].Message)
	}
	if mutationResponse.Data.Eyes == nil || mutationResponse.Data.Eyes.Reaction == nil ||
		mutationResponse.Data.Eyes.Reaction.Content != "EYES" {
		return fmt.Errorf("post GitHub review intake: response omitted the EYES reaction")
	}
	if postIntake && (mutationResponse.Data.Intake == nil ||
		mutationResponse.Data.Intake.CommentEdge == nil ||
		mutationResponse.Data.Intake.CommentEdge.Node == nil ||
		strings.TrimSpace(mutationResponse.Data.Intake.CommentEdge.Node.ID) == "") {
		return fmt.Errorf("post GitHub review intake: response omitted the intake comment ID")
	}
	return nil
}

func readBuckbotReviewLifecycleState(
	ctx context.Context,
	event gitwatcher.PullRequestEvent,
	owner string,
	repo string,
	marker string,
) (string, bool, error) {
	var prID string
	var viewerLogin string
	postIntake := true
	after := ""
	seenCursors := make(map[string]struct{})
	for {
		variables := map[string]any{
			"owner":  owner,
			"name":   repo,
			"number": event.Number,
			"after":  nil,
		}
		if after != "" {
			variables["after"] = after
		}
		output, err := runBuckbotGraphQL(ctx, buckbotReviewLifecycleQuery, variables)
		if err != nil {
			return "", false, fmt.Errorf("read GitHub review intake state: %w", err)
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
						Comments   struct {
							PageInfo struct {
								HasNextPage bool   `json:"hasNextPage"`
								EndCursor   string `json:"endCursor"`
							} `json:"pageInfo"`
							Nodes []struct {
								Author struct {
									Login string `json:"login"`
								} `json:"author"`
								Body string `json:"body"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(output, &response); err != nil {
			return "", false, fmt.Errorf("decode GitHub review intake state: %w", err)
		}
		if len(response.Errors) > 0 {
			return "", false, fmt.Errorf("read GitHub review intake state: %s", response.Errors[0].Message)
		}
		if response.Data.Repository == nil || response.Data.Repository.PullRequest == nil {
			return "", false, fmt.Errorf("read GitHub review intake state: pull request not found")
		}
		if response.Data.Viewer.Login == "" {
			return "", false, fmt.Errorf("read GitHub review intake state: authenticated viewer missing")
		}
		pr := response.Data.Repository.PullRequest
		if pr.HeadRefOID != event.HeadSHA {
			return "", false, fmt.Errorf("post GitHub review intake: head changed from %s to %s", event.HeadSHA, pr.HeadRefOID)
		}
		if prID == "" {
			prID = pr.ID
			viewerLogin = response.Data.Viewer.Login
		} else if pr.ID != prID {
			return "", false, fmt.Errorf("read GitHub review intake state: pull request identity changed during pagination")
		} else if response.Data.Viewer.Login != viewerLogin {
			return "", false, fmt.Errorf("read GitHub review intake state: authenticated viewer changed during pagination")
		}
		for _, comment := range pr.Comments.Nodes {
			if comment.Author.Login == viewerLogin && hasExactLeadingBuckbotMarker(comment.Body, marker) {
				postIntake = false
			}
		}
		if !pr.Comments.PageInfo.HasNextPage {
			return prID, postIntake, nil
		}
		cursor := pr.Comments.PageInfo.EndCursor
		if cursor == "" {
			return "", false, fmt.Errorf("read GitHub review intake state: pagination omitted the next cursor")
		}
		if _, duplicate := seenCursors[cursor]; duplicate {
			return "", false, fmt.Errorf("read GitHub review intake state: pagination repeated cursor %q", cursor)
		}
		seenCursors[cursor] = struct{}{}
		after = cursor
	}
}

func formatBuckbotReviewIntake(marker, headSHA string, intake buckbotReviewIntake) string {
	revision := strings.TrimSpace(headSHA)
	if len(revision) > 12 {
		revision = revision[:12]
	}
	var details []string
	if modelID := strings.TrimSpace(intake.Model); modelID != "" {
		details = append(details, "model `"+modelID+"`")
	}
	if sizeClass := strings.TrimSpace(intake.SizeClass); sizeClass != "" {
		details = append(details, strings.ToLower(sizeClass)+" review")
	}
	if effort := strings.TrimSpace(intake.ReasoningEffort); effort != "" {
		details = append(details, strings.ToLower(effort)+" reasoning")
	}
	if intake.BudgetUSD > 0 {
		details = append(details, fmt.Sprintf("$%.2f limit", intake.BudgetUSD))
	}
	suffix := ""
	if len(details) > 0 {
		suffix = "\n\n_" + strings.Join(details, " · ") + "_"
	}
	return fmt.Sprintf(
		"%s\n## Buckbot review started\n\nI captured commit `%s`. I will inspect the changed surface and check concrete risks against CI.\n\nA deeper review will follow with the verdict, evidence, and demonstrated line findings.%s",
		marker,
		revision,
		suffix,
	)
}

func splitBuckbotRepository(repository string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(repository), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("post GitHub review: invalid repository %q", repository)
	}
	return parts[0], parts[1], nil
}

func runBuckbotGraphQL(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	payload, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return nil, err
	}
	return runBuckbotGitHubFn(ctx, []string{"api", "graphql", "--input", "-"}, payload)
}

func runBuckbotGitHub(ctx context.Context, args []string, input []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	if len(input) > 0 {
		cmd.Stdin = bytes.NewReader(input)
	}
	return cmd.CombinedOutput()
}

func formatBuckbotInlineFinding(finding commands.Finding) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<!-- buckbot:%s -->\n**%s · %s**\n\n%s", finding.ID, finding.Severity, finding.Title, finding.Evidence)
	if finding.Impact != "" {
		fmt.Fprintf(&sb, "\n\n**Impact:** %s", finding.Impact)
	}
	if finding.Fix != "" {
		fmt.Fprintf(&sb, "\n\n**Recommended change:** %s", finding.Fix)
	}
	if finding.SuggestedFix != "" {
		fmt.Fprintf(&sb, "\n\n```suggestion\n%s\n```", strings.TrimSpace(finding.SuggestedFix))
	}
	return sb.String()
}
