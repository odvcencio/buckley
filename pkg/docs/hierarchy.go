package docs

import (
	"fmt"
	"os"
	"path/filepath"
)

// HierarchyManager creates and maintains the opinionated documentation structure
type HierarchyManager struct {
	rootDir string // Root documentation directory (typically "docs")
}

// NewHierarchyManager creates a new hierarchy manager
func NewHierarchyManager(rootDir string) *HierarchyManager {
	return &HierarchyManager{
		rootDir: rootDir,
	}
}

// Initialize creates the complete documentation hierarchy if it doesn't exist
// This is called on first run or when docs/ is missing
func (h *HierarchyManager) Initialize() error {
	// Create directory structure
	dirs := []string{
		h.rootDir,
		filepath.Join(h.rootDir, "architecture"),
		filepath.Join(h.rootDir, "architecture", "decisions"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Create README.md if it doesn't exist
	readmePath := filepath.Join(h.rootDir, "README.md")
	if !fileExists(readmePath) {
		if err := h.createRootReadme(readmePath); err != nil {
			return err
		}
	}

	// Create architecture/overview.md if it doesn't exist
	overviewPath := filepath.Join(h.rootDir, "architecture", "overview.md")
	if !fileExists(overviewPath) {
		if err := h.createArchitectureOverview(overviewPath); err != nil {
			return err
		}
	}

	// Create architecture/decisions/README.md if it doesn't exist
	adrReadmePath := filepath.Join(h.rootDir, "architecture", "decisions", "README.md")
	if !fileExists(adrReadmePath) {
		if err := h.createADRReadme(adrReadmePath); err != nil {
			return err
		}
	}

	return nil
}

// createRootReadme creates the main docs/README.md
func (h *HierarchyManager) createRootReadme(path string) error {
	content := `# Documentation

Buckley documentation for users and contributors.

## Contents

- CLI.md - Command-line interface reference
- CONFIGURATION.md - Configuration options and hierarchy
- architecture/decisions/ - Architecture Decision Records (ADRs)

## Architecture

See architecture/overview.md for system architecture.
`
	return os.WriteFile(path, []byte(content), 0644)
}

// createArchitectureOverview creates the architecture/overview.md
func (h *HierarchyManager) createArchitectureOverview(path string) error {
	content := `# Architecture Overview

Buckley follows Domain-Driven Design with Clean Architecture principles.

See also:
- ADRs in docs/architecture/decisions/
`
	return os.WriteFile(path, []byte(content), 0644)
}

// createADRReadme creates the architecture/decisions/README.md
func (h *HierarchyManager) createADRReadme(path string) error {
	content := `# Architecture Decision Records

ADRs capture important architectural decisions with context and consequences.

## Format

- Status: Proposed | Accepted | Deprecated | Superseded
- Context, Decision, Consequences
- Optional Supersedes/Superseded By links

## Index

(ADRs will be listed here as they are added)

---
`
	return os.WriteFile(path, []byte(content), 0644)
}

// Exists checks if the documentation hierarchy exists
func (h *HierarchyManager) Exists() bool {
	// Check if key directories exist
	requiredDirs := []string{
		filepath.Join(h.rootDir, "architecture"),
	}

	for _, dir := range requiredDirs {
		if !dirExists(dir) {
			return false
		}
	}

	return true
}

// ValidateStructure checks if the documentation structure is intact
func (h *HierarchyManager) ValidateStructure() error {
	if !h.Exists() {
		return fmt.Errorf("documentation hierarchy is incomplete")
	}

	// Check for README files
	requiredFiles := []string{
		filepath.Join(h.rootDir, "README.md"),
		filepath.Join(h.rootDir, "architecture", "overview.md"),
	}

	for _, file := range requiredFiles {
		if !fileExists(file) {
			return fmt.Errorf("required file missing: %s", file)
		}
	}

	return nil
}

// Helper functions

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
