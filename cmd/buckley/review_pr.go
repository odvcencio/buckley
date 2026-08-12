package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/diffsignal"
	"m31labs.dev/buckley/pkg/gitwatcher"
	knowledgehyphae "m31labs.dev/buckley/pkg/knowledge/hyphae"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/transparency"
)

var postCompletedBuckbotReviewFn buckbotPoster = postBuckbotReview
var waitCompletedBuckbotPostRetryFn = waitForBuckbotRetry

const (
	maxCompletedBuckbotPostAttempts  = 3
	completedBuckbotPostRetryDelay   = time.Second
	completedBuckbotPostAttemptLimit = 7 * time.Second
	completedBuckbotPostBudget       = 25 * time.Second
)

type reviewPRCommandOptions struct {
	verbose              bool
	showCost             bool
	post                 bool
	model                string
	criticModel          string
	timeout              time.Duration
	outputFile           string
	prRef                string
	budgetUSD            float64
	noBudget             bool
	maxTurns             int
	maxToolCalls         int
	maxDiff              int
	maxSupportingContext int
	maxRetries           int
	forceSinglePass      bool
	forceShards          int
	concurrency          int
}

func parseReviewPRCommandOptions(args []string) (reviewPRCommandOptions, error) {
	fs := flag.NewFlagSet("review-pr", flag.ContinueOnError)
	verbose := fs.Bool("verbose", false, "show full context and reasoning")
	showCost := fs.Bool("cost", true, "show token/cost breakdown")
	post := fs.Bool("post", false, "post the completed review to GitHub (default: dry run)")
	modelFlag := fs.String("model", "", "model to use; codex/auto scales Luna to Terra to Sol")
	criticModel := fs.String("critic-model", "", "opt-in approval critic model for large or business-critical reviews")
	timeout := fs.Duration("timeout", defaultReviewTimeout, "total review timeout")
	outputFile := fs.String("output", "", "write review to file instead of stdout")
	budgetUSD := fs.Float64("budget", 0, "maximum model spend in USD (0 = configured policy; defaults to uncapped)")
	noBudget := fs.Bool("no-budget", false, "ignore a configured review cost cap for this run")
	maxTurns := fs.Int("max-turns", 0, "hard model turn limit per review pass (0 = adaptive)")
	maxToolCalls := fs.Int("max-tool-calls", 0, "maximum inspection/verification tool calls per review pass (0 = unlimited)")
	maxDiff := fs.Int("max-diff-bytes", 0, "maximum prioritized diff bytes (0 = Buckbot default)")
	maxSupportingContext := fs.Int("max-context-tokens", 0, "maximum supporting-context tokens; diff and evidence IDs are protected (0 = Buckbot default)")
	maxRetries := fs.Int("max-validation-attempts", 0, "maximum schema-validation attempts (0 = Buckbot default)")
	singlePass := fs.Bool("single-pass", false, "force one review pass even if the diff would otherwise fan out into shards")
	shards := fs.Int("shards", 0, "force fan-out into exactly this many shards, for testing (0 = derive from content)")
	concurrency := fs.Int("concurrency", defaultShardConcurrency, "maximum number of shards reviewed at once")

	if err := fs.Parse(interspersedReviewPRArgs(args)); err != nil {
		return reviewPRCommandOptions{}, err
	}
	if *budgetUSD < 0 {
		return reviewPRCommandOptions{}, fmt.Errorf("--budget must be zero or greater")
	}
	if *maxToolCalls < 0 {
		return reviewPRCommandOptions{}, fmt.Errorf("--max-tool-calls must be zero or greater")
	}
	if *noBudget && *budgetUSD > 0 {
		return reviewPRCommandOptions{}, fmt.Errorf("--no-budget cannot be combined with --budget")
	}

	if fs.NArg() != 1 || fs.Arg(0) == "" {
		return reviewPRCommandOptions{}, reviewPRUsageError()
	}
	return reviewPRCommandOptions{
		verbose:              *verbose,
		showCost:             *showCost,
		post:                 *post,
		model:                *modelFlag,
		criticModel:          *criticModel,
		timeout:              *timeout,
		outputFile:           *outputFile,
		prRef:                fs.Arg(0),
		budgetUSD:            *budgetUSD,
		noBudget:             *noBudget,
		maxTurns:             *maxTurns,
		maxToolCalls:         *maxToolCalls,
		maxDiff:              *maxDiff,
		maxSupportingContext: *maxSupportingContext,
		maxRetries:           *maxRetries,
		forceSinglePass:      *singlePass,
		forceShards:          *shards,
		concurrency:          *concurrency,
	}, nil
}

