package config

import "github.com/odvcencio/buckley/pkg/personality"

func mergeACPConfig(base, override *Config, raw map[string]any) {
	if override.ACP.EventStore != "" {
		base.ACP.EventStore = override.ACP.EventStore
	}
	if override.ACP.NATS.URL != "" {
		base.ACP.NATS.URL = override.ACP.NATS.URL
	}
	if override.ACP.NATS.Username != "" {
		base.ACP.NATS.Username = override.ACP.NATS.Username
	}
	if override.ACP.NATS.Password != "" {
		base.ACP.NATS.Password = override.ACP.NATS.Password
	}
	if override.ACP.NATS.Token != "" {
		base.ACP.NATS.Token = override.ACP.NATS.Token
	}
	if boolFieldSet(raw, "acp", "nats", "tls") {
		base.ACP.NATS.TLS = override.ACP.NATS.TLS
	}
	if override.ACP.NATS.StreamPrefix != "" {
		base.ACP.NATS.StreamPrefix = override.ACP.NATS.StreamPrefix
	}
	if override.ACP.NATS.SnapshotBucket != "" {
		base.ACP.NATS.SnapshotBucket = override.ACP.NATS.SnapshotBucket
	}
	if override.ACP.NATS.ConnectTimeout != 0 {
		base.ACP.NATS.ConnectTimeout = override.ACP.NATS.ConnectTimeout
	}
	if override.ACP.NATS.RequestTimeout != 0 {
		base.ACP.NATS.RequestTimeout = override.ACP.NATS.RequestTimeout
	}
	if override.ACP.Listen != "" {
		base.ACP.Listen = override.ACP.Listen
	}
	if boolFieldSet(raw, "acp", "allow_insecure_local") {
		base.ACP.AllowInsecureLocal = override.ACP.AllowInsecureLocal
	}
	if override.ACP.TLSCertFile != "" {
		base.ACP.TLSCertFile = override.ACP.TLSCertFile
	}
	if override.ACP.TLSKeyFile != "" {
		base.ACP.TLSKeyFile = override.ACP.TLSKeyFile
	}
	if override.ACP.TLSClientCAFile != "" {
		base.ACP.TLSClientCAFile = override.ACP.TLSClientCAFile
	}
}

func mergeModelConfig(base, override *Config, raw map[string]any) {
	if override.Models.Planning != "" {
		base.Models.Planning = override.Models.Planning
	}
	if override.Models.Execution != "" {
		base.Models.Execution = override.Models.Execution
	}
	if override.Models.Review != "" {
		base.Models.Review = override.Models.Review
	}
	if boolFieldSet(raw, "models", "curated") {
		base.Models.Curated = append([]string{}, override.Models.Curated...)
	}
	if boolFieldSet(raw, "models", "vision_fallback") {
		base.Models.VisionFallback = append([]string{}, override.Models.VisionFallback...)
	}
	if boolFieldSet(raw, "models", "default_provider") {
		base.Models.DefaultProvider = override.Models.DefaultProvider
	}
	if boolFieldSet(raw, "models", "reasoning") {
		base.Models.Reasoning = override.Models.Reasoning
	}
	if boolFieldSet(raw, "models", "utility", "commit") {
		base.Models.Utility.Commit = override.Models.Utility.Commit
	}
	if boolFieldSet(raw, "models", "utility", "pr") {
		base.Models.Utility.PR = override.Models.Utility.PR
	}
	if boolFieldSet(raw, "models", "utility", "compaction") {
		base.Models.Utility.Compaction = override.Models.Utility.Compaction
	}
	if boolFieldSet(raw, "models", "utility", "todo_plan") {
		base.Models.Utility.TodoPlan = override.Models.Utility.TodoPlan
	}
	if boolFieldSet(raw, "models", "fallback_chains") {
		if override.Models.FallbackChains == nil {
			base.Models.FallbackChains = nil
		} else if len(override.Models.FallbackChains) == 0 {
			base.Models.FallbackChains = map[string][]string{}
		} else {
			if base.Models.FallbackChains == nil {
				base.Models.FallbackChains = make(map[string][]string)
			}
			for k, v := range override.Models.FallbackChains {
				base.Models.FallbackChains[k] = append([]string{}, v...)
			}
		}
	}
}

