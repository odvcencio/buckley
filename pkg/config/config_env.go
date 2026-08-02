package config

import (
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/buckley/v2/pkg/giturl"
)

// applyEnvOverrides applies environment variable overrides with a single
// reflective walk of the Config struct, driven by its `env` struct tags:
// a field carrying `env:"BUCKLEY_XXX"` is set from os.Getenv("BUCKLEY_XXX")
// when that var is present and parses cleanly for the field's type.
//
// About a dozen sections need more than a 1:1 var-to-field mapping --
// fallback var names (BUCKLEY_MODEL_REASONING falls back to
// BUCKLEY_REASONING), one-directional overrides (BUCKLEY_DISABLE_TOON only
// ever turns encoding.use_toon off), implicit-enable cascades (an OpenAI
// API key implies providers.openai.enabled), a *bool field, CSV-encoded
// lists, and a positive-only guard on a few numeric fields. Those are
// registered in envStrategies instead of being expressed as struct tags,
// mirroring pkg/config/merge.go's mergeStrategies.
func applyEnvOverrides(cfg *Config, configEnv map[string]string) {
	ctx := envCtx{cfg: cfg, configEnv: configEnv}
	walkEnvFields(ctx, reflect.ValueOf(cfg).Elem(), nil)

	// Not tied to a single env var: if both basic-auth fields ended up
	// set (from any source -- env vars above, or a config.yaml layer
	// merged in before applyEnvOverrides runs) and basic auth isn't
	// already enabled, turn it on. See IPCConfig's doc comment.
	if cfg.IPC.BasicAuthUsername != "" && cfg.IPC.BasicAuthPassword != "" && !cfg.IPC.BasicAuthEnabled {
		cfg.IPC.BasicAuthEnabled = true
	}
}

// envCtx carries state envStrategy hooks need. cfg gives hooks whose
// cascade crosses top-level sections (only envCodexProvider, into
// Models.*) access beyond their own subtree. configEnv is the
// ~/.buckley/config.env fallback map (see loadConfigEnvVars in
// config_load.go), consulted only for OPENROUTER_API_KEY.
type envCtx struct {
	cfg       *Config
	configEnv map[string]string
}

// envStrategy replaces the default `env`-tag dispatch for one dotted yaml
// path. For a struct-kind field this takes over its entire subtree, the
// same way a merge.go mergeStrategy does.
type envStrategy func(ctx envCtx, field reflect.Value, path []string)

// envStrategies documents every env var whose behavior is more than a
// plain "var present -> set field": see each hook's doc comment for the
// var names it reads and the extra rule it applies.
var envStrategies = map[string]envStrategy{
	"models.reasoning":                 envReasoning,
	"postures.default":                 envPosture,
	"sandbox.allow_unsafe":             envSandboxAllowUnsafe,
	"sandbox.docker.network_enabled":   envDockerNetwork,
	"encoding.use_toon":                envUseToon,
	"diagnostics.network_logs_enabled": envNetworkLogs,

	"providers.openrouter": envOpenRouterProvider,
	"providers.openai":     envOpenAIProvider,
	"providers.anthropic":  envAnthropicProvider,
	"providers.google":     envGoogleProvider,
	"providers.ollama":     envOllamaProvider,
	"providers.litellm":    envLiteLLMProvider,
	"providers.codex":      envCodexProvider,

	"experiment.max_concurrent":     envExperimentMaxConcurrent,
	"experiment.default_timeout":    envExperimentDefaultTimeout,
	"experiment.max_cost_per_run":   envExperimentMaxCostPerRun,
	"experiment.max_tokens_per_run": envExperimentMaxTokensPerRun,

	"notify.telegram": envTelegram,
	"notify.slack":    envSlack,

	"git_clone": envGitClone,
}

// walkEnvFields recursively applies env overrides to each exported field
// of a struct-kind value, extending path with each field's yaml tag name
// (reusing merge.go's path convention so envStrategies and mergeStrategies
// read the same way).
func walkEnvFields(ctx envCtx, v reflect.Value, path []string) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" { // unexported
			continue
		}
		name := yamlFieldName(field)
		if name == "-" {
			continue
		}
		fieldPath := sub(path, name)
		fv := v.Field(i)

		if strategy, ok := envStrategies[strings.Join(fieldPath, ".")]; ok {
			strategy(ctx, fv, fieldPath)
			continue
		}
		if fv.Kind() == reflect.Struct && fv.Type() != durationType {
			walkEnvFields(ctx, fv, fieldPath)
			continue
		}
		applyTaggedEnvValue(field, fv)
	}
}

var durationType = reflect.TypeOf(time.Duration(0))