// interspersedReviewPRArgs lets callers use the natural
// `review-pr <ref> -model ...` form without silently treating every trailing
// flag as an ignored positional argument. The standard flag package stops at
// the first positional argument, so move the one PR reference behind the
// command's flags before parsing.
func interspersedReviewPRArgs(args []string) []string {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}

		flags = append(flags, arg)
		name, hasInlineValue := reviewPRFlagName(arg)
		if hasInlineValue || !reviewPRFlagTakesValue(name) || i+1 >= len(args) {
			continue
		}
		flags = append(flags, args[i+1])
		i++
	}
	return append(flags, positionals...)
}

func reviewPRFlagName(arg string) (string, bool) {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		return before, true
	}
	return name, false
}

func reviewPRFlagTakesValue(name string) bool {
	switch name {
	case "model", "critic-model", "timeout", "output", "budget", "max-turns", "max-tool-calls", "max-diff-bytes", "max-context-tokens", "max-validation-attempts",
		"shards", "concurrency":
		return true
	default:
		return false
	}
}

func reviewPRUsageError() error {
	return fmt.Errorf("usage: buckley review-pr <pr-number-or-url> [flags]\n\nExamples:\n  buckley review-pr 123\n  buckley review-pr 123 -model codex/auto\n  buckley review-pr 123 -model codex/gpt-5.6-terra-high\n  buckley review-pr https://github.com/owner/repo/pull/123")
}

// runReviewPRCommand reviews a remote PR using gh CLI integration.
func runReviewPRCommand(args []string) error {
	sweepStaleReviewWorkspaces()

	opts, err := parseReviewPRCommandOptions(args)
	if err != nil {
		return err
	}

	restoreModelOverride := applyCommandModelOverride(opts.model)
	defer restoreModelOverride()

	cfg, mgr, store, err := initDependenciesFn()
	if store != nil {
		defer store.Close()
	}
	if err != nil {
		return fmt.Errorf("init dependencies: %w", err)
	}
	if strings.TrimSpace(opts.criticModel) != "" {
		cfg.Buckbot.CriticModel = strings.TrimSpace(opts.criticModel)
	}

	runtime, err := newReviewCommandRuntime(cfg, mgr)
	if err != nil {
		return fmt.Errorf("no model configured (set BUCKLEY_MODEL_REVIEW or configure models.review)")
	}

	reviewTimeout := opts.timeout
	if opts.post && reviewTimeout > defaultReviewTimeout {
		reviewTimeout = defaultReviewTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), reviewTimeout)
	defer cancel()

	if !quietMode {
		printReviewModelSelection(runtime)
		if runtime.policy.adaptiveReasoning {
			termOut.Dim("Reasoning effort: adaptive by model and review size")
		} else if runtime.reasoningEffort != "" {
			termOut.Dim("Reasoning effort: %s", runtime.reasoningEffort)
		}
		termOut.Dim("Reviewing PR: %s", opts.prRef)
	}

	policy := runtime.policy.withOverrides(automatedReviewOptions{
		maxIterations:              opts.maxTurns,
		maxToolCalls:               opts.maxToolCalls,
		maxRetries:                 opts.maxRetries,
		maxDiffBytes:               opts.maxDiff,
		maxSupportingContextTokens: opts.maxSupportingContext,
		maxCostUSD:                 opts.budgetUSD,
		clearCostBudget:            opts.noBudget,
		forceSinglePass:            opts.forceSinglePass,
		forceShards:                opts.forceShards,
		concurrency:                opts.concurrency,
	})
	if opts.post {
		policy.contextReady = func(ctx context.Context, prInfo *commands.PRInfo, plan automatedReviewOptions) error {
			if prInfo == nil {
				return fmt.Errorf("post GitHub review intake: pull request metadata unavailable")
			}
			return postBuckbotReviewLifecycle(ctx, gitwatcher.PullRequestEvent{
				Repository: prInfo.Repository,
				Number:     prInfo.Number,
				HeadSHA:    prInfo.HeadSHA,
			}, buckbotReviewIntake{
				Model:           plan.modelID,
				ReasoningEffort: plan.reasoningEffort,
				SizeClass:       plan.sizeClass,
				BudgetUSD:       plan.maxCostUSD,
			})
		}
	}
	result, prInfo, reviewErr := runPRReviewWithOptions(ctx, opts.prRef, runtime.framework, policy, opts.post)

	if opts.verbose && result != nil && result.contextAudit != nil {
		printReviewContextAudit(result.contextAudit)
	}
	if reviewErr != nil {
		if result == nil || !result.incomplete || strings.TrimSpace(result.reviewText) == "" {
			return reviewErr
		}
		if err := writePRReviewOutput(opts.outputFile, result.reviewText, prInfo); err != nil {
			return fmt.Errorf("%w; also failed to write salvaged review: %v", reviewErr, err)
		}
		if opts.showCost && result.trace != nil {
			printReviewCost(result.trace, runtime.ledger)
		}
		return fmt.Errorf("%w; incomplete review salvaged%s", reviewErr, reviewSalvageDestination(opts.outputFile))
	}

	if result.reviewText == "" {
		return fmt.Errorf("no review generated")
	}
	if !quietMode {
		printReviewAttemptCounts(result)
	}

	if err := writePRReviewOutput(opts.outputFile, result.reviewText, prInfo); err != nil {
		return err
	}
	if opts.post {
		postCtx, postCancel := context.WithTimeout(context.Background(), completedBuckbotPostBudget)
		defer postCancel()
		if err := postCompletedPRReview(postCtx, prInfo, result.reviewText); err != nil {
			return err
		}
		if !quietMode {
			termOut.Success("Review posted to GitHub")
		}
	}

	if opts.showCost && result.trace != nil {
		printReviewCost(result.trace, runtime.ledger)
	}

	return nil
}

