package config

import corev1 "k8s.io/api/core/v1"

func mergeBatchConfig(base, override *Config, raw map[string]any) {
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
}

func mergeGitCloneConfig(base, override *Config, raw map[string]any) {
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
}

func mergeGitEventsConfig(base, override *Config, raw map[string]any) {
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
}

func mergeInputConfig(base, override *Config, raw map[string]any) {
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
}

func mergeWorktreeConfig(base, override *Config, raw map[string]any) {
	if boolFieldSet(raw, "worktrees", "use_containers") {
		base.Worktrees.UseContainers = override.Worktrees.UseContainers
	}
	if override.Worktrees.RootPath != "" {
		base.Worktrees.RootPath = override.Worktrees.RootPath
	}
	if override.Worktrees.ContainerService != "" {
		base.Worktrees.ContainerService = override.Worktrees.ContainerService
	}
}

func mergeIPCConfig(base, override *Config, raw map[string]any) {
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
}

func mergeWorkflowPhaseConfig(base, override *Config) {
	if len(override.Workflow.TaskPhaseLoop) > 0 {
		base.Workflow.TaskPhaseLoop = append([]string{}, override.Workflow.TaskPhaseLoop...)
	}
	if len(override.Workflow.TaskPhases) > 0 {
		base.Workflow.TaskPhases = append([]TaskPhaseConfig{}, override.Workflow.TaskPhases...)
	}
}
