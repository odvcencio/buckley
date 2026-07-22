package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/gitwatcher"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/transparency"
)

var buckbotLoadConfigFn = config.Load
var buckbotListenFn = http.ListenAndServe

type buckbotReviewer func(context.Context, gitwatcher.PullRequestEvent) (string, float64, error)
type buckbotPoster func(context.Context, gitwatcher.PullRequestEvent, string) error

type buckbotService struct {
	cfg      config.BuckbotConfig
	review   buckbotReviewer
	post     buckbotPoster
	mu       sync.Mutex
	seen     map[string]struct{}
	spentUSD float64
}

func newBuckbotService(cfg config.BuckbotConfig, review buckbotReviewer, post buckbotPoster) *buckbotService {
	return &buckbotService{cfg: cfg, review: review, post: post, seen: make(map[string]struct{})}
}

func (s *buckbotService) handle(event gitwatcher.PullRequestEvent) {
	key := fmt.Sprintf("%s#%d@%s", event.Repository, event.Number, event.HeadSHA)
	s.mu.Lock()
	if _, exists := s.seen[key]; exists || s.spentUSD+s.cfg.PerReviewBudgetUSD > s.cfg.MonthlyBudgetUSD {
		s.mu.Unlock()
		return
	}
	s.seen[key] = struct{}{}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	review, cost, err := s.review(ctx, event)
	if err != nil || strings.TrimSpace(review) == "" || cost > s.cfg.PerReviewBudgetUSD {
		return
	}
	s.mu.Lock()
	if s.spentUSD+cost > s.cfg.MonthlyBudgetUSD {
		s.mu.Unlock()
		return
	}
	s.spentUSD += cost
	s.mu.Unlock()
	_ = s.post(ctx, event, review)
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
	defer costStore.Close()
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
			defer store.Close()
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
		ref := fmt.Sprintf("https://github.com/%s/pull/%d", event.Repository, event.Number)
		result, _, err := runPRReviewWithIterationLimit(ctx, ref, runtime.framework, botCfg.MaxReviewIterations)
		if err != nil {
			return "", 0, err
		}
		if err := saveBuckbotSpend(costStore, event, botCfg.Model, runtime.ledger.Entries()); err != nil {
			return "", 0, err
		}
		return result.reviewText, runtime.ledger.SessionTotal(), nil
	}
}

func saveBuckbotSpend(store *storage.Store, event gitwatcher.PullRequestEvent, modelID string, entries []transparency.CostEntry) error {
	if store == nil {
		return fmt.Errorf("Buckbot cost store required")
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
