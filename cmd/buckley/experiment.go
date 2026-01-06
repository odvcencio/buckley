package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/odvcencio/buckley/pkg/config"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/experiment"
	"github.com/odvcencio/buckley/pkg/notify"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/worktree"
)

type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value != "" {
		*s = append(*s, value)
	}
	return nil
}

func runExperimentCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley experiment <run|list|show|diff|replay>")
	}
	switch args[0] {
	case "run":
		return runExperimentRun(args[1:])
	case "list":
		return runExperimentList(args[1:])
	case "show":
		return runExperimentShow(args[1:])
	case "diff":
		return runExperimentDiff(args[1:])
	case "replay":
		return runExperimentReplay(args[1:])
	default:
		return fmt.Errorf("unknown experiment command: %s", args[0])
	}
}

func runExperimentRun(args []string) error {
	fs := flag.NewFlagSet("experiment run", flag.ContinueOnError)
	var models stringSliceFlag
	fs.Var(&models, "m", "Model to compare (repeatable)")
	fs.Var(&models, "model", "Model to compare (repeatable)")
	fs.Var(&models, "models", "Model to compare (repeatable)")

	var prompt string
	fs.StringVar(&prompt, "p", "", "Task prompt")
	fs.StringVar(&prompt, "prompt", "", "Task prompt")

	var criteriaFlags stringSliceFlag
	fs.Var(&criteriaFlags, "criteria", "Success criteria (type:target, repeatable)")

	timeout := fs.Duration("timeout", 0, "Timeout per variant (default from config)")
	maxConcurrent := fs.Int("max-concurrent", 0, "Maximum concurrent variants")

	name, remaining := extractExperimentName(args)
	if err := fs.Parse(remaining); err != nil {
		return err
	}
	if name == "" && fs.NArg() > 0 {
		name = fs.Arg(0)
	}
	if name == "" || len(models) == 0 || strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("usage: buckley experiment run <name> -m <model> -p <prompt>")
	}

	cfg, mgr, store, err := initDependenciesFn()
	if err != nil {
		return err
	}
	if !cfg.Experiment.Enabled {
		return withExitCode(fmt.Errorf("experiments are disabled (set experiment.enabled=true or BUCKLEY_EXPERIMENT_ENABLED=1)"), 2)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	projectCtx, err := projectcontext.NewLoader(cwd).Load()
	if err != nil {
		return err
	}

	root := strings.TrimSpace(cfg.Experiment.WorktreeRoot)
	if root == "" {
		root = cfg.Worktrees.RootPath
	}
	worktreeManager, err := worktree.NewManager(cwd, root)
	if err != nil {
		return err
	}

	exp := experiment.Experiment{
		ID:   ulid.Make().String(),
		Name: name,
		Task: experiment.Task{
			Prompt:  strings.TrimSpace(prompt),
			Timeout: *timeout,
		},
	}

	for _, modelID := range models {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		exp.Variants = append(exp.Variants, experiment.Variant{
			ID:         ulid.Make().String(),
			Name:       modelID,
			ModelID:    modelID,
			ProviderID: mgr.ProviderIDForModel(modelID),
		})
	}
	if len(exp.Variants) == 0 {
		return fmt.Errorf("no valid models specified")
	}

	criteria, err := parseCriteriaFlags(criteriaFlags)
	if err != nil {
		return err
	}
	exp.Criteria = criteria

	runnerCfg := experiment.RunnerConfig{
		MaxConcurrent:  cfg.Experiment.MaxConcurrent,
		DefaultTimeout: cfg.Experiment.DefaultTimeout,
		CleanupOnDone:  cfg.Experiment.CleanupOnDone,
	}
	if *maxConcurrent > 0 {
		runnerCfg.MaxConcurrent = *maxConcurrent
	}

	notifyMgr := buildNotifyManager(cfg)
	runner, err := experiment.NewRunner(runnerCfg, experiment.Dependencies{
		Config:         cfg,
		ModelManager:   mgr,
		ProjectContext: projectCtx,
		Notify:         notifyMgr,
		Worktree:       worktreeManager,
		Store:          experiment.NewStoreFromStorage(store),
	})
	if err != nil {
		return err
	}

	ctx := context.Background()
	results, runErr := runner.RunExperiment(ctx, &exp)

	reporter := experiment.NewReporter()
	report := reporter.MarkdownTable(&exp, results)
	if strings.TrimSpace(report) != "" {
		fmt.Println(report)
	}

	return runErr
}

