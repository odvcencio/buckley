package config

import (
	"reflect"
	"strings"
)

// mergeConfigs merges override into base with a single reflective walk of
// the Config struct, driven by its yaml struct tags. A field is copied
// from override to base only when the field's dotted yaml path is present
// in raw -- the override file's raw YAML, unmarshaled to map[string]any --
// including fields explicitly set to their zero value. This closes the
// "explicit zero doesn't override" gap flagged in PR #105's review: before
// this walker existed, most hand-written per-section mergers gated
// non-bool scalars on `override.Field != zeroValue` instead of raw
// presence, so `session_budget: 0` in a project config silently kept the
// base default instead of landing.
//
// Slices always replace whole (cloned, matching the pre-reflection
// mergers' `append([]T{}, override...)` idiom). Maps replace whole by
// default, preserving nil vs. empty-map distinctions the way the
// pre-reflection mergers did for pointer/map fields. A handful of
// sections need different semantics -- per-name slice merges, per-key map
// merges, a project-scope security boundary, provider "implicitly
// enabled" cascades -- and are registered in mergeStrategies instead of
// being special-cased inline in the walker.
func mergeConfigs(base, override *Config, raw map[string]any, projectScope bool) {
	if override == nil {
		return
	}
	ctx := mergeCtx{raw: raw, projectScope: projectScope}
	mergeStruct(ctx, reflect.ValueOf(base).Elem(), reflect.ValueOf(override).Elem(), nil)
}

// mergeCtx carries the per-call state the walker and its strategy hooks
// need: the raw YAML presence map and whether this layer is the
// project-scoped config file (./.buckley/config.yaml), which is denied
// write access to a few security-relevant fields (see mergeStrategies'
// approval.mode / sandbox.mode / sandbox.allow_unsafe entries).
type mergeCtx struct {
	raw          map[string]any
	projectScope bool
}

// mergeStrategy overrides the default field-merge behavior for one dotted
// yaml path. A registered strategy replaces the recursive default
// entirely for that field -- for a struct-kind field, that includes
// everything beneath it -- so each hook stays a small, self-contained
// function instead of growing special cases inside the generic walker.
type mergeStrategy func(ctx mergeCtx, base, override reflect.Value, path []string)

// mergeStrategies documents every point where a section's merge semantics
// diverge from the generic default (recurse into structs; boolFieldSet-gate
// and whole-replace/clone everything else). Each entry below mirrors an
// intentional divergence audited from the pre-reflection loader_merge_*.go
// files; see the referenced hook for the specific behavior it preserves.
var mergeStrategies = map[string]mergeStrategy{
	// Project config cannot loosen the approval/sandbox security boundary:
	// only the user config layer (~/.buckley/config.yaml) may set these.
	"approval.mode":        scopedScalarMerge,
	"sandbox.mode":         scopedScalarMerge,
	"sandbox.allow_unsafe": scopedScalarMerge,

	// Per-name / per-key merges: a project config can add or override a
	// single named entry without restating everything else at that level.
	"postures.layers":             mergeMapPerKeyBoolGated,
	"mcp.servers":                 mergeMCPServers,
	"personality.phase_overrides": mergeMapPerKeyIfNonEmpty,
	"personality.personas":        mergeMapPerKeyIfNonEmpty,
	"providers.model_routing":     mergeMapPerKeyIfNonEmpty,
	"models.fallback_chains":      mergeMapPerKeyTriState,

	// Provider sections where an API key, base URL, command, or model list
	// implicitly enables the provider when "enabled" itself isn't set.
	"providers.ollama":            mergeOllamaProvider,
	"providers.openai_compatible": mergeOpenAICompatibleProvider,
	"providers.litellm":           mergeOpenAICompatibleProvider,
	"providers.codex":             mergeCodexProvider,

	// Treated as one atomic unit when present, not merged field-by-field.
	"batch.job_template.resources": mergeWholeValueIfPresent,

	// The one pointer-target field whose nested slice the pre-reflection
	// merger explicitly normalized to non-nil; every other pointer/map/
	// slice-nested-in-a-clone field preserves nil-ness (see deepClone).
	"batch.job_template.workspace_volume_template": mergeWorkspaceVolumeTemplate,
}

// mergeStruct recursively merges each exported field of a struct-kind
// value, extending path with each field's yaml tag name.
func mergeStruct(ctx mergeCtx, base, override reflect.Value, path []string) {
	t := base.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" { // unexported
			continue
		}
		name := yamlFieldName(field)
		if name == "-" {
			continue
		}
		mergeField(ctx, base.Field(i), override.Field(i), sub(path, name))
	}
}