func mergeProviderConfig(base, override *Config, raw map[string]any) {
	if override.Providers.OpenRouter.APIKey != "" {
		base.Providers.OpenRouter.APIKey = override.Providers.OpenRouter.APIKey
	}
	if override.Providers.OpenRouter.BaseURL != "" {
		base.Providers.OpenRouter.BaseURL = override.Providers.OpenRouter.BaseURL
	}
	if boolFieldSet(raw, "providers", "openrouter", "enabled") {
		base.Providers.OpenRouter.Enabled = override.Providers.OpenRouter.Enabled
	}

	if override.Providers.OpenAI.APIKey != "" {
		base.Providers.OpenAI.APIKey = override.Providers.OpenAI.APIKey
	}
	if override.Providers.OpenAI.BaseURL != "" {
		base.Providers.OpenAI.BaseURL = override.Providers.OpenAI.BaseURL
	}
	if boolFieldSet(raw, "providers", "openai", "enabled") {
		base.Providers.OpenAI.Enabled = override.Providers.OpenAI.Enabled
	}

	if override.Providers.Anthropic.APIKey != "" {
		base.Providers.Anthropic.APIKey = override.Providers.Anthropic.APIKey
	}
	if override.Providers.Anthropic.BaseURL != "" {
		base.Providers.Anthropic.BaseURL = override.Providers.Anthropic.BaseURL
	}
	if boolFieldSet(raw, "providers", "anthropic", "enabled") {
		base.Providers.Anthropic.Enabled = override.Providers.Anthropic.Enabled
	}

	if override.Providers.Google.APIKey != "" {
		base.Providers.Google.APIKey = override.Providers.Google.APIKey
	}
	if override.Providers.Google.BaseURL != "" {
		base.Providers.Google.BaseURL = override.Providers.Google.BaseURL
	}
	if boolFieldSet(raw, "providers", "google", "enabled") {
		base.Providers.Google.Enabled = override.Providers.Google.Enabled
	}

	ollamaEnabledSet := boolFieldSet(raw, "providers", "ollama", "enabled")
	if override.Providers.Ollama.APIKey != "" {
		base.Providers.Ollama.APIKey = override.Providers.Ollama.APIKey
	}
	if override.Providers.Ollama.BaseURL != "" {
		base.Providers.Ollama.BaseURL = override.Providers.Ollama.BaseURL
	}
	if ollamaEnabledSet {
		base.Providers.Ollama.Enabled = override.Providers.Ollama.Enabled
	} else if override.Providers.Ollama.APIKey != "" || override.Providers.Ollama.BaseURL != "" {
		base.Providers.Ollama.Enabled = true
	}

	litellmEnabledSet := boolFieldSet(raw, "providers", "litellm", "enabled")
	if override.Providers.LiteLLM.BaseURL != "" {
		base.Providers.LiteLLM.BaseURL = override.Providers.LiteLLM.BaseURL
	}
	if override.Providers.LiteLLM.APIKey != "" {
		base.Providers.LiteLLM.APIKey = override.Providers.LiteLLM.APIKey
	}
	if boolFieldSet(raw, "providers", "litellm", "models") {
		base.Providers.LiteLLM.Models = append([]string{}, override.Providers.LiteLLM.Models...)
	}
	if boolFieldSet(raw, "providers", "litellm", "fallbacks") {
		if override.Providers.LiteLLM.Fallbacks == nil {
			base.Providers.LiteLLM.Fallbacks = nil
		} else if len(override.Providers.LiteLLM.Fallbacks) == 0 {
			base.Providers.LiteLLM.Fallbacks = map[string][]string{}
		} else {
			if base.Providers.LiteLLM.Fallbacks == nil {
				base.Providers.LiteLLM.Fallbacks = make(map[string][]string)
			}
			for k, v := range override.Providers.LiteLLM.Fallbacks {
				base.Providers.LiteLLM.Fallbacks[k] = append([]string{}, v...)
			}
		}
	}
	if boolFieldSet(raw, "providers", "litellm", "router") {
		base.Providers.LiteLLM.Router = override.Providers.LiteLLM.Router
	}
	if litellmEnabledSet {
		base.Providers.LiteLLM.Enabled = override.Providers.LiteLLM.Enabled
	} else if override.Providers.LiteLLM.APIKey != "" ||
		override.Providers.LiteLLM.BaseURL != "" ||
		boolFieldSet(raw, "providers", "litellm", "models") ||
		boolFieldSet(raw, "providers", "litellm", "fallbacks") ||
		boolFieldSet(raw, "providers", "litellm", "router") {
		base.Providers.LiteLLM.Enabled = true
	}

	if len(override.Providers.ModelRouting) > 0 {
		for k, v := range override.Providers.ModelRouting {
			base.Providers.ModelRouting[k] = v
		}
	}
}

