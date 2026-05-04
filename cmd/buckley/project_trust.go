package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
)

const projectTrustFileName = "project-trust.json"

type projectTrustStatus string

const (
	projectTrustUnknown    projectTrustStatus = ""
	projectTrustTrusted    projectTrustStatus = "trusted"
	projectTrustRestricted projectTrustStatus = "restricted"
)

type projectTrustRecord struct {
	Path   string             `json:"path"`
	Status projectTrustStatus `json:"status"`
}

type projectTrustFile struct {
	Version  int                  `json:"version"`
	Projects []projectTrustRecord `json:"projects"`
}

type projectTrustStore struct {
	path     string
	statuses map[string]projectTrustStatus
}

var promptProjectTrustFn = promptProjectTrust

func (s projectTrustStatus) String() string {
	if s == projectTrustUnknown {
		return "unknown"
	}
	return string(s)
}

func resolveProjectTrustPath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(envBuckleyDataDir)); dir != "" {
		expanded, err := expandHomePath(dir)
		if err != nil {
			return "", err
		}
		return filepath.Join(expanded, projectTrustFileName), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".buckley", projectTrustFileName), nil
}

func loadProjectTrustStore(path string) (*projectTrustStore, error) {
	store := &projectTrustStore{
		path:     path,
		statuses: make(map[string]projectTrustStatus),
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, fmt.Errorf("read project trust store: %w", err)
	}

	var raw projectTrustFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse project trust store: %w", err)
	}

	for _, project := range raw.Projects {
		root := normalizeProjectTrustPath(project.Path)
		if root == "" {
			continue
		}
		switch project.Status {
		case projectTrustTrusted, projectTrustRestricted:
			store.statuses[root] = project.Status
		}
	}

	return store, nil
}

func (s *projectTrustStore) Status(projectRoot string) projectTrustStatus {
	if s == nil {
		return projectTrustUnknown
	}
	root := normalizeProjectTrustPath(projectRoot)
	if root == "" {
		return projectTrustUnknown
	}
	if status, ok := s.statuses[root]; ok {
		return status
	}
	return projectTrustUnknown
}

func (s *projectTrustStore) Set(projectRoot string, status projectTrustStatus) error {
	if s == nil {
		return nil
	}
	root := normalizeProjectTrustPath(projectRoot)
	if root == "" {
		return fmt.Errorf("project path cannot be empty")
	}
	if status != projectTrustTrusted && status != projectTrustRestricted {
		return fmt.Errorf("invalid project trust status: %s", status)
	}
	if s.statuses == nil {
		s.statuses = make(map[string]projectTrustStatus)
	}
	s.statuses[root] = status
	return s.save()
}

func (s *projectTrustStore) Reset(projectRoot string) error {
	if s == nil {
		return nil
	}
	root := normalizeProjectTrustPath(projectRoot)
	if root == "" {
		return fmt.Errorf("project path cannot be empty")
	}
	delete(s.statuses, root)
	return s.save()
}

func (s *projectTrustStore) save() error {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(s.path) == "" {
		return fmt.Errorf("project trust store path is empty")
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create project trust dir: %w", err)
	}

	projects := make([]projectTrustRecord, 0, len(s.statuses))
	for path, status := range s.statuses {
		if status != projectTrustTrusted && status != projectTrustRestricted {
			continue
		}
		projects = append(projects, projectTrustRecord{
			Path:   path,
			Status: status,
		})
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Path < projects[j].Path
	})

	payload, err := json.MarshalIndent(projectTrustFile{
		Version:  1,
		Projects: projects,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal project trust store: %w", err)
	}
	payload = append(payload, '\n')

	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, payload, 0o644); err != nil {
		return fmt.Errorf("write project trust store: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		if writeErr := os.WriteFile(s.path, payload, 0o644); writeErr != nil {
			return fmt.Errorf("replace project trust store: %w", err)
		}
		_ = os.Remove(tmpPath)
	}
	return nil
}

func normalizeProjectTrustPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	absPath = filepath.Clean(absPath)

	if repoRoot, ok := findGitRepoRoot(absPath); ok {
		if resolved, err := filepath.Abs(repoRoot); err == nil {
			absPath = filepath.Clean(resolved)
		}
	}

	return absPath
}