// mergeField applies a registered strategy when one exists for path;
// otherwise it recurses into struct-kind fields, or -- for everything
// else (scalars, slices, maps, pointers) -- copies a clone of override
// into base when path is present in raw.
func mergeField(ctx mergeCtx, base, override reflect.Value, path []string) {
	if strategy, ok := mergeStrategies[strings.Join(path, ".")]; ok {
		strategy(ctx, base, override, path)
		return
	}
	if base.Kind() == reflect.Struct {
		mergeStruct(ctx, base, override, path)
		return
	}
	if !boolFieldSet(ctx.raw, path...) {
		return
	}
	if base.Kind() == reflect.Slice {
		// A directly-tagged Config slice field: the pre-reflection mergers
		// always cloned these with `append([]T{}, override...)`, which
		// normalizes a nil override to a non-nil empty slice. Nested
		// slices (inside a map value, pointer target, or struct clone)
		// instead preserve nil-ness -- see deepClone.
		base.Set(cloneSliceNonNil(override))
		return
	}
	base.Set(deepClone(override))
}

// cloneSliceNonNil clones a slice to a non-nil result even when v itself
// is nil, matching `append([]T{}, v...)`. Each element is cloned with
// deepClone, so nested collections within slice elements (e.g. a
// []TaskPhaseConfig element's own slice fields) still preserve nil-ness.
func cloneSliceNonNil(v reflect.Value) reflect.Value {
	n := v.Len()
	out := reflect.MakeSlice(v.Type(), n, n)
	for i := 0; i < n; i++ {
		out.Index(i).Set(deepClone(v.Index(i)))
	}
	return out
}

// yamlFieldName returns the yaml tag name for f, falling back to the
// lowercased Go field name the way gopkg.in/yaml.v3 does when a field
// carries no tag.
func yamlFieldName(f reflect.StructField) string {
	tag := f.Tag.Get("yaml")
	if tag == "" {
		return strings.ToLower(f.Name)
	}
	name := strings.Split(tag, ",")[0]
	if name == "" {
		return strings.ToLower(f.Name)
	}
	return name
}

// sub returns a new path slice with name appended, never aliasing path's
// backing array (multiple sibling fields extend the same parent path).
func sub(path []string, name string) []string {
	out := make([]string, len(path)+1)
	copy(out, path)
	out[len(path)] = name
	return out
}

// boolFieldSet reports whether the dotted path is present as a key in the
// raw YAML document (regardless of the value at that key, including an
// explicit null/zero value). A missing intermediate key, or a non-map
// value where a nested map was expected, reports false.
func boolFieldSet(raw map[string]any, path ...string) bool {
	if len(path) == 0 || raw == nil {
		return false
	}
	current := any(raw)
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return false
		}
		val, ok := m[key]
		if !ok {
			return false
		}
		current = val
	}
	return true
}

// deepClone returns an independent copy of v: slices, maps, and pointers
// all preserve nil vs. non-nil (matching a plain Go value-copy, which is
// what the pre-reflection mergers did for everything nested inside a
// per-key map merge, a per-name slice merge, or a whole-struct/pointer
// replace), and structs clone field-by-field. Every other kind (strings,
// numbers, bools, interfaces) is returned as-is since Go's value
// semantics already copy it on assignment. Use cloneSliceNonNil instead
// when the field being merged is one the pre-reflection mergers built
// with `append([]T{}, override...)`, which normalizes nil to empty.
func deepClone(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		n := v.Len()
		out := reflect.MakeSlice(v.Type(), n, n)
		for i := 0; i < n; i++ {
			out.Index(i).Set(deepClone(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), deepClone(iter.Value()))
		}
		return out
	case reflect.Ptr:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(deepClone(v.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath != "" {
				continue
			}
			out.Field(i).Set(deepClone(v.Field(i)))
		}
		return out
	default:
		return v
	}
}

// scopedScalarMerge applies the default boolFieldSet-gated scalar merge,
// except when ctx.projectScope is true: the project config layer
// (./.buckley/config.yaml) cannot set this field, matching
// mergeApprovalConfig/mergeSandboxConfig's `if !projectScope` guard
// around approval.mode, sandbox.mode, and sandbox.allow_unsafe in the
// pre-reflection mergers. This keeps a project repo from silently
// loosening the security posture a user configured.
func scopedScalarMerge(ctx mergeCtx, base, override reflect.Value, path []string) {
	if ctx.projectScope {
		return
	}
	if !boolFieldSet(ctx.raw, path...) {
		return
	}
	base.Set(deepClone(override))
}

// mergeMapPerKeyBoolGated merges override's map entries into base by key,
// creating base's map if it was nil, without discarding keys already
// present in base. Gated on raw presence at path. Used for
// postures.layers, mirroring mergePosturesConfig: a project config can
// add or override a single named posture layer without restating the
// built-in ones.
func mergeMapPerKeyBoolGated(ctx mergeCtx, base, override reflect.Value, path []string) {
	if !boolFieldSet(ctx.raw, path...) {
		return
	}
	mergeMapPerKey(base, override)
}

