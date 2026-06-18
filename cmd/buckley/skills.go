package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/skill"
)

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
	case "", "list", "ls":
		return runSkillsListCommand(args)
	case "show", "inspect":
		return runSkillsShowCommand(args)
	default:
		return fmt.Errorf("unknown skills subcommand: %s (use list or show)", subCmd)
	}
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