// applyTaggedEnvValue applies the plain `env:"VAR"` rule for one leaf
// field: read os.Getenv(VAR) and, if non-empty and it parses cleanly for
// the field's type, set it. String fields are read as-is (the
// pre-reflection function didn't trim most string vars); numeric and
// duration fields are trimmed before parsing; bool fields accept the same
// 1/true/yes/on, 0/false/no/off tokens envBool always did, applying
// either value. Only the Kinds today's tagged fields actually use are
// handled -- add a case here if a future field needs one.
func applyTaggedEnvValue(field reflect.StructField, v reflect.Value) {
	tag := field.Tag.Get("env")
	if tag == "" {
		return
	}
	switch {
	case v.Kind() == reflect.String:
		if raw := os.Getenv(tag); raw != "" {
			v.SetString(raw)
		}
	case v.Kind() == reflect.Bool:
		if val, ok := envBool(tag); ok {
			v.SetBool(val)
		}
	case v.Type() == durationType:
		if raw := strings.TrimSpace(os.Getenv(tag)); raw != "" {
			if d, err := time.ParseDuration(raw); err == nil {
				v.SetInt(int64(d))
			}
		}
	case v.Kind() == reflect.Int64:
		if raw := strings.TrimSpace(os.Getenv(tag)); raw != "" {
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
				v.SetInt(n)
			}
		}
	}
}

// envBool parses an env var the way the pre-reflection envBool did:
// 1/true/yes/on (case-insensitive) -> (true, true); 0/false/no/off ->
// (false, true); unset or unrecognized -> (false, false).
func envBool(key string) (bool, bool) {
	val := os.Getenv(key)
	if val == "" {
		return false, false
	}
	switch strings.ToLower(val) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

// envReasoning applies BUCKLEY_MODEL_REASONING, falling back to the
// legacy BUCKLEY_REASONING when the primary var is unset.
func envReasoning(ctx envCtx, field reflect.Value, path []string) {
	if v := os.Getenv("BUCKLEY_MODEL_REASONING"); v != "" {
		field.SetString(v)
	} else if v := os.Getenv("BUCKLEY_REASONING"); v != "" {
		field.SetString(v)
	}
}

// envPosture applies BUCKLEY_POSTURE, trimmed.
func envPosture(ctx envCtx, field reflect.Value, path []string) {
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_POSTURE")); v != "" {
		field.SetString(v)
	}
}

// envSandboxAllowUnsafe applies BUCKLEY_UNSAFE, but only to turn the
// field on: an absent or false/no/off value never clears an operator's
// explicit sandbox.allow_unsafe: true from a config.yaml file.
func envSandboxAllowUnsafe(ctx envCtx, field reflect.Value, path []string) {
	if val, ok := envBool("BUCKLEY_UNSAFE"); ok && val {
		field.SetBool(true)
	}
}

// envDockerNetwork applies BUCKLEY_DOCKER_SANDBOX_NETWORK to the
// *bool NetworkEnabled field, which distinguishes "not set" (nil) from
// "explicitly set to false".
func envDockerNetwork(ctx envCtx, field reflect.Value, path []string) {
	if val, ok := envBool("BUCKLEY_DOCKER_SANDBOX_NETWORK"); ok {
		field.Set(reflect.ValueOf(&val))
	}
}

// envUseToon applies BUCKLEY_USE_TOON symmetrically, then
// BUCKLEY_DISABLE_TOON one-directionally: a true value turns encoding
// off, but a false value is a no-op (never re-enables it).
func envUseToon(ctx envCtx, field reflect.Value, path []string) {
	if val, ok := envBool("BUCKLEY_USE_TOON"); ok {
		field.SetBool(val)
	} else if val, ok := envBool("BUCKLEY_DISABLE_TOON"); ok && val {
		field.SetBool(false)
	}
}

// envNetworkLogs applies BUCKLEY_NETWORK_LOGS_ENABLED symmetrically, then
// BUCKLEY_DISABLE_NETWORK_LOGS one-directionally, mirroring envUseToon.
func envNetworkLogs(ctx envCtx, field reflect.Value, path []string) {
	if val, ok := envBool("BUCKLEY_NETWORK_LOGS_ENABLED"); ok {
		field.SetBool(val)
	} else if val, ok := envBool("BUCKLEY_DISABLE_NETWORK_LOGS"); ok && val {
		field.SetBool(false)
	}
}

// envOpenRouterProvider applies OPENROUTER_API_KEY, falling back to the
// ~/.buckley/config.env file's OPENROUTER_API_KEY when the environment
// doesn't set it and the field is still empty (config.yaml may have set
// it already, in which case neither source overrides it). OpenRouter's
// Enabled flag has no env var.
func envOpenRouterProvider(ctx envCtx, field reflect.Value, path []string) {
	p := field.Addr().Interface().(*ProviderSettings)
	if v := os.Getenv("OPENROUTER_API_KEY"); v != "" {
		p.APIKey = v
	} else if p.APIKey == "" {
		if v := ctx.configEnv["OPENROUTER_API_KEY"]; v != "" {
			p.APIKey = v
		}
	}
}

