package tui

import (
	"sort"
	"strings"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/fluffyui/widgets"
)

func (c *Controller) showModelPickerLocked() {
	items, _ := c.collectModelPickerItemsLocked(nil)
	if len(items) == 0 {
		return
	}

	c.app.ShowModelPicker(items, func(item widgets.PaletteItem) {
		modelID := item.ID
		if id, ok := item.Data.(string); ok && strings.TrimSpace(id) != "" {
			modelID = id
		}
		c.setExecutionModel(modelID)
	})
}

func (c *Controller) showModelPicker() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.showModelPickerLocked()
}

func (c *Controller) collectModelPickerItemsLocked(curated map[string]struct{}) ([]widgets.PaletteItem, map[string]model.ModelInfo) {
	if c.modelMgr == nil {
		c.app.AddMessage("Model catalog unavailable in this session.", "system")
		return nil, nil
	}

	catalog := c.modelMgr.GetCatalog()
	if catalog == nil || len(catalog.Data) == 0 {
		c.app.AddMessage("No models available from configured providers.", "system")
		return nil, nil
	}

	execID := strings.TrimSpace(c.cfg.Models.Execution)
	planID := strings.TrimSpace(c.cfg.Models.Planning)
	reviewID := strings.TrimSpace(c.cfg.Models.Review)

	catalogIndex := make(map[string]model.ModelInfo, len(catalog.Data))
	grouped := make(map[string][]model.ModelInfo)
	for _, info := range catalog.Data {
		catalogIndex[info.ID] = info
		group := modelGroupKey(info.ID, c.modelMgr)
		grouped[group] = append(grouped[group], info)
	}

	groups := make([]string, 0, len(grouped))
	for group := range grouped {
		groups = append(groups, group)
	}
	sort.Strings(groups)

	items := make([]widgets.PaletteItem, 0, len(catalog.Data))
	pinnedIDs := preferredModelIDs(execID, planID, reviewID, catalogIndex)
	pinnedSet := make(map[string]struct{}, len(pinnedIDs))
	if len(pinnedIDs) > 0 {
		for _, modelID := range pinnedIDs {
			info, ok := catalogIndex[modelID]
			if !ok {
				continue
			}
			pinnedSet[modelID] = struct{}{}
			tags := modelRoleTags(modelID, execID, planID, reviewID)
			tags = appendModelTag(tags, curated, modelID)
			items = append(items, widgets.PaletteItem{
				ID:          modelID,
				Category:    "Pinned",
				Label:       "  " + modelID,
				Description: info.ID,
				Shortcut:    strings.Join(tags, ","),
				Data:        modelID,
			})
		}
	}
	for _, group := range groups {
		models := grouped[group]
		sort.Slice(models, func(i, j int) bool {
			return models[i].ID < models[j].ID
		})

		for _, info := range models {
			if _, ok := pinnedSet[info.ID]; ok {
				continue
			}
			label := modelLabel(info.ID, group)
			tags := modelRoleTags(info.ID, execID, planID, reviewID)
			tags = appendModelTag(tags, curated, info.ID)
			items = append(items, widgets.PaletteItem{
				ID:          info.ID,
				Category:    group,
				Label:       "  " + label,
				Description: info.ID,
				Shortcut:    strings.Join(tags, ","),
				Data:        info.ID,
			})
		}
	}

	return items, catalogIndex
}

func (c *Controller) setExecutionModel(modelID string) {
	c.mu.Lock()
	c.setExecutionModelLocked(modelID)
	sess := (*SessionState)(nil)
	if len(c.sessions) > 0 && c.currentSession >= 0 && c.currentSession < len(c.sessions) {
		sess = c.sessions[c.currentSession]
	}
	c.mu.Unlock()
	if sess != nil {
		c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
	}
}

func (c *Controller) setExecutionModelLocked(modelID string) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		c.app.AddMessage("Model ID required. Try /model to open the picker.", "system")
		return
	}

	if c.modelMgr != nil && !catalogHasModel(c.modelMgr, modelID) {
		c.app.AddMessage("Model not found in catalog: "+modelID, "system")
		return
	}

	c.cfg.Models.Execution = modelID
	c.app.SetModelName(modelID)
	c.app.AddMessage("Execution model set to "+modelID, "system")
}

func catalogHasModel(mgr *model.Manager, modelID string) bool {
	if mgr == nil {
		return true
	}
	catalog := mgr.GetCatalog()
	if catalog == nil {
		return false
	}
	for _, info := range catalog.Data {
		if info.ID == modelID {
			return true
		}
	}
	return false
}

func modelGroupKey(modelID string, mgr *model.Manager) string {
	if parts := strings.SplitN(modelID, "/", 2); len(parts) == 2 {
		return parts[0]
	}
	if mgr != nil {
		if provider := mgr.ProviderIDForModel(modelID); provider != "" {
			return provider
		}
	}
	return "other"
}

func modelLabel(modelID, group string) string {
	label := modelID
	prefix := group + "/"
	if group != "" && group != "other" && strings.HasPrefix(modelID, prefix) {
		label = strings.TrimPrefix(modelID, prefix)
	}
	if strings.TrimSpace(label) == "" {
		return modelID
	}
	return label
}

func modelRoleTags(modelID, execID, planID, reviewID string) []string {
	var tags []string
	if execID != "" && modelID == execID {
		tags = append(tags, "exec")
	}
	if planID != "" && modelID == planID {
		tags = append(tags, "plan")
	}
	if reviewID != "" && modelID == reviewID {
		tags = append(tags, "review")
	}
	return tags
}

func preferredModelIDs(execID, planID, reviewID string, catalog map[string]model.ModelInfo) []string {
	ids := make([]string, 0, 4)
	seen := make(map[string]struct{})
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		if catalog != nil {
			if _, ok := catalog[id]; !ok {
				return
			}
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	add(execID)
	add(planID)
	add(reviewID)
	return ids
}
