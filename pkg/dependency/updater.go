package dependency

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// UpdateResult represents the result of a dependency update
type UpdateResult struct {
	Module      string
	OldVersion  string
	NewVersion  string
	Success     bool
	TestsPassed bool
	Error       error
}

// Updater manages dependency updates
type Updater struct {
	rootPath   string
	runTests   bool
	autoCommit bool

	// Allow stubbing in tests
	commandRunner func(cmd *exec.Cmd) error
}

// NewUpdater creates a new dependency updater
func NewUpdater(rootPath string) *Updater {
	return &Updater{
		rootPath:   rootPath,
		runTests:   true,
		autoCommit: false,
		commandRunner: func(cmd *exec.Cmd) error {
			return cmd.Run()
		},
	}
}

// SetRunTests enables or disables running tests after updates
func (u *Updater) SetRunTests(run bool) {
	u.runTests = run
}

// SetAutoCommit enables or disables automatic git commits
func (u *Updater) SetAutoCommit(auto bool) {
	u.autoCommit = auto
}

// UpdateModule updates a specific module to the latest version
func (u *Updater) UpdateModule(modulePath string) (*UpdateResult, error) {
	result := &UpdateResult{
		Module: modulePath,
	}

	// Get current version
	currentVersion, err := u.getCurrentVersion(modulePath)
	if err != nil {
		result.Error = fmt.Errorf("failed to get current version: %w", err)
		return result, result.Error
	}
	result.OldVersion = currentVersion

	// Update to latest
	cmd := exec.Command("go", "get", "-u", modulePath)
	cmd.Dir = u.rootPath

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := u.commandRunner(cmd); err != nil {
		result.Error = fmt.Errorf("go get failed: %w: %s", err, stderr.String())
		return result, result.Error
	}

	// Tidy up
	tidyCmd := exec.Command("go", "mod", "tidy")
	tidyCmd.Dir = u.rootPath
	if err := u.commandRunner(tidyCmd); err != nil {
		result.Error = fmt.Errorf("go mod tidy failed: %w", err)
		return result, result.Error
	}

	// Get new version
	newVersion, err := u.getCurrentVersion(modulePath)
	if err != nil {
		result.Error = fmt.Errorf("failed to get new version: %w", err)
		return result, result.Error
	}
	result.NewVersion = newVersion
	result.Success = true

	// Run tests if enabled
	if u.runTests {
		result.TestsPassed = u.runTestSuite()
		if !result.TestsPassed {
			result.Success = false
			result.Error = fmt.Errorf("tests failed after update")
			return result, result.Error
		}
	}

	// Auto-commit if enabled
	if u.autoCommit && result.Success && result.TestsPassed {
		commitMsg := fmt.Sprintf("chore(deps): update %s from %s to %s", modulePath, currentVersion, newVersion)
		if err := u.createCommit(commitMsg); err != nil {
			result.Error = fmt.Errorf("failed to commit: %w", err)
		}
	}

	return result, nil
}

// UpdateAll updates all outdated dependencies
func (u *Updater) UpdateAll() ([]UpdateResult, error) {
	outdated, err := u.GetOutdated()
	if err != nil {
		return nil, fmt.Errorf("failed to get outdated modules: %w", err)
	}

	results := make([]UpdateResult, 0, len(outdated))

	for _, module := range outdated {
		result, err := u.UpdateModule(module.Path)
		if err != nil {
			// Continue with other updates even if one fails
			results = append(results, *result)
			continue
		}
		results = append(results, *result)
	}

	return results, nil
}