func projectTrustStatusForPath(path string) (projectTrustStatus, string, string, error) {
	if strings.TrimSpace(path) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return projectTrustUnknown, "", "", fmt.Errorf("get working directory: %w", err)
		}
		path = cwd
	}

	root := normalizeProjectTrustPath(path)
	if root == "" {
		return projectTrustUnknown, "", "", fmt.Errorf("resolve project root: %s", path)
	}

	storePath, err := resolveProjectTrustPath()
	if err != nil {
		return projectTrustUnknown, root, "", err
	}
	store, err := loadProjectTrustStore(storePath)
	if err != nil {
		return projectTrustUnknown, root, storePath, err
	}
	return store.Status(root), root, storePath, nil
}

func ensureProjectTrust(cfg *config.Config, workDir string) (projectTrustStatus, string, error) {
	status, root, storePath, err := projectTrustStatusForPath(workDir)
	if err != nil {
		return projectTrustUnknown, "", err
	}

	if status == projectTrustUnknown && stdinIsTerminalFn() {
		store, err := loadProjectTrustStore(storePath)
		if err != nil {
			return projectTrustUnknown, root, err
		}
		status, err = promptProjectTrustFn(root)
		if err != nil {
			return projectTrustUnknown, root, err
		}
		if err := store.Set(root, status); err != nil {
			return status, root, fmt.Errorf("save project trust: %w", err)
		}
	}

	if status == projectTrustRestricted {
		applyProjectTrustRestrictions(cfg)
		fmt.Fprintf(os.Stderr, "Project trust restricted: approval=%s trust_level=%s for %s\n", cfg.Approval.Mode, cfg.Orchestrator.TrustLevel, root)
	}

	return status, root, nil
}

func promptProjectTrust(projectRoot string) (projectTrustStatus, error) {
	fmt.Println("Project trust check")
	fmt.Printf("  Path: %s\n", projectRoot)
	fmt.Println("  Trusted projects keep your configured autonomy.")
	fmt.Println("  Restricted projects are capped to approval=safe and trust_level=conservative.")
	fmt.Print("Trust this project? [y/N]: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return projectTrustUnknown, fmt.Errorf("read trust prompt: %w", err)
	}

	switch strings.ToLower(strings.TrimSpace(input)) {
	case "y", "yes":
		fmt.Println("Project marked trusted.")
		return projectTrustTrusted, nil
	default:
		fmt.Println("Project marked restricted.")
		return projectTrustRestricted, nil
	}
}

func applyProjectTrustRestrictions(cfg *config.Config) {
	if cfg == nil {
		return
	}
	cfg.Approval.Mode = clampProjectApprovalMode(cfg.Approval.Mode)
	cfg.Orchestrator.TrustLevel = clampProjectTrustLevel(cfg.Orchestrator.TrustLevel)
	cfg.Approval.AllowNetwork = false
	cfg.Sandbox.AllowNetwork = false
}

func clampProjectApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "ask", "explicit", "manual":
		return "ask"
	case "safe", "readonly", "read-only", "":
		return "safe"
	case "auto", "automatic", "workspace", "yolo", "full", "dangerous":
		return "safe"
	default:
		return config.DefaultApprovalMode
	}
}

func clampProjectTrustLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "conservative", "":
		return "conservative"
	case "balanced", "autonomous":
		return "conservative"
	default:
		return "conservative"
	}
}

func runTrustCommand(args []string) error {
	subCmd := "status"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		subCmd = strings.ToLower(strings.TrimSpace(args[0]))
	}

	var target string
	if len(args) > 1 {
		target = args[1]
	}

	status, root, storePath, err := projectTrustStatusForPath(target)
	if err != nil {
		return err
	}
	store, err := loadProjectTrustStore(storePath)
	if err != nil {
		return err
	}

	switch subCmd {
	case "status":
		fmt.Printf("Project: %s\n", root)
		fmt.Printf("Status:  %s\n", status)
		fmt.Printf("Store:   %s\n", storePath)
	case "allow", "trust":
		if err := store.Set(root, projectTrustTrusted); err != nil {
			return err
		}
		fmt.Printf("Marked trusted: %s\n", root)
	case "deny", "restrict":
		if err := store.Set(root, projectTrustRestricted); err != nil {
			return err
		}
		fmt.Printf("Marked restricted: %s\n", root)
	case "reset", "forget", "rm":
		if err := store.Reset(root); err != nil {
			return err
		}
		fmt.Printf("Cleared trust decision: %s\n", root)
	default:
		return fmt.Errorf("unknown trust command: %s (use status, allow, deny, or reset)", subCmd)
	}

	return nil
}
