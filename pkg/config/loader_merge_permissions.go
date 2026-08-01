package config

import "m31labs.dev/buckley/v2/pkg/policy"

// mergePermissionsConfig merges the permissions.project and
// permissions.user glob-rule lists. Both scopes are eligible for both
// user (~/.buckley/config.yaml) and project (./.buckley/config.yaml)
// files: a user config sets permissions.user for personal defaults, and a
// project config typically sets permissions.project, but neither is
// restricted to a single file (matching the existing list-merge
// convention, e.g. mergeApprovalConfig's trusted_paths/denied_paths).
func mergePermissionsConfig(base, override *Config, raw map[string]any) {
	if boolFieldSet(raw, "permissions", "project") {
		base.Permissions.Project = append([]policy.PermissionRule{}, override.Permissions.Project...)
	}
	if boolFieldSet(raw, "permissions", "user") {
		base.Permissions.User = append([]policy.PermissionRule{}, override.Permissions.User...)
	}
}

// mergePosturesConfig merges postures.default and postures.layers. Layers
// are merged per-name so a project config can add or override a single
// posture without needing to restate every built-in layer.
func mergePosturesConfig(base, override *Config, raw map[string]any) {
	if boolFieldSet(raw, "postures", "default") {
		base.Postures.Default = override.Postures.Default
	}
	if boolFieldSet(raw, "postures", "layers") {
		if base.Postures.Layers == nil {
			base.Postures.Layers = make(map[string]PostureConfig, len(override.Postures.Layers))
		}
		for name, layer := range override.Postures.Layers {
			base.Postures.Layers[name] = layer
		}
	}
}
