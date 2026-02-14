package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/odvcencio/buckley/pkg/ralph"
	"gopkg.in/yaml.v3"
)

func runRalphControl(args []string) error {
	fs := flag.NewFlagSet("ralph control", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: buckley ralph control [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Manage Ralph control file settings.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}

	pause := fs.Bool("pause", false, "Pause Ralph execution")
	resume := fs.Bool("resume", false, "Resume Ralph execution")
	status := fs.Bool("status", false, "Show current control file status")
	nextBackend := fs.String("next-backend", "", "Switch to specified backend")
	set := fs.String("set", "", "Set config value (KEY=VALUE, supports dot notation)")
	controlFile := fs.String("control-file", "ralph-control.yaml", "Path to control file")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	// Count mutually exclusive options
	optCount := 0
	if *pause {
		optCount++
	}
	if *resume {
		optCount++
	}
	if *status {
		optCount++
	}
	if *nextBackend != "" {
		optCount++
	}
	if *set != "" {
		optCount++
	}

	if optCount == 0 {
		fs.Usage()
		return fmt.Errorf("one of --pause, --resume, --status, --next-backend, or --set is required")
	}
	if optCount > 1 {
		return fmt.Errorf("only one of --pause, --resume, --status, --next-backend, or --set can be specified")
	}

	// Handle --status separately as it doesn't need to write
	if *status {
		return showControlStatus(*controlFile)
	}

	// Load or create control config
	cfg, err := loadOrCreateControlConfig(*controlFile)
	if err != nil {
		return err
	}

	// Apply the requested change
	switch {
	case *pause:
		cfg.Override.Paused = true
		fmt.Println("Ralph execution paused")
	case *resume:
		cfg.Override.Paused = false
		fmt.Println("Ralph execution resumed")
	case *nextBackend != "":
		cfg.Override.NextAction = *nextBackend
		fmt.Printf("Next backend set to: %s\n", *nextBackend)
	case *set != "":
		if err := setControlConfigValue(cfg, *set); err != nil {
			return fmt.Errorf("setting config value: %w", err)
		}
		fmt.Printf("Config updated: %s\n", *set)
	}

	// Write back to file
	return saveControlConfig(*controlFile, cfg)
}

// loadOrCreateControlConfig loads an existing control config or creates a default one.
func loadOrCreateControlConfig(path string) (*ralph.ControlConfig, error) {
	// Check if file exists first
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return defaultControlConfig(), nil
	}

	cfg, err := ralph.LoadControlConfig(path)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// defaultControlConfig returns a default control configuration.
func defaultControlConfig() *ralph.ControlConfig {
	return &ralph.ControlConfig{
		Backends: map[string]ralph.BackendConfig{
			"buckley": {
				Type:    "internal",
				Enabled: true,
			},
		},
		Mode: ralph.ModeSequential,
		Override: ralph.OverrideConfig{
			Paused: false,
		},
	}
}

// saveControlConfig writes the control config to a file.
func saveControlConfig(path string, cfg *ralph.ControlConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling control config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing control config: %w", err)
	}
	return nil
}

// showControlStatus displays the current state of the control file.
func showControlStatus(path string) error {
	cfg, err := loadOrCreateControlConfig(path)
	if err != nil {
		return err
	}

	exists := true
	if _, err := os.Stat(path); os.IsNotExist(err) {
		exists = false
	}

	fmt.Println("Ralph Control Status")
	if exists {
		fmt.Printf("  Control file: %s\n", path)
	} else {
		fmt.Printf("  Control file: %s (not created, showing defaults)\n", path)
	}
	fmt.Printf("  Mode: %s\n", cfg.Mode)
	fmt.Printf("  Paused: %t\n", cfg.Override.Paused)

	if cfg.Override.NextAction != "" {
		fmt.Printf("  Next action: %s\n", cfg.Override.NextAction)
	}

	fmt.Println()
	fmt.Println("Backends:")
	for name, backend := range cfg.Backends {
		backendType := backend.Type
		if backendType == "" {
			backendType = "external"
		}
		status := "disabled"
		if backend.Enabled {
			status = "enabled"
		}
		fmt.Printf("  %s (%s): %s\n", name, backendType, status)
	}

	if len(cfg.Override.ActiveBackends) > 0 {
		fmt.Println()
		fmt.Printf("Active backends override: %v\n", cfg.Override.ActiveBackends)
	}

	return nil
}

// setControlConfigValue parses a KEY=VALUE string and sets the value in the config.
// Supports dot notation for nested values, e.g.:
//   - mode=parallel
//   - override.paused=true
//   - backends.claude.enabled=true
//   - backends.claude.options.model=haiku
