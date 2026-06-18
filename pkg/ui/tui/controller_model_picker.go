package tui

import (
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/ui/widgets"
)

func buildModelPickerItems(catalog []model.ModelInfo, mgr *model.Manager, execID, planID, reviewID string, curated map[string]struct{}) ([]widgets.PaletteItem, map[string]model.ModelInfo) {
	catalogIndex, grouped := indexModelCatalog(catalog, mgr)
	pinnedIDs := preferredModelIDs(execID, planID, reviewID, catalogIndex)
	pinnedSet := make(map[string]struct{}, len(pinnedIDs))

	items := make([]widgets.PaletteItem, 0, len(catalog))
	items = appendPinnedModelItems(items, pinnedSet, catalogIndex, pinnedIDs, execID, planID, reviewID, curated)
	items = appendGroupedModelItems(items, pinnedSet, grouped, execID, planID, reviewID, curated)
	return items, catalogIndex
}

func indexModelCatalog(catalog []model.ModelInfo, mgr *model.Manager) (map[string]model.ModelInfo, map[string][]model.ModelInfo) {
	catalogIndex := make(map[string]model.ModelInfo, len(catalog))
	grouped := make(map[string][]model.ModelInfo)
	for _, info := range catalog {
		catalogIndex[info.ID] = info
		group := modelGroupKey(info.ID, mgr)
		grouped[group] = append(grouped[group], info)
	}
	return catalogIndex, grouped
}

func appendPinnedModelItems(items []widgets.PaletteItem, pinnedSet map[string]struct{}, catalogIndex map[string]model.ModelInfo, pinnedIDs []string, execID, planID, reviewID string, curated map[string]struct{}) []widgets.PaletteItem {
	for _, modelID := range pinnedIDs {
		info, ok := catalogIndex[modelID]
		if !ok {
			continue
		}
		pinnedSet[modelID] = struct{}{}
		tags := modelRoleTags(modelID, execID, planID, reviewID)
		tags = appendModelTag(tags, curated, modelID)
		items = append(items, modelPickerItem(info.ID, "Pinned", modelID, tags))
	}
	return items
}

func appendGroupedModelItems(items []widgets.PaletteItem, pinnedSet map[string]struct{}, grouped map[string][]model.ModelInfo, execID, planID, reviewID string, curated map[string]struct{}) []widgets.PaletteItem {
	groups := sortedModelGroups(grouped)
	for _, group := range groups {
		models := sortedModelGroupEntries(grouped[group])
		for _, info := range models {
			if _, ok := pinnedSet[info.ID]; ok {
				continue
			}
			label := modelLabel(info.ID, group)
			tags := modelRoleTags(info.ID, execID, planID, reviewID)
			tags = appendModelTag(tags, curated, info.ID)
			items = append(items, modelPickerItem(info.ID, group, label, tags))
		}
	}
	return items
}

func sortedModelGroups(grouped map[string][]model.ModelInfo) []string {
	groups := make([]string, 0, len(grouped))
	for group := range grouped {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return groups
}

func sortedModelGroupEntries(models []model.ModelInfo) []model.ModelInfo {
	sorted := append([]model.ModelInfo(nil), models...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ID < sorted[j].ID
	})
	return sorted
}

func modelPickerItem(modelID, category, label string, tags []string) widgets.PaletteItem {
	return widgets.PaletteItem{
		ID:          modelID,
		Category:    category,
		Label:       "  " + label,
		Description: modelID,
		Shortcut:    strings.Join(tags, ","),
		Data:        modelID,
	}
}