func postCompletedPRReview(ctx context.Context, prInfo *commands.PRInfo, reviewText string) error {
	if prInfo == nil {
		return fmt.Errorf("post GitHub review: pull request metadata unavailable")
	}
	if prInfo.Host != "" && prInfo.Host != "github.com" {
		return fmt.Errorf("post GitHub review: unsupported host %q", prInfo.Host)
	}
	if strings.TrimSpace(prInfo.Repository) == "" || prInfo.Number <= 0 || strings.TrimSpace(prInfo.HeadSHA) == "" {
		return fmt.Errorf("post GitHub review: incomplete pull request identity")
	}
	event := gitwatcher.PullRequestEvent{
		Repository: prInfo.Repository,
		Number:     prInfo.Number,
		HeadSHA:    prInfo.HeadSHA,
	}
	var lastErr error
	for attempt := 0; attempt < maxCompletedBuckbotPostAttempts; attempt++ {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, completedBuckbotPostAttemptLimit)
		lastErr = postCompletedBuckbotReviewFn(attemptCtx, event, reviewText)
		attemptCancel()
		if lastErr == nil {
			return nil
		}
		if !isRetryableBuckbotPostError(lastErr) || attempt+1 == maxCompletedBuckbotPostAttempts {
			return lastErr
		}
		delay := completedBuckbotPostRetryDelay * time.Duration(1<<attempt)
		if err := waitCompletedBuckbotPostRetryFn(ctx, delay); err != nil {
			return fmt.Errorf("post GitHub review retry: %w", err)
		}
	}
	return lastErr
}

// defaultShardConcurrency bounds how many shards review-pr reviews at once
// when a PR fans out. Shard count itself has no cap (see
// diffsignal.PrioritizeShards); this only bounds wall-clock parallelism, so
// cost and time scale with PR size instead of coverage being sacrificed.
const defaultShardConcurrency = 4

