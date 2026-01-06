package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/containerexec"
	"github.com/odvcencio/buckley/pkg/containers"
	"github.com/odvcencio/buckley/pkg/envdetect"
)

// CreateWithContainers creates a worktree and sets up containers
func (wm *Manager) CreateWithContainers(branchName string) (*Worktree, error) {
	// Create the worktree first
	wt, err := wm.Create(branchName)
	if err != nil {
		return nil, err
	}

	// Detect environment and generate compose file
	if err := wm.setupContainers(wt.Path); err != nil {
		// Clean up worktree if container setup fails
		wm.Remove(branchName, true)
		return nil, fmt.Errorf("failed to setup containers: %w", err)
	}

	return wt, nil
}

// setupContainers detects the environment and sets up containers for a worktree
func (wm *Manager) setupContainers(wtPath string) error {
	// Detect environment
	detector := envdetect.NewDetector(wtPath)
	profile, err := detector.Detect()
	if err != nil {
		return fmt.Errorf("failed to detect environment: %w", err)
	}

	// Skip if no languages detected
	if len(profile.Languages) == 0 && len(profile.Services) == 0 {
		return nil
	}

	// Generate compose file
	generator, err := containers.NewGenerator()
	if err != nil {
		return fmt.Errorf("failed to create generator: %w", err)
	}

	composePath := filepath.Join(wtPath, "docker-compose.worktree.yml")
	if err := generator.Generate(profile, composePath); err != nil {
		return fmt.Errorf("failed to generate compose file: %w", err)
	}

	// Generate secrets/env file
	secretsMgr := containers.NewSecretsManager(wtPath)
	if err := secretsMgr.GenerateEnvFile(); err != nil {
		// Non-fatal - just warn
		fmt.Printf("Warning: failed to generate env file: %v\n", err)
	}

	// Start containers
	if err := wm.startContainers(composePath); err != nil {
		return fmt.Errorf("failed to start containers: %w", err)
	}

	return nil
}

// setupContainersWithSpec provisions containers using an explicit spec.
func (wm *Manager) setupContainersWithSpec(wtPath string, spec *ContainerSpec) error {
	if spec == nil {
		return wm.setupContainers(wtPath)
	}

	switch strings.ToLower(spec.Driver) {
	case "", "compose":
		composePath, err := wm.prepareComposeFile(wtPath, spec)
		if err != nil {
			return err
		}
		return wm.startContainers(composePath)
	case "docker":
		composePath, err := wm.generateComposeFromSpec(wtPath, spec)
		if err != nil {
			return err
		}
		return wm.startContainers(composePath)
	default:
		return fmt.Errorf("unsupported container driver: %s", spec.Driver)
	}
}

