package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/odvcencio/buckley/pkg/hunt"
)

// runHuntCommand scans the codebase for improvement suggestions.
func runHuntCommand(args []string) error {
	fs := flag.NewFlagSet("hunt", flag.ContinueOnError)
	rootDir := fs.String("dir", "", "Root directory to scan (default: current directory)")
	limit := fs.Int("limit", 10, "Maximum number of suggestions to show")
	severity := fs.Int("min-severity", 1, "Minimum severity level (1-10)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Determine root directory
	dir := *rootDir
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
	}

	// Create hunt engine
	engine := hunt.NewEngine(dir)

	// Register available analyzers
	engine.AddAnalyzer(&hunt.TODOAnalyzer{})
	engine.AddAnalyzer(&hunt.LintAnalyzer{})
	engine.AddAnalyzer(&hunt.DependencyAnalyzer{})

	// Run scan
	fmt.Printf("Scanning %s for improvements...\n\n", dir)
	suggestions, err := engine.Scan()
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	// Filter by severity
	var filtered []hunt.ImprovementSuggestion
	for _, s := range suggestions {
		if s.Severity >= *severity {
			filtered = append(filtered, s)
		}
	}

	if len(filtered) == 0 {
		fmt.Println("✓ No improvement suggestions found at this severity level")
		return nil
	}

	// Limit results
	if len(filtered) > *limit {
		filtered = filtered[:*limit]
	}

	// Display results
	fmt.Printf("Found %d improvement suggestions:\n\n", len(filtered))
	for i, s := range filtered {
		severityIcon := strings.Repeat("!", s.Severity/2)
		if severityIcon == "" {
			severityIcon = "."
		}
		effortLabel := s.Effort
		if effortLabel == "" {
			effortLabel = "unknown"
		}

		fmt.Printf("%d. [%s] %s\n", i+1, severityIcon, s.Rationale)
		fmt.Printf("   Category: %s | Effort: %s | Severity: %d\n", s.Category, effortLabel, s.Severity)
		if s.File != "" {
			location := s.File
			if s.LineStart > 0 {
				location = fmt.Sprintf("%s:%d", s.File, s.LineStart)
			}
			fmt.Printf("   Location: %s\n", location)
		}
		if s.Snippet != "" {
			fmt.Printf("   Snippet: %s\n", s.Snippet)
		}
		fmt.Println()
	}

	return nil
}