type automatedReviewOptions struct {
	maxIterations              int
	maxToolCalls               int
	maxVerificationCalls       int
	maxRetries                 int
	maxDiffBytes               int
	maxSupportingContextTokens int
	maxCostUSD                 float64
	clearCostBudget            bool
	criticReserveUSD           float64
	approvalCritic             bool
	verificationTimeout        time.Duration
	explorationTimeout         time.Duration
	synthesisLead              time.Duration
	criticReserve              time.Duration
	criticMaxIterations        int
	criticMaxToolCalls         int
	criticExploration          time.Duration
	criticSynthesisLead        time.Duration
	sizeClass                  string
	modelID                    string
	reasoningEffort            string
	reasoningMaxTokens         int
	maxOutputTokens            int
	adaptiveCodexModel         bool
	adaptiveReasoning          bool
	engine                     *rules.Engine
	contextReady               func(context.Context, *commands.PRInfo, automatedReviewOptions) error

	// forceSinglePass and forceShards are CLI testing knobs (see -single-pass
	// and -shards on review-pr). forceShards > 0 targets that many shards by
	// computing an effective per-shard budget; forceSinglePass overrides it
	// back to exactly one shard covering the whole diff.
	forceSinglePass bool
	forceShards     int

	// concurrency bounds how many shards run at once; it never bounds shard
	// count. 0 uses defaultShardConcurrency.
	concurrency int

	// costPerMillionTokens feeds commands.ProjectShardCost's dollar
	// estimate. 0 skips the dollar projection (token/shard counts still
	// report) rather than fabricating a rate.
	costPerMillionTokens float64

	// postingGate configures the posted-review size gate (see
	// runPRReviewWithOptions's willPost parameter). A zero value (nil
	// CoreAssociations) means "unset"; postingGateOrDefault substitutes
	// commands.DefaultPostingGateConfig() in that case.
	postingGate commands.PostingGateConfig
}

func (opts automatedReviewOptions) postingGateOrDefault() commands.PostingGateConfig {
	if opts.postingGate.CoreAssociations == nil {
		return commands.DefaultPostingGateConfig()
	}
	return opts.postingGate
}

// buckbotPostingGateConfig builds the posting gate config from Buckbot's
// configuration section, falling back to commands.DefaultPostingGateConfig
// for any field left at its zero value.
func buckbotPostingGateConfig(cfg config.BuckbotConfig) commands.PostingGateConfig {
	gate := commands.DefaultPostingGateConfig()
	if len(cfg.PostingCoreAssociations) > 0 {
		gate.CoreAssociations = make(map[string]bool, len(cfg.PostingCoreAssociations))
		for _, association := range cfg.PostingCoreAssociations {
			association = strings.TrimSpace(association)
			if association != "" {
				gate.CoreAssociations[association] = true
			}
		}
	}
	if len(cfg.PostingAllowlist) > 0 {
		gate.AllowlistUsernames = make(map[string]bool, len(cfg.PostingAllowlist))
		for _, username := range cfg.PostingAllowlist {
			username = strings.ToLower(strings.TrimSpace(username))
			if username != "" {
				gate.AllowlistUsernames[username] = true
			}
		}
	}
	if cfg.PostingSizeThresholdBytes > 0 {
		gate.HighSignalByteThreshold = cfg.PostingSizeThresholdBytes
	}
	return gate
}

func defaultAutomatedReviewOptions(cfg *config.Config) automatedReviewOptions {
	if cfg == nil {
		return automatedReviewOptions{}
	}
	opts := automatedReviewOptions{
		maxIterations:              cfg.Buckbot.MaxReviewIterations,
		maxToolCalls:               cfg.Buckbot.MaxToolCalls,
		maxRetries:                 cfg.Buckbot.MaxValidationAttempts,
		maxDiffBytes:               cfg.Buckbot.MaxDiffBytes,
		maxSupportingContextTokens: cfg.Buckbot.MaxSupportingContextTokens,
		maxCostUSD:                 cfg.Buckbot.PerReviewBudgetUSD,
		reasoningEffort:            resolveConfiguredReviewReasoning(cfg),
		adaptiveReasoning:          reviewReasoningIsAdaptive(cfg, reviewReasoningOverride()),
		postingGate:                buckbotPostingGateConfig(cfg.Buckbot),
	}
	if strings.TrimSpace(cfg.Buckbot.CriticModel) != "" {
		opts.criticReserveUSD = cfg.Buckbot.PerReviewBudgetUSD * 0.12
		opts.approvalCritic = true
	}
	return opts
}