// startContainers starts the containers using docker compose
func (wm *Manager) startContainers(composePath string) error {
	cmd := exec.Command("docker", "compose", "-f", composePath, "up", "-d", "--wait")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose failed: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// StopContainers stops containers for a worktree
func (wm *Manager) StopContainers(branchName string) error {
	wtPath := wm.getWorktreePath(branchName)
	composePath := filepath.Join(wtPath, "docker-compose.worktree.yml")

	cmd := exec.Command("docker", "compose", "-f", composePath, "down")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to stop containers: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// RemoveWithContainers removes a worktree and cleans up containers
func (wm *Manager) RemoveWithContainers(branchName string, deleteBranch bool, removeVolumes bool) error {
	wtPath := wm.getWorktreePath(branchName)
	composePath := filepath.Join(wtPath, "docker-compose.worktree.yml")

	// Stop and remove containers
	args := []string{"compose", "-f", composePath, "down"}
	if removeVolumes {
		args = append(args, "-v") // Remove volumes
	}

	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Continue even if docker cleanup fails
		fmt.Printf("Warning: failed to cleanup containers: %v\nOutput: %s\n", err, string(output))
	}

	// Remove the worktree
	return wm.Remove(branchName, deleteBranch)
}

// ContainerStatus is deprecated; use containerexec.ServiceStatus.
type ContainerStatus = containerexec.ServiceStatus

// GetContainerStatus returns the status of containers for a worktree.
func (wm *Manager) GetContainerStatus(branchName string) ([]containerexec.ServiceStatus, error) {
	wtPath := wm.getWorktreePath(branchName)
	composePath := filepath.Join(wtPath, "docker-compose.worktree.yml")
	return containerexec.GetStatus(composePath)
}

// RestartContainers restarts containers for a worktree
func (wm *Manager) RestartContainers(branchName string) error {
	wtPath := wm.getWorktreePath(branchName)
	composePath := filepath.Join(wtPath, "docker-compose.worktree.yml")

	cmd := exec.Command("docker", "compose", "-f", composePath, "restart")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to restart containers: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// GetContainerLogs retrieves logs for a specific service
func (wm *Manager) GetContainerLogs(branchName string, serviceName string, follow bool) error {
	wtPath := wm.getWorktreePath(branchName)
	composePath := filepath.Join(wtPath, "docker-compose.worktree.yml")

	args := []string{"compose", "-f", composePath, "logs"}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, serviceName)

	cmd := exec.Command("docker", args...)
	cmd.Stdout = nil // Caller should set this
	cmd.Stderr = nil // Caller should set this

	return cmd.Run()
}

func (wm *Manager) prepareComposeFile(wtPath string, spec *ContainerSpec) (string, error) {
	if spec.ComposeFile == "" {
		return wm.generateComposeFromSpec(wtPath, spec)
	}

	source := spec.ComposeFile
	if !filepath.IsAbs(source) {
		source = filepath.Join(wm.repoPath, source)
	}

	data, err := os.ReadFile(source)
	if err != nil {
		return "", fmt.Errorf("read compose file %s: %w", source, err)
	}

	rendered := wm.renderComposeTemplate(string(data), wtPath, spec)
	target := filepath.Join(wtPath, "docker-compose.worktree.yml")
	if err := os.WriteFile(target, []byte(rendered), 0644); err != nil {
		return "", fmt.Errorf("write compose file: %w", err)
	}
	return target, nil
}

func (wm *Manager) generateComposeFromSpec(wtPath string, spec *ContainerSpec) (string, error) {
	if spec.BaseImage == "" {
		return "", fmt.Errorf("container spec missing base_image")
	}

	mounts := []string{}
	if spec.workspaceMountEnabled() {
		mounts = append(mounts, fmt.Sprintf("%s:%s", filepath.ToSlash(wtPath), spec.Workdir))
	}
	for _, mount := range spec.Mounts {
		mounts = append(mounts, mount)
	}

	var sb strings.Builder
	sb.WriteString("version: '3.9'\nservices:\n  dev:\n")
	if spec.Name != "" {
		sb.WriteString("    container_name: ")
		sb.WriteString(spec.Name)
		sb.WriteByte('\n')
	}
	sb.WriteString("    image: ")
	sb.WriteString(spec.BaseImage)
	sb.WriteByte('\n')
	sb.WriteString("    command: /bin/sh -c \"while sleep 3600; do :; done\"\n")
	sb.WriteString("    working_dir: ")
	sb.WriteString(spec.Workdir)
	sb.WriteByte('\n')
	if len(mounts) > 0 {
		sb.WriteString("    volumes:\n")
		for _, mount := range mounts {
			sb.WriteString("      - \"")
			sb.WriteString(mount)
			sb.WriteString("\"\n")
		}
	}
	if len(spec.Env) > 0 {
		sb.WriteString("    environment:\n")
		for key, value := range spec.Env {
			sb.WriteString("      - ")
			sb.WriteString(key)
			sb.WriteString("=")
			sb.WriteString(resolveEnvValue(value))
			sb.WriteByte('\n')
		}
	}
	if len(spec.Ports) > 0 {
		sb.WriteString("    ports:\n")
		for _, port := range spec.Ports {
			sb.WriteString("      - \"")
			sb.WriteString(port)
			sb.WriteString("\"\n")
		}
	}

	target := filepath.Join(wtPath, "docker-compose.worktree.yml")
	if err := os.WriteFile(target, []byte(sb.String()), 0644); err != nil {
		return "", fmt.Errorf("write generated compose file: %w", err)
	}
	return target, nil
}

func (wm *Manager) renderComposeTemplate(template string, wtPath string, spec *ContainerSpec) string {
	replacer := strings.NewReplacer(
		"{{WORKTREE_PATH}}", filepath.ToSlash(wtPath),
		"{{REPO_PATH}}", filepath.ToSlash(wm.repoPath),
		"{{WORKDIR}}", spec.Workdir,
	)
	rendered := replacer.Replace(template)
	for key, value := range spec.Env {
		placeholder := fmt.Sprintf("{{ENV_%s}}", strings.ToUpper(key))
		rendered = strings.ReplaceAll(rendered, placeholder, resolveEnvValue(value))
	}
	return rendered
}

func resolveEnvValue(raw string) string {
	if strings.HasPrefix(raw, "${") && strings.HasSuffix(raw, "}") {
		key := strings.TrimSuffix(strings.TrimPrefix(raw, "${"), "}")
		if val, ok := os.LookupEnv(key); ok {
			return val
		}
		return ""
	}
	return raw
}
