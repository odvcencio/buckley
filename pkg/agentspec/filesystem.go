package agentspec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

func LoadFilesystemRuntimeProfile(agentRoot string) (*RuntimeProfile, error) {
	spec, extraDiagnostics, err := LoadFilesystemSpec(agentRoot)
	if err != nil {
		return nil, err
	}
	diagnostics := append([]Diagnostic{}, spec.Validate()...)
	diagnostics = append(diagnostics, extraDiagnostics...)
	if hasErrors(diagnostics) {
		return nil, fmt.Errorf("invalid agent layout %s:\n%s", agentRoot, formatDiagnostics(diagnostics))
	}
	files, err := loadInstructionFiles(spec.Instructions.Files)
	if err != nil {
		return nil, err
	}
	return &RuntimeProfile{
		SourcePath:       spec.Metadata["agent_root"],
		Spec:             spec,
		InstructionFiles: files,
	}, nil
}

func LoadFilesystemSpec(agentRoot string) (*Spec, []Diagnostic, error) {
	agentRoot = strings.TrimSpace(agentRoot)
	if agentRoot == "" {
		return nil, nil, fmt.Errorf("agent layout path is required")
	}
	absRoot, err := filepath.Abs(agentRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve agent layout path: %w", err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("stat agent layout: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("agent layout path is not a directory: %s", absRoot)
	}

	appRoot := filesystemAppRoot(absRoot)
	var d diagnostics
	instructionFiles, err := filesystemInstructionFiles(absRoot, "instructions", true, &d)
	if err != nil {
		return nil, nil, err
	}
	skills, err := filesystemMarkdownSlugs(filepath.Join(absRoot, "skills"))
	if err != nil {
		return nil, nil, err
	}
	subagents, err := filesystemSubagents(absRoot, &d)
	if err != nil {
		return nil, nil, err
	}
	filesystemWarnUnsupportedRootSlots(absRoot, &d)

	spec := &Spec{
		Version: Version,
		Name:    filesystemAgentName(appRoot),
		Summary: "Filesystem agent layout",
		Instructions: InstructionSpec{
			Files: instructionFiles,
		},
		Skills:    skills,
		Subagents: subagents,
		Metadata: map[string]string{
			"layout":     DiscoveredKindFilesystem,
			"agent_root": absRoot,
			"app_root":   appRoot,
		},
	}
	return spec, d.items, nil
}

func filesystemAppRoot(agentRoot string) string {
	if filepath.Base(agentRoot) == "agent" {
		return filepath.Dir(agentRoot)
	}
	return agentRoot
}

func filesystemAgentName(appRoot string) string {
	if name := filesystemPackageName(appRoot); name != "" {
		return normalizeFilesystemIdentifier(name)
	}
	return normalizeFilesystemIdentifier(filepath.Base(appRoot))
}

func filesystemPackageName(appRoot string) string {
	data, err := os.ReadFile(filepath.Join(appRoot, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	return strings.TrimSpace(pkg.Name)
}

func normalizeFilesystemIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if idx := strings.LastIndex(value, "/"); idx >= 0 {
		value = value[idx+1:]
	}
	value = strings.TrimPrefix(value, "@")
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		allowed := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '-'
		if !allowed {
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
			continue
		}
		b.WriteRune(r)
		lastDash = r == '-'
	}
	out := strings.Trim(b.String(), ".-_")
	if out == "" {
		return "agent"
	}
	first := []rune(out)[0]
	if !unicode.IsLetter(first) && !unicode.IsDigit(first) {
		return "agent-" + out
	}
	return out
}

func filesystemInstructionFiles(root, slot string, required bool, d *diagnostics) ([]string, error) {
	files := []string{}
	filePath := filepath.Join(root, slot+".md")
	if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
		files = append(files, filePath)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat filesystem instruction file: %w", err)
	}

	dirPath := filepath.Join(root, slot)
	dirFiles, err := filesystemMarkdownFiles(dirPath)
	if err != nil {
		return nil, err
	}
	files = append(files, dirFiles...)
	sort.Strings(files)

	tsPath := filepath.Join(root, slot+".ts")
	if info, err := os.Stat(tsPath); err == nil && !info.IsDir() {
		d.add(SeverityWarning, slot, "typescript instructions are not loaded by Buckley; add markdown instructions for compatibility")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat filesystem instruction module: %w", err)
	}
	if required && len(files) == 0 {
		d.add(SeverityError, slot, "markdown instructions not found; add instructions.md or instructions/*.md")
	}
	return files, nil
}

