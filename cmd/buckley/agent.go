package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"m31labs.dev/buckley/pkg/agentspec"
	projectcontext "m31labs.dev/buckley/pkg/context"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/tool"
)

func runAgentCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley agent <init|list|check|show|info|subagent|subagents|run> [args...]")
	}
	switch args[0] {
	case "init":
		return runAgentInit(args[1:])
	case "list":
		return runAgentList(args[1:])
	case "check":
		return runAgentCheck(args[1:])
	case "show":
		return runAgentShow(args[1:])
	case "info":
		return runAgentInfo(args[1:])
	case "subagent", "subagents":
		return runAgentSubagentsCommand(args[1:])
	case "run", "invoke":
		return runAgentRun(args[1:])
	default:
		return fmt.Errorf("unknown agent subcommand: %s (use init, list, check, show, info, subagent, subagents, or run)", args[0])
	}
}

type agentInitResult struct {
	Root     string   `json:"root"`
	AgentDir string   `json:"agent_dir"`
	Created  []string `json:"created,omitempty"`
	Existing []string `json:"existing,omitempty"`
	DryRun   bool     `json:"dry_run,omitempty"`
}

func runAgentInit(args []string) error {
	fs := flag.NewFlagSet("agent init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	pathFlag := fs.String("path", ".", "project root where agent/ should be created")
	force := fs.Bool("force", false, "overwrite generated instructions.md if it already exists")
	dryRun := fs.Bool("dry-run", false, "show what would be created without writing files")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: buckley agent init [--path <dir>] [--force] [--dry-run] [--json|--format json]")
	}
	target := strings.TrimSpace(*pathFlag)
	if fs.NArg() == 1 {
		if target != "." {
			return fmt.Errorf("usage: buckley agent init [--path <dir>] [--force] [--dry-run] [--json|--format json]")
		}
		target = strings.TrimSpace(fs.Arg(0))
	}
	if target == "" {
		target = "."
	}
	formatValue := strings.ToLower(strings.TrimSpace(*format))
	switch formatValue {
	case "", "text":
	case "json":
		*jsonOutput = true
	default:
		return fmt.Errorf("unknown format %q (use text or json)", *format)
	}

	result, err := initFilesystemAgentLayout(target, *force, *dryRun)
	if err != nil {
		return err
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	printAgentInitResult(os.Stdout, result)
	return nil
}

type agentListSnapshot struct {
	Found bool                       `json:"found"`
	Root  string                     `json:"root,omitempty"`
	Count int                        `json:"count"`
	Specs []agentspec.DiscoveredSpec `json:"specs,omitempty"`
}

type agentSubagentsSnapshot struct {
	Source    string                 `json:"source,omitempty"`
	Agent     string                 `json:"agent,omitempty"`
	Count     int                    `json:"count"`
	Subagents []agentSubagentSummary `json:"subagents,omitempty"`
}

type agentSubagentInitResult struct {
	Name        string   `json:"name"`
	Root        string   `json:"root"`
	SubagentDir string   `json:"subagent_dir"`
	Created     []string `json:"created,omitempty"`
	Existing    []string `json:"existing,omitempty"`
	DryRun      bool     `json:"dry_run,omitempty"`
}

type agentInfoSnapshot struct {
	Source      string                     `json:"source,omitempty"`
	Project     bool                       `json:"project,omitempty"`
	Spec        *agentspec.Spec            `json:"spec,omitempty"`
	Valid       bool                       `json:"valid"`
	Diagnostics []agentspec.Diagnostic     `json:"diagnostics,omitempty"`
	Slots       []agentspec.FilesystemSlot `json:"slots,omitempty"`
	Subagents   []agentSubagentSummary     `json:"subagents,omitempty"`
}

type agentSubagentSummary struct {
	Name         string   `json:"name"`
	Model        string   `json:"model"`
	ToolTier     string   `json:"tool_tier"`
	ToolFilter   string   `json:"tool_filter"`
	Persona      string   `json:"persona,omitempty"`
	Skills       []string `json:"skills,omitempty"`
	ApprovalMode string   `json:"approval_mode,omitempty"`
	MaxToolCalls int      `json:"max_tool_calls,omitempty"`
	Instructions bool     `json:"instructions"`
	Invoke       string   `json:"invoke"`
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

func runAgentSubagents(args []string) error {
	fs := flag.NewFlagSet("agent subagents", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	format := fs.String("format", "text", "output format: text or json")
	projectSpec := fs.Bool("project", false, "use the default discovered project agent spec")
	specSelect := fs.String("spec", "", "project agent spec name, kind, or path to inspect")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := normalizeJSONFormatFlag(*format, jsonOutput); err != nil {
		return err
	}
	path, project, err := resolveAgentDefaultSpecPath("subagents", *projectSpec, *specSelect, fs.Args())
	if err != nil {
		return err
	}
	profile, err := agentspec.LoadRuntimeProfile(path)
	if err != nil {
		return err
	}
	snapshot, err := buildAgentSubagentsSnapshot(profile, project, strings.TrimSpace(*specSelect), path)
	if err != nil {
		return err
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snapshot)
	}
	printAgentSubagentsText(os.Stdout, snapshot)
	return nil
}

func runAgentInfo(args []string) error {
	fs := flag.NewFlagSet("agent info", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	format := fs.String("format", "text", "output format: text or json")
	projectSpec := fs.Bool("project", false, "use the default discovered project agent spec")
	specSelect := fs.String("spec", "", "project agent spec name, kind, or path to inspect")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := normalizeJSONFormatFlag(*format, jsonOutput); err != nil {
		return err
	}
	path, project, err := resolveAgentDefaultSpecPath("info", *projectSpec, *specSelect, fs.Args())
	if err != nil {
		return err
	}
	snapshot, err := buildAgentInfoSnapshot(path, project, strings.TrimSpace(*specSelect))
	if err != nil {
		return err
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snapshot)
	}
	printAgentInfoText(os.Stdout, snapshot)
	return nil
}

func runAgentSubagentsCommand(args []string) error {
	if len(args) == 0 {
		return runAgentSubagents(args)
	}
	switch strings.TrimSpace(args[0]) {
	case "init", "create", "new":
		return runAgentSubagentInit(args[1:])
	case "list", "ls":
		return runAgentSubagents(args[1:])
	default:
		return runAgentSubagents(args)
	}
}

func runAgentSubagentInit(args []string) error {
	fs := flag.NewFlagSet("agent subagent init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	pathFlag := fs.String("path", ".", "project root where agent/subagents should be created")
	description := fs.String("description", "", "model-facing role description for the subagent")
	force := fs.Bool("force", false, "overwrite generated subagent instructions.md if it already exists")
	dryRun := fs.Bool("dry-run", false, "show what would be created without writing files")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: buckley agent subagent init [--path <dir>] [--description <text>] [--force] [--dry-run] [--json|--format json] <subagent>")
	}
	if err := normalizeJSONFormatFlag(*format, jsonOutput); err != nil {
		return err
	}
	result, err := initFilesystemSubagentLayout(agentSubagentInitOptions{
		Root:        *pathFlag,
		Name:        fs.Arg(0),
		Description: *description,
		Force:       *force,
		DryRun:      *dryRun,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	printAgentSubagentInitResult(os.Stdout, result)
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
		if kind := strings.TrimSpace(spec.Kind); kind != "" && kind != agentspec.DiscoveredKindBuckley {
			fmt.Fprintf(w, ", kind=%s", kind)
		}
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
		if slots := formatAgentFilesystemSlots(spec.Slots); slots != "" {
			fmt.Fprintf(w, "    slots: %s\n", slots)
		}
		printAgentListDiagnostics(w, spec.Diagnostics)
	}
}

func printAgentListDiagnostics(w io.Writer, diagnostics []agentspec.Diagnostic) {
	for _, diagnostic := range diagnostics {
		severity := strings.TrimSpace(diagnostic.Severity)
		if severity == "" {
			severity = "diagnostic"
		}
		path := strings.TrimSpace(diagnostic.Path)
		message := strings.TrimSpace(diagnostic.Message)
		switch {
		case path != "" && message != "":
			fmt.Fprintf(w, "    %s %s: %s\n", severity, path, message)
		case message != "":
			fmt.Fprintf(w, "    %s: %s\n", severity, message)
		case path != "":
			fmt.Fprintf(w, "    %s %s\n", severity, path)
		}
	}
}

func resolveAgentDefaultSpecPath(command string, project bool, selector string, args []string) (string, bool, error) {
	selector = strings.TrimSpace(selector)
	if selector != "" {
		project = true
	}
	command = strings.TrimSpace(command)
	if command == "" {
		command = "info"
	}
	usage := fmt.Sprintf("usage: buckley agent %s [--project|--spec <name|kind|path>] [agent.yaml|agent-dir]", command)
	if len(args) > 1 {
		return "", false, fmt.Errorf("%s", usage)
	}
	if len(args) == 1 {
		if project {
			return "", false, fmt.Errorf("%s", usage)
		}
		path := strings.TrimSpace(args[0])
		if path == "" {
			return "", false, fmt.Errorf("agent spec path is required")
		}
		return path, false, nil
	}
	path, err := resolveProjectAgentSpecPath(selector)
	if err != nil {
		return "", false, err
	}
	return path, true, nil
}

func buildAgentInfoSnapshot(path string, project bool, selector string) (agentInfoSnapshot, error) {
	spec, diagnostics, err := loadAgentSpec(path)
	if err != nil {
		return agentInfoSnapshot{}, err
	}
	snapshot := agentInfoSnapshot{
		Source:      strings.TrimSpace(path),
		Project:     project,
		Spec:        spec,
		Valid:       !hasAgentSpecErrors(diagnostics),
		Diagnostics: diagnostics,
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		surface, err := agentspec.InspectFilesystemSurface(path)
		if err != nil {
			snapshot.Diagnostics = append(snapshot.Diagnostics, agentspec.Diagnostic{
				Severity: agentspec.SeverityWarning,
				Path:     "layout",
				Message:  fmt.Sprintf("could not inspect filesystem slots: %v", err),
			})
		} else {
			snapshot.Slots = surface.Slots
		}
	}
	if snapshot.Valid {
		profile, err := agentspec.LoadRuntimeProfile(path)
		if err != nil {
			return agentInfoSnapshot{}, err
		}
		subagents, err := buildAgentSubagentsSnapshot(profile, project, selector, path)
		if err != nil {
			return agentInfoSnapshot{}, err
		}
		snapshot.Source = strings.TrimSpace(subagents.Source)
		snapshot.Subagents = subagents.Subagents
	}
	return snapshot, nil
}

func buildAgentSubagentsSnapshot(profile *agentspec.RuntimeProfile, project bool, selector string, path string) (agentSubagentsSnapshot, error) {
	if profile == nil || profile.Spec == nil {
		return agentSubagentsSnapshot{}, fmt.Errorf("agent profile is required")
	}
	names := agentspec.SubagentNames(profile.Spec)
	snapshot := agentSubagentsSnapshot{
		Source: strings.TrimSpace(profile.SourcePath),
		Agent:  strings.TrimSpace(profile.Spec.Name),
		Count:  len(names),
	}
	for _, name := range names {
		subProfile, err := profile.SubagentProfile(name)
		if err != nil {
			return agentSubagentsSnapshot{}, err
		}
		summary := agentSubagentSummary{
			Name:         name,
			Model:        previewAgentRunModel(agentRunOptions{}, subProfile),
			ToolTier:     previewAgentRunToolTier(subProfile),
			ToolFilter:   previewAgentRunToolFilter(subProfile),
			Skills:       append([]string(nil), subProfile.Spec.Skills...),
			ApprovalMode: strings.TrimSpace(subProfile.Spec.Policies.ApprovalMode),
			MaxToolCalls: subProfile.Spec.Policies.MaxToolCalls,
			Instructions: len(subProfile.InstructionFiles) > 0 || strings.TrimSpace(subProfile.Spec.Instructions.Prompt) != "",
			Invoke:       agentSubagentInvokeExample(project, selector, path, name),
		}
		if strings.TrimSpace(subProfile.Spec.Persona) != strings.TrimSpace(profile.Spec.Persona) {
			summary.Persona = strings.TrimSpace(subProfile.Spec.Persona)
		}
		sort.Strings(summary.Skills)
		snapshot.Subagents = append(snapshot.Subagents, summary)
	}
	return snapshot, nil
}

func agentSubagentInvokeExample(project bool, selector string, path string, name string) string {
	name = strings.TrimSpace(name)
	if project {
		parts := []string{"buckley", "agent", "run", "--project"}
		if selector = strings.TrimSpace(selector); selector != "" {
			parts = append(parts, "--spec", shellQuote(selector))
		}
		parts = append(parts, shellQuote(name), shellQuote("<task>"))
		return strings.Join(parts, " ")
	}
	return strings.Join([]string{"buckley", "agent", "run", shellQuote(path), shellQuote(name), shellQuote("<task>")}, " ")
}

func shellQuote(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, " \t\n\"'\\$`") {
		return strconv.Quote(value)
	}
	return value
}

func printAgentSubagentsText(w io.Writer, snapshot agentSubagentsSnapshot) {
	fmt.Fprintf(w, "Agent subagents: %d", snapshot.Count)
	if snapshot.Agent != "" {
		fmt.Fprintf(w, " (%s)", snapshot.Agent)
	}
	if snapshot.Source != "" {
		fmt.Fprintf(w, " %s", snapshot.Source)
	}
	fmt.Fprintln(w)
	for _, sub := range snapshot.Subagents {
		fmt.Fprintf(w, "  - %s: model=%s, tool_tier=%s, tools=%s", sub.Name, sub.Model, sub.ToolTier, sub.ToolFilter)
		if sub.Persona != "" {
			fmt.Fprintf(w, ", persona=%s", sub.Persona)
		}
		if len(sub.Skills) > 0 {
			fmt.Fprintf(w, ", skills=%s", strings.Join(sub.Skills, ","))
		}
		if sub.ApprovalMode != "" {
			fmt.Fprintf(w, ", approval=%s", sub.ApprovalMode)
		}
		if sub.MaxToolCalls > 0 {
			fmt.Fprintf(w, ", max_tool_calls=%d", sub.MaxToolCalls)
		}
		if sub.Instructions {
			fmt.Fprint(w, ", instructions=true")
		}
		if sub.Invoke != "" {
			fmt.Fprintf(w, "\n    invoke: %s", sub.Invoke)
		}
		fmt.Fprintln(w)
	}
}

func printAgentInfoText(w io.Writer, snapshot agentInfoSnapshot) {
	fmt.Fprintln(w, "Agent info")
	if snapshot.Source != "" {
		fmt.Fprintf(w, "Source: %s\n", snapshot.Source)
	}
	fmt.Fprintf(w, "Valid: %t\n\n", snapshot.Valid)
	fmt.Fprint(w, agentspec.RenderText(snapshot.Spec, snapshot.Diagnostics))
	if slots := formatAgentFilesystemSlots(snapshot.Slots); slots != "" {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Filesystem slots: %s\n", slots)
	}
	if len(snapshot.Subagents) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Runnable subagents:")
	for _, sub := range snapshot.Subagents {
		fmt.Fprintf(w, "  - %s: model=%s, tool_tier=%s, tools=%s", sub.Name, sub.Model, sub.ToolTier, sub.ToolFilter)
		if sub.Persona != "" {
			fmt.Fprintf(w, ", persona=%s", sub.Persona)
		}
		if len(sub.Skills) > 0 {
			fmt.Fprintf(w, ", skills=%s", strings.Join(sub.Skills, ","))
		}
		if sub.ApprovalMode != "" {
			fmt.Fprintf(w, ", approval=%s", sub.ApprovalMode)
		}
		if sub.MaxToolCalls > 0 {
			fmt.Fprintf(w, ", max_tool_calls=%d", sub.MaxToolCalls)
		}
		if sub.Instructions {
			fmt.Fprint(w, ", instructions=true")
		}
		if sub.Invoke != "" {
			fmt.Fprintf(w, "\n    invoke: %s", sub.Invoke)
		}
		fmt.Fprintln(w)
	}
}

func formatAgentFilesystemSlots(slots []agentspec.FilesystemSlot) string {
	if len(slots) == 0 {
		return ""
	}
	supported := []string{}
	unsupported := []string{}
	for _, slot := range slots {
		item := strings.TrimSpace(slot.Name)
		if item == "" {
			continue
		}
		if slot.Count > 0 {
			item = fmt.Sprintf("%s=%d", item, slot.Count)
		}
		if slot.Supported {
			supported = append(supported, item)
		} else {
			unsupported = append(unsupported, item)
		}
	}
	sort.Strings(supported)
	sort.Strings(unsupported)
	parts := []string{}
	if len(supported) > 0 {
		parts = append(parts, strings.Join(supported, ", "))
	}
	if len(unsupported) > 0 {
		parts = append(parts, "unsupported: "+strings.Join(unsupported, ", "))
	}
	return strings.Join(parts, "; ")
}

type agentSubagentInitOptions struct {
	Root        string
	Name        string
	Description string
	Force       bool
	DryRun      bool
}

func initFilesystemSubagentLayout(opts agentSubagentInitOptions) (agentSubagentInitResult, error) {
	name, err := cleanAgentSubagentName(opts.Name)
	if err != nil {
		return agentSubagentInitResult{}, err
	}
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return agentSubagentInitResult{}, fmt.Errorf("resolve subagent init path: %w", err)
	}
	if info, err := os.Stat(absRoot); err == nil {
		if !info.IsDir() {
			return agentSubagentInitResult{}, fmt.Errorf("subagent init path is not a directory: %s", absRoot)
		}
	} else if !os.IsNotExist(err) {
		return agentSubagentInitResult{}, fmt.Errorf("stat subagent init path: %w", err)
	}

	subagentDir := filepath.Join(absRoot, "agent", "subagents", name)
	result := agentSubagentInitResult{
		Name:        name,
		Root:        absRoot,
		SubagentDir: subagentDir,
		DryRun:      opts.DryRun,
	}
	if _, _, err := ensureAgentInitPath(absRoot, "", true, false, opts.DryRun); err != nil {
		return agentSubagentInitResult{}, err
	}
	paths := []struct {
		path    string
		content string
		dir     bool
		force   bool
	}{
		{path: filepath.Join(absRoot, "agent"), dir: true},
		{path: filepath.Join(absRoot, "agent", "instructions.md"), content: defaultAgentInstructions()},
		{path: filepath.Join(absRoot, "agent", "skills"), dir: true},
		{path: filepath.Join(absRoot, "agent", "subagents"), dir: true},
		{path: filepath.Join(absRoot, projectEvalScenarioDir), dir: true},
		{path: subagentDir, dir: true},
		{path: filepath.Join(subagentDir, "instructions.md"), content: renderSubagentInstructions(name, opts.Description), force: opts.Force},
		{path: filepath.Join(subagentDir, "skills"), dir: true},
	}
	for _, item := range paths {
		created, existing, err := ensureAgentInitPath(item.path, item.content, item.dir, item.force, opts.DryRun)
		if err != nil {
			return agentSubagentInitResult{}, err
		}
		rel := agentInitRelativePath(absRoot, item.path, item.dir)
		if created {
			result.Created = append(result.Created, rel)
		} else if existing {
			result.Existing = append(result.Existing, rel)
		}
	}
	return result, nil
}

func cleanAgentSubagentName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("subagent name is required")
	}
	if filepath.ToSlash(value) != value || strings.Contains(value, "/") || strings.Contains(value, `\`) {
		return "", fmt.Errorf("invalid subagent name %q: filesystem subagents must be immediate agent/subagents/<name> directories", value)
	}
	if value == "." || value == ".." {
		return "", fmt.Errorf("invalid subagent name %q", value)
	}
	if value == "agent" {
		return "", fmt.Errorf("subagent name %q is reserved for the built-in self-copy agent", value)
	}
	for _, r := range value {
		if r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		return "", fmt.Errorf("invalid subagent name %q: use letters, digits, dots, dashes, or underscores", value)
	}
	return value, nil
}

func renderSubagentInstructions(name, description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		description = defaultSubagentDescription(name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You are the %s subagent.\n\n", skillTitle(name))
	b.WriteString(description)
	b.WriteString("\n\n")
	b.WriteString("Work on focused delegated tasks. Inspect the relevant context, keep changes scoped, and report validation performed plus any remaining risks.\n")
	return b.String()
}

func defaultSubagentDescription(name string) string {
	label := strings.ToLower(skillTitle(name))
	return fmt.Sprintf("Handle focused %s tasks for this Buckley project.", label)
}

func printAgentSubagentInitResult(w io.Writer, result agentSubagentInitResult) {
	action := "Created"
	createdLabel := "Created"
	if result.DryRun {
		action = "Would create"
		createdLabel = "Would create"
	}
	fmt.Fprintf(w, "%s filesystem subagent %s at %s\n", action, result.Name, result.SubagentDir)
	if len(result.Created) > 0 {
		fmt.Fprintf(w, "%s:\n", createdLabel)
		for _, path := range result.Created {
			fmt.Fprintf(w, "  - %s\n", path)
		}
	}
	if len(result.Existing) > 0 {
		fmt.Fprintln(w, "Existing:")
		for _, path := range result.Existing {
			fmt.Fprintf(w, "  - %s\n", path)
		}
	}
	fmt.Fprintf(w, "Next: buckley agent run --project %s <task>\n", shellQuote(result.Name))
}

func initFilesystemAgentLayout(target string, force, dryRun bool) (agentInitResult, error) {
	root, err := filepath.Abs(strings.TrimSpace(target))
	if err != nil {
		return agentInitResult{}, fmt.Errorf("resolve agent init path: %w", err)
	}
	if info, err := os.Stat(root); err == nil {
		if !info.IsDir() {
			return agentInitResult{}, fmt.Errorf("agent init path is not a directory: %s", root)
		}
	} else if !os.IsNotExist(err) {
		return agentInitResult{}, fmt.Errorf("stat agent init path: %w", err)
	}

	result := agentInitResult{
		Root:     root,
		AgentDir: filepath.Join(root, "agent"),
		DryRun:   dryRun,
	}
	if _, _, err := ensureAgentInitPath(root, "", true, false, dryRun); err != nil {
		return agentInitResult{}, err
	}
	paths := []struct {
		path    string
		content string
		dir     bool
		force   bool
	}{
		{path: result.AgentDir, dir: true},
		{path: filepath.Join(result.AgentDir, "instructions.md"), content: defaultAgentInstructions(), force: force},
		{path: filepath.Join(result.AgentDir, "skills"), dir: true},
		{path: filepath.Join(result.AgentDir, "subagents"), dir: true},
		{path: filepath.Join(root, projectEvalScenarioDir), dir: true},
	}
	for _, item := range paths {
		created, existing, err := ensureAgentInitPath(item.path, item.content, item.dir, item.force, dryRun)
		if err != nil {
			return agentInitResult{}, err
		}
		rel := agentInitRelativePath(root, item.path, item.dir)
		if created {
			result.Created = append(result.Created, rel)
		} else if existing {
			result.Existing = append(result.Existing, rel)
		}
	}
	return result, nil
}

func ensureAgentInitPath(path, content string, dir, force, dryRun bool) (created bool, existing bool, err error) {
	info, statErr := os.Stat(path)
	if statErr == nil {
		if dir {
			if !info.IsDir() {
				return false, false, fmt.Errorf("agent init path exists and is not a directory: %s", path)
			}
			return false, true, nil
		}
		if info.IsDir() {
			return false, false, fmt.Errorf("agent init path exists and is a directory: %s", path)
		}
		if !force {
			return false, true, nil
		}
		if dryRun {
			return true, false, nil
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return false, false, fmt.Errorf("write agent init file: %w", err)
		}
		return true, false, nil
	}
	if !os.IsNotExist(statErr) {
		return false, false, fmt.Errorf("stat agent init path: %w", statErr)
	}
	if dryRun {
		return true, false, nil
	}
	if dir {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return false, false, fmt.Errorf("create agent init directory: %w", err)
		}
		return true, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, false, fmt.Errorf("create agent init parent directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, false, fmt.Errorf("write agent init file: %w", err)
	}
	return true, false, nil
}

func agentInitRelativePath(root, path string, dir bool) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		rel = path
	}
	rel = filepath.ToSlash(rel)
	if dir && !strings.HasSuffix(rel, "/") {
		rel += "/"
	}
	return rel
}

func defaultAgentInstructions() string {
	return strings.TrimSpace(`You are a careful coding agent for this repository.

Inspect the current state before editing. Prefer small, well-tested changes. Follow repository instructions, use project-local skills when relevant, and report validation plus any remaining risks.`) + "\n"
}

func printAgentInitResult(w io.Writer, result agentInitResult) {
	action := "Created"
	createdLabel := "Created"
	if result.DryRun {
		action = "Would create"
		createdLabel = "Would create"
	}
	fmt.Fprintf(w, "%s filesystem agent layout at %s\n", action, result.AgentDir)
	if len(result.Created) > 0 {
		fmt.Fprintf(w, "%s:\n", createdLabel)
		for _, path := range result.Created {
			fmt.Fprintf(w, "  - %s\n", path)
		}
	}
	if len(result.Existing) > 0 {
		fmt.Fprintln(w, "Existing:")
		for _, path := range result.Existing {
			fmt.Fprintf(w, "  - %s\n", path)
		}
	}
	fmt.Fprintln(w, "Next: buckley agent show --project")
}

func runAgentCheck(args []string) error {
	fs := flag.NewFlagSet("agent check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectSpec := fs.Bool("project", false, "check the default discovered project agent spec")
	specSelect := fs.String("spec", "", "project agent spec name, kind, or path to check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, err := resolveAgentCommandSpecPath("check", *projectSpec, *specSelect, fs.Args())
	if err != nil {
		return err
	}
	spec, diagnostics, err := loadAgentSpec(path)
	if err != nil {
		return err
	}
	if hasAgentSpecErrors(diagnostics) {
		fmt.Print(agentspec.RenderText(spec, diagnostics))
		return fmt.Errorf("agent spec has validation errors")
	}
	fmt.Printf("OK: %s is a valid Buckley agent spec\n", path)
	if len(diagnostics) > 0 {
		fmt.Print(agentspec.RenderText(spec, diagnostics))
	}
	return nil
}

type agentRunOptions struct {
	agentPath  string
	project    bool
	specSelect string
	subagent   string
	task       string
	model      string
	toolTier   string
	dryRun     bool
}

func runAgentRun(args []string) error {
	opts, err := parseAgentRunArgs(args)
	if err != nil {
		return err
	}

	profile, err := loadAgentRunProfile(opts)
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
	projectSpec := fs.Bool("project", false, "use a discovered project agent spec from .buckley/agent.yaml or .buckley/agents")
	specSelect := fs.String("spec", "", "project agent spec name or path to use with --project")
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
		project:    *projectSpec || strings.TrimSpace(*specSelect) != "",
		specSelect: strings.TrimSpace(*specSelect),
		subagent:   strings.TrimSpace(*subagent),
		model:      strings.TrimSpace(*modelID),
		toolTier:   strings.TrimSpace(*toolTier),
		dryRun:     *dryRun,
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
	if opts.project {
		if opts.subagent != "" {
			if len(rest) < 1 {
				return agentRunOptions{}, fmt.Errorf("usage: buckley agent run --project --subagent <name> <task...>")
			}
			opts.task = strings.TrimSpace(strings.Join(rest, " "))
		} else {
			if len(rest) < 2 {
				return agentRunOptions{}, fmt.Errorf("usage: buckley agent run --project <subagent> <task...>")
			}
			opts.subagent = strings.TrimSpace(rest[0])
			opts.task = strings.TrimSpace(strings.Join(rest[1:], " "))
		}
	} else if opts.subagent != "" {
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
		if !opts.project {
			return agentRunOptions{}, fmt.Errorf("agent spec path is required")
		}
	}
	if opts.subagent == "" {
		return agentRunOptions{}, fmt.Errorf("subagent name is required")
	}
	if opts.task == "" {
		return agentRunOptions{}, fmt.Errorf("subagent task is required")
	}
	return opts, nil
}

func loadAgentRunProfile(opts agentRunOptions) (*agentspec.RuntimeProfile, error) {
	if !opts.project {
		return agentspec.LoadRuntimeProfile(opts.agentPath)
	}
	path, err := resolveProjectAgentSpecPath(opts.specSelect)
	if err != nil {
		return nil, err
	}
	return agentspec.LoadRuntimeProfile(path)
}

func resolveProjectAgentSpecPath(selector string) (string, error) {
	selector = strings.TrimSpace(selector)
	discovery, err := agentspec.DiscoverProjectSpecs(".")
	if err != nil {
		return "", err
	}
	if len(discovery.Specs) == 0 {
		return "", fmt.Errorf("project agent specs not found; create .buckley/agent.yaml or agent/instructions.md, then run `buckley agent list`")
	}
	if selector == "" {
		for _, spec := range discovery.Specs {
			base := strings.ToLower(filepath.Base(spec.Path))
			parent := strings.ToLower(filepath.Base(filepath.Dir(spec.Path)))
			if (base == "agent.yaml" || base == "agent.yml") && parent == ".buckley" {
				return spec.Path, nil
			}
		}
		if len(discovery.Specs) == 1 {
			return discovery.Specs[0].Path, nil
		}
		return "", fmt.Errorf("multiple project agent specs found; pass --spec <name|path> or run `buckley agent list`")
	}

	matches := []agentspec.DiscoveredSpec{}
	for _, spec := range discovery.Specs {
		if projectAgentSpecMatches(spec, selector) {
			matches = append(matches, spec)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("project agent spec %q not found; run `buckley agent list`", selector)
	case 1:
		return matches[0].Path, nil
	default:
		return "", fmt.Errorf("project agent spec %q is ambiguous; pass a more specific --spec value", selector)
	}
}

func projectAgentSpecMatches(spec agentspec.DiscoveredSpec, selector string) bool {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return false
	}
	path := strings.TrimSpace(spec.Path)
	if path == selector || filepath.ToSlash(path) == filepath.ToSlash(selector) {
		return true
	}
	if strings.TrimSpace(spec.Name) == selector {
		return true
	}
	if strings.TrimSpace(spec.Kind) == selector {
		return true
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if stem == selector {
		return true
	}
	for _, rel := range projectAgentSpecRelativePaths(path) {
		if rel == selector || strings.TrimSuffix(rel, filepath.Ext(rel)) == selector {
			return true
		}
	}
	return false
}

func projectAgentSpecRelativePaths(path string) []string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	marker := "/.buckley/"
	idx := strings.LastIndex(path, marker)
	if idx < 0 {
		return []string{filepath.Base(path)}
	}
	rel := path[idx+len(marker):]
	return []string{rel, strings.TrimPrefix(rel, "agents/")}
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
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	format := fs.String("format", "text", "output format: text or json")
	projectSpec := fs.Bool("project", false, "show the default discovered project agent spec")
	specSelect := fs.String("spec", "", "project agent spec name, kind, or path to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, err := resolveAgentCommandSpecPath("show", *projectSpec, *specSelect, fs.Args())
	if err != nil {
		return err
	}
	spec, diagnostics, err := loadAgentSpec(path)
	if err != nil {
		return err
	}
	formatValue := strings.ToLower(strings.TrimSpace(*format))
	switch formatValue {
	case "", "text":
	case "json":
		*jsonOutput = true
	default:
		return fmt.Errorf("unknown format %q (use text or json)", *format)
	}
	if *jsonOutput {
		data, err := agentspec.JSON(spec, diagnostics)
		if err != nil {
			return fmt.Errorf("encoding agent spec: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}
	fmt.Print(agentspec.RenderText(spec, diagnostics))
	return nil
}

func resolveAgentCommandSpecPath(command string, project bool, selector string, args []string) (string, error) {
	selector = strings.TrimSpace(selector)
	if selector != "" {
		project = true
	}
	usage := fmt.Sprintf("usage: buckley agent %s [--project|--spec <name|kind|path>] <agent.yaml|agent-dir>", command)
	if project {
		if len(args) != 0 {
			return "", fmt.Errorf("%s", usage)
		}
		return resolveProjectAgentSpecPath(selector)
	}
	if len(args) != 1 {
		return "", fmt.Errorf("%s", usage)
	}
	path := strings.TrimSpace(args[0])
	if path == "" {
		return "", fmt.Errorf("%s", usage)
	}
	return path, nil
}

func loadAgentSpec(path string) (*agentspec.Spec, []agentspec.Diagnostic, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		spec, extraDiagnostics, err := agentspec.LoadFilesystemSpec(path)
		if err != nil {
			return nil, nil, err
		}
		diagnostics := append([]agentspec.Diagnostic{}, spec.Validate()...)
		diagnostics = append(diagnostics, extraDiagnostics...)
		return spec, diagnostics, nil
	}
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