// mergeMapPerKeyIfNonEmpty merges override's map entries into base by
// key, the same way mergeMapPerKeyBoolGated does, but gated on the
// override map being non-empty instead of raw presence. Used for
// personality.phase_overrides, personality.personas, and
// providers.model_routing, mirroring the pre-reflection mergers'
// `len(override.X) > 0` guards for those three fields.
func mergeMapPerKeyIfNonEmpty(ctx mergeCtx, base, override reflect.Value, path []string) {
	if override.Len() == 0 {
		return
	}
	mergeMapPerKey(base, override)
}

// mergeMapPerKey is the shared per-key merge body for
// mergeMapPerKeyBoolGated and mergeMapPerKeyIfNonEmpty.
func mergeMapPerKey(base, override reflect.Value) {
	if base.IsNil() {
		base.Set(reflect.MakeMapWithSize(base.Type(), override.Len()))
	}
	iter := override.MapRange()
	for iter.Next() {
		base.SetMapIndex(iter.Key(), deepClone(iter.Value()))
	}
}

// mergeMapPerKeyTriState merges a map[string][]string field with the
// three-way nil/empty/populated handling mergeModelConfig used for
// models.fallback_chains (and, inline, mergeProviderConfig used for
// providers.litellm.fallbacks): an explicit null clears base to nil, an
// explicit empty map clears base to an empty map, and a populated map
// merges per-key without discarding base's other keys. Each value slice
// clones non-nil (`append([]string{}, v...)` in the pre-reflection
// mergers), unlike mergeMapPerKey's nil-preserving default.
func mergeMapPerKeyTriState(ctx mergeCtx, base, override reflect.Value, path []string) {
	if !boolFieldSet(ctx.raw, path...) {
		return
	}
	switch {
	case override.IsNil():
		base.Set(reflect.Zero(base.Type()))
	case override.Len() == 0:
		base.Set(reflect.MakeMap(base.Type()))
	default:
		if base.IsNil() {
			base.Set(reflect.MakeMapWithSize(base.Type(), override.Len()))
		}
		iter := override.MapRange()
		for iter.Next() {
			base.SetMapIndex(iter.Key(), cloneSliceNonNil(iter.Value()))
		}
	}
}

// mergeMCPServers merges mcp.servers by server name (like
// mergeMapPerKeyBoolGated's map handling, but for a []MCPServerConfig
// keyed by each element's Name field): a project config can add or
// override a single named server without restating every server defined
// at the user scope. Order is base's servers first, then any new servers
// override introduces, matching mergeMCPConfig.
func mergeMCPServers(ctx mergeCtx, base, override reflect.Value, path []string) {
	if !boolFieldSet(ctx.raw, path...) {
		return
	}
	order := make([]string, 0, base.Len()+override.Len())
	merged := make(map[string]reflect.Value, base.Len()+override.Len())
	add := func(v reflect.Value) {
		name := v.FieldByName("Name").String()
		if _, exists := merged[name]; !exists {
			order = append(order, name)
		}
		merged[name] = v
	}
	for i := 0; i < base.Len(); i++ {
		add(base.Index(i))
	}
	for i := 0; i < override.Len(); i++ {
		add(override.Index(i))
	}
	out := reflect.MakeSlice(base.Type(), 0, len(order))
	for _, name := range order {
		out = reflect.Append(out, merged[name])
	}
	base.Set(out)
}

// mergeOllamaProvider merges providers.ollama, mirroring
// mergeProviderConfig's Ollama block: api_key/base_url/enabled merge
// individually when present in raw, and -- when "enabled" itself isn't
// set -- an explicit api_key or base_url implicitly enables the provider.
func mergeOllamaProvider(ctx mergeCtx, base, override reflect.Value, path []string) {
	b := base.Addr().Interface().(*ProviderSettings)
	o := override.Interface().(ProviderSettings)

	apiKeySet := boolFieldSet(ctx.raw, sub(path, "api_key")...)
	baseURLSet := boolFieldSet(ctx.raw, sub(path, "base_url")...)
	enabledSet := boolFieldSet(ctx.raw, sub(path, "enabled")...)

	if apiKeySet {
		b.APIKey = o.APIKey
	}
	if baseURLSet {
		b.BaseURL = o.BaseURL
	}
	if enabledSet {
		b.Enabled = o.Enabled
	} else if apiKeySet || baseURLSet {
		b.Enabled = true
	}
}

