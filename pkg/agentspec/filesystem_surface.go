package agentspec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FilesystemSurface struct {
	AgentRoot string           `json:"agent_root"`
	AppRoot   string           `json:"app_root"`
	Slots     []FilesystemSlot `json:"slots,omitempty"`
}

type FilesystemSlot struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Supported bool   `json:"supported"`
	Count     int    `json:"count,omitempty"`
}

func InspectFilesystemSurface(agentRoot string) (FilesystemSurface, error) {
	agentRoot = strings.TrimSpace(agentRoot)
	if agentRoot == "" {
		return FilesystemSurface{}, fmt.Errorf("agent layout path is required")
	}
	absRoot, err := filepath.Abs(agentRoot)
	if err != nil {
		return FilesystemSurface{}, fmt.Errorf("resolve agent layout path: %w", err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return FilesystemSurface{}, fmt.Errorf("stat agent layout: %w", err)
	}
	if !info.IsDir() {
		return FilesystemSurface{}, fmt.Errorf("agent layout path is not a directory: %s", absRoot)
	}

	appRoot := filesystemAppRoot(absRoot)
	surface := FilesystemSurface{
		AgentRoot: absRoot,
		AppRoot:   appRoot,
	}
	for _, slot := range []filesystemSlotDefinition{
		{name: "instructions", path: absRoot, supported: true, count: countFilesystemInstructions},
		{name: "skills", path: filepath.Join(absRoot, "skills"), supported: true, count: countFilesystemSkills},
		{name: "subagents", path: filepath.Join(absRoot, "subagents"), supported: true, count: countFilesystemSubagents},
		{name: "evals", path: filepath.Join(appRoot, "evals"), supported: true, count: countFilesystemEvalScenarios},
		{name: "tools", path: filepath.Join(absRoot, "tools"), count: countFilesystemRegularFiles},
		{name: "connections", path: filepath.Join(absRoot, "connections"), count: countFilesystemRegularFiles},
		{name: "channels", path: filepath.Join(absRoot, "channels"), count: countFilesystemRegularFiles},
		{name: "schedules", path: filepath.Join(absRoot, "schedules"), count: countFilesystemRegularFiles},
		{name: "hooks", path: filepath.Join(absRoot, "hooks"), count: countFilesystemRegularFiles},
		{name: "sandbox", path: filepath.Join(absRoot, "sandbox"), count: countFilesystemRegularFiles},
		{name: "lib", path: filepath.Join(absRoot, "lib"), count: countFilesystemRegularFiles},
		{name: "agent.ts", path: filepath.Join(absRoot, "agent.ts"), count: countFilesystemFile},
		{name: "instructions.ts", path: filepath.Join(absRoot, "instructions.ts"), count: countFilesystemFile},
		{name: "sandbox.ts", path: filepath.Join(absRoot, "sandbox.ts"), count: countFilesystemFile},
		{name: "instrumentation.ts", path: filepath.Join(absRoot, "instrumentation.ts"), count: countFilesystemFile},
	} {
		item, ok, err := inspectFilesystemSlot(slot)
		if err != nil {
			return FilesystemSurface{}, err
		}
		if ok {
			surface.Slots = append(surface.Slots, item)
		}
	}
	sort.Slice(surface.Slots, func(i, j int) bool {
		if surface.Slots[i].Supported != surface.Slots[j].Supported {
			return surface.Slots[i].Supported
		}
		return surface.Slots[i].Name < surface.Slots[j].Name
	})
	return surface, nil
}

type filesystemSlotDefinition struct {
	name      string
	path      string
	supported bool
	count     func(string) (int, bool, error)
}

func inspectFilesystemSlot(def filesystemSlotDefinition) (FilesystemSlot, bool, error) {
	count, exists, err := def.count(def.path)
	if err != nil {
		return FilesystemSlot{}, false, fmt.Errorf("inspect filesystem slot %s: %w", def.name, err)
	}
	if !exists {
		return FilesystemSlot{}, false, nil
	}
	return FilesystemSlot{
		Name:      def.name,
		Path:      def.path,
		Supported: def.supported,
		Count:     count,
	}, true, nil
}

func countFilesystemInstructions(root string) (int, bool, error) {
	count := 0
	filePath := filepath.Join(root, "instructions.md")
	if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
		count++
	} else if err != nil && !os.IsNotExist(err) {
		return 0, false, err
	}
	dirFiles, err := filesystemMarkdownFiles(filepath.Join(root, "instructions"))
	if err != nil {
		return 0, false, err
	}
	count += len(dirFiles)
	if count == 0 {
		if exists, err := pathExists(filepath.Join(root, "instructions")); err != nil || exists {
			return count, exists, err
		}
		return 0, false, nil
	}
	return count, true, nil
}

func countFilesystemSkills(root string) (int, bool, error) {
	exists, err := pathExists(root)
	if err != nil || !exists {
		return 0, exists, err
	}
	slugs, err := filesystemSkillSlugs(root)
	if err != nil {
		return 0, false, err
	}
	return len(slugs), true, nil
}

func countFilesystemSubagents(root string) (int, bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.TrimSpace(entry.Name()) != "" {
			count++
		}
	}
	return count, true, nil
}

func countFilesystemEvalScenarios(root string) (int, bool, error) {
	return countFilesystemFiles(root, func(path string) bool {
		ext := strings.ToLower(filepath.Ext(path))
		return ext == ".json" || ext == ".yaml" || ext == ".yml"
	})
}

func countFilesystemRegularFiles(root string) (int, bool, error) {
	return countFilesystemFiles(root, func(string) bool { return true })
}

func countFilesystemFile(path string) (int, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if info.IsDir() {
		return 0, true, nil
	}
	return 1, true, nil
}

func countFilesystemFiles(root string, include func(string) bool) (int, bool, error) {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if !info.IsDir() {
		return 1, true, nil
	}
	count := 0
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if include(path) {
			count++
		}
		return nil
	}); err != nil {
		return 0, false, err
	}
	return count, true, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