// envOpenAIProvider applies OPENAI_API_KEY, which also unconditionally
// enables the provider when set.
func envOpenAIProvider(ctx envCtx, field reflect.Value, path []string) {
	p := field.Addr().Interface().(*ProviderSettings)
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		p.APIKey = v
		p.Enabled = true
	}
}

// envAnthropicProvider applies ANTHROPIC_API_KEY, mirroring
// envOpenAIProvider.
func envAnthropicProvider(ctx envCtx, field reflect.Value, path []string) {
	p := field.Addr().Interface().(*ProviderSettings)
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		p.APIKey = v
		p.Enabled = true
	}
}

// envGoogleProvider applies GOOGLE_API_KEY, mirroring envOpenAIProvider.
func envGoogleProvider(ctx envCtx, field reflect.Value, path []string) {
	p := field.Addr().Interface().(*ProviderSettings)
	if v := os.Getenv("GOOGLE_API_KEY"); v != "" {
		p.APIKey = v
		p.Enabled = true
	}
}

// envOllamaProvider applies BUCKLEY_OLLAMA_ENABLED and
// BUCKLEY_OLLAMA_BASE_URL; the base URL also unconditionally enables the
// provider when set.
func envOllamaProvider(ctx envCtx, field reflect.Value, path []string) {
	p := field.Addr().Interface().(*ProviderSettings)
	if v, ok := envBool("BUCKLEY_OLLAMA_ENABLED"); ok {
		p.Enabled = v
	}
	if v := os.Getenv("BUCKLEY_OLLAMA_BASE_URL"); v != "" {
		p.BaseURL = v
		p.Enabled = true
	}
}

// envLiteLLMProvider applies BUCKLEY_LITELLM_ENABLED,
// BUCKLEY_LITELLM_BASE_URL (falling back to the unprefixed
// LITELLM_BASE_URL, which does not enable the provider), and
// BUCKLEY_LITELLM_API_KEY (falling back to the unprefixed
// LITELLM_API_KEY, which -- unlike the base URL fallback -- does enable
// the provider, matching the pre-reflection asymmetry).
func envLiteLLMProvider(ctx envCtx, field reflect.Value, path []string) {
	p := field.Addr().Interface().(*LiteLLMConfig)
	if v, ok := envBool("BUCKLEY_LITELLM_ENABLED"); ok {
		p.Enabled = v
	}
	if v := os.Getenv("BUCKLEY_LITELLM_BASE_URL"); v != "" {
		p.BaseURL = v
		p.Enabled = true
	} else if v := os.Getenv("LITELLM_BASE_URL"); v != "" && p.BaseURL == "" {
		p.BaseURL = v
	}
	if v := os.Getenv("BUCKLEY_LITELLM_API_KEY"); v != "" {
		p.APIKey = v
		p.Enabled = true
	} else if v := os.Getenv("LITELLM_API_KEY"); v != "" && p.APIKey == "" {
		p.APIKey = v
		p.Enabled = true
	}
}

// envCodexProvider applies BUCKLEY_CODEX_ENABLED, BUCKLEY_CODEX_COMMAND
// (which also enables the provider), and BUCKLEY_CODEX_MODEL. The model
// var, when set, always wins the Enabled flag (applied last, after
// BUCKLEY_CODEX_ENABLED and BUCKLEY_CODEX_COMMAND, exactly matching the
// pre-reflection function's statement order) and replaces
// Models.Execution/Planning/Review when each is unset or still an
// OpenRouter default model.
func envCodexProvider(ctx envCtx, field reflect.Value, path []string) {
	cfg := ctx.cfg
	p := field.Addr().Interface().(*CodexConfig)

	codexModel := normalizeCodexModel(os.Getenv("BUCKLEY_CODEX_MODEL"))
	if v, ok := envBool("BUCKLEY_CODEX_ENABLED"); ok {
		p.Enabled = v
	}
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_CODEX_COMMAND")); v != "" {
		p.Command = v
		p.Enabled = true
	}
	if codexModel != "" {
		p.Models = []string{codexModel}
		p.Enabled = true
		if cfg.Models.Execution == "" || isOpenRouterDefaultModel(cfg.Models.Execution) {
			cfg.Models.Execution = codexModel
		}
		if cfg.Models.Planning == "" || isOpenRouterDefaultModel(cfg.Models.Planning) {
			cfg.Models.Planning = codexModel
		}
		if cfg.Models.Review == "" || isOpenRouterDefaultModel(cfg.Models.Review) {
			cfg.Models.Review = codexModel
		}
	}
}