func filesystemSubagents(agentRoot string, d *diagnostics) ([]SubagentSpec, error) {
	subagents := []SubagentSpec{{Name: "agent"}}
	subagentsDir := filepath.Join(agentRoot, "subagents")
	entries, err := os.ReadDir(subagentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return subagents, nil
		}
		return nil, fmt.Errorf("read filesystem subagents: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || name == "agent" {
			continue
		}
		root := filepath.Join(subagentsDir, entry.Name())
		instructionFiles, err := filesystemInstructionFiles(root, "instructions", false, d)
		if err != nil {
			return nil, err
		}
		instructions, err := readFilesystemInstructionBundle(root, instructionFiles)
		if err != nil {
			return nil, err
		}
		skills, err := filesystemMarkdownSlugs(filepath.Join(root, "skills"))
		if err != nil {
			return nil, err
		}
		filesystemWarnUnsupportedSubagentSlots(name, root, d)
		subagents = append(subagents, SubagentSpec{
			Name:         name,
			Skills:       skills,
			Instructions: instructions,
		})
	}
	sort.Slice(subagents, func(i, j int) bool {
		return subagents[i].Name < subagents[j].Name
	})
	return subagents, nil
}

func readFilesystemInstructionBundle(root string, paths []string) (string, error) {
	parts := []string{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading filesystem subagent instructions %s: %w", path, err)
		}
		content := strings.TrimSpace(string(data))
		if len(content) > MaxInstructionFileChars {
			content = content[:MaxInstructionFileChars] + "\n[truncated]"
		}
		label := path
		if rel, err := filepath.Rel(root, path); err == nil {
			label = rel
		}
		parts = append(parts, fmt.Sprintf("## %s\n%s", filepath.ToSlash(label), content))
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n")), nil
}

func filesystemMarkdownSlugs(root string) ([]string, error) {
	files, err := filesystemMarkdownFiles(root)
	if err != nil {
		return nil, err
	}
	slugs := make([]string, 0, len(files))
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, fmt.Errorf("resolve filesystem markdown slug: %w", err)
		}
		rel = filepath.ToSlash(rel)
		slugs = append(slugs, strings.TrimSuffix(rel, filepath.Ext(rel)))
	}
	sort.Strings(slugs)
	return slugs, nil
}

func filesystemMarkdownFiles(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat filesystem markdown path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("filesystem markdown path is not a directory: %s", root)
	}
	files := []string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read filesystem markdown path: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

func filesystemWarnUnsupportedRootSlots(agentRoot string, d *diagnostics) {
	for _, slot := range []string{"tools", "connections", "channels", "schedules", "hooks", "sandbox"} {
		filesystemWarnUnsupportedSlot(slot, filepath.Join(agentRoot, slot), d)
	}
	for _, file := range []string{"agent.ts", "sandbox.ts", "instrumentation.ts"} {
		filesystemWarnUnsupportedSlot(file, filepath.Join(agentRoot, file), d)
	}
}

func filesystemWarnUnsupportedSubagentSlots(name, root string, d *diagnostics) {
	prefix := "subagents." + name + "."
	for _, slot := range []string{"tools", "connections", "hooks", "sandbox"} {
		filesystemWarnUnsupportedSlot(prefix+slot, filepath.Join(root, slot), d)
	}
	for _, file := range []string{"agent.ts", "sandbox.ts"} {
		filesystemWarnUnsupportedSlot(prefix+file, filepath.Join(root, file), d)
	}
}

func filesystemWarnUnsupportedSlot(path string, diskPath string, d *diagnostics) {
	if _, err := os.Stat(diskPath); err == nil {
		d.add(SeverityWarning, path, "authored code slot discovered; Buckley imports markdown instructions, skills, and subagents but does not execute this slot yet")
	} else if err != nil && !os.IsNotExist(err) {
		d.add(SeverityWarning, path, fmt.Sprintf("could not inspect authored code slot: %v", err))
	}
}
