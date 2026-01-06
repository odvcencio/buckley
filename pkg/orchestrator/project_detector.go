package orchestrator

import (
	"os"
	"path/filepath"
)

// ProjectType represents the type of project
type ProjectType string

const (
	ProjectTypeGo         ProjectType = "go"
	ProjectTypeNodeJS     ProjectType = "nodejs"
	ProjectTypeTypeScript ProjectType = "typescript"
	ProjectTypePython     ProjectType = "python"
	ProjectTypeRust       ProjectType = "rust"
	ProjectTypeJava       ProjectType = "java"
	ProjectTypeUnknown    ProjectType = "unknown"
)

// ProjectDetector detects project type based on marker files
type ProjectDetector struct {
	basePath string
}

// NewProjectDetector creates a new project detector
func NewProjectDetector(basePath string) *ProjectDetector {
	if basePath == "" {
		basePath = "."
	}
	return &ProjectDetector{basePath: basePath}
}

// DetectType detects the primary project type
func (pd *ProjectDetector) DetectType() ProjectType {
	// Check for Go project
	if pd.fileExists("go.mod") || pd.fileExists("go.sum") {
		return ProjectTypeGo
	}

	// Check for Rust project
	if pd.fileExists("Cargo.toml") {
		return ProjectTypeRust
	}

	// Check for TypeScript/JavaScript projects
	if pd.fileExists("package.json") {
		// Check if it's TypeScript
		if pd.fileExists("tsconfig.json") {
			return ProjectTypeTypeScript
		}
		return ProjectTypeNodeJS
	}

	// Check for Python project
	if pd.fileExists("requirements.txt") || pd.fileExists("setup.py") ||
		pd.fileExists("pyproject.toml") || pd.fileExists("Pipfile") {
		return ProjectTypePython
	}

	// Check for Java project
	if pd.fileExists("pom.xml") || pd.fileExists("build.gradle") ||
		pd.fileExists("build.gradle.kts") {
		return ProjectTypeJava
	}

	return ProjectTypeUnknown
}

// fileExists checks if a file exists relative to basePath
func (pd *ProjectDetector) fileExists(filename string) bool {
	path := filepath.Join(pd.basePath, filename)
	_, err := os.Stat(path)
	return err == nil
}

// HasTestFramework checks if a test framework is available
func (pd *ProjectDetector) HasTestFramework() bool {
	projectType := pd.DetectType()

	switch projectType {
	case ProjectTypeGo:
		// Go has built-in testing
		return pd.fileExists("go.mod")
	case ProjectTypeRust:
		// Rust has built-in testing
		return pd.fileExists("Cargo.toml")
	case ProjectTypeNodeJS, ProjectTypeTypeScript:
		// Check for common test frameworks
		return pd.fileExists("package.json")
	case ProjectTypePython:
		// Check for pytest or unittest
		return pd.fileExists("tests") || pd.fileExists("test")
	case ProjectTypeJava:
		// Check for Maven/Gradle
		return pd.fileExists("pom.xml") || pd.fileExists("build.gradle")
	default:
		return false
	}
}

// GetTestCommand returns the appropriate test command
func (pd *ProjectDetector) GetTestCommand() string {
	projectType := pd.DetectType()

	switch projectType {
	case ProjectTypeGo:
		return "go test ./..."
	case ProjectTypeRust:
		return "cargo test"
	case ProjectTypeNodeJS, ProjectTypeTypeScript:
		return "npm test"
	case ProjectTypePython:
		if pd.fileExists("pytest.ini") || pd.fileExists("pyproject.toml") {
			return "pytest"
		}
		return "python -m unittest discover"
	case ProjectTypeJava:
		if pd.fileExists("pom.xml") {
			return "mvn test"
		}
		return "gradle test"
	default:
		return ""
	}
}

// GetBuildCommand returns the appropriate build command
func (pd *ProjectDetector) GetBuildCommand() string {
	projectType := pd.DetectType()

	switch projectType {
	case ProjectTypeGo:
		return "go build ./..."
	case ProjectTypeRust:
		return "cargo build"
	case ProjectTypeNodeJS, ProjectTypeTypeScript:
		return "npm run build"
	case ProjectTypePython:
		return "" // Python doesn't typically have a build step
	case ProjectTypeJava:
		if pd.fileExists("pom.xml") {
			return "mvn compile"
		}
		return "gradle build"
	default:
		return ""
	}
}

// GetLinterCommand returns the appropriate linter command
func (pd *ProjectDetector) GetLinterCommand() string {
	projectType := pd.DetectType()

	switch projectType {
	case ProjectTypeGo:
		// Check for golangci-lint config
		if pd.fileExists(".golangci.yml") || pd.fileExists(".golangci.yaml") {
			return "golangci-lint run"
		}
		return "go vet ./..."
	case ProjectTypeRust:
		return "cargo clippy"
	case ProjectTypeNodeJS, ProjectTypeTypeScript:
		if pd.fileExists(".eslintrc") || pd.fileExists(".eslintrc.json") ||
			pd.fileExists("eslint.config.js") {
			return "eslint ."
		}
		return ""
	case ProjectTypePython:
		if pd.fileExists(".flake8") || pd.fileExists("setup.cfg") {
			return "flake8"
		}
		return "pylint"
	case ProjectTypeJava:
		return "" // Java doesn't have a standard linter
	default:
		return ""
	}
}

// GetFormatterCommand returns the appropriate formatter command
func (pd *ProjectDetector) GetFormatterCommand() string {
	projectType := pd.DetectType()

	switch projectType {
	case ProjectTypeGo:
		return "gofmt -w ."
	case ProjectTypeRust:
		return "cargo fmt"
	case ProjectTypeNodeJS, ProjectTypeTypeScript:
		if pd.fileExists(".prettierrc") || pd.fileExists("prettier.config.js") {
			return "prettier --write ."
		}
		return ""
	case ProjectTypePython:
		if pd.fileExists("pyproject.toml") {
			return "black ."
		}
		return ""
	case ProjectTypeJava:
		return "" // Java doesn't have a standard formatter
	default:
		return ""
	}
}
