package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/gitwatcher"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/transparency"
)

var buckbotLoadConfigFn = config.Load
var buckbotListenFn = http.ListenAndServe

type buckbotReviewer func(context.Context, gitwatcher.PullRequestEvent) (string, float64, error)
type buckbotPoster func(context.Context, gitwatcher.PullRequestEvent, string) error
type buckbotSalvager func(gitwatcher.PullRequestEvent, string, error) (string, error)

const (
	buckbotReviewAttemptTimeout = 5 * time.Minute
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
	fs := flag.NewFlagSet("buckbot", flag.ContinueOnError)
	bind := fs.String("bind", "", "address to bind (default: buckbot.webhook_bind or 127.0.0.1:8086)")
	secret := fs.String("secret", "", "shared webhook secret (overrides config)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := buckbotLoadConfigFn()
	if err != nil {
		return withExitCode(err, 2)
	}
	if !cfg.Buckbot.Enabled {
		return withExitCode(fmt.Errorf("buckbot disabled in config; enable buckbot.enabled to run this daemon"), 2)
	}
	addr := strings.TrimSpace(*bind)
	if addr == "" {
		addr = strings.TrimSpace(cfg.Buckbot.WebhookBind)
	}
	if addr == "" {
		addr = "127.0.0.1:8086"
	}
	webhookSecret := strings.TrimSpace(chooseSecret(*secret, cfg.Buckbot.Secret))
	if !isLoopbackAddress(addr) && webhookSecret == "" {
		return withExitCode(fmt.Errorf("refusing to bind buckbot to %q without a shared secret", addr), 2)
	}
	dbPath, err := resolveDBPath()
	if err != nil {
		return err
	}
	costStore, err := storage.New(dbPath)
	if err != nil {
		return fmt.Errorf("open Buckbot cost store: %w", err)
	}
	defer func() { _ = costStore.Close() }()
	monthlySpend, err := costStore.GetMonthlyCostForPrincipal("buckbot")
	if err != nil {
		return fmt.Errorf("load Buckbot monthly spend: %w", err)
	}
	service := newBuckbotService(cfg.Buckbot, newBuckbotReviewer(cfg.Buckbot, costStore), postBuckbotReview)
	service.spentUSD = monthlySpend
	fmt.Printf("Buckbot listening for pull_request webhooks on %s using %s\n", addr, cfg.Buckbot.Model)
	return buckbotListenFn(addr, gitwatcher.NewPullRequestHandler(webhookSecret, service.handle))
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
		cfgCopy.Models.Review = botCfg.Model
		runtime, err := newReviewCommandRuntime(&cfgCopy, mgr)
		if err != nil {
			return "", 0, err
		}
		criticModel := strings.TrimSpace(botCfg.CriticModel)
		if criticModel != "" && criticModel != botCfg.Model {
			criticRunner := oneshot.NewRLMRunner(oneshot.RLMRunnerConfig{
				Models:          mgr,
				Registry:        runtime.registry,
				Ledger:          runtime.ledger,
				ModelID:         criticModel,
				ReasoningEffort: model.ResolveReasoningEffort(&cfgCopy, mgr, nil, criticModel, "review"),
			})
			runtime.framework = runtime.framework.WithApprovalCriticRunner(criticRunner)
		}
		ref := fmt.Sprintf("https://github.com/%s/pull/%d", event.Repository, event.Number)
		result, _, reviewErr := runPRReviewWithOptions(ctx, ref, runtime.framework, automatedReviewOptions{
			maxIterations:    botCfg.MaxReviewIterations,
			maxRetries:       botCfg.MaxValidationAttempts,
			maxDiffBytes:     botCfg.MaxDiffBytes,
			maxCostUSD:       botCfg.PerReviewBudgetUSD,
			criticReserveUSD: botCfg.PerReviewBudgetUSD * 0.12,
		})
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
	cmd := exec.CommandContext(ctx, "gh", "pr", "review", fmt.Sprint(event.Number), "--repo", event.Repository, "--comment", "--body", review)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("post GitHub review: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
