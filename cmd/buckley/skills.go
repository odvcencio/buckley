package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"m31labs.dev/buckley/pkg/skill"
)

type skillsInitResult struct {
	Name        string   `json:"name"`
	Root        string   `json:"root"`
	Path        string   `json:"path"`
	Created     []string `json:"created,omitempty"`
	Existing    []string `json:"existing,omitempty"`
	Overwritten []string `json:"overwritten,omitempty"`
	DryRun      bool     `json:"dry_run,omitempty"`
}

type skillsCommandList struct {
	Count       int              `json:"count"`
	BySource    map[string]int   `json:"by_source"`
	Available   []infoSkillEntry `json:"available"`
	Diagnostics []string         `json:"diagnostics,omitempty"`
}

type skillsCommandEntry struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Source       string   `json:"source"`
	Path         string   `json:"path,omitempty"`
	Phase        string   `json:"phase,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Content      string   `json:"content,omitempty"`
}

func runSkillsCommand(args []string) error {
	subCmd := "list"
	if len(args) > 0 {
		first := strings.TrimSpace(args[0])
		if first != "" && !strings.HasPrefix(first, "-") {
			subCmd = first
			args = args[1:]
		}
	}

	switch subCmd {
	case "init", "create", "new":
		return runSkillsInitCommand(args)
	case "", "list", "ls":
		return runSkillsListCommand(args)
	case "show", "inspect":
		return runSkillsShowCommand(args)
	default:
		return fmt.Errorf("unknown skills subcommand: %s (use init, list, or show)", subCmd)
	}
}

func runSkillsInitCommand(args []string) error {
	fs := flag.NewFlagSet("skills init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	pathFlag := fs.String("path", ".", "project root where agent/skills should be created")
	description := fs.String("description", "", "model-facing routing description for the skill")
	allowedTools := fs.String("allowed-tools", "", "comma-separated allowed tool names for this skill")
	force := fs.Bool("force", false, "overwrite SKILL.md if it already exists")
	dryRun := fs.Bool("dry-run", false, "show what would be created without writing files")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: buckley skills init [--path <dir>] [--description <text>] [--allowed-tools a,b] [--force] [--dry-run] [--json|--format json] <skill>")
	}
	if err := normalizeJSONFormatFlag(*format, jsonOutput); err != nil {
		return err
	}
	result, err := initAgentSkill(agentSkillInitOptions{
		Root:         *pathFlag,
		Name:         fs.Arg(0),
		Description:  *description,
		AllowedTools: splitCommaList(*allowedTools),
		Force:        *force,
		DryRun:       *dryRun,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	printSkillsInitResult(os.Stdout, result)
	return nil
}

func runSkillsListCommand(args []string) error {
	fs := flag.NewFlagSet("skills list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	format := fs.String("format", "text", "output format: text or json")
	source := fs.String("source", "", "filter by source: bundled, plugin, personal, agent, or project")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: buckley skills [list] [--json|--format json] [--source <source>]")
	}
	if err := normalizeJSONFormatFlag(*format, jsonOutput); err != nil {
		return err
	}

	inspected, diagnostics := inspectSkills()
	list := skillsCommandList{
		Count:       inspected.snapshot.Count,
		BySource:    cloneIntMap(inspected.snapshot.BySource),
		Available:   append([]infoSkillEntry(nil), inspected.snapshot.Available...),
		Diagnostics: diagnostics,
	}
	if strings.TrimSpace(*source) != "" {
		list = filterSkillsCommandList(list, *source)
	}

	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(list)
	}
	printSkillsList(os.Stdout, list)
	return nil
}

func runSkillsShowCommand(args []string) error {
	fs := flag.NewFlagSet("skills show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: buckley skills show [--json|--format json] <skill>")
	}
	if err := normalizeJSONFormatFlag(*format, jsonOutput); err != nil {
		return err
	}

	inspected, _ := inspectSkills()
	s := inspected.registry.GetSkill(fs.Arg(0))
	if s == nil {
		return fmt.Errorf("skill %q not found", fs.Arg(0))
	}
	entry := skillCommandEntry(s)
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entry)
	}
	printSkillShow(os.Stdout, entry)
	return nil
}

func normalizeJSONFormatFlag(format string, jsonOutput *bool) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text":
	case "json":
		*jsonOutput = true
	default:
		return fmt.Errorf("unknown format %q (use text or json)", format)
	}
	return nil
}

func filterSkillsCommandList(list skillsCommandList, source string) skillsCommandList {
	source = strings.TrimSpace(source)
	if source == "" {
		return list
	}
	filtered := make([]infoSkillEntry, 0, len(list.Available))
	bySource := map[string]int{}
	for _, entry := range list.Available {
		if entry.Source != source {
			continue
		}
		filtered = append(filtered, entry)
		bySource[entry.Source]++
	}
	list.Count = len(filtered)
	list.BySource = bySource
	list.Available = filtered
	return list
}

type agentSkillInitOptions struct {
	Root         string
	Name         string
	Description  string
	AllowedTools []string
	Force        bool
	DryRun       bool
}

func initAgentSkill(opts agentSkillInitOptions) (skillsInitResult, error) {
	name, err := cleanAgentSkillName(opts.Name)
	if err != nil {
		return skillsInitResult{}, err
	}
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return skillsInitResult{}, fmt.Errorf("resolve skill init path: %w", err)
	}
	if info, err := os.Stat(absRoot); err == nil {
		if !info.IsDir() {
			return skillsInitResult{}, fmt.Errorf("skill init path is not a directory: %s", absRoot)
		}
	} else if !os.IsNotExist(err) {
		return skillsInitResult{}, fmt.Errorf("stat skill init path: %w", err)
	}

	skillDir := filepath.Join(absRoot, "agent", "skills", filepath.FromSlash(name))
	skillFile := filepath.Join(skillDir, "SKILL.md")
	result := skillsInitResult{
		Name:   name,
		Root:   absRoot,
		Path:   skillFile,
		DryRun: opts.DryRun,
	}
	if _, _, _, err := ensureSkillInitPath(absRoot, "", true, false, opts.DryRun); err != nil {
		return skillsInitResult{}, err
	}
	for _, dir := range []string{filepath.Join(absRoot, "agent"), filepath.Join(absRoot, "agent", "skills"), skillDir} {
		created, existing, _, err := ensureSkillInitPath(dir, "", true, false, opts.DryRun)
		if err != nil {
			return skillsInitResult{}, err
		}
		rel := agentInitRelativePath(absRoot, dir, true)
		if created {
			result.Created = append(result.Created, rel)
		} else if existing {
			result.Existing = append(result.Existing, rel)
		}
	}

	content := renderSkillInitMarkdown(name, opts.Description, opts.AllowedTools)
	created, existing, overwritten, err := ensureSkillInitPath(skillFile, content, false, opts.Force, opts.DryRun)
	if err != nil {
		return skillsInitResult{}, err
	}
	rel := agentInitRelativePath(absRoot, skillFile, false)
	switch {
	case overwritten:
		result.Overwritten = append(result.Overwritten, rel)
	case created:
		result.Created = append(result.Created, rel)
	case existing:
		result.Existing = append(result.Existing, rel)
	}
	return result, nil
}

func ensureSkillInitPath(path, content string, dir, force, dryRun bool) (created bool, existing bool, overwritten bool, err error) {
	info, statErr := os.Stat(path)
	if statErr == nil {
		if dir {
			if !info.IsDir() {
				return false, false, false, fmt.Errorf("skill init path exists and is not a directory: %s", path)
			}
			return false, true, false, nil
		}
		if info.IsDir() {
			return false, false, false, fmt.Errorf("skill init path exists and is a directory: %s", path)
		}
		if !force {
			return false, true, false, nil
		}
		if dryRun {
			return false, false, true, nil
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return false, false, false, fmt.Errorf("write skill file: %w", err)
		}
		return false, false, true, nil
	}
	if !os.IsNotExist(statErr) {
		return false, false, false, fmt.Errorf("stat skill init path: %w", statErr)
	}
	if dryRun {
		return true, false, false, nil
	}
	if dir {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return false, false, false, fmt.Errorf("create skill directory: %w", err)
		}
		return true, false, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, false, false, fmt.Errorf("create skill parent directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, false, false, fmt.Errorf("write skill file: %w", err)
	}
	return true, false, false, nil
}

func cleanAgentSkillName(value string) (string, error) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	value = strings.Trim(value, "/")
	if value == "" {
		return "", fmt.Errorf("skill name is required")
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid skill name %q", value)
		}
		for _, r := range part {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
				continue
			}
			return "", fmt.Errorf("invalid skill name %q: use letters, digits, dots, dashes, underscores, and slashes", value)
		}
	}
	return strings.Join(parts, "/"), nil
}

func renderSkillInitMarkdown(name, description string, allowedTools []string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		description = defaultSkillDescription(name)
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "description: %s\n", quoteYAMLString(description))
	allowedTools = cleanToolNames(allowedTools)
	sort.Strings(allowedTools)
	if len(allowedTools) > 0 {
		b.WriteString("allowed_tools:\n")
		for _, toolName := range allowedTools {
			fmt.Fprintf(&b, "  - %s\n", quoteYAMLString(toolName))
		}
	}
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n", skillTitle(name))
	b.WriteString("Use this procedure when the request matches the description above.\n\n")
	b.WriteString("## Steps\n\n")
	b.WriteString("- Inspect the relevant project context before changing files.\n")
	b.WriteString("- Apply the workflow in small, reviewable steps.\n")
	b.WriteString("- Report validation performed and any remaining risks.\n")
	return b.String()
}

func defaultSkillDescription(name string) string {
	label := strings.ToLower(skillTitle(name))
	return fmt.Sprintf("Use when the user needs the %s workflow.", label)
}

func skillTitle(name string) string {
	name = strings.ReplaceAll(filepath.Base(filepath.ToSlash(name)), "-", " ")
	name = strings.ReplaceAll(name, "_", " ")
	words := strings.Fields(name)
	for i, word := range words {
		runes := []rune(word)
		if len(runes) == 0 {
			continue
		}
		runes[0] = unicode.ToUpper(runes[0])
		words[i] = string(runes)
	}
	if len(words) == 0 {
		return "Skill"
	}
	return strings.Join(words, " ")
}

func quoteYAMLString(value string) string {
	data, err := json.Marshal(strings.TrimSpace(value))
	if err != nil {
		return "\"\""
	}
	return string(data)
}

func splitCommaList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func printSkillsInitResult(w io.Writer, result skillsInitResult) {
	action := "Created"
	if result.DryRun {
		action = "Would create"
	}
	fmt.Fprintf(w, "%s agent skill %s at %s\n", action, result.Name, result.Path)
	printPathList(w, "Created", "Would create", result.Created, result.DryRun)
	printPathList(w, "Overwritten", "Would overwrite", result.Overwritten, result.DryRun)
	printPathList(w, "Existing", "Existing", result.Existing, false)
	fmt.Fprintf(w, "Next: buckley skills show %s\n", result.Name)
}

func printPathList(w io.Writer, label, dryLabel string, paths []string, dryRun bool) {
	if len(paths) == 0 {
		return
	}
	if dryRun {
		label = dryLabel
	}
	fmt.Fprintf(w, "%s:\n", label)
	for _, path := range paths {
		fmt.Fprintf(w, "  - %s\n", path)
	}
}

func printSkillsList(w io.Writer, list skillsCommandList) {
	fmt.Fprintf(w, "Skills: %d", list.Count)
	if len(list.BySource) > 0 {
		fmt.Fprintf(w, " (%s)", renderCounts(list.BySource))
	}
	fmt.Fprintln(w)
	for _, entry := range list.Available {
		fmt.Fprintf(w, "  - %s", entry.Name)
		if entry.Source != "" {
			fmt.Fprintf(w, " [%s]", entry.Source)
		}
		if entry.Description != "" {
			fmt.Fprintf(w, ": %s", entry.Description)
		}
		if entry.Phase != "" {
			fmt.Fprintf(w, ", phase=%s", entry.Phase)
		}
		if len(entry.AllowedTools) > 0 {
			fmt.Fprintf(w, ", allowed_tools=%s", strings.Join(entry.AllowedTools, ","))
		}
		if entry.Path != "" {
			fmt.Fprintf(w, ", path=%s", entry.Path)
		}
		fmt.Fprintln(w)
	}
	if len(list.Diagnostics) > 0 {
		fmt.Fprintln(w, "Diagnostics:")
		for _, diagnostic := range list.Diagnostics {
			fmt.Fprintf(w, "  - %s\n", diagnostic)
		}
	}
}

func printSkillShow(w io.Writer, entry skillsCommandEntry) {
	fmt.Fprintf(w, "Skill: %s\n", entry.Name)
	if entry.Description != "" {
		fmt.Fprintf(w, "Description: %s\n", entry.Description)
	}
	if entry.Source != "" {
		fmt.Fprintf(w, "Source: %s\n", entry.Source)
	}
	if entry.Path != "" {
		fmt.Fprintf(w, "Path: %s\n", entry.Path)
	}
	if entry.Phase != "" {
		fmt.Fprintf(w, "Phase: %s\n", entry.Phase)
	}
	if len(entry.AllowedTools) > 0 {
		fmt.Fprintf(w, "Allowed tools: %s\n", strings.Join(entry.AllowedTools, ", "))
	}
	if entry.Content != "" {
		fmt.Fprintln(w, "\nContent:")
		fmt.Fprintln(w, strings.TrimSpace(entry.Content))
	}
}

func skillCommandEntry(s *skill.Skill) skillsCommandEntry {
	if s == nil {
		return skillsCommandEntry{}
	}
	allowedTools := cleanToolNames(s.AllowedTools)
	sort.Strings(allowedTools)
	return skillsCommandEntry{
		Name:         s.Name,
		Description:  s.Description,
		Source:       s.Source,
		Path:         s.FilePath,
		Phase:        s.Phase,
		AllowedTools: allowedTools,
		Content:      s.Content,
	}
}

func cloneIntMap(values map[string]int) map[string]int {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]int, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