func mergeExperimentAndEncodingConfig(base, override *Config, raw map[string]any) {
	if boolFieldSet(raw, "experiment", "enabled") {
		base.Experiment.Enabled = override.Experiment.Enabled
	}
	if boolFieldSet(raw, "experiment", "max_concurrent") {
		base.Experiment.MaxConcurrent = override.Experiment.MaxConcurrent
	}
	if boolFieldSet(raw, "experiment", "default_timeout") {
		base.Experiment.DefaultTimeout = override.Experiment.DefaultTimeout
	}
	if boolFieldSet(raw, "experiment", "worktree_root") {
		base.Experiment.WorktreeRoot = override.Experiment.WorktreeRoot
	}
	if boolFieldSet(raw, "experiment", "cleanup_on_done") {
		base.Experiment.CleanupOnDone = override.Experiment.CleanupOnDone
	}
	if boolFieldSet(raw, "experiment", "max_cost_per_run") {
		base.Experiment.MaxCostPerRun = override.Experiment.MaxCostPerRun
	}
	if boolFieldSet(raw, "experiment", "max_tokens_per_run") {
		base.Experiment.MaxTokensPerRun = override.Experiment.MaxTokensPerRun
	}

	if boolFieldSet(raw, "encoding", "use_toon") {
		base.Encoding.UseToon = override.Encoding.UseToon
	}
}

func mergePersonalityConfig(base, override *Config, raw map[string]any) {
	if override.Personality.QuirkProbability != 0 {
		base.Personality.QuirkProbability = override.Personality.QuirkProbability
	}
	if boolFieldSet(raw, "personality", "enabled") {
		base.Personality.Enabled = override.Personality.Enabled
	}
	if override.Personality.Tone != "" {
		base.Personality.Tone = override.Personality.Tone
	}
	if override.Personality.DefaultPersona != "" {
		base.Personality.DefaultPersona = override.Personality.DefaultPersona
	}
	if boolFieldSet(raw, "personality", "categories") {
		if override.Personality.Categories == nil {
			base.Personality.Categories = nil
		} else {
			base.Personality.Categories = make(map[string]bool, len(override.Personality.Categories))
			for k, v := range override.Personality.Categories {
				base.Personality.Categories[k] = v
			}
		}
	}
	if len(override.Personality.PhaseOverrides) > 0 {
		if base.Personality.PhaseOverrides == nil {
			base.Personality.PhaseOverrides = make(map[string]string)
		}
		for k, v := range override.Personality.PhaseOverrides {
			base.Personality.PhaseOverrides[k] = v
		}
	}
	if len(override.Personality.Personas) > 0 {
		if base.Personality.Personas == nil {
			base.Personality.Personas = make(map[string]personality.PersonaDefinition)
		}
		for id, def := range override.Personality.Personas {
			base.Personality.Personas[id] = def
		}
	}
}