func (defaults automatedReviewOptions) withOverrides(overrides automatedReviewOptions) automatedReviewOptions {
	if overrides.maxIterations > 0 {
		defaults.maxIterations = overrides.maxIterations
	}
	if overrides.maxToolCalls > 0 {
		defaults.maxToolCalls = overrides.maxToolCalls
	}
	if overrides.maxRetries > 0 {
		defaults.maxRetries = overrides.maxRetries
	}
	if overrides.maxDiffBytes > 0 {
		defaults.maxDiffBytes = overrides.maxDiffBytes
	}
	if overrides.maxSupportingContextTokens > 0 {
		defaults.maxSupportingContextTokens = overrides.maxSupportingContextTokens
	}
	if overrides.clearCostBudget {
		defaults.maxCostUSD = 0
		defaults.criticReserveUSD = 0
	} else if overrides.maxCostUSD > 0 {
		defaults.maxCostUSD = overrides.maxCostUSD
		if defaults.approvalCritic {
			defaults.criticReserveUSD = overrides.maxCostUSD * 0.12
		}
	}
	if overrides.forceSinglePass {
		defaults.forceSinglePass = true
	}
	if overrides.forceShards > 0 {
		defaults.forceShards = overrides.forceShards
	}
	if overrides.concurrency > 0 {
		defaults.concurrency = overrides.concurrency
	}
	if overrides.costPerMillionTokens > 0 {
		defaults.costPerMillionTokens = overrides.costPerMillionTokens
	}
	return defaults
}

func reviewContextProvidersForModel(modelID string) []commands.PRContextProvider {
	providers := []commands.PRContextProvider{knowledgehyphae.NewReviewContextProvider()}
	if isQwenReviewModel(modelID) {
		providers = append(providers, commands.NewWorkflowRiskContextProvider())
	}
	return providers
}

