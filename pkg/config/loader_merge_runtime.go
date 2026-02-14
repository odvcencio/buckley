package config

import "time"

func mergeMemoryConfig(base, override *Config, raw map[string]any) {
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
}

func mergeOrchestratorConfig(base, override *Config, raw map[string]any) {
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
}

func mergeExecutionAndRLMConfig(base, override *Config, raw map[string]any) {
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
	if boolFieldSet(raw, "rlm", "sub_agent", "model") {
		base.RLM.SubAgent.Model = override.RLM.SubAgent.Model
	}
	if boolFieldSet(raw, "rlm", "sub_agent", "max_concurrent") {
		base.RLM.SubAgent.MaxConcurrent = override.RLM.SubAgent.MaxConcurrent
	}
	if boolFieldSet(raw, "rlm", "sub_agent", "timeout") {
		base.RLM.SubAgent.Timeout = override.RLM.SubAgent.Timeout
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
}

func mergeApprovalConfig(base, override *Config, raw map[string]any, projectScope bool) {
	if !projectScope {
		if boolFieldSet(raw, "approval", "mode") {
			base.Approval.Mode = override.Approval.Mode
		}
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
}

func mergeSandboxConfig(base, override *Config, raw map[string]any, projectScope bool) {
	if !projectScope {
		if boolFieldSet(raw, "sandbox", "mode") {
			base.Sandbox.Mode = override.Sandbox.Mode
		}
		if boolFieldSet(raw, "sandbox", "allow_unsafe") {
			base.Sandbox.AllowUnsafe = override.Sandbox.AllowUnsafe
		}
	}
	if boolFieldSet(raw, "sandbox", "workspace_path") {
		base.Sandbox.WorkspacePath = override.Sandbox.WorkspacePath
	}
	if boolFieldSet(raw, "sandbox", "allowed_paths") {
		base.Sandbox.AllowedPaths = append([]string{}, override.Sandbox.AllowedPaths...)
	}
	if boolFieldSet(raw, "sandbox", "denied_paths") {
		base.Sandbox.DeniedPaths = append([]string{}, override.Sandbox.DeniedPaths...)
	}
	if boolFieldSet(raw, "sandbox", "allowed_commands") {
		base.Sandbox.AllowedCommands = append([]string{}, override.Sandbox.AllowedCommands...)
	}
	if boolFieldSet(raw, "sandbox", "denied_commands") {
		base.Sandbox.DeniedCommands = append([]string{}, override.Sandbox.DeniedCommands...)
	}
	if boolFieldSet(raw, "sandbox", "allow_network") {
		base.Sandbox.AllowNetwork = override.Sandbox.AllowNetwork
	}
	if boolFieldSet(raw, "sandbox", "timeout") {
		base.Sandbox.Timeout = override.Sandbox.Timeout
	}
	if boolFieldSet(raw, "sandbox", "max_output_bytes") {
		base.Sandbox.MaxOutputBytes = override.Sandbox.MaxOutputBytes
	}
}

func mergeToolMiddlewareConfig(base, override *Config, raw map[string]any) {
	if boolFieldSet(raw, "tool_middleware", "default_timeout") {
		base.ToolMiddleware.DefaultTimeout = override.ToolMiddleware.DefaultTimeout
	}
	if boolFieldSet(raw, "tool_middleware", "per_tool_timeouts") {
		if override.ToolMiddleware.PerToolTimeouts == nil {
			base.ToolMiddleware.PerToolTimeouts = nil
		} else {
			base.ToolMiddleware.PerToolTimeouts = make(map[string]time.Duration, len(override.ToolMiddleware.PerToolTimeouts))
			for k, v := range override.ToolMiddleware.PerToolTimeouts {
				base.ToolMiddleware.PerToolTimeouts[k] = v
			}
		}
	}
	if boolFieldSet(raw, "tool_middleware", "max_result_bytes") {
		base.ToolMiddleware.MaxResultBytes = override.ToolMiddleware.MaxResultBytes
	}
	if boolFieldSet(raw, "tool_middleware", "retry", "max_attempts") {
		base.ToolMiddleware.Retry.MaxAttempts = override.ToolMiddleware.Retry.MaxAttempts
	}
	if boolFieldSet(raw, "tool_middleware", "retry", "initial_delay") {
		base.ToolMiddleware.Retry.InitialDelay = override.ToolMiddleware.Retry.InitialDelay
	}
	if boolFieldSet(raw, "tool_middleware", "retry", "max_delay") {
		base.ToolMiddleware.Retry.MaxDelay = override.ToolMiddleware.Retry.MaxDelay
	}
	if boolFieldSet(raw, "tool_middleware", "retry", "multiplier") {
		base.ToolMiddleware.Retry.Multiplier = override.ToolMiddleware.Retry.Multiplier
	}
	if boolFieldSet(raw, "tool_middleware", "retry", "jitter") {
		base.ToolMiddleware.Retry.Jitter = override.ToolMiddleware.Retry.Jitter
	}
}
