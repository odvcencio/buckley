package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/agentspec"
	projectcontext "m31labs.dev/buckley/pkg/context"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/tool"
)

func runAgentCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley agent <list|check|show|run> [args...]")
	}
	switch args[0] {
	case "list":
		return runAgentList(args[1:])
	case "check":
		return runAgentCheck(args[1:])
	case "show":
		return runAgentShow(args[1:])
	case "run", "invoke":
		return runAgentRun(args[1:])
	default:
		return fmt.Errorf("unknown agent subcommand: %s (use list, check, show, or run)", args[0])
	}
}

type agentListSnapshot struct {
	Found bool                       `json:"found"`
	Root  string                     `json:"root,omitempty"`
	Count int                        `json:"count"`
	Specs []agentspec.DiscoveredSpec `json:"specs,omitempty"`
}

func runAgentList(args []string) error {
	fs := flag.NewFlagSet("agent list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: buckley agent list [--json|--format json]")
	}

	snapshot, err := buildAgentListSnapshot(".")
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(*format)) {
	case "", "text":
	case "json":
		*jsonOutput = true
	default:
		return fmt.Errorf("unknown format %q (use text or json)", *format)
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snapshot)
	}
	printAgentListText(os.Stdout, snapshot)
	return nil
}

func buildAgentListSnapshot(start string) (agentListSnapshot, error) {
	discovery, err := agentspec.DiscoverProjectSpecs(start)
	if err != nil {
		return agentListSnapshot{}, err
	}
	return agentListSnapshot{
		Found: len(discovery.Specs) > 0,
		Root:  discovery.Root,
		Count: len(discovery.Specs),
		Specs: discovery.Specs,
	}, nil
}

