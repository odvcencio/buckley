package config

import corev1 "k8s.io/api/core/v1"

// BatchConfig controls containerized task execution
type BatchConfig struct {
	Enabled           bool                    `yaml:"enabled"`
	Namespace         string                  `yaml:"namespace"`
	Kubeconfig        string                  `yaml:"kubeconfig"`
	WaitForCompletion bool                    `yaml:"wait_for_completion"`
	FollowLogs        bool                    `yaml:"follow_logs"`
	JobTemplate       BatchJobTemplateConfig  `yaml:"job_template"`
	RemoteBranch      BatchRemoteBranchConfig `yaml:"remote_branch"`
}

// BatchJobTemplateConfig defines the job template for each task container
type BatchJobTemplateConfig struct {
	Image                   string                      `yaml:"image"`
	ImagePullPolicy         string                      `yaml:"image_pull_policy"`
	ServiceAccount          string                      `yaml:"service_account"`
	Command                 []string                    `yaml:"command"`
	Args                    []string                    `yaml:"args"`
	Env                     map[string]string           `yaml:"env"`
	EnvFromSecrets          []string                    `yaml:"env_from_secrets"`
	EnvFromConfigMaps       []string                    `yaml:"env_from_configmaps"`
	WorkspaceClaim          string                      `yaml:"workspace_claim"`
	WorkspaceMountPath      string                      `yaml:"workspace_mount_path"`
	WorkspaceVolumeTemplate *BatchVolumeTemplateConfig  `yaml:"workspace_volume_template"`
	SharedConfigClaim       string                      `yaml:"shared_config_claim"`
	SharedConfigMountPath   string                      `yaml:"shared_config_mount_path"`
	TTLSecondsAfterFinished int32                       `yaml:"ttl_seconds_after_finished"`
	BackoffLimit            int32                       `yaml:"backoff_limit"`
	ImagePullSecrets        []string                    `yaml:"image_pull_secrets"`
	Resources               corev1.ResourceRequirements `yaml:"resources"`
	NodeSelector            map[string]string           `yaml:"node_selector"`
	Tolerations             []corev1.Toleration         `yaml:"tolerations"`
	Affinity                *corev1.Affinity            `yaml:"affinity"`
	ConfigMap               string                      `yaml:"config_map"`
	ConfigMapMountPath      string                      `yaml:"config_map_mount_path"`
}

// BatchVolumeTemplateConfig defines ephemeral PVC templates mounted per task.
type BatchVolumeTemplateConfig struct {
	StorageClass string   `yaml:"storage_class"`
	AccessModes  []string `yaml:"access_modes"`
	Size         string   `yaml:"size"`
}

// BatchRemoteBranchConfig describes how remote feature branches are generated
type BatchRemoteBranchConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Prefix     string `yaml:"prefix"`
	RemoteName string `yaml:"remote_name"`
}
