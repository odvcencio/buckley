package types

// PermissionTier is ordered — higher tiers include lower.
type PermissionTier int

const (
	TierReadOnly PermissionTier = iota
	TierWorkspaceWrite
	TierShellExec
	TierFullAccess
)

var permissionTierNames = [...]string{"read_only", "workspace_write", "shell_exec", "full_access"}

func (t PermissionTier) String() string {
	if int(t) < len(permissionTierNames) {
		return permissionTierNames[t]
	}
	return "unknown"
}

func ParsePermissionTier(s string) PermissionTier {
	for i, name := range permissionTierNames {
		if name == s {
			return PermissionTier(i)
		}
	}
	return TierReadOnly
}

// SandboxLevel controls isolation for tool execution.
type SandboxLevel int

const (
	SandboxNone SandboxLevel = iota
	SandboxWorkspace
	SandboxNetworkOff
	SandboxFull
)

var sandboxLevelNames = [...]string{"none", "workspace", "network_off", "full"}

func (l SandboxLevel) String() string {
	if int(l) < len(sandboxLevelNames) {
		return sandboxLevelNames[l]
	}
	return "unknown"
}

func ParseSandboxLevel(s string) SandboxLevel {
	for i, name := range sandboxLevelNames {
		if name == s {
			return SandboxLevel(i)
		}
	}
	return SandboxNone
}
