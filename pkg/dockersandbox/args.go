package dockersandbox

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"m31labs.dev/buckley/pkg/config"
)

// buildCreateArgs constructs the docker create argument list for a hardened container.
func buildCreateArgs(cfg config.DockerSandboxConfig, workspacePath, containerName string) []string {
	return buildCreateArgsWithOwner(cfg, workspacePath, containerName, "", "")
}

func buildCreateArgsWithOwner(cfg config.DockerSandboxConfig, workspacePath, containerName, ownerLabel, ownerToken string) []string {
	uid, gid := configuredContainerIdentity(cfg)
	args := []string{
		"create",
		"--name", containerName,
	}
	if ownerLabel != "" && ownerToken != "" {
		args = append(args, "--label", ownerLabel+"="+ownerToken)
	}
	if cfg.NeverPull {
		args = append(args, "--pull", "never")
	}
	if entrypoint := strings.TrimSpace(cfg.Entrypoint); entrypoint != "" {
		args = append(args, "--entrypoint", entrypoint)
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
		args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,destination=%s", workspacePath, mount))
		if cfg.HideGitMetadata {
			gitPath := filepath.Join(workspacePath, ".git")
			if info, err := os.Lstat(gitPath); err == nil && info.IsDir() {
				args = append(args, "--mount", fmt.Sprintf("type=tmpfs,destination=%s/.git,tmpfs-mode=000", mount))
			} else {
				args = append(args, "--mount", fmt.Sprintf("type=bind,source=/dev/null,destination=%s/.git,readonly", mount))
			}
		}
	}

	// Writable tmpfs for /tmp
	tmpfsSize := cfg.Resources.TmpfsSize
	if tmpfsSize == "" {
		tmpfsSize = "64m"
	}
	args = append(args, "--tmpfs", fmt.Sprintf("/tmp:size=%s", tmpfsSize))
	if cfg.EphemeralHome {
		homeOptions := fmt.Sprintf("/buckley-home:size=%s,mode=1777", tmpfsSize)
		if uid != "" && gid != "" {
			homeOptions = fmt.Sprintf("/buckley-home:size=%s,mode=0700,uid=%s,gid=%s", tmpfsSize, uid, gid)
		}
		args = append(args,
			"--tmpfs", homeOptions,
			"--env", "HOME=/buckley-home",
			"--env", "TMPDIR=/tmp",
			"--env", "TMP=/tmp",
			"--env", "TEMP=/tmp",
		)
	}

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
	if uid != "" && gid != "" {
		args = append(args, "--user", fmt.Sprintf("%s:%s", uid, gid))
	}

	// Image + long-lived entrypoint
	image := cfg.Image
	if image == "" {
		image = "ubuntu:24.04"
	}
	args = append(args, image)
	if strings.TrimSpace(cfg.Entrypoint) != "" {
		args = append(args, "infinity")
	} else {
		args = append(args, "sleep", "infinity")
	}

	return args
}

func configuredContainerIdentity(cfg config.DockerSandboxConfig) (string, string) {
	if cfg.ContainerUser != "" {
		uid, gid, ok := strings.Cut(cfg.ContainerUser, ":")
		if ok {
			return uid, gid
		}
		return "", ""
	}
	current, err := user.Current()
	if err != nil {
		return "", ""
	}
	return current.Uid, current.Gid
}
