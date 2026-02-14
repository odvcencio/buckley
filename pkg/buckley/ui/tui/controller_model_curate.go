package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odvcencio/fluffyui/widgets"
	"gopkg.in/yaml.v3"
)

func (c *Controller) handleModelCurate(args []string) {
	if len(args) == 0 {
		c.showModelCuratePicker()
		return
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "list":
		c.showCuratedModels()
	case "clear":
		c.mu.Lock()
		c.cfg.Models.Curated = nil
		c.mu.Unlock()
		c.app.AddMessage("Cleared curated models. Use /model curate save to persist.", "system")
	case "save":
		target := "project"
		if len(args) > 1 {
			target = strings.ToLower(args[1])
		}
		c.saveCuratedModels(target)
	case "add":
		modelID := strings.TrimSpace(strings.Join(args[1:], " "))
		if modelID == "" {
			c.app.AddMessage("Model ID required. Usage: /model curate add <id>", "system")
			return
		}
		c.addCuratedModel(modelID)
	case "remove", "rm":
		modelID := strings.TrimSpace(strings.Join(args[1:], " "))
		if modelID == "" {
			c.app.AddMessage("Model ID required. Usage: /model curate remove <id>", "system")
			return
		}
		c.removeCuratedModel(modelID)
	default:
		c.app.AddMessage("Usage: /model curate [list|add|remove|clear|save]", "system")
	}
}

func (c *Controller) showModelCuratePickerLocked() {
	curatedSet := curatedModelSet(c.cfg.Models.Curated)
	items, _ := c.collectModelPickerItemsLocked(curatedSet)
	if len(items) == 0 {
		return
	}

	c.app.ShowModelPicker(items, func(item widgets.PaletteItem) {
		modelID := item.ID
		if id, ok := item.Data.(string); ok && strings.TrimSpace(id) != "" {
			modelID = id
		}
		changed := c.toggleCuratedModel(modelID)
		if changed {
			c.app.AddMessage("Curated models updated. Use /model curate save to persist.", "system")
		}
	})
}

func (c *Controller) showModelCuratePicker() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.showModelCuratePickerLocked()
}

func (c *Controller) toggleCuratedModel(modelID string) bool {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return false
	}
	if c.modelMgr != nil && !catalogHasModel(c.modelMgr, modelID) {
		c.app.AddMessage("Model not found in catalog: "+modelID, "system")
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	curated := append([]string{}, c.cfg.Models.Curated...)
	for i, id := range curated {
		if id == modelID {
			curated = append(curated[:i], curated[i+1:]...)
			c.cfg.Models.Curated = curated
			return true
		}
	}
	curated = append(curated, modelID)
	c.cfg.Models.Curated = curated
	return true
}

func (c *Controller) addCuratedModel(modelID string) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return
	}
	if c.modelMgr != nil && !catalogHasModel(c.modelMgr, modelID) {
		c.app.AddMessage("Model not found in catalog: "+modelID, "system")
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if slices.Contains(c.cfg.Models.Curated, modelID) {
		c.app.AddMessage("Model already in curated list: "+modelID, "system")
		return
	}
	c.cfg.Models.Curated = append(c.cfg.Models.Curated, modelID)
	c.app.AddMessage("Added model to curated list. Use /model curate save to persist.", "system")
}

func (c *Controller) removeCuratedModel(modelID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	curated := append([]string{}, c.cfg.Models.Curated...)
	for i, id := range curated {
		if id == modelID {
			curated = append(curated[:i], curated[i+1:]...)
			c.cfg.Models.Curated = curated
			c.app.AddMessage("Removed model from curated list. Use /model curate save to persist.", "system")
			return
		}
	}
	c.app.AddMessage("Model not in curated list: "+modelID, "system")
}

func (c *Controller) showCuratedModelsLocked() {
	curated := append([]string{}, c.cfg.Models.Curated...)

	if len(curated) == 0 {
		c.app.AddMessage("Curated list is empty. ACP will use execution/planning/review defaults.", "system")
		return
	}
	c.app.AddMessage("Curated models:\n- "+strings.Join(curated, "\n- "), "system")
}

func (c *Controller) showCuratedModels() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.showCuratedModelsLocked()
}

func (c *Controller) saveCuratedModels(target string) {
	c.mu.Lock()
	curated := append([]string{}, c.cfg.Models.Curated...)
	c.mu.Unlock()

	path, err := curatedConfigPath(c.workDir, target)
	if err != nil {
		c.app.AddMessage("Could not resolve config path: "+err.Error(), "system")
		return
	}
	if err := writeCuratedModels(path, curated); err != nil {
		c.app.AddMessage("Failed to write curated models: "+err.Error(), "system")
		return
	}
	c.app.AddMessage("Saved curated models to "+path, "system")
}

func curatedConfigPath(workDir, target string) (string, error) {
	target = strings.TrimSpace(strings.ToLower(target))
	switch target {
	case "", "project":
		return filepath.Join(workDir, ".buckley", "config.yaml"), nil
	case "user", "global":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".buckley", "config.yaml"), nil
	default:
		return "", fmt.Errorf("unknown target %q (use project or user)", target)
	}
}

func writeCuratedModels(path string, curated []string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	var raw map[string]any
	data, err := os.ReadFile(path)
	if err == nil {
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if raw == nil {
		raw = make(map[string]any)
	}

	modelsRaw, ok := raw["models"].(map[string]any)
	if !ok {
		modelsRaw = make(map[string]any)
	}
	modelsRaw["curated"] = curated
	raw["models"] = modelsRaw

	out, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func curatedModelSet(models []string) map[string]struct{} {
	set := make(map[string]struct{}, len(models))
	for _, id := range models {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

func appendModelTag(tags []string, curated map[string]struct{}, modelID string) []string {
	if curated == nil {
		return tags
	}
	if _, ok := curated[modelID]; ok {
		if len(tags) == 0 {
			return []string{"curated"}
		}
		return append(tags, "curated")
	}
	return tags
}
