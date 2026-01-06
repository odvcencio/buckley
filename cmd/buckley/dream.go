package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/odvcencio/buckley/pkg/dream"
)

// runDreamCommand analyzes a codebase and suggests architectural improvements.
func runDreamCommand(args []string) error {
	fs := flag.NewFlagSet("dream", flag.ContinueOnError)
	rootDir := fs.String("dir", "", "Root directory to analyze (default: current directory)")
	showIdeas := fs.Bool("ideas", false, "Generate improvement ideas based on analysis")
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

	// Create analyzer
	analyzer := dream.NewAnalyzer(dir)

	// Run analysis
	fmt.Printf("Analyzing codebase at %s...\n\n", dir)
	analysis, err := analyzer.Analyze()
	if err != nil {
		return fmt.Errorf("analysis failed: %w", err)
	}

	// Display results
	fmt.Println("📊 Codebase Analysis")
	fmt.Println(strings.Repeat("─", 50))
	fmt.Printf("\n📁 Structure:\n")
	fmt.Printf("   Total files: %d\n", analysis.TotalFiles)
	fmt.Printf("   Total lines: %d\n", analysis.TotalLines)
	fmt.Printf("   Packages: %d\n", len(analysis.PackageStructure))

	if len(analysis.Languages) > 0 {
		fmt.Printf("\n💻 Languages:\n")
		for lang, count := range analysis.Languages {
			fmt.Printf("   %s: %d files\n", lang, count)
		}
	}

	if len(analysis.EntryPoints) > 0 {
		fmt.Printf("\n🚀 Entry Points:\n")
		for _, ep := range analysis.EntryPoints {
			fmt.Printf("   %s\n", ep)
		}
	}

	fmt.Printf("\n🏗  Architecture:\n")
	fmt.Printf("   Type: %s (confidence: %.0f%%)\n", analysis.Architecture.Type, analysis.Architecture.Confidence*100)
	if len(analysis.Architecture.Indicators) > 0 {
		fmt.Printf("   Indicators:\n")
		for _, ind := range analysis.Architecture.Indicators {
			fmt.Printf("     - %s\n", ind)
		}
	}

	if len(analysis.Gaps) > 0 {
		fmt.Printf("\n⚠️  Gaps Identified:\n")
		for _, gap := range analysis.Gaps {
			icon := "○"
			switch gap.Severity {
			case "critical":
				icon = "🔴"
			case "important":
				icon = "🟡"
			case "nice-to-have":
				icon = "🟢"
			}
			fmt.Printf("   %s [%s] %s: %s\n", icon, gap.Category, gap.Severity, gap.Description)
			fmt.Printf("      → %s\n", gap.Suggestion)
		}
	} else {
		fmt.Println("\n✅ No significant gaps detected!")
	}

	// Generate improvement ideas if requested
	if *showIdeas {
		generator := dream.NewGenerator(analysis)
		ideas := generator.GenerateIdeas()

		if len(ideas) > 0 {
			fmt.Printf("\n💡 Improvement Ideas\n")
			fmt.Println(strings.Repeat("─", 50))
			for _, idea := range ideas {
				fmt.Printf("\n%s\n", dream.FormatIdea(idea))
			}
		}
	}

	return nil
}