func printAgentListText(w io.Writer, snapshot agentListSnapshot) {
	if !snapshot.Found {
		fmt.Fprintln(w, "Agent specs: 0 (not found)")
		return
	}
	fmt.Fprintf(w, "Agent specs: %d (%s)\n", snapshot.Count, snapshot.Root)
	for _, spec := range snapshot.Specs {
		status := "valid"
		if !spec.Valid || spec.Error != "" {
			status = "invalid"
		}
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(w, "  - %s (%s): %s", name, status, spec.Path)
		if len(spec.Subagents) > 0 {
			fmt.Fprintf(w, ", subagents=%s", strings.Join(spec.Subagents, ","))
		}
		if spec.Summary != "" {
			fmt.Fprintf(w, ", summary=%q", spec.Summary)
		}
		if spec.Error != "" {
			fmt.Fprintf(w, ", error=%s", spec.Error)
		}
		fmt.Fprintln(w)
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
	toolTier  string
	dryRun    bool
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
	if opts.toolTier != "" {
		subProfile.Spec.Tools.Tier = opts.toolTier
	}
	if opts.dryRun {
		fmt.Print(renderAgentRunPreview(opts, subProfile))
		return nil
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
	toolTier := fs.String("tool-tier", "", "override tool tier: none, read_only, standard, or full")
	noTools := fs.Bool("no-tools", false, "run without tools")
	dryRun := fs.Bool("dry-run", false, "show resolved subagent invocation without calling the model")
	if err := fs.Parse(args); err != nil {
		return agentRunOptions{}, err
	}

	rest := fs.Args()
	opts := agentRunOptions{
		subagent: strings.TrimSpace(*subagent),
		model:    strings.TrimSpace(*modelID),
		toolTier: strings.TrimSpace(*toolTier),
		dryRun:   *dryRun,
	}
	if *noTools {
		if opts.toolTier != "" && opts.toolTier != "none" {
			return agentRunOptions{}, fmt.Errorf("--no-tools conflicts with --tool-tier %s", opts.toolTier)
		}
		opts.toolTier = "none"
	}
	if opts.toolTier != "" && !validAgentRunToolTier(opts.toolTier) {
		return agentRunOptions{}, fmt.Errorf("tool tier must be none, read_only, standard, or full")
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

func renderAgentRunPreview(opts agentRunOptions, profile *agentspec.RuntimeProfile) string {
	var b strings.Builder
	b.WriteString("Agent run preview\n")
	if profile != nil && strings.TrimSpace(profile.SourcePath) != "" {
		fmt.Fprintf(&b, "Source: %s\n", strings.TrimSpace(profile.SourcePath))
	}
	if profile != nil && profile.Spec != nil {
		fmt.Fprintf(&b, "Agent: %s\n", strings.TrimSpace(profile.Spec.Name))
	}
	fmt.Fprintf(&b, "Subagent: %s\n", strings.TrimSpace(opts.subagent))
	fmt.Fprintf(&b, "Model: %s\n", previewAgentRunModel(opts, profile))
	fmt.Fprintf(&b, "Tool tier: %s\n", previewAgentRunToolTier(profile))
	fmt.Fprintf(&b, "Tool filter: %s\n", previewAgentRunToolFilter(profile))
	if profile != nil && profile.Spec != nil {
		if len(profile.Spec.Tools.Deny) > 0 {
			fmt.Fprintf(&b, "Denied tools: %s\n", strings.Join(cleanToolNames(profile.Spec.Tools.Deny), ", "))
		}
		if len(profile.Spec.Skills) > 0 {
			fmt.Fprintf(&b, "Skills: %s\n", strings.Join(profile.Spec.Skills, ", "))
		}
		if mode := strings.TrimSpace(profile.Spec.Policies.ApprovalMode); mode != "" {
			fmt.Fprintf(&b, "Approval mode: %s\n", mode)
		}
		if strings.TrimSpace(profile.Spec.Instructions.Prompt) != "" {
			b.WriteString("Instructions: yes\n")
		}
	}
	fmt.Fprintf(&b, "Task: %s\n", strings.TrimSpace(opts.task))
	return b.String()
}

func previewAgentRunModel(opts agentRunOptions, profile *agentspec.RuntimeProfile) string {
	if opts.model != "" {
		return opts.model + " (flag override)"
	}
	if modelOverrideFlag != "" {
		return strings.TrimSpace(modelOverrideFlag) + " (global override)"
	}
	if profile != nil && profile.Spec != nil {
		if modelID := strings.TrimSpace(profile.Spec.Models.Execution); modelID != "" {
			return modelID
		}
		if modelID := strings.TrimSpace(profile.Spec.Models.Chat); modelID != "" {
			return modelID
		}
	}
	return "(configured execution model)"
}

func previewAgentRunToolTier(profile *agentspec.RuntimeProfile) string {
	if profile == nil || profile.Spec == nil {
		return "full"
	}
	if tier := strings.TrimSpace(profile.Spec.Tools.Tier); tier != "" {
		return tier
	}
	return "full"
}

func previewAgentRunToolFilter(profile *agentspec.RuntimeProfile) string {
	if profile == nil || profile.Spec == nil {
		return "unrestricted"
	}
	allowed := resolveOneShotToolFilter(profile, tool.NewRegistry(), append([]string(nil), profile.Spec.Tools.Allow...))
	switch {
	case allowed == nil:
		return "unrestricted"
	case len(allowed) == 0:
		return "none"
	default:
		return summarizeToolNames(allowed)
	}
}

func summarizeToolNames(names []string) string {
	names = cleanToolNames(names)
	sort.Strings(names)
	const maxPreviewTools = 12
	if len(names) <= maxPreviewTools {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%d tools (%s, ...)", len(names), strings.Join(names[:maxPreviewTools], ", "))
}

func validAgentRunToolTier(tier string) bool {
	switch strings.TrimSpace(tier) {
	case "none", "read_only", "standard", "full":
		return true
	default:
		return false
	}
}

func formatSubagentTask(subagent, task string) string {
	return fmt.Sprintf("Subagent %q task:\n\n%s\n\nComplete the task directly. If you use tools, inspect files, run commands, or change anything, report what you did, validation performed, and remaining risks. If the task only needs an answer, answer directly and do not claim unperformed actions.", strings.TrimSpace(subagent), strings.TrimSpace(task))
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
