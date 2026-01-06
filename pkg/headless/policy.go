package headless

// ResourceLimits describes optional runtime constraints for headless sessions.
// Not all fields are enforceable outside of containerized deployments.
type ResourceLimits struct {
	CPU            string `json:"cpu,omitempty"`
	Memory         string `json:"memory,omitempty"`
	Storage        string `json:"storage,omitempty"`
	TimeoutSeconds int32  `json:"timeoutSeconds,omitempty"`
}

// ToolPolicy constrains which tools are available and how they execute.
type ToolPolicy struct {
	AllowedTools       []string `json:"allowedTools,omitempty"`
	DeniedTools        []string `json:"deniedTools,omitempty"`
	RequireApproval    []string `json:"requireApproval,omitempty"`
	MaxExecTimeSeconds int32    `json:"maxExecTimeSeconds,omitempty"`
	MaxFileSizeBytes   int64    `json:"maxFileSizeBytes,omitempty"`
}