// runPRReviewWithOptions reviews prRef. willPost tells the posting-size gate
// whether this run's result will be written back to the pull request: a
// local/dry-run review (willPost=false) is always unrestricted, at any PR
// size and from any author, because its cost is the caller's own to spend.
// A posted review (willPost=true) that exceeds the gate's high-signal-byte
// threshold requires a core maintainer or owner author; otherwise it
// declines before running any shard (see commands.EvaluatePostingGate).
func runPRReviewWithOptions(ctx context.Context, prRef string, framework *oneshot.Framework, opts automatedReviewOptions, willPost bool) (*reviewCommandResult, *commands.PRInfo, error) {
	spinner := newReviewProgress("Fetching PR details...")
	spinner.Start()

	contextOpts := commands.DefaultPRContextOptions()
	contextOpts.Context = ctx
	contextOpts.Providers = reviewContextProvidersForModel(opts.modelID)
	if opts.maxDiffBytes > 0 {
		contextOpts.MaxDiffBytes = opts.maxDiffBytes
	}
	if opts.maxSupportingContextTokens > 0 {
		contextOpts.MaxSupportingContextTokens = opts.maxSupportingContextTokens
	}
	prCtx, audit, err := commands.AssemblePRContextWithOptions(prRef, contextOpts)
	if err != nil {
		spinner.StopWithError(err.Error())
		return nil, nil, fmt.Errorf("assemble PR context: %w", err)
	}
	spinner.SetMessage("Reviewing immutable PR head...")

	if willPost {
		gateCfg := opts.postingGateOrDefault()
		decision := commands.EvaluatePostingGate(gateCfg, true, prCtx.Shards, prCtx.PR.Author, prCtx.PR.AuthorAssoc, nil)
		if decision.Blocked {
			spinner.StopWithError("declined: pull request exceeds the posting size threshold")
			comment := commands.BuildDeclineComment(decision, gateCfg.HighSignalByteThreshold)
			return &reviewCommandResult{reviewText: comment, contextAudit: audit}, prCtx.PR, nil
		}
	}
	shards := effectivePRShards(prCtx, opts)
	if len(shards.Shards) > 1 {
		return runPRReviewSharded(ctx, framework, opts, prCtx, audit, shards, spinner)
	}

	plan := resolveReviewExecutionPlan(opts.engine, rules.ReviewPlanFacts{
		FileCount:         len(prCtx.Files),
		DiffBytes:         len(prCtx.Diff),
		BlastRadius:       prCtx.CanopyBlastRadius,
		ContextIncomplete: prCtx.HasIncompleteContext(),
		HasFeedback:       prCtx.HasReviewFeedback(),
	})
	opts = opts.withExecutionPlan(plan)
	opts = opts.withVerificationTargetBudget(prCtx.Files)
	if opts.contextReady != nil {
		if err := opts.contextReady(ctx, prCtx.PR, opts); err != nil {
			spinner.StopWithError(err.Error())
			return nil, prCtx.PR, fmt.Errorf("prepare posted review: %w", err)
		}
	}
	spinner.SetMessage(fmt.Sprintf("Running %s review with %s reasoning...", opts.sizeClass, opts.reasoningEffort))
	userPrompt := appendReviewExecutionPlan(commands.BuildPRPrompt(prCtx), opts)
	reviewDef := commands.ReviewPRDef{
		ChangedFiles:                prCtx.Files,
		ContextIncomplete:           prCtx.HasIncompleteContext(),
		CIStatus:                    prCtx.PR.CIStatus,
		CIProvenance:                prCtx.CIProvenance,
		RequiresFeedbackDisposition: prCtx.HasReviewFeedback(),
		RequiredFeedbackIDs:         prCtx.RequiredFeedbackIDs(),
		MaxIterations:               opts.maxIterations,
		ApprovalCritic:              opts.approvalCritic,
	}
	opts = boundPRApprovalCritic(opts, reviewDef)
	fwResult, runErr := framework.RunRLM(ctx, reviewDef, oneshot.RLMRunOpts{
		UserPrompt:               userPrompt,
		Audit:                    audit,
		MaxRetries:               opts.maxRetries,
		MaxIterations:            opts.maxIterations,
		MaxToolCalls:             opts.maxToolCalls,
		MaxVerificationCalls:     opts.maxVerificationCalls,
		MaxCostUSD:               opts.maxCostUSD,
		ApprovalCriticReserveUSD: opts.criticReserveUSD,
		ApprovalCriticReserve:    enabledReviewDuration(opts.approvalCritic, opts.criticReserve),
		CriticMaxIterations:      opts.criticMaxIterations,
		CriticMaxToolCalls:       opts.criticMaxToolCalls,
		CriticExplorationTimeout: opts.criticExploration,
		CriticSynthesisLead:      opts.criticSynthesisLead,
		ExplorationTimeout:       opts.explorationTimeout,
		SynthesisLead:            opts.synthesisLead,
		VerificationTimeout:      opts.verificationTimeout,
		ModelID:                  opts.modelID,
		ReasoningEffort:          opts.reasoningEffort,
		ReasoningMaxTokens:       opts.reasoningMaxTokens,
		SnapshotPolicy: model.ReviewSnapshotPolicy{
			Mode:           model.ReviewSnapshotHead,
			ExpectedCommit: prCtx.PR.HeadSHA,
		},
	})

	if runErr != nil {
		spinner.StopWithError(runErr.Error())
		partial := reviewResultFromRLM(fwResult, audit)
		if strings.TrimSpace(partial.reviewText) != "" {
			return partial, prCtx.PR, fmt.Errorf("review failed: %w", runErr)
		}
		return nil, prCtx.PR, fmt.Errorf("review failed: %w", runErr)
	}
	spinner.SetMessage("Revalidating PR head...")
	if err := commands.RevalidatePRContext(prCtx); err != nil {
		spinner.StopWithError(err.Error())
		revalidationErr := fmt.Errorf("review target changed or could not be revalidated: %w", err)
		partial := reviewResultFromRLM(fwResult, audit)
		partial.incomplete = true
		partial.incompleteWhy = revalidationErr.Error()
		partial.reviewText = markIncompleteReview(partial.reviewText, partial.incompleteWhy)
		partial.parsed = nil
		return partial, prCtx.PR, revalidationErr
	}

	spinner.StopWithSuccess("PR review complete")
	return reviewResultFromRLM(fwResult, audit), prCtx.PR, nil
}

func boundPRApprovalCritic(opts automatedReviewOptions, def commands.ReviewPRDef) automatedReviewOptions {
	if !opts.approvalCritic || !def.AuthoritativeRemoteCIPasses() {
		return opts
	}
	opts.criticMaxIterations = 1
	opts.criticMaxToolCalls = 0
	return opts
}