// GetOutdated returns a list of outdated modules
func (u *Updater) GetOutdated() ([]OutdatedModule, error) {
	// Check if go.mod exists
	goModPath := filepath.Join(u.rootPath, "go.mod")
	if _, err := os.Stat(goModPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("go.mod not found: %w", err)
	}

	cmd := exec.Command("go", "list", "-u", "-m", "-json", "all")
	cmd.Dir = u.rootPath

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list failed: %w", err)
	}

	var outdated []OutdatedModule
	decoder := json.NewDecoder(&stdout)

	for decoder.More() {
		var module struct {
			Path    string `json:"Path"`
			Version string `json:"Version"`
			Update  *struct {
				Version string `json:"Version"`
			} `json:"Update"`
			Indirect bool `json:"Indirect"`
		}

		if err := decoder.Decode(&module); err != nil {
			continue
		}

		if module.Update != nil && module.Update.Version != "" {
			outdated = append(outdated, OutdatedModule{
				Path:       module.Path,
				Current:    module.Version,
				Latest:     module.Update.Version,
				IsIndirect: module.Indirect,
			})
		}
	}

	return outdated, nil
}

// getCurrentVersion gets the current version of a module
func (u *Updater) getCurrentVersion(modulePath string) (string, error) {
	cmd := exec.Command("go", "list", "-m", "-json", modulePath)
	cmd.Dir = u.rootPath

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", err
	}

	var module struct {
		Version string `json:"Version"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &module); err != nil {
		return "", err
	}

	return module.Version, nil
}

// runTestSuite runs the test suite
func (u *Updater) runTestSuite() bool {
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = u.rootPath

	// Suppress output, just check exit code
	return u.commandRunner(cmd) == nil
}

// createCommit creates a git commit with the changes
func (u *Updater) createCommit(message string) error {
	// Add go.mod and go.sum
	addCmd := exec.Command("git", "add", "go.mod", "go.sum")
	addCmd.Dir = u.rootPath
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	// Commit
	commitCmd := exec.Command("git", "commit", "-m", message)
	commitCmd.Dir = u.rootPath
	if err := commitCmd.Run(); err != nil {
		return fmt.Errorf("git commit failed: %w", err)
	}

	return nil
}

// CheckSecurity checks for security vulnerabilities in dependencies
func (u *Updater) CheckSecurity() ([]SecurityVulnerability, error) {
	// Run govulncheck if available
	if _, err := exec.LookPath("govulncheck"); err != nil {
		return nil, fmt.Errorf("govulncheck not found in PATH")
	}

	cmd := exec.Command("govulncheck", "-json", "./...")
	cmd.Dir = u.rootPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// govulncheck exits with non-zero if vulns found, so we don't check error
	_ = cmd.Run()

	// Parse JSON output
	var vulns []SecurityVulnerability
	decoder := json.NewDecoder(&stdout)

	for decoder.More() {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			continue
		}

		// Parse vulnerability findings
		if entry["finding"] != nil {
			finding := entry["finding"].(map[string]any)

			vuln := SecurityVulnerability{
				ID:          getString(finding, "osv"),
				Package:     getString(finding, "package"),
				Version:     getString(finding, "version"),
				FixedIn:     getString(finding, "fixed_version"),
				Description: getString(finding, "description"),
				Severity:    getString(finding, "severity"),
			}

			vulns = append(vulns, vuln)
		}
	}

	return vulns, nil
}

// getString safely extracts a string from a map
func getString(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// OutdatedModule represents an outdated dependency
type OutdatedModule struct {
	Path       string
	Current    string
	Latest     string
	IsIndirect bool
}

// SecurityVulnerability represents a security issue
type SecurityVulnerability struct {
	ID          string
	Package     string
	Version     string
	FixedIn     string
	Description string
	Severity    string
}

// String returns a human-readable representation
func (v SecurityVulnerability) String() string {
	severity := v.Severity
	if severity == "" {
		severity = "UNKNOWN"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%s] %s\n", severity, v.ID))
	sb.WriteString(fmt.Sprintf("  Package: %s@%s\n", v.Package, v.Version))
	if v.FixedIn != "" {
		sb.WriteString(fmt.Sprintf("  Fixed in: %s\n", v.FixedIn))
	}
	if v.Description != "" {
		sb.WriteString(fmt.Sprintf("  %s\n", v.Description))
	}

	return sb.String()
}
