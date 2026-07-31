package config

func mergeContextFabricConfig(base, override *Config, raw map[string]any) {
	if boolFieldSet(raw, "context_fabric", "enabled") {
		base.ContextFabric.Enabled = override.ContextFabric.Enabled
	}
	if boolFieldSet(raw, "context_fabric", "shadow") {
		base.ContextFabric.Shadow = override.ContextFabric.Shadow
	}
	if boolFieldSet(raw, "context_fabric", "policy_version") {
		base.ContextFabric.PolicyVersion = override.ContextFabric.PolicyVersion
	}
	if boolFieldSet(raw, "context_fabric", "renderer") {
		base.ContextFabric.Renderer = override.ContextFabric.Renderer
	}
	if boolFieldSet(raw, "context_fabric", "output_format") {
		base.ContextFabric.OutputFormat = override.ContextFabric.OutputFormat
	}
	if override.ContextFabric.BudgetTolerance != 0 {
		base.ContextFabric.BudgetTolerance = override.ContextFabric.BudgetTolerance
	}
	if boolFieldSet(raw, "context_fabric", "receipt_reuse") {
		base.ContextFabric.ReceiptReuse = override.ContextFabric.ReceiptReuse
	}
	if boolFieldSet(raw, "context_fabric", "auto_expand") {
		base.ContextFabric.AutoExpand = override.ContextFabric.AutoExpand
	}
	if boolFieldSet(raw, "context_fabric", "remote_inputs") {
		base.ContextFabric.RemoteInputs = override.ContextFabric.RemoteInputs
	}

	if override.ContextFabric.Pressure.Dedupe != 0 {
		base.ContextFabric.Pressure.Dedupe = override.ContextFabric.Pressure.Dedupe
	}
	if override.ContextFabric.Pressure.Checkpoint != 0 {
		base.ContextFabric.Pressure.Checkpoint = override.ContextFabric.Pressure.Checkpoint
	}
	if override.ContextFabric.Pressure.Compact != 0 {
		base.ContextFabric.Pressure.Compact = override.ContextFabric.Pressure.Compact
	}
	if override.ContextFabric.Pressure.Emergency != 0 {
		base.ContextFabric.Pressure.Emergency = override.ContextFabric.Pressure.Emergency
	}

	if boolFieldSet(raw, "context_fabric", "evidence", "store_enabled") {
		base.ContextFabric.Evidence.StoreEnabled = override.ContextFabric.Evidence.StoreEnabled
	}
	if override.ContextFabric.Evidence.InlineBytes != 0 {
		base.ContextFabric.Evidence.InlineBytes = override.ContextFabric.Evidence.InlineBytes
	}
	if override.ContextFabric.Evidence.RetentionDays != 0 {
		base.ContextFabric.Evidence.RetentionDays = override.ContextFabric.Evidence.RetentionDays
	}
	if boolFieldSet(raw, "context_fabric", "evidence", "compress") {
		base.ContextFabric.Evidence.Compress = override.ContextFabric.Evidence.Compress
	}

	if boolFieldSet(raw, "context_fabric", "checkpoint", "enabled") {
		base.ContextFabric.Checkpoint.Enabled = override.ContextFabric.Checkpoint.Enabled
	}
	if boolFieldSet(raw, "context_fabric", "checkpoint", "model_polish") {
		base.ContextFabric.Checkpoint.ModelPolish = override.ContextFabric.Checkpoint.ModelPolish
	}
}

func mergeAgentControllerConfig(base, override *Config, raw map[string]any) {
	if boolFieldSet(raw, "agent_controller", "mode") {
		base.AgentController.Mode = override.AgentController.Mode
	}
	if boolFieldSet(raw, "agent_controller", "policy_version") {
		base.AgentController.PolicyVersion = override.AgentController.PolicyVersion
	}
	if boolFieldSet(raw, "agent_controller", "critic", "enabled") {
		base.AgentController.Critic.Enabled = override.AgentController.Critic.Enabled
	}
	if boolFieldSet(raw, "agent_controller", "critic", "model") {
		base.AgentController.Critic.Model = override.AgentController.Critic.Model
	}
	if override.AgentController.Critic.ConfidenceThreshold != 0 {
		base.AgentController.Critic.ConfidenceThreshold = override.AgentController.Critic.ConfidenceThreshold
	}
	if override.AgentController.EmergencyFuse.ModelRequests != 0 {
		base.AgentController.EmergencyFuse.ModelRequests = override.AgentController.EmergencyFuse.ModelRequests
	}
	if override.AgentController.EmergencyFuse.ToolExecutions != 0 {
		base.AgentController.EmergencyFuse.ToolExecutions = override.AgentController.EmergencyFuse.ToolExecutions
	}
	if override.AgentController.EmergencyFuse.WallTime != 0 {
		base.AgentController.EmergencyFuse.WallTime = override.AgentController.EmergencyFuse.WallTime
	}
}

func mergeAgentOperationsConfig(base, override *Config, raw map[string]any) {
	if boolFieldSet(raw, "agent_operations", "review_changes") {
		base.AgentOperations.ReviewChanges = override.AgentOperations.ReviewChanges
	}
	if boolFieldSet(raw, "agent_operations", "review_pull_request") {
		base.AgentOperations.ReviewPullRequest = override.AgentOperations.ReviewPullRequest
	}
	if boolFieldSet(raw, "agent_operations", "post_review") {
		base.AgentOperations.PostReview = override.AgentOperations.PostReview
	}
	if boolFieldSet(raw, "agent_operations", "prepare_commit") {
		base.AgentOperations.PrepareCommit = override.AgentOperations.PrepareCommit
	}
	if boolFieldSet(raw, "agent_operations", "commit_changes") {
		base.AgentOperations.CommitChanges = override.AgentOperations.CommitChanges
	}
	if boolFieldSet(raw, "agent_operations", "push_changes") {
		base.AgentOperations.PushChanges = override.AgentOperations.PushChanges
	}
}

func mergeMetricsConfig(base, override *Config, raw map[string]any) {
	if boolFieldSet(raw, "metrics", "enabled") {
		base.Metrics.Enabled = override.Metrics.Enabled
	}
	if boolFieldSet(raw, "metrics", "export") {
		base.Metrics.Export = override.Metrics.Export
	}
	if boolFieldSet(raw, "metrics", "include_raw_content") {
		base.Metrics.IncludeRawContent = override.Metrics.IncludeRawContent
	}
}