func runExperimentList(args []string) error {
	fs := flag.NewFlagSet("experiment list", flag.ContinueOnError)
	statusFilter := fs.String("status", "", "Filter by status (pending|running|completed|failed|cancelled)")
	limit := fs.Int("limit", 20, "Maximum experiments to list")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store, err := initExperimentStore()
	if err != nil {
		return err
	}
	expStore := experiment.NewStoreFromStorage(store)
	if expStore == nil {
		return fmt.Errorf("experiment store unavailable")
	}

	status, err := parseExperimentStatus(*statusFilter)
	if err != nil {
		return err
	}

	experiments, err := expStore.ListExperiments(*limit, status)
	if err != nil {
		return err
	}
	if len(experiments) == 0 {
		fmt.Println("No experiments found.")
		return nil
	}

	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tNAME\tSTATUS\tCREATED")
	for _, exp := range experiments {
		created := exp.CreatedAt.Local().Format(time.RFC3339)
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", exp.ID, exp.Name, exp.Status, created)
	}
	return writer.Flush()
}

func runExperimentShow(args []string) error {
	fs := flag.NewFlagSet("experiment show", flag.ContinueOnError)
	format := fs.String("format", "auto", "Output format: auto, terminal, markdown, compact")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: buckley experiment show <id|name> [--format auto|terminal|markdown|compact]")
	}
	identifier := strings.TrimSpace(fs.Arg(0))

	store, err := initExperimentStore()
	if err != nil {
		return err
	}
	expStore := experiment.NewStoreFromStorage(store)
	if expStore == nil {
		return fmt.Errorf("experiment store unavailable")
	}

	exp, err := expStore.GetExperiment(identifier)
	if err != nil {
		return err
	}
	if exp == nil {
		exp, err = expStore.FindExperimentByName(identifier)
		if err != nil {
			return err
		}
	}
	if exp == nil {
		return fmt.Errorf("experiment not found: %s", identifier)
	}

	comparator := experiment.NewComparator(expStore)

	// Determine output format
	outputFormat := strings.ToLower(strings.TrimSpace(*format))
	if outputFormat == "auto" {
		// Use terminal format if stdout is a terminal, otherwise markdown
		if isInteractiveTerminal() {
			outputFormat = "terminal"
		} else {
			outputFormat = "markdown"
		}
	}

	switch outputFormat {
	case "terminal":
		termReporter := experiment.NewTerminalReporter(comparator)
		if noColor {
			termReporter.SetNoColor(true)
		}
		return termReporter.RenderReport(exp)
	case "compact":
		termReporter := experiment.NewTerminalReporter(comparator)
		if noColor {
			termReporter.SetNoColor(true)
		}
		return termReporter.RenderCompact(exp)
	case "markdown":
		reporter := experiment.NewReporterWithComparator(comparator)
		report, err := reporter.ComparisonMarkdown(exp)
		if err != nil {
			return err
		}
		fmt.Println(report)
		return nil
	default:
		return fmt.Errorf("unknown format: %s (use: auto, terminal, markdown, compact)", outputFormat)
	}
}

