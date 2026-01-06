package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/odvcencio/buckley/pkg/personality"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
)

// loadAndMerge loads a YAML file and merges it into the config.
func loadAndMerge(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var override Config
	if err := yaml.Unmarshal(data, &override); err != nil {
		return fmt.Errorf("parsing YAML: %w", err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing YAML: %w", err)
	}

	mergeConfigs(cfg, &override, raw)
	return nil
}

// mergeConfigs merges override into base.
func mergeConfigs(base, override *Config, raw map[string]any) {
	if override == nil {
		return
	}

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

	if override.Memory.AutoCompactThreshold != 0 {
		base.Memory.AutoCompactThreshold = override.Memory.AutoCompactThreshold
	}
	if override.Memory.MaxCompactions != 0 {
		base.Memory.MaxCompactions = override.Memory.MaxCompactions
	}
	if boolFieldSet(raw, "memory", "summary_timeout_secs") {
		base.Memory.SummaryTimeoutSecs = override.Memory.SummaryTimeoutSecs
	}
	if boolFieldSet(raw, "memory", "retrieval_enabled") {
		base.Memory.RetrievalEnabled = override.Memory.RetrievalEnabled
	}
	if override.Memory.RetrievalLimit != 0 {
		base.Memory.RetrievalLimit = override.Memory.RetrievalLimit
	}
	if override.Memory.RetrievalMaxTokens != 0 {
		base.Memory.RetrievalMaxTokens = override.Memory.RetrievalMaxTokens
	}

	if override.Orchestrator.MaxSelfHealAttempts != 0 {
		base.Orchestrator.MaxSelfHealAttempts = override.Orchestrator.MaxSelfHealAttempts
	}
	if override.Orchestrator.MaxReviewCycles != 0 {
		base.Orchestrator.MaxReviewCycles = override.Orchestrator.MaxReviewCycles
	}
	if override.Orchestrator.TrustLevel != "" {
		base.Orchestrator.TrustLevel = override.Orchestrator.TrustLevel
	}
	if boolFieldSet(raw, "orchestrator", "auto_workflow") {
		base.Orchestrator.AutoWorkflow = override.Orchestrator.AutoWorkflow
	}
	if boolFieldSet(raw, "orchestrator", "planning", "enabled") {
		base.Orchestrator.Planning.Enabled = override.Orchestrator.Planning.Enabled
	}
	if boolFieldSet(raw, "orchestrator", "planning", "complexity_threshold") {
		base.Orchestrator.Planning.ComplexityThreshold = override.Orchestrator.Planning.ComplexityThreshold
	}
	if boolFieldSet(raw, "orchestrator", "planning", "planning_model") {
		base.Orchestrator.Planning.PlanningModel = override.Orchestrator.Planning.PlanningModel
	}
	if boolFieldSet(raw, "orchestrator", "planning", "long_run_enabled") {
		base.Orchestrator.Planning.LongRunEnabled = override.Orchestrator.Planning.LongRunEnabled
	}
	if boolFieldSet(raw, "orchestrator", "planning", "long_run_max_minutes") {
		base.Orchestrator.Planning.LongRunMaxMinutes = override.Orchestrator.Planning.LongRunMaxMinutes
	}
	if boolFieldSet(raw, "orchestrator", "planning", "long_run_log_decisions") {
		base.Orchestrator.Planning.LongRunLogDecisions = override.Orchestrator.Planning.LongRunLogDecisions
	}
	if boolFieldSet(raw, "orchestrator", "planning", "long_run_pause_on_risk") {
		base.Orchestrator.Planning.LongRunPauseOnRisk = override.Orchestrator.Planning.LongRunPauseOnRisk
	}

	if boolFieldSet(raw, "execution", "mode") {
		base.Execution.Mode = override.Execution.Mode
	}
	if boolFieldSet(raw, "oneshot", "mode") {
		base.Oneshot.Mode = override.Oneshot.Mode
	}
	if boolFieldSet(raw, "rlm", "coordinator", "model") {
		base.RLM.Coordinator.Model = override.RLM.Coordinator.Model
	}
	if boolFieldSet(raw, "rlm", "coordinator", "max_iterations") {
		base.RLM.Coordinator.MaxIterations = override.RLM.Coordinator.MaxIterations
	}
	if boolFieldSet(raw, "rlm", "coordinator", "max_tokens_budget") {
		base.RLM.Coordinator.MaxTokensBudget = override.RLM.Coordinator.MaxTokensBudget
	}
	if boolFieldSet(raw, "rlm", "coordinator", "max_wall_time") {
		base.RLM.Coordinator.MaxWallTime = override.RLM.Coordinator.MaxWallTime
	}
	if boolFieldSet(raw, "rlm", "coordinator", "confidence_threshold") {
		base.RLM.Coordinator.ConfidenceThreshold = override.RLM.Coordinator.ConfidenceThreshold
	}
	if boolFieldSet(raw, "rlm", "coordinator", "stream_partials") {
		base.RLM.Coordinator.StreamPartials = override.RLM.Coordinator.StreamPartials
	}
	if boolFieldSet(raw, "rlm", "tiers") {
		if override.RLM.Tiers == nil {
			base.RLM.Tiers = nil
		} else {
			if base.RLM.Tiers == nil {
				base.RLM.Tiers = make(map[string]RLMTierConfig, len(override.RLM.Tiers))
			}
			for name, tier := range override.RLM.Tiers {
				base.RLM.Tiers[name] = mergeRLMTierConfig(base.RLM.Tiers[name], tier)
			}
		}
	}
	if boolFieldSet(raw, "rlm", "scratchpad", "max_entries_memory") {
		base.RLM.Scratchpad.MaxEntriesMemory = override.RLM.Scratchpad.MaxEntriesMemory
	}
	if boolFieldSet(raw, "rlm", "scratchpad", "max_raw_bytes_memory") {
		base.RLM.Scratchpad.MaxRawBytesMemory = override.RLM.Scratchpad.MaxRawBytesMemory
	}
	if boolFieldSet(raw, "rlm", "scratchpad", "eviction_policy") {
		base.RLM.Scratchpad.EvictionPolicy = override.RLM.Scratchpad.EvictionPolicy
	}
	if boolFieldSet(raw, "rlm", "scratchpad", "default_ttl") {
		base.RLM.Scratchpad.DefaultTTL = override.RLM.Scratchpad.DefaultTTL
	}
	if boolFieldSet(raw, "rlm", "scratchpad", "persist_artifacts") {
		base.RLM.Scratchpad.PersistArtifacts = override.RLM.Scratchpad.PersistArtifacts
	}
	if boolFieldSet(raw, "rlm", "scratchpad", "persist_decisions") {
		base.RLM.Scratchpad.PersistDecisions = override.RLM.Scratchpad.PersistDecisions
	}

	if boolFieldSet(raw, "approval", "mode") {
		base.Approval.Mode = override.Approval.Mode
	}
	if boolFieldSet(raw, "approval", "trusted_paths") {
		base.Approval.TrustedPaths = append([]string{}, override.Approval.TrustedPaths...)
	}
	if boolFieldSet(raw, "approval", "denied_paths") {
		base.Approval.DeniedPaths = append([]string{}, override.Approval.DeniedPaths...)
	}
	if boolFieldSet(raw, "approval", "allow_network") {
		base.Approval.AllowNetwork = override.Approval.AllowNetwork
	}
	if boolFieldSet(raw, "approval", "allowed_tools") {
		base.Approval.AllowedTools = append([]string{}, override.Approval.AllowedTools...)
	}
	if boolFieldSet(raw, "approval", "denied_tools") {
		base.Approval.DeniedTools = append([]string{}, override.Approval.DeniedTools...)
	}
	if boolFieldSet(raw, "approval", "auto_approve_patterns") {
		base.Approval.AutoApprovePatterns = append([]string{}, override.Approval.AutoApprovePatterns...)
	}

	if boolFieldSet(raw, "batch", "enabled") {
		base.Batch.Enabled = override.Batch.Enabled
	}
	if boolFieldSet(raw, "batch", "namespace") {
		base.Batch.Namespace = override.Batch.Namespace
	}
	if boolFieldSet(raw, "batch", "kubeconfig") {
		base.Batch.Kubeconfig = override.Batch.Kubeconfig
	}
	if boolFieldSet(raw, "batch", "wait_for_completion") {
		base.Batch.WaitForCompletion = override.Batch.WaitForCompletion
	}
	if boolFieldSet(raw, "batch", "follow_logs") {
		base.Batch.FollowLogs = override.Batch.FollowLogs
	}

	if boolFieldSet(raw, "batch", "job_template", "image") {
		base.Batch.JobTemplate.Image = override.Batch.JobTemplate.Image
	}
	if boolFieldSet(raw, "batch", "job_template", "image_pull_policy") {
		base.Batch.JobTemplate.ImagePullPolicy = override.Batch.JobTemplate.ImagePullPolicy
	}
	if boolFieldSet(raw, "batch", "job_template", "service_account") {
		base.Batch.JobTemplate.ServiceAccount = override.Batch.JobTemplate.ServiceAccount
	}
	if boolFieldSet(raw, "batch", "job_template", "command") {
		base.Batch.JobTemplate.Command = append([]string{}, override.Batch.JobTemplate.Command...)
	}
	if boolFieldSet(raw, "batch", "job_template", "args") {
		base.Batch.JobTemplate.Args = append([]string{}, override.Batch.JobTemplate.Args...)
	}
	if boolFieldSet(raw, "batch", "job_template", "env") {
		if override.Batch.JobTemplate.Env == nil {
			base.Batch.JobTemplate.Env = nil
		} else {
			base.Batch.JobTemplate.Env = make(map[string]string, len(override.Batch.JobTemplate.Env))
			for k, v := range override.Batch.JobTemplate.Env {
				base.Batch.JobTemplate.Env[k] = v
			}
		}
	}
	if boolFieldSet(raw, "batch", "job_template", "env_from_secrets") {
		base.Batch.JobTemplate.EnvFromSecrets = append([]string{}, override.Batch.JobTemplate.EnvFromSecrets...)
	}
	if boolFieldSet(raw, "batch", "job_template", "env_from_configmaps") {
		base.Batch.JobTemplate.EnvFromConfigMaps = append([]string{}, override.Batch.JobTemplate.EnvFromConfigMaps...)
	}
	if boolFieldSet(raw, "batch", "job_template", "workspace_claim") {
		base.Batch.JobTemplate.WorkspaceClaim = override.Batch.JobTemplate.WorkspaceClaim
	}
	if boolFieldSet(raw, "batch", "job_template", "workspace_mount_path") {
		base.Batch.JobTemplate.WorkspaceMountPath = override.Batch.JobTemplate.WorkspaceMountPath
	}
	if boolFieldSet(raw, "batch", "job_template", "workspace_volume_template") {
		if override.Batch.JobTemplate.WorkspaceVolumeTemplate == nil {
			base.Batch.JobTemplate.WorkspaceVolumeTemplate = nil
		} else {
			vt := *override.Batch.JobTemplate.WorkspaceVolumeTemplate
			vt.AccessModes = append([]string{}, override.Batch.JobTemplate.WorkspaceVolumeTemplate.AccessModes...)
			base.Batch.JobTemplate.WorkspaceVolumeTemplate = &vt
		}
	}
	if boolFieldSet(raw, "batch", "job_template", "shared_config_claim") {
		base.Batch.JobTemplate.SharedConfigClaim = override.Batch.JobTemplate.SharedConfigClaim
	}
	if boolFieldSet(raw, "batch", "job_template", "shared_config_mount_path") {
		base.Batch.JobTemplate.SharedConfigMountPath = override.Batch.JobTemplate.SharedConfigMountPath
	}
	if boolFieldSet(raw, "batch", "job_template", "ttl_seconds_after_finished") {
		base.Batch.JobTemplate.TTLSecondsAfterFinished = override.Batch.JobTemplate.TTLSecondsAfterFinished
	}
	if boolFieldSet(raw, "batch", "job_template", "backoff_limit") {
		base.Batch.JobTemplate.BackoffLimit = override.Batch.JobTemplate.BackoffLimit
	}
	if boolFieldSet(raw, "batch", "job_template", "image_pull_secrets") {
		base.Batch.JobTemplate.ImagePullSecrets = append([]string{}, override.Batch.JobTemplate.ImagePullSecrets...)
	}
	if boolFieldSet(raw, "batch", "job_template", "resources") {
		base.Batch.JobTemplate.Resources = override.Batch.JobTemplate.Resources
	}
	if boolFieldSet(raw, "batch", "job_template", "node_selector") {
		if override.Batch.JobTemplate.NodeSelector == nil {
			base.Batch.JobTemplate.NodeSelector = nil
		} else {
			base.Batch.JobTemplate.NodeSelector = make(map[string]string, len(override.Batch.JobTemplate.NodeSelector))
			for k, v := range override.Batch.JobTemplate.NodeSelector {
				base.Batch.JobTemplate.NodeSelector[k] = v
			}
		}
	}
	if boolFieldSet(raw, "batch", "job_template", "tolerations") {
		base.Batch.JobTemplate.Tolerations = append([]corev1.Toleration{}, override.Batch.JobTemplate.Tolerations...)
	}
	if boolFieldSet(raw, "batch", "job_template", "affinity") {
		base.Batch.JobTemplate.Affinity = override.Batch.JobTemplate.Affinity
	}
	if boolFieldSet(raw, "batch", "job_template", "config_map") {
		base.Batch.JobTemplate.ConfigMap = override.Batch.JobTemplate.ConfigMap
	}
	if boolFieldSet(raw, "batch", "job_template", "config_map_mount_path") {
		base.Batch.JobTemplate.ConfigMapMountPath = override.Batch.JobTemplate.ConfigMapMountPath
	}

	if boolFieldSet(raw, "batch", "remote_branch", "enabled") {
		base.Batch.RemoteBranch.Enabled = override.Batch.RemoteBranch.Enabled
	}
	if boolFieldSet(raw, "batch", "remote_branch", "prefix") {
		base.Batch.RemoteBranch.Prefix = override.Batch.RemoteBranch.Prefix
	}
	if boolFieldSet(raw, "batch", "remote_branch", "remote_name") {
		base.Batch.RemoteBranch.RemoteName = override.Batch.RemoteBranch.RemoteName
	}

	if boolFieldSet(raw, "git_clone", "allowed_schemes") {
		base.GitClone.AllowedSchemes = append([]string{}, override.GitClone.AllowedSchemes...)
	}
	if boolFieldSet(raw, "git_clone", "allowed_hosts") {
		base.GitClone.AllowedHosts = append([]string{}, override.GitClone.AllowedHosts...)
	}
	if boolFieldSet(raw, "git_clone", "denied_hosts") {
		base.GitClone.DeniedHosts = append([]string{}, override.GitClone.DeniedHosts...)
	}
	if boolFieldSet(raw, "git_clone", "deny_private_networks") {
		base.GitClone.DenyPrivateNetworks = override.GitClone.DenyPrivateNetworks
	}
	if boolFieldSet(raw, "git_clone", "resolve_dns") {
		base.GitClone.ResolveDNS = override.GitClone.ResolveDNS
	}
	if boolFieldSet(raw, "git_clone", "deny_scp_syntax") {
		base.GitClone.DenySCPSyntax = override.GitClone.DenySCPSyntax
	}
	if boolFieldSet(raw, "git_clone", "dns_resolve_timeout_seconds") {
		base.GitClone.DNSResolveTimeoutSec = override.GitClone.DNSResolveTimeoutSec
	}

	if boolFieldSet(raw, "git_events", "enabled") {
		base.GitEvents.Enabled = override.GitEvents.Enabled
	}
	if boolFieldSet(raw, "git_events", "secret") {
		base.GitEvents.Secret = override.GitEvents.Secret
	}
	if boolFieldSet(raw, "git_events", "auto_regression_plan") {
		base.GitEvents.AutoRegressionPlan = override.GitEvents.AutoRegressionPlan
	}
	if boolFieldSet(raw, "git_events", "webhook_bind") {
		base.GitEvents.WebhookBind = override.GitEvents.WebhookBind
	}
	if boolFieldSet(raw, "git_events", "regression_command") {
		base.GitEvents.RegressionCommand = override.GitEvents.RegressionCommand
	}
	if boolFieldSet(raw, "git_events", "release_command") {
		base.GitEvents.ReleaseCommand = override.GitEvents.ReleaseCommand
	}
	if boolFieldSet(raw, "git_events", "failure_command") {
		base.GitEvents.FailureCommand = override.GitEvents.FailureCommand
	}

	if boolFieldSet(raw, "input", "transcription", "provider") {
		base.Input.Transcription.Provider = override.Input.Transcription.Provider
	}
	if boolFieldSet(raw, "input", "transcription", "whisper_model") {
		base.Input.Transcription.WhisperModel = override.Input.Transcription.WhisperModel
	}
	if boolFieldSet(raw, "input", "transcription", "api_endpoint") {
		base.Input.Transcription.APIEndpoint = override.Input.Transcription.APIEndpoint
	}
	if boolFieldSet(raw, "input", "transcription", "timeout") {
		base.Input.Transcription.Timeout = override.Input.Transcription.Timeout
	}

	if boolFieldSet(raw, "input", "video", "enabled") {
		base.Input.Video.Enabled = override.Input.Video.Enabled
	}
	if boolFieldSet(raw, "input", "video", "max_frames") {
		base.Input.Video.MaxFrames = override.Input.Video.MaxFrames
	}
	if boolFieldSet(raw, "input", "video", "extract_audio") {
		base.Input.Video.ExtractAudio = override.Input.Video.ExtractAudio
	}
	if boolFieldSet(raw, "input", "video", "ffmpeg_path") {
		base.Input.Video.FFmpegPath = override.Input.Video.FFmpegPath
	}

	if boolFieldSet(raw, "worktrees", "use_containers") {
		base.Worktrees.UseContainers = override.Worktrees.UseContainers
	}
	if override.Worktrees.RootPath != "" {
		base.Worktrees.RootPath = override.Worktrees.RootPath
	}
	if override.Worktrees.ContainerService != "" {
		base.Worktrees.ContainerService = override.Worktrees.ContainerService
	}

	if boolFieldSet(raw, "ipc", "enabled") {
		base.IPC.Enabled = override.IPC.Enabled
	}
	if override.IPC.Bind != "" {
		base.IPC.Bind = override.IPC.Bind
	}
	if boolFieldSet(raw, "ipc", "enable_browser") {
		base.IPC.EnableBrowser = override.IPC.EnableBrowser
	}
	if boolFieldSet(raw, "ipc", "public_metrics") {
		base.IPC.PublicMetrics = override.IPC.PublicMetrics
	}
	if boolFieldSet(raw, "ipc", "require_token") {
		base.IPC.RequireToken = override.IPC.RequireToken
	}
	if boolFieldSet(raw, "ipc", "basic_auth_enabled") {
		base.IPC.BasicAuthEnabled = override.IPC.BasicAuthEnabled
	}
	if override.IPC.BasicAuthUsername != "" {
		base.IPC.BasicAuthUsername = override.IPC.BasicAuthUsername
	}
	if override.IPC.BasicAuthPassword != "" {
		base.IPC.BasicAuthPassword = override.IPC.BasicAuthPassword
	}
	if override.IPC.PushSubject != "" {
		base.IPC.PushSubject = override.IPC.PushSubject
	}
	if len(override.IPC.AllowedOrigins) > 0 {
		base.IPC.AllowedOrigins = append([]string{}, override.IPC.AllowedOrigins...)
	}

	if len(override.Workflow.TaskPhaseLoop) > 0 {
		base.Workflow.TaskPhaseLoop = append([]string{}, override.Workflow.TaskPhaseLoop...)
	}
	if len(override.Workflow.TaskPhases) > 0 {
		base.Workflow.TaskPhases = append([]TaskPhaseConfig{}, override.Workflow.TaskPhases...)
	}

	if override.CostManagement.SessionBudget != 0 {
		base.CostManagement.SessionBudget = override.CostManagement.SessionBudget
	}
	if override.CostManagement.DailyBudget != 0 {
		base.CostManagement.DailyBudget = override.CostManagement.DailyBudget
	}
	if override.CostManagement.MonthlyBudget != 0 {
		base.CostManagement.MonthlyBudget = override.CostManagement.MonthlyBudget
	}
	if override.CostManagement.AutoStopAt != 0 {
		base.CostManagement.AutoStopAt = override.CostManagement.AutoStopAt
	}

	if override.RetryPolicy.MaxRetries != 0 {
		base.RetryPolicy.MaxRetries = override.RetryPolicy.MaxRetries
	}
	if override.RetryPolicy.InitialBackoff != 0 {
		base.RetryPolicy.InitialBackoff = override.RetryPolicy.InitialBackoff
	}
	if override.RetryPolicy.MaxBackoff != 0 {
		base.RetryPolicy.MaxBackoff = override.RetryPolicy.MaxBackoff
	}
	if override.RetryPolicy.Multiplier != 0 {
		base.RetryPolicy.Multiplier = override.RetryPolicy.Multiplier
	}

	if override.Artifacts.PlanningDir != "" {
		base.Artifacts.PlanningDir = override.Artifacts.PlanningDir
	}
	if override.Artifacts.ExecutionDir != "" {
		base.Artifacts.ExecutionDir = override.Artifacts.ExecutionDir
	}
	if override.Artifacts.ReviewDir != "" {
		base.Artifacts.ReviewDir = override.Artifacts.ReviewDir
	}
	if override.Artifacts.ArchiveDir != "" {
		base.Artifacts.ArchiveDir = override.Artifacts.ArchiveDir
	}
	if boolFieldSet(raw, "artifacts", "archive_by_month") {
		base.Artifacts.ArchiveByMonth = override.Artifacts.ArchiveByMonth
	}
	if boolFieldSet(raw, "artifacts", "auto_archive_on_pr_merge") {
		base.Artifacts.AutoArchiveOnPRMerge = override.Artifacts.AutoArchiveOnPRMerge
	}

	if override.Workflow.PlanningQuestionsMin != 0 {
		base.Workflow.PlanningQuestionsMin = override.Workflow.PlanningQuestionsMin
	}
	if override.Workflow.PlanningQuestionsMax != 0 {
		base.Workflow.PlanningQuestionsMax = override.Workflow.PlanningQuestionsMax
	}
	if boolFieldSet(raw, "workflow", "incremental_approval") {
		base.Workflow.IncrementalApproval = override.Workflow.IncrementalApproval
	}
	if boolFieldSet(raw, "workflow", "pause_on_business_ambiguity") {
		base.Workflow.PauseOnBusinessAmbiguity = override.Workflow.PauseOnBusinessAmbiguity
	}
	if boolFieldSet(raw, "workflow", "pause_on_architectural_conflict") {
		base.Workflow.PauseOnArchitecturalConflict = override.Workflow.PauseOnArchitecturalConflict
	}
	if boolFieldSet(raw, "workflow", "pause_on_complexity_explosion") {
		base.Workflow.PauseOnComplexityExplosion = override.Workflow.PauseOnComplexityExplosion
	}
	if boolFieldSet(raw, "workflow", "pause_on_environment_mismatch") {
		base.Workflow.PauseOnEnvironmentMismatch = override.Workflow.PauseOnEnvironmentMismatch
	}
	if override.Workflow.ReviewIterationsMax != 0 {
		base.Workflow.ReviewIterationsMax = override.Workflow.ReviewIterationsMax
	}
	if boolFieldSet(raw, "workflow", "allow_nits_in_approval") {
		base.Workflow.AllowNitsInApproval = override.Workflow.AllowNitsInApproval
	}
	if boolFieldSet(raw, "workflow", "generate_opportunistic_improvements") {
		base.Workflow.GenerateOpportunisticImprovements = override.Workflow.GenerateOpportunisticImprovements
	}

	if override.Compaction.ContextThreshold != 0 {
		base.Compaction.ContextThreshold = override.Compaction.ContextThreshold
	}
	if override.Compaction.TaskInterval != 0 {
		base.Compaction.TaskInterval = override.Compaction.TaskInterval
	}
	if override.Compaction.TokenThreshold != 0 {
		base.Compaction.TokenThreshold = override.Compaction.TokenThreshold
	}
	if override.Compaction.TargetReduction != 0 {
		base.Compaction.TargetReduction = override.Compaction.TargetReduction
	}
	if boolFieldSet(raw, "compaction", "preserve_commands") {
		base.Compaction.PreserveCommands = override.Compaction.PreserveCommands
	}
	if len(override.Compaction.Models) > 0 {
		base.Compaction.Models = override.Compaction.Models
	}

	if override.UI.ActivityPanelDefault != "" {
		base.UI.ActivityPanelDefault = override.UI.ActivityPanelDefault
	}
	if override.UI.DiffViewerDefault != "" {
		base.UI.DiffViewerDefault = override.UI.DiffViewerDefault
	}
	if override.UI.ToolGroupingWindowSeconds != 0 {
		base.UI.ToolGroupingWindowSeconds = override.UI.ToolGroupingWindowSeconds
	}
	if boolFieldSet(raw, "ui", "show_tool_costs") {
		base.UI.ShowToolCosts = override.UI.ShowToolCosts
	}
	if boolFieldSet(raw, "ui", "show_intent_statements") {
		base.UI.ShowIntentStatements = override.UI.ShowIntentStatements
	}
	if boolFieldSet(raw, "ui", "high_contrast") {
		base.UI.HighContrast = override.UI.HighContrast
	}
	if boolFieldSet(raw, "ui", "use_text_labels") {
		base.UI.UseTextLabels = override.UI.UseTextLabels
	}
	if boolFieldSet(raw, "ui", "reduce_animation") {
		base.UI.ReduceAnimation = override.UI.ReduceAnimation
	}

	if boolFieldSet(raw, "commenting", "require_function_docs") {
		base.Commenting.RequireFunctionDocs = override.Commenting.RequireFunctionDocs
	}
	if override.Commenting.RequireBlockCommentsOverLines != 0 {
		base.Commenting.RequireBlockCommentsOverLines = override.Commenting.RequireBlockCommentsOverLines
	}
	if boolFieldSet(raw, "commenting", "comment_non_obvious_only") {
		base.Commenting.CommentNonObviousOnly = override.Commenting.CommentNonObviousOnly
	}

	if boolFieldSet(raw, "diagnostics", "network_logs_enabled") {
		base.Diagnostics.NetworkLogsEnabled = override.Diagnostics.NetworkLogsEnabled
	}
}

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

func mergeRLMTierConfig(base, override RLMTierConfig) RLMTierConfig {
	if strings.TrimSpace(override.Model) != "" {
		base.Model = override.Model
	}
	if strings.TrimSpace(override.Provider) != "" {
		base.Provider = override.Provider
	}
	if override.Models != nil {
		base.Models = append([]string{}, override.Models...)
	}
	if override.MaxCostPerMillion != 0 {
		base.MaxCostPerMillion = override.MaxCostPerMillion
	}
	if override.MinContextWindow != 0 {
		base.MinContextWindow = override.MinContextWindow
	}
	if override.Prefer != nil {
		base.Prefer = append([]string{}, override.Prefer...)
	}
	if override.Requires != nil {
		base.Requires = append([]string{}, override.Requires...)
	}
	return base
}
