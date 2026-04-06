package dockersandbox

import (
	"fmt"
	"os/user"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
)

// buildCreateArgs constructs the docker create argument list for a hardened container.
func buildCreateArgs(cfg config.DockerSandboxConfig, workspacePath, containerName string) []string {
	args := []string{
		"create",
		"--name", containerName,
	}

	if cfg.ReadOnlyRoot {
		args = append(args, "--read-only")
	}

	// Workspace bind mount
	mount := cfg.WorkspaceMount
	if mount == "" {
		mount = "/workspace"
	}
	if workspacePath != "" {
		args = append(args, "-v", fmt.Sprintf("%s:%s", workspacePath, mount))
	}

	// Writable tmpfs for /tmp
	tmpfsSize := cfg.Resources.TmpfsSize
	if tmpfsSize == "" {
		tmpfsSize = "64m"
	}
	args = append(args, "--tmpfs", fmt.Sprintf("/tmp:size=%s", tmpfsSize))

	// Network
	if cfg.NetworkEnabled == nil || !*cfg.NetworkEnabled {
		args = append(args, "--network", "none")
	}

	// Resource limits
	if cpus := strings.TrimSpace(cfg.Resources.CPUs); cpus != "" {
		args = append(args, "--cpus", cpus)
	}
	if mem := strings.TrimSpace(cfg.Resources.Memory); mem != "" {
		args = append(args, "--memory", mem)
	}
	if cfg.Resources.PidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprintf("%d", cfg.Resources.PidsLimit))
	}

	// Security
	if cfg.Security.NoNewPrivileges {
		args = append(args, "--security-opt", "no-new-privileges")
	}
	for _, cap := range cfg.Security.DropCapabilities {
		if cap = strings.TrimSpace(cap); cap != "" {
			args = append(args, "--cap-drop", cap)
		}
	}
	for _, cap := range cfg.Security.AddCapabilities {
		if cap = strings.TrimSpace(cap); cap != "" {
			args = append(args, "--cap-add", cap)
		}
	}
	if profile := strings.TrimSpace(cfg.Security.SeccompProfile); profile != "" {
		args = append(args, "--security-opt", fmt.Sprintf("seccomp=%s", profile))
	}
	if profile := strings.TrimSpace(cfg.Security.AppArmorProfile); profile != "" {
		args = append(args, "--security-opt", fmt.Sprintf("apparmor=%s", profile))
	}

	// Match host UID/GID
	if u, err := user.Current(); err == nil {
		args = append(args, "--user", fmt.Sprintf("%s:%s", u.Uid, u.Gid))
	}

	// Image + long-lived entrypoint
	image := cfg.Image
	if image == "" {
		image = "ubuntu:24.04"
	}
	args = append(args, image, "sleep", "infinity")

	return args
}
