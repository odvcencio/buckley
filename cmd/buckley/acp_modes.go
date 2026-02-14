package main

import (
	"strings"

	"github.com/odvcencio/buckley/pkg/acp"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
)

func buildACPModelModes(cfg *config.Config, mgr *model.Manager) *acp.SessionModeState {
	curated := curatedModelIDs(cfg, mgr)
	if len(curated) == 0 {
		return nil
	}

	modes := make([]acp.SessionMode, 0, len(curated))
	for _, modelID := range curated {
		if strings.TrimSpace(modelID) == "" {
			continue
		}
		name := modelID
		desc := modelID
		if mgr != nil {
			if info, err := mgr.GetModelInfo(modelID); err == nil {
				if strings.TrimSpace(info.Name) != "" {
					name = info.Name
				}
				if strings.TrimSpace(info.Description) != "" {
					desc = info.Description
				}
			}
		}

		modes = append(modes, acp.SessionMode{
			ID:          acpModePrefix + modelID,
			Name:        name,
			Description: desc,
		})
	}
	if len(modes) == 0 {
		return nil
	}

	current := modes[0].ID
	return &acp.SessionModeState{
		CurrentModeID:  current,
		AvailableModes: modes,
	}
}

func curatedModelIDs(cfg *config.Config, mgr *model.Manager) []string {
	var base []string
	if cfg != nil && len(cfg.Models.Curated) > 0 {
		base = append([]string{}, cfg.Models.Curated...)
	} else if cfg != nil {
		base = []string{
			cfg.Models.Execution,
			cfg.Models.Planning,
			cfg.Models.Review,
		}
	}

	execID := ""
	if cfg != nil {
		execID = strings.TrimSpace(cfg.Models.Execution)
	}
	if execID != "" && (len(base) == 0 || strings.TrimSpace(base[0]) != execID) {
		base = append([]string{execID}, base...)
	}

	return filterCuratedModels(base, mgr)
}

func filterCuratedModels(ids []string, mgr *model.Manager) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		if mgr != nil && !acpCatalogHasModel(mgr, id) {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 && len(ids) > 0 {
		id := strings.TrimSpace(ids[0])
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func acpCatalogHasModel(mgr *model.Manager, modelID string) bool {
	if mgr == nil {
		return true
	}
	catalog := mgr.GetCatalog()
	if catalog == nil {
		return true
	}
	for _, info := range catalog.Data {
		if info.ID == modelID {
			return true
		}
	}
	return false
}

func resolveACPModelOverride(cfg *config.Config, mgr *model.Manager, modeID string) string {
	modeID = strings.TrimSpace(modeID)
	if modeID == "" || modeID == acpDefaultMode {
		return ""
	}
	if strings.HasPrefix(modeID, acpModePrefix) {
		modeID = strings.TrimPrefix(modeID, acpModePrefix)
	}
	if mgr != nil && !acpCatalogHasModel(mgr, modeID) {
		return ""
	}
	return modeID
}