// effectivePRShards decides the shard partitioning runPRReviewWithOptions
// reviews against: prCtx.Shards by default, forced to exactly one shard
// (opts.forceSinglePass) or a caller-chosen shard count (opts.forceShards),
// for testing. Shard count itself is otherwise derived purely from content
// (see diffsignal.PrioritizeShards) and never capped.
func effectivePRShards(prCtx *commands.PRContext, opts automatedReviewOptions) diffsignal.ShardResult {
	switch {
	case opts.forceSinglePass:
		return diffsignal.ShardResult{
			Shards:          []diffsignal.Shard{{Context: prCtx.Diff, Files: append([]string(nil), prCtx.Files...)}},
			LowSignal:       prCtx.Shards.LowSignal,
			Summary:         prCtx.Shards.Summary,
			HighSignalBytes: prCtx.Shards.HighSignalBytes,
			HighSignalFiles: prCtx.Shards.HighSignalFiles,
			Truncated:       prCtx.Shards.Truncated,
		}
	case opts.forceShards > 0 && prCtx.Shards.HighSignalBytes > 0:
		budget := diffsignal.EffectiveShardBudget(prCtx.Shards.HighSignalBytes, opts.forceShards)
		return diffsignal.PrioritizeShards(prCtx.RawDiff, budget)
	default:
		return prCtx.Shards
	}
}