func normalizeCodexModel(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return ""
	}
	if strings.HasPrefix(modelID, "codex/") {
		return modelID
	}
	return "codex/" + modelID
}

// envExperimentMaxConcurrent applies BUCKLEY_EXPERIMENT_MAX_CONCURRENT
// only when it parses as a positive int.
func envExperimentMaxConcurrent(ctx envCtx, field reflect.Value, path []string) {
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_EXPERIMENT_MAX_CONCURRENT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			field.SetInt(int64(n))
		}
	}
}

// envExperimentDefaultTimeout applies BUCKLEY_EXPERIMENT_DEFAULT_TIMEOUT
// only when it parses as a positive duration.
func envExperimentDefaultTimeout(ctx envCtx, field reflect.Value, path []string) {
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_EXPERIMENT_DEFAULT_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			field.SetInt(int64(d))
		}
	}
}

// envExperimentMaxCostPerRun applies BUCKLEY_EXPERIMENT_MAX_COST_PER_RUN
// only when it parses as a positive float.
func envExperimentMaxCostPerRun(ctx envCtx, field reflect.Value, path []string) {
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_EXPERIMENT_MAX_COST_PER_RUN")); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			field.SetFloat(n)
		}
	}
}

// envExperimentMaxTokensPerRun applies
// BUCKLEY_EXPERIMENT_MAX_TOKENS_PER_RUN only when it parses as a positive
// int.
func envExperimentMaxTokensPerRun(ctx envCtx, field reflect.Value, path []string) {
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_EXPERIMENT_MAX_TOKENS_PER_RUN")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			field.SetInt(int64(n))
		}
	}
}

// envTelegram applies BUCKLEY_TELEGRAM_ENABLED, BUCKLEY_TELEGRAM_BOT_TOKEN
// (which also enables Telegram when it wasn't already), and
// BUCKLEY_TELEGRAM_CHAT_ID.
func envTelegram(ctx envCtx, field reflect.Value, path []string) {
	p := field.Addr().Interface().(*TelegramConfig)
	if v, ok := envBool("BUCKLEY_TELEGRAM_ENABLED"); ok {
		p.Enabled = v
	}
	if v := os.Getenv("BUCKLEY_TELEGRAM_BOT_TOKEN"); v != "" {
		p.BotToken = v
		if !p.Enabled {
			p.Enabled = true
		}
	}
	if v := os.Getenv("BUCKLEY_TELEGRAM_CHAT_ID"); v != "" {
		p.ChatID = v
	}
}

// envSlack applies BUCKLEY_SLACK_ENABLED, BUCKLEY_SLACK_WEBHOOK_URL
// (which also enables Slack when it wasn't already), and
// BUCKLEY_SLACK_CHANNEL.
func envSlack(ctx envCtx, field reflect.Value, path []string) {
	p := field.Addr().Interface().(*SlackConfig)
	if v, ok := envBool("BUCKLEY_SLACK_ENABLED"); ok {
		p.Enabled = v
	}
	if v := os.Getenv("BUCKLEY_SLACK_WEBHOOK_URL"); v != "" {
		p.WebhookURL = v
		if !p.Enabled {
			p.Enabled = true
		}
	}
	if v := os.Getenv("BUCKLEY_SLACK_CHANNEL"); v != "" {
		p.Channel = v
	}
}

// envGitClone applies all seven git_clone env vars. GitClone is
// pkg/giturl.ClonePolicy, outside pkg/config, so it can't carry env
// struct tags at all (see its field in the Config struct); every var is
// handled here instead of split between tags and hooks.
func envGitClone(ctx envCtx, field reflect.Value, path []string) {
	p := field.Addr().Interface().(*giturl.ClonePolicy)
	if v := os.Getenv("BUCKLEY_GIT_ALLOWED_SCHEMES"); v != "" {
		p.AllowedSchemes = splitCommaList(v)
	}
	if v := os.Getenv("BUCKLEY_GIT_ALLOWED_HOSTS"); v != "" {
		p.AllowedHosts = splitCommaList(v)
	}
	if v := os.Getenv("BUCKLEY_GIT_DENIED_HOSTS"); v != "" {
		p.DeniedHosts = splitCommaList(v)
	}
	if v, ok := envBool("BUCKLEY_GIT_DENY_PRIVATE_NETWORKS"); ok {
		p.DenyPrivateNetworks = v
	}
	if v, ok := envBool("BUCKLEY_GIT_RESOLVE_DNS"); ok {
		p.ResolveDNS = v
	}
	if v, ok := envBool("BUCKLEY_GIT_DENY_SCP_SYNTAX"); ok {
		p.DenySCPSyntax = v
	}
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_GIT_DNS_TIMEOUT_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.DNSResolveTimeoutSec = n
		}
	}
}

func splitCommaList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}