func runExperimentDiff(args []string) error {
	fs := flag.NewFlagSet("experiment diff", flag.ContinueOnError)
	showOutput := fs.Bool("output", false, "Show full output comparison (can be long)")
	maxOutputLen := fs.Int("max-output", 500, "Maximum output length per variant")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: buckley experiment diff <id|name> [--output] [--max-output N]")
	}
	identifier := strings.TrimSpace(fs.Arg(0))

	store, err := initExperimentStore()
	if err != nil {
		return err
	}
	expStore := experiment.NewStoreFromStorage(store)
	if expStore == nil {
		return fmt.Errorf("experiment store unavailable")
	}

	exp, err := expStore.GetExperiment(identifier)
	if err != nil {
		return err
	}
	if exp == nil {
		exp, err = expStore.FindExperimentByName(identifier)
		if err != nil {
			return err
		}
	}
	if exp == nil {
		return fmt.Errorf("experiment not found: %s", identifier)
	}

	runs, err := expStore.ListRuns(exp.ID)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Println("No runs found for this experiment.")
		return nil
	}

	// Build variant lookup
	variantByID := make(map[string]experiment.Variant, len(exp.Variants))
	for _, v := range exp.Variants {
		variantByID[v.ID] = v
	}

	// Print header
	fmt.Printf("# Experiment Diff: %s\n\n", exp.Name)
	fmt.Printf("Comparing %d variants:\n\n", len(runs))

	// Summary table
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "VARIANT\tSTATUS\tFILES\tTOKENS\tCOST\tDURATION")
	for _, run := range runs {
		variant := variantByID[run.VariantID]
		name := variant.ModelID
		if name == "" {
			name = variant.Name
		}
		status := string(run.Status)
		files := fmt.Sprintf("%d", len(run.Files))
		tokens := fmt.Sprintf("%d", run.Metrics.PromptTokens+run.Metrics.CompletionTokens)
		cost := "-"
		if run.Metrics.TotalCost > 0 {
			cost = fmt.Sprintf("$%.4f", run.Metrics.TotalCost)
		}
		duration := fmt.Sprintf("%dms", run.Metrics.DurationMs)
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", name, status, files, tokens, cost, duration)
	}
	writer.Flush()
	fmt.Println()

	// Files comparison
	fmt.Println("## Files Modified by Each Variant")
	fmt.Println()
	for _, run := range runs {
		variant := variantByID[run.VariantID]
		name := variant.ModelID
		if name == "" {
			name = variant.Name
		}
		fmt.Printf("### %s\n", name)
		if len(run.Files) == 0 {
			fmt.Println("  (no files modified)")
		} else {
			for _, f := range run.Files {
				fmt.Printf("  - %s\n", f)
			}
		}
		fmt.Println()
	}

	// Output comparison (if requested or short enough)
	if *showOutput || *maxOutputLen > 0 {
		fmt.Println("## Output Comparison")
		fmt.Println()
		for _, run := range runs {
			variant := variantByID[run.VariantID]
			name := variant.ModelID
			if name == "" {
				name = variant.Name
			}
			fmt.Printf("### %s\n", name)
			if run.Error != nil && *run.Error != "" {
				fmt.Printf("**Error:** %s\n", *run.Error)
			}
			output := strings.TrimSpace(run.Output)
			if output == "" {
				fmt.Println("(no output)")
			} else if *showOutput {
				fmt.Printf("```\n%s\n```\n", output)
			} else if len(output) > *maxOutputLen {
				fmt.Printf("```\n%s...\n```\n", output[:*maxOutputLen])
				fmt.Printf("_(truncated, use --output to see full)_\n")
			} else {
				fmt.Printf("```\n%s\n```\n", output)
			}
			fmt.Println()
		}
	}

	return nil
}

func runExperimentReplay(args []string) error {
	fs := flag.NewFlagSet("experiment replay", flag.ContinueOnError)
	var modelID string
	fs.StringVar(&modelID, "m", "", "Model to use for replay")
	fs.StringVar(&modelID, "model", "", "Model to use for replay")
	var providerID string
	fs.StringVar(&providerID, "provider", "", "Provider override for replay")
	var systemPrompt string
	fs.StringVar(&systemPrompt, "system-prompt", "", "System prompt override")
	var temperatureRaw string
	fs.StringVar(&temperatureRaw, "temperature", "", "Temperature override")
	deterministic := fs.Bool("deterministic-tools", false, "Replay tool calls deterministically (best-effort)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: buckley experiment replay <session-id> -m <model>")
	}
	sourceSessionID := strings.TrimSpace(fs.Arg(0))
	if sourceSessionID == "" {
		return fmt.Errorf("source session id is required")
	}
	if strings.TrimSpace(modelID) == "" {
		return fmt.Errorf("model id is required")
	}

	cfg, mgr, store, err := initDependenciesFn()
	if err != nil {
		return err
	}
	if !cfg.Experiment.Enabled {
		return withExitCode(fmt.Errorf("experiments are disabled (set experiment.enabled=true or BUCKLEY_EXPERIMENT_ENABLED=1)"), 2)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	projectCtx, err := projectcontext.NewLoader(cwd).Load()
	if err != nil {
		return err
	}

	root := strings.TrimSpace(cfg.Experiment.WorktreeRoot)
	if root == "" {
		root = cfg.Worktrees.RootPath
	}
	worktreeManager, err := worktree.NewManager(cwd, root)
	if err != nil {
		return err
	}

	runnerCfg := experiment.RunnerConfig{
		MaxConcurrent:  cfg.Experiment.MaxConcurrent,
		DefaultTimeout: cfg.Experiment.DefaultTimeout,
		CleanupOnDone:  cfg.Experiment.CleanupOnDone,
	}
	notifyMgr := buildNotifyManager(cfg)
	runner, err := experiment.NewRunner(runnerCfg, experiment.Dependencies{
		Config:         cfg,
		ModelManager:   mgr,
		ProjectContext: projectCtx,
		Notify:         notifyMgr,
		Worktree:       worktreeManager,
		Store:          experiment.NewStoreFromStorage(store),
	})
	if err != nil {
		return err
	}

	if providerID == "" {
		providerID = mgr.ProviderIDForModel(modelID)
	}

	var systemOverride *string
	if strings.TrimSpace(systemPrompt) != "" {
		value := strings.TrimSpace(systemPrompt)
		systemOverride = &value
	}
	var tempOverride *float64
	if strings.TrimSpace(temperatureRaw) != "" {
		value, err := parseFloatFlag(temperatureRaw)
		if err != nil {
			return err
		}
		tempOverride = &value
	}

	replayer, err := experiment.NewReplayer(store, runner)
	if err != nil {
		return err
	}
	_, err = replayer.Replay(context.Background(), experiment.ReplayConfig{
		SourceSessionID:    sourceSessionID,
		NewModelID:         modelID,
		NewProviderID:      providerID,
		NewSystemPrompt:    systemOverride,
		NewTemperature:     tempOverride,
		DeterministicTools: *deterministic,
	})
	return err
}

