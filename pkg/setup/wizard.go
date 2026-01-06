package setup

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// Dependency represents a setup prerequisite
type Dependency struct {
	Name        string
	Type        string
	CheckFunc   func() bool
	InstallFunc func() error
	Prompt      string
	DocsLink    string
}

// Checker validates that required dependencies are present
type Checker struct {
	required []Dependency
}

// NewChecker constructs a dependency checker with default requirements
func NewChecker() *Checker {
	return &Checker{
		required: []Dependency{
			openRouterDependency(),
			gitDependency(),
		},
	}
}

// CheckAll returns the dependencies that are currently missing
func (c *Checker) CheckAll() ([]Dependency, error) {
	missing := []Dependency{}
	for _, dep := range c.required {
		if dep.CheckFunc == nil {
			continue
		}
		if !dep.CheckFunc() {
			missing = append(missing, dep)
		}
	}
	return missing, nil
}

// RunWizard guides the user through installing missing dependencies
func (c *Checker) RunWizard(missing []Dependency) error {
	if len(missing) == 0 {
		return nil
	}

	fmt.Println("🔧 Buckley Setup Wizard")
	fmt.Println("Some required dependencies are missing.")

	reader := bufio.NewReader(os.Stdin)

	for i, dep := range missing {
		fmt.Printf("[%d/%d] %s (%s)\n", i+1, len(missing), dep.Name, dep.Type)
		if dep.Prompt != "" {
			fmt.Println(dep.Prompt)
		}
		if dep.DocsLink != "" {
			fmt.Printf("Docs: %s\n", dep.DocsLink)
		}

		if dep.InstallFunc == nil {
			fmt.Println("Please install manually and re-run Buckley.")
			continue
		}

		fmt.Print("\nSet this up now? [Y/n]: ")
		choice, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(choice)) == "n" {
			fmt.Println("Skipping. Buckley cannot continue until this is resolved.")
			continue
		}

		if err := dep.InstallFunc(); err != nil {
			return fmt.Errorf("%s setup failed: %w", dep.Name, err)
		}

		fmt.Println("✓ Done!")
	}

	return nil
}

func openRouterDependency() Dependency {
	return Dependency{
		Name: "OpenRouter API Key",
		Type: "env_var",
		CheckFunc: func() bool {
			// Check environment variable first
			if strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) != "" {
				return true
			}
			// Check config.env file as fallback
			return checkConfigEnvFile() != ""
		},
		InstallFunc: func() error {
			fmt.Println("1. Visit https://openrouter.ai/keys")
			fmt.Println("2. Create a new API key")
			fmt.Print("\nPaste your API key: ")

			apiKey, err := readSecretInput()
			if err != nil {
				return err
			}
			apiKey = strings.TrimSpace(apiKey)
			if apiKey == "" {
				return errors.New("API key cannot be empty")
			}

			if err := validateAPIKey(apiKey); err != nil {
				return err
			}

			fmt.Print("Save key for future runs? [Y/n]: ")
			if confirmDefaultYes() {
				if err := persistAPIKey(apiKey); err != nil {
					return err
				}
				fmt.Println("Saved to ~/.buckley/config.env. Add 'source ~/.buckley/config.env' to your shell profile.")
			}

			// Make key available for current process
			_ = os.Setenv("OPENROUTER_API_KEY", apiKey)
			return nil
		},
		Prompt:   "Buckley needs your OpenRouter API key to talk to models.",
		DocsLink: "https://openrouter.ai/keys",
	}
}

func gitDependency() Dependency {
	return Dependency{
		Name: "Git",
		Type: "binary",
		CheckFunc: func() bool {
			_, err := exec.LookPath("git")
			return err == nil
		},
		Prompt:   "Install Git so Buckley can manage repositories and worktrees.",
		DocsLink: "https://git-scm.com/downloads",
	}
}

func readSecretInput() (string, error) {
	bytePassword, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(bytePassword), nil
}

func confirmDefaultYes() bool {
	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "" || response == "y" || response == "yes"
}

func validateAPIKey(key string) error {
	if len(key) < 8 {
		return errors.New("API key looks too short")
	}
	// Full validation will occur during client initialization.
	return nil
}

func persistAPIKey(apiKey string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to locate home directory: %w", err)
	}

	dir := filepath.Join(home, ".buckley")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}

	envPath := filepath.Join(dir, "config.env")
	line := fmt.Sprintf("export OPENROUTER_API_KEY=\"%s\"\n", apiKey)
	if err := os.WriteFile(envPath, []byte(line), 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", envPath, err)
	}

	return nil
}

// checkConfigEnvFile reads ~/.buckley/config.env and extracts OPENROUTER_API_KEY
func checkConfigEnvFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	envPath := filepath.Join(home, ".buckley", "config.env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		return ""
	}

	// Parse simple KEY=value format (with optional 'export' prefix)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip comments
		if strings.HasPrefix(line, "#") {
			continue
		}
		// Strip optional 'export ' prefix
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "OPENROUTER_API_KEY=") {
			apiKey := strings.TrimPrefix(line, "OPENROUTER_API_KEY=")
			apiKey = strings.Trim(apiKey, "\"'") // Remove quotes if present
			return apiKey
		}
	}

	return ""
}