// mergeOpenAICompatibleProvider merges an OpenAI-compatible provider block:
// base_url/api_key/models/supported_parameters/context_lengths/fallbacks/router merge
// individually when present in raw (fallbacks with the same
// tri-state nil/empty/populated handling as mergeMapPerKeyTriState), and
// -- when "enabled" itself isn't set -- any of those other fields being
// present implicitly enables the provider.
func mergeOpenAICompatibleProvider(ctx mergeCtx, base, override reflect.Value, path []string) {
	b := base.Addr().Interface().(*OpenAICompatibleConfig)
	o := override.Interface().(OpenAICompatibleConfig)

	baseURLSet := boolFieldSet(ctx.raw, sub(path, "base_url")...)
	apiKeySet := boolFieldSet(ctx.raw, sub(path, "api_key")...)
	modelsSet := boolFieldSet(ctx.raw, sub(path, "models")...)
	supportedParametersSet := boolFieldSet(ctx.raw, sub(path, "supported_parameters")...)
	contextLengthsSet := boolFieldSet(ctx.raw, sub(path, "context_lengths")...)
	fallbacksSet := boolFieldSet(ctx.raw, sub(path, "fallbacks")...)
	routerSet := boolFieldSet(ctx.raw, sub(path, "router")...)
	enabledSet := boolFieldSet(ctx.raw, sub(path, "enabled")...)

	if baseURLSet {
		b.BaseURL = o.BaseURL
	}
	if apiKeySet {
		b.APIKey = o.APIKey
	}
	if modelsSet {
		b.Models = append([]string{}, o.Models...)
	}
	if supportedParametersSet {
		b.SupportedParameters = cloneStringSliceMap(o.SupportedParameters)
	}
	if contextLengthsSet {
		b.ContextLengths = cloneStringIntMap(o.ContextLengths)
	}
	if fallbacksSet {
		switch {
		case o.Fallbacks == nil:
			b.Fallbacks = nil
		case len(o.Fallbacks) == 0:
			b.Fallbacks = map[string][]string{}
		default:
			if b.Fallbacks == nil {
				b.Fallbacks = make(map[string][]string, len(o.Fallbacks))
			}
			for k, v := range o.Fallbacks {
				b.Fallbacks[k] = append([]string{}, v...)
			}
		}
	}
	if routerSet {
		b.Router = o.Router
	}
	if enabledSet {
		b.Enabled = o.Enabled
	} else if apiKeySet || baseURLSet || modelsSet || supportedParametersSet || contextLengthsSet || fallbacksSet || routerSet {
		b.Enabled = true
	}
}

func cloneStringIntMap(source map[string]int) map[string]int {
	if source == nil {
		return nil
	}
	cloned := make(map[string]int, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneStringSliceMap(source map[string][]string) map[string][]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string][]string, len(source))
	for key, values := range source {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

// mergeCodexProvider merges providers.codex, mirroring
// mergeProviderConfig's Codex block: command/models/enabled merge
// individually when present in raw, and -- when "enabled" itself isn't
// set -- an explicit command or model list implicitly enables the
// provider.
func mergeCodexProvider(ctx mergeCtx, base, override reflect.Value, path []string) {
	b := base.Addr().Interface().(*CodexConfig)
	o := override.Interface().(CodexConfig)

	commandSet := boolFieldSet(ctx.raw, sub(path, "command")...)
	modelsSet := boolFieldSet(ctx.raw, sub(path, "models")...)
	enabledSet := boolFieldSet(ctx.raw, sub(path, "enabled")...)

	if commandSet {
		b.Command = o.Command
	}
	if modelsSet {
		b.Models = append([]string{}, o.Models...)
	}
	if enabledSet {
		b.Enabled = o.Enabled
	} else if commandSet || modelsSet {
		b.Enabled = true
	}
}

// mergeWholeValueIfPresent replaces base with a clone of override as one
// atomic unit when path is present in raw, instead of recursing
// field-by-field. Used for batch.job_template.resources, mirroring
// mergeBatchConfig treating `resources:` as a single opaque value.
func mergeWholeValueIfPresent(ctx mergeCtx, base, override reflect.Value, path []string) {
	if !boolFieldSet(ctx.raw, path...) {
		return
	}
	base.Set(deepClone(override))
}

// mergeWorkspaceVolumeTemplate merges
// batch.job_template.workspace_volume_template as one atomic unit (like
// mergeWholeValueIfPresent), but clones AccessModes non-nil, matching
// mergeBatchConfig's explicit `append([]string{}, override...AccessModes...)`
// for this one field.
func mergeWorkspaceVolumeTemplate(ctx mergeCtx, base, override reflect.Value, path []string) {
	if !boolFieldSet(ctx.raw, path...) {
		return
	}
	if override.IsNil() {
		base.Set(reflect.Zero(base.Type()))
		return
	}
	o := override.Interface().(*BatchVolumeTemplateConfig)
	clone := *o
	clone.AccessModes = append([]string{}, o.AccessModes...)
	base.Set(reflect.ValueOf(&clone))
}