func extractExperimentName(args []string) (string, []string) {
	if len(args) == 0 {
		return "", args
	}
	if strings.HasPrefix(args[0], "-") {
		return "", args
	}
	return args[0], args[1:]
}

func parseExperimentStatus(raw string) (experiment.ExperimentStatus, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return "", nil
	}
	switch raw {
	case string(experiment.ExperimentPending):
		return experiment.ExperimentPending, nil
	case string(experiment.ExperimentRunning):
		return experiment.ExperimentRunning, nil
	case string(experiment.ExperimentCompleted):
		return experiment.ExperimentCompleted, nil
	case string(experiment.ExperimentFailed):
		return experiment.ExperimentFailed, nil
	case string(experiment.ExperimentCancelled):
		return experiment.ExperimentCancelled, nil
	default:
		return "", fmt.Errorf("invalid status: %s", raw)
	}
}

func parseCriteriaFlags(values []string) ([]experiment.SuccessCriterion, error) {
	if len(values) == 0 {
		return nil, nil
	}

	var criteria []experiment.SuccessCriterion
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.SplitN(raw, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid criteria (expected type:target): %s", raw)
		}
		typ := strings.ToLower(strings.TrimSpace(parts[0]))
		target := strings.TrimSpace(parts[1])
		if typ == "" || target == "" {
			return nil, fmt.Errorf("invalid criteria (expected type:target): %s", raw)
		}

		criterionType := experiment.CriterionType(typ)
		switch criterionType {
		case experiment.CriterionTestPass,
			experiment.CriterionFileExists,
			experiment.CriterionContains,
			experiment.CriterionCommand,
			experiment.CriterionManual:
		default:
			return nil, fmt.Errorf("unknown criterion type: %s", typ)
		}

		name := fmt.Sprintf("%s: %s", typ, target)
		criteria = append(criteria, experiment.SuccessCriterion{
			Name:   name,
			Type:   criterionType,
			Target: target,
			Weight: 1,
		})
	}

	return criteria, nil
}

func initExperimentStore() (*storage.Store, error) {
	dbPath, err := resolveDBPath()
	if err != nil {
		return nil, err
	}
	return storage.New(dbPath)
}

func parseFloatFlag(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("value is required")
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid float: %s", raw)
	}
	return value, nil
}

// buildNotifyManager creates a notification manager from config.
// Returns nil if notifications are disabled or no adapters are configured.
func buildNotifyManager(cfg *config.Config) *notify.Manager {
	if cfg == nil || !cfg.Notify.Enabled {
		return nil
	}

	var adapters []notify.Adapter

	// Configure Telegram adapter
	if cfg.Notify.Telegram.Enabled {
		telegramCfg := notify.TelegramConfig{
			BotToken: cfg.Notify.Telegram.BotToken,
			ChatID:   cfg.Notify.Telegram.ChatID,
		}
		if adapter, err := notify.NewTelegramAdapter(telegramCfg); err == nil {
			adapters = append(adapters, adapter)
		}
	}

	// Configure Slack adapter
	if cfg.Notify.Slack.Enabled {
		slackCfg := notify.SlackConfig{
			WebhookURL: cfg.Notify.Slack.WebhookURL,
			Channel:    cfg.Notify.Slack.Channel,
		}
		if adapter, err := notify.NewSlackAdapter(slackCfg); err == nil {
			adapters = append(adapters, adapter)
		}
	}

	if len(adapters) == 0 {
		return nil
	}

	return notify.NewManager(nil, adapters...)
}
