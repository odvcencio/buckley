package types

import "testing"

func TestPermissionTier_Ordering(t *testing.T) {
	if TierReadOnly >= TierWorkspaceWrite {
		t.Error("read_only should be less than workspace_write")
	}
	if TierWorkspaceWrite >= TierShellExec {
		t.Error("workspace_write should be less than shell_exec")
	}
	if TierShellExec >= TierFullAccess {
		t.Error("shell_exec should be less than full_access")
	}
}

func TestPermissionTier_String(t *testing.T) {
	tests := []struct {
		tier PermissionTier
		want string
	}{
		{TierReadOnly, "read_only"},
		{TierWorkspaceWrite, "workspace_write"},
		{TierShellExec, "shell_exec"},
		{TierFullAccess, "full_access"},
	}
	for _, tt := range tests {
		if got := tt.tier.String(); got != tt.want {
			t.Errorf("PermissionTier(%d).String() = %q, want %q", tt.tier, got, tt.want)
		}
	}
}

func TestSandboxLevel_String(t *testing.T) {
	tests := []struct {
		level SandboxLevel
		want  string
	}{
		{SandboxNone, "none"},
		{SandboxWorkspace, "workspace"},
		{SandboxNetworkOff, "network_off"},
		{SandboxFull, "full"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("SandboxLevel(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestParsePermissionTier(t *testing.T) {
	tests := []struct {
		input string
		want  PermissionTier
	}{
		{"read_only", TierReadOnly},
		{"workspace_write", TierWorkspaceWrite},
		{"shell_exec", TierShellExec},
		{"full_access", TierFullAccess},
		{"unknown", TierReadOnly}, // fallback
	}
	for _, tt := range tests {
		if got := ParsePermissionTier(tt.input); got != tt.want {
			t.Errorf("ParsePermissionTier(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseSandboxLevel(t *testing.T) {
	tests := []struct {
		input string
		want  SandboxLevel
	}{
		{"none", SandboxNone},
		{"workspace", SandboxWorkspace},
		{"network_off", SandboxNetworkOff},
		{"full", SandboxFull},
		{"unknown", SandboxNone}, // fallback
	}
	for _, tt := range tests {
		if got := ParseSandboxLevel(tt.input); got != tt.want {
			t.Errorf("ParseSandboxLevel(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
