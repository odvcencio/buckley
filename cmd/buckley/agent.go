package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"m31labs.dev/buckley/pkg/agentspec"
	projectcontext "m31labs.dev/buckley/pkg/context"
	"m31labs.dev/buckley/pkg/orchestrator"
)

func runAgentCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley agent <check|show|run> [args...]")
	}
	switch args[0] {
	case "check":
		return runAgentCheck(args[1:])
	case "show":
		return runAgentShow(args[1:])
	case "run", "invoke":
		return runAgentRun(args[1:])
	default:
		return fmt.Errorf("unknown agent subcommand: %s (use check, show, or run)", args[0])
	}
}

func runAgentCheck(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: buckley agent check <agent.yaml>")
	}
	spec, diagnostics, err := loadAgentSpec(args[0])
	if err != nil {
		return err
	}
	if hasAgentSpecErrors(diagnostics) {
		fmt.Print(agentspec.RenderText(spec, diagnostics))
		return fmt.Errorf("agent spec has validation errors")
	}
	fmt.Printf("OK: %s is a valid Buckley agent spec\n", args[0])
	if len(diagnostics) > 0 {
		fmt.Print(agentspec.RenderText(spec, diagnostics))
	}
	return nil
}

type agentRunOptions struct {
	agentPath string
	subagent  string
	task      string
	model     string
}

func runAgentRun(args []string) error {
	opts, err := parseAgentRunArgs(args)
	if err != nil {
		return err
	}

	profile, err := agentspec.LoadRuntimeProfile(opts.agentPath)
	if err != nil {
		return err
	}
	subProfile, err := profile.SubagentProfile(opts.subagent)
	if err != nil {
		return err
	}

	cfg, mgr, store, err := initDependenciesFn()
	if err != nil {
		return err
	}
	defer store.Close()

	subProfile.ApplyToConfig(cfg)
	modelOverride := strings.TrimSpace(subProfile.Spec.Models.Execution)
	if opts.model != "" {
		applyStartupModelOverride(cfg, opts.model)
		modelOverride = opts.model
	} else if modelOverrideFlag != "" {
		applyStartupModelOverride(cfg, modelOverrideFlag)
		modelOverride = modelOverrideFlag
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	projectCtx, err := projectcontext.NewLoader(cwd).Load()
	if err != nil {
		projectCtx = nil
	}
	planStore := orchestrator.NewFilePlanStore(cfg.Artifacts.PlanningDir)
	allowedTools := append([]string(nil), subProfile.Spec.Tools.Allow...)
	exitCode := executeOneShot(formatSubagentTask(opts.subagent, opts.task), cfg, mgr, store, projectCtx, planStore, subProfile, modelOverride, allowedTools)
	if exitCode != 0 {
		return withExitCode(fmt.Errorf("agent run failed"), exitCode)
	}
	return nil
}

func parseAgentRunArgs(args []string) (agentRunOptions, error) {
	fs := flag.NewFlagSet("agent run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	subagent := fs.String("subagent", "", "subagent name from the agent spec")
	modelID := fs.String("model", "", "override model for this subagent task")
	if err := fs.Parse(args); err != nil {
		return agentRunOptions{}, err
	}

	rest := fs.Args()
	opts := agentRunOptions{
		subagent: strings.TrimSpace(*subagent),
		model:    strings.TrimSpace(*modelID),
	}
	if opts.subagent != "" {
		if len(rest) < 2 {
			return agentRunOptions{}, fmt.Errorf("usage: buckley agent run --subagent <name> <agent.yaml> <task...>")
		}
		opts.agentPath = strings.TrimSpace(rest[0])
		opts.task = strings.TrimSpace(strings.Join(rest[1:], " "))
	} else {
		if len(rest) < 3 {
			return agentRunOptions{}, fmt.Errorf("usage: buckley agent run <agent.yaml> <subagent> <task...>")
		}
		opts.agentPath = strings.TrimSpace(rest[0])
		opts.subagent = strings.TrimSpace(rest[1])
		opts.task = strings.TrimSpace(strings.Join(rest[2:], " "))
	}
	if opts.agentPath == "" {
		return agentRunOptions{}, fmt.Errorf("agent spec path is required")
	}
	if opts.subagent == "" {
		return agentRunOptions{}, fmt.Errorf("subagent name is required")
	}
	if opts.task == "" {
		return agentRunOptions{}, fmt.Errorf("subagent task is required")
	}
	return opts, nil
}

func formatSubagentTask(subagent, task string) string {
	return fmt.Sprintf("Subagent %q task:\n\n%s\n\nReport actions taken, findings, validation, and remaining risks.", strings.TrimSpace(subagent), strings.TrimSpace(task))
}

func runAgentShow(args []string) error {
	fs := flag.NewFlagSet("agent show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: buckley agent show [--format text|json] <agent.yaml>")
	}
	spec, diagnostics, err := loadAgentSpec(fs.Arg(0))
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(*format)) {
	case "", "text":
		fmt.Print(agentspec.RenderText(spec, diagnostics))
	case "json":
		data, err := agentspec.JSON(spec, diagnostics)
		if err != nil {
			return fmt.Errorf("encoding agent spec: %w", err)
		}
		fmt.Println(string(data))
	default:
		return fmt.Errorf("unknown format %q (use text or json)", *format)
	}
	return nil
}

func loadAgentSpec(path string) (*agentspec.Spec, []agentspec.Diagnostic, error) {
	spec, err := agentspec.LoadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return spec, spec.Validate(), nil
}

func hasAgentSpecErrors(diagnostics []agentspec.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == agentspec.SeverityError {
			return true
		}
	}
	return false
}