// runPRReviewSharded reviews a PR whose diff was fanned out into more than
// one shard: it projects and logs the cost before running anything, refuses
// (without running a single shard) if the projection exceeds a configured
// budget, runs every shard through a concurrency-bounded worker pool, and
// reduces the results into one final review. Every high-signal file is
// reviewed by exactly one shard; low-signal files and shards that never ran
// are never silently omitted from the final coverage ledger (see
// commands.MergeShardedPRReview).
func runPRReviewSharded(
	ctx context.Context,
	framework *oneshot.Framework,
	opts automatedReviewOptions,
	prCtx *commands.PRContext,
	audit *transparency.ContextAudit,
	shards diffsignal.ShardResult,
	spinner reviewProgress,
) (*reviewCommandResult, *commands.PRInfo, error) {
	concurrency := opts.concurrency
	if concurrency <= 0 {
		concurrency = defaultShardConcurrency
	}

	projection := commands.ProjectShardCostWithContext(shards, prCtx, opts.costPerMillionTokens)
	if !quietMode {
		termOut.Dim("PR fan-out: %d shards, %d high-signal bytes across %d files, concurrency %d, ~%d estimated tokens",
			projection.ShardCount, projection.HighSignalBytes, projection.HighSignalFiles, concurrency, projection.EstimatedTotalTokens)
		if projection.EstimatedTotalCostUSD > 0 {
			termOut.Dim("Estimated shard review cost: $%.4f", projection.EstimatedTotalCostUSD)
		}
	}
	if opts.maxCostUSD > 0 && projection.EstimatedTotalCostUSD > opts.maxCostUSD {
		spinner.StopWithError("projected shard review cost exceeds budget")
		return nil, prCtx.PR, fmt.Errorf(
			"refusing to run: reviewing all %d shards is projected to cost $%.4f, which exceeds the $%.4f budget; "+
				"raise -budget to at least $%.4f for full coverage, or split the pull request",
			projection.ShardCount, projection.EstimatedTotalCostUSD, opts.maxCostUSD, projection.EstimatedTotalCostUSD)
	}

	plan := resolveReviewExecutionPlan(opts.engine, rules.ReviewPlanFacts{
		FileCount:         len(prCtx.Files),
		DiffBytes:         len(prCtx.Diff),
		ContextIncomplete: prCtx.HasIncompleteContext(),
		HasFeedback:       prCtx.HasReviewFeedback(),
	})
	opts = opts.withExecutionPlan(plan)
	if opts.contextReady != nil {
		if err := opts.contextReady(ctx, prCtx.PR, opts); err != nil {
			spinner.StopWithError(err.Error())
			return nil, prCtx.PR, fmt.Errorf("prepare posted review: %w", err)
		}
	}
	spinner.SetMessage(fmt.Sprintf("Running %d-shard review with %s reasoning...", len(shards.Shards), opts.reasoningEffort))

	run := func(shardCtx context.Context, shard diffsignal.Shard, index int) (*commands.ParsedReview, error) {
		primary := index == 0
		shardOpts := opts.withVerificationTargetBudget(shard.Files)
		prompt := appendReviewExecutionPlan(commands.BuildPRShardPrompt(prCtx, shard, index, len(shards.Shards), primary), shardOpts)
		reviewDef := commands.ReviewPRDef{
			ChangedFiles:      shard.Files,
			ContextIncomplete: prCtx.HasIncompleteContext(),
			CIStatus:          prCtx.PR.CIStatus,
			CIProvenance:      prCtx.CIProvenance,
			MaxIterations:     opts.maxIterations,
		}
		if primary {
			reviewDef.RequiresFeedbackDisposition = prCtx.HasReviewFeedback()
			reviewDef.RequiredFeedbackIDs = prCtx.RequiredFeedbackIDs()
		}
		fwResult, runErr := framework.RunRLM(shardCtx, reviewDef, oneshot.RLMRunOpts{
			UserPrompt:           prompt,
			MaxRetries:           opts.maxRetries,
			MaxIterations:        shardOpts.maxIterations,
			MaxToolCalls:         shardOpts.maxToolCalls,
			MaxVerificationCalls: shardOpts.maxVerificationCalls,
			MaxCostUSD:           shardOpts.maxCostUSD,
			ExplorationTimeout:   shardOpts.explorationTimeout,
			SynthesisLead:        shardOpts.synthesisLead,
			VerificationTimeout:  shardOpts.verificationTimeout,
			ModelID:              shardOpts.modelID,
			ReasoningEffort:      shardOpts.reasoningEffort,
			SnapshotPolicy: model.ReviewSnapshotPolicy{
				Mode:           model.ReviewSnapshotHead,
				ExpectedCommit: prCtx.PR.HeadSHA,
			},
		})
		if runErr != nil {
			return nil, runErr
		}
		result, ok := fwResult.Value.(*commands.ReviewRLMResult)
		if !ok || result.Parsed == nil {
			return nil, fmt.Errorf("shard %d produced no parsed review", index+1)
		}
		return result.Parsed, nil
	}

	shardReviews, runErr := commands.RunPRShardsConcurrently(ctx, shards.Shards, concurrency, run)
	if runErr != nil {
		spinner.StopWithError(runErr.Error())
		return nil, prCtx.PR, fmt.Errorf("sharded review failed: %w", runErr)
	}

	merged, rendered := commands.MergeShardedPRReview(shardReviews, shards.LowSignal, prCtx.Files, commands.DefaultSynthesisFanIn)

	if err := commands.RevalidatePRContext(prCtx); err != nil {
		spinner.StopWithError(err.Error())
		revalidationErr := fmt.Errorf("review target changed or could not be revalidated: %w", err)
		return &reviewCommandResult{
			reviewText:    markIncompleteReview(rendered, revalidationErr.Error()),
			incomplete:    true,
			incompleteWhy: revalidationErr.Error(),
			contextAudit:  audit,
		}, prCtx.PR, revalidationErr
	}

	spinner.StopWithSuccess(fmt.Sprintf("%d-shard PR review complete", len(shards.Shards)))
	return &reviewCommandResult{
		reviewText:   rendered,
		parsed:       merged,
		contextAudit: audit,
	}, prCtx.PR, nil
}

func writePRReviewOutput(outputFile, reviewText string, prInfo *commands.PRInfo) error {
	if outputFile != "" {
		if err := os.WriteFile(outputFile, []byte(reviewText), 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		termOut.Success("Review written to %s", outputFile)
		return nil
	}
	printPRReview(reviewText, prInfo)
	return nil
}

func printPRReview(reviewText string, prInfo *commands.PRInfo) {
	termOut.Newline()
	if prInfo != nil {
		termOut.Header(fmt.Sprintf("PR #%d: %s", prInfo.Number, prInfo.Title))
		termOut.Dim("By @%s · %s · CI: %s", prInfo.Author, prInfo.State, prInfo.CIStatus)
	} else {
		termOut.Header("PR REVIEW")
	}
	termOut.Newline()
	fmt.Println(reviewText)
}
