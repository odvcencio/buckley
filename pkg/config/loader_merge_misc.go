package config

func mergeCostConfig(base, override *Config) {
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
}

func mergeRetryPolicyConfig(base, override *Config) {
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
}

func mergeArtifactsConfig(base, override *Config, raw map[string]any) {
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
}

func mergeWorkflowConfig(base, override *Config, raw map[string]any) {
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
}

func mergeCompactionConfig(base, override *Config, raw map[string]any) {
	if override.Compaction.ContextThreshold != 0 {
		base.Compaction.ContextThreshold = override.Compaction.ContextThreshold
	}
	if override.Compaction.RLMAutoTrigger != 0 {
		base.Compaction.RLMAutoTrigger = override.Compaction.RLMAutoTrigger
	}
	if override.Compaction.CompactionRatio != 0 {
		base.Compaction.CompactionRatio = override.Compaction.CompactionRatio
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
}

func mergeUIConfig(base, override *Config, raw map[string]any) {
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
	if override.UI.MessageMetadata != "" {
		base.UI.MessageMetadata = override.UI.MessageMetadata
	}
	if override.UI.SidebarWidth != 0 {
		base.UI.SidebarWidth = override.UI.SidebarWidth
	}
	if override.UI.SidebarMinWidth != 0 {
		base.UI.SidebarMinWidth = override.UI.SidebarMinWidth
	}
	if override.UI.SidebarMaxWidth != 0 {
		base.UI.SidebarMaxWidth = override.UI.SidebarMaxWidth
	}
	if boolFieldSet(raw, "ui", "audio", "enabled") {
		base.UI.Audio.Enabled = override.UI.Audio.Enabled
	}
	if boolFieldSet(raw, "ui", "audio", "assets_path") {
		base.UI.Audio.AssetsPath = override.UI.Audio.AssetsPath
	}
	if boolFieldSet(raw, "ui", "audio", "master_volume") {
		base.UI.Audio.MasterVolume = override.UI.Audio.MasterVolume
	}
	if boolFieldSet(raw, "ui", "audio", "sfx_volume") {
		base.UI.Audio.SFXVolume = override.UI.Audio.SFXVolume
	}
	if boolFieldSet(raw, "ui", "audio", "music_volume") {
		base.UI.Audio.MusicVolume = override.UI.Audio.MusicVolume
	}
	if boolFieldSet(raw, "ui", "audio", "muted") {
		base.UI.Audio.Muted = override.UI.Audio.Muted
	}
	if override.WebUI.BaseURL != "" {
		base.WebUI.BaseURL = override.WebUI.BaseURL
	}
}

func mergeCommentingConfig(base, override *Config, raw map[string]any) {
	if boolFieldSet(raw, "commenting", "require_function_docs") {
		base.Commenting.RequireFunctionDocs = override.Commenting.RequireFunctionDocs
	}
	if override.Commenting.RequireBlockCommentsOverLines != 0 {
		base.Commenting.RequireBlockCommentsOverLines = override.Commenting.RequireBlockCommentsOverLines
	}
	if boolFieldSet(raw, "commenting", "comment_non_obvious_only") {
		base.Commenting.CommentNonObviousOnly = override.Commenting.CommentNonObviousOnly
	}
}

func mergeDiagnosticsConfig(base, override *Config, raw map[string]any) {
	if boolFieldSet(raw, "diagnostics", "network_logs_enabled") {
		base.Diagnostics.NetworkLogsEnabled = override.Diagnostics.NetworkLogsEnabled
	}
}
