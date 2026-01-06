package dream

import (
	"fmt"
	"strings"
)

// DreamIdea represents a greenfield architectural idea
type DreamIdea struct {
	Title        string
	Category     string // "feature", "architecture", "refactoring", "tooling"
	Description  string
	Benefits     []string
	Effort       string // "small", "medium", "large"
	Dependencies []string
	Example      string
}

// Generator generates dream mode suggestions
type Generator struct {
	analysis *CodebaseAnalysis
}

// NewGenerator creates a new dream mode generator
func NewGenerator(analysis *CodebaseAnalysis) *Generator {
	return &Generator{analysis: analysis}
}

// GenerateIdeas generates architectural and feature ideas
func (g *Generator) GenerateIdeas() []DreamIdea {
	ideas := []DreamIdea{}

	// Add architecture-specific ideas
	ideas = append(ideas, g.generateArchitectureIdeas()...)

	// Add gap-based ideas
	ideas = append(ideas, g.generateGapIdeas()...)

	// Add modern practice ideas
	ideas = append(ideas, g.generateModernPracticeIdeas()...)

	return ideas
}

// generateArchitectureIdeas generates ideas based on current architecture
func (g *Generator) generateArchitectureIdeas() []DreamIdea {
	ideas := []DreamIdea{}

	switch g.analysis.Architecture.Type {
	case "cli":
		ideas = append(ideas, DreamIdea{
			Title:       "Interactive Shell Mode",
			Category:    "feature",
			Description: "Add REPL-style interactive shell for complex workflows",
			Benefits:    []string{"Improved UX for power users", "Reduced startup overhead", "Command history and autocomplete"},
			Effort:      "medium",
			Example:     "Use bubbletea or similar TUI library for rich terminal interface",
		})

		ideas = append(ideas, DreamIdea{
			Title:        "Plugin System",
			Category:     "architecture",
			Description:  "Allow users to extend functionality via plugins",
			Benefits:     []string{"Extensible without core changes", "Community contributions", "Custom workflows"},
			Effort:       "large",
			Dependencies: []string{"Plugin discovery mechanism", "Sandboxed execution", "Plugin API versioning"},
			Example:      "Load plugins from ~/.config/app/plugins/ directory",
		})

	case "web":
		ideas = append(ideas, DreamIdea{
			Title:       "GraphQL API Layer",
			Category:    "architecture",
			Description: "Add GraphQL alongside REST for flexible client queries",
			Benefits:    []string{"Reduced over-fetching", "Better client performance", "Type-safe API"},
			Effort:      "medium",
			Example:     "Use gqlgen or similar library for schema-first development",
		})

		ideas = append(ideas, DreamIdea{
			Title:       "Real-time Features with WebSockets",
			Category:    "feature",
			Description: "Add WebSocket support for live updates and collaboration",
			Benefits:    []string{"Real-time user experience", "Reduced polling overhead", "Collaborative features"},
			Effort:      "medium",
			Example:     "Use gorilla/websocket for connection management",
		})

	case "library":
		ideas = append(ideas, DreamIdea{
			Title:       "Fluent API Design",
			Category:    "architecture",
			Description: "Refactor API to use builder pattern for better ergonomics",
			Benefits:    []string{"Improved developer experience", "Type-safe configuration", "Discoverable API"},
			Effort:      "large",
			Example:     "NewClient().WithAuth(...).WithTimeout(...).Build()",
		})
	}

	// Universal ideas
	ideas = append(ideas, DreamIdea{
		Title:       "Structured Logging with Levels",
		Category:    "tooling",
		Description: "Replace fmt.Printf with structured logging library",
		Benefits:    []string{"Better debugging in production", "Log aggregation friendly", "Configurable verbosity"},
		Effort:      "small",
		Example:     "Use slog (Go 1.21+) or zerolog for high-performance logging",
	})

	return ideas
}

// generateGapIdeas generates ideas based on detected gaps
func (g *Generator) generateGapIdeas() []DreamIdea {
	ideas := []DreamIdea{}

	for _, gap := range g.analysis.Gaps {
		switch gap.Category {
		case "testing":
			ideas = append(ideas, DreamIdea{
				Title:       "Test Infrastructure Setup",
				Category:    "tooling",
				Description: "Add comprehensive testing framework with fixtures and helpers",
				Benefits:    []string{"Faster test writing", "Consistent test patterns", "Better coverage"},
				Effort:      "medium",
				Example:     "Create pkg/testutil with common mocks, fixtures, and assertions",
			})

			ideas = append(ideas, DreamIdea{
				Title:       "Property-Based Testing",
				Category:    "testing",
				Description: "Add property-based tests for complex logic",
				Benefits:    []string{"Find edge cases automatically", "Better confidence in correctness", "Reduced manual test cases"},
				Effort:      "small",
				Example:     "Use gopter or rapid for generative testing",
			})

		case "docs":
			ideas = append(ideas, DreamIdea{
				Title:       "Auto-Generated API Documentation",
				Category:    "tooling",
				Description: "Generate API docs from code comments and type signatures",
				Benefits:    []string{"Always up-to-date docs", "Less maintenance burden", "Better onboarding"},
				Effort:      "small",
				Example:     "Use godoc, swaggo (for REST), or gqlgen (for GraphQL)",
			})

		case "automation":
			ideas = append(ideas, DreamIdea{
				Title:       "CI/CD Pipeline with Multi-Stage Deployment",
				Category:    "tooling",
				Description: "Automated testing, building, and deployment pipeline",
				Benefits:    []string{"Faster releases", "Reduced human error", "Automated quality gates"},
				Effort:      "medium",
				Example:     "GitHub Actions with dev/staging/prod environments",
			})

		case "monitoring":
			ideas = append(ideas, DreamIdea{
				Title:        "Observability Stack",
				Category:     "architecture",
				Description:  "Add metrics, tracing, and centralized logging",
				Benefits:     []string{"Production visibility", "Faster debugging", "Performance insights"},
				Effort:       "large",
				Dependencies: []string{"Metrics library (Prometheus)", "Tracing (OpenTelemetry)", "Log aggregation"},
				Example:      "Prometheus + Grafana for metrics, Jaeger for tracing",
			})

		case "security":
			ideas = append(ideas, DreamIdea{
				Title:        "Security Hardening",
				Category:     "architecture",
				Description:  "Implement defense-in-depth security practices",
				Benefits:     []string{"Reduced attack surface", "Compliance readiness", "User trust"},
				Effort:       "large",
				Dependencies: []string{"Auth layer", "Input validation", "Rate limiting", "HTTPS enforcement"},
				Example:      "Use OAuth2/OIDC for auth, OWASP guidelines for web security",
			})
		}
	}

	return ideas
}

// generateModernPracticeIdeas generates ideas for modern development practices
func (g *Generator) generateModernPracticeIdeas() []DreamIdea {
	ideas := []DreamIdea{}

	// Suggest based on project size
	if g.analysis.TotalFiles > 50 {
		ideas = append(ideas, DreamIdea{
			Title:       "Monorepo Tooling",
			Category:    "tooling",
			Description: "Add monorepo management for better multi-package development",
			Benefits:    []string{"Shared dependencies", "Atomic cross-package changes", "Unified CI/CD"},
			Effort:      "medium",
			Example:     "Use Go workspaces or Bazel for build orchestration",
		})
	}

	// Language-specific ideas
	if hasLanguage(g.analysis, "Go") {
		ideas = append(ideas, DreamIdea{
			Title:       "Generics Refactoring",
			Category:    "refactoring",
			Description: "Refactor common patterns to use Go generics (1.18+)",
			Benefits:    []string{"Type safety", "Reduced code duplication", "Better performance"},
			Effort:      "medium",
			Example:     "Generic Map, Filter, Reduce helpers; type-safe containers",
		})

		ideas = append(ideas, DreamIdea{
			Title:       "Context-Aware Cancellation",
			Category:    "architecture",
			Description: "Ensure all long-running operations accept context.Context",
			Benefits:    []string{"Graceful shutdown", "Request timeouts", "Resource cleanup"},
			Effort:      "small",
			Example:     "Pass context through all I/O operations and goroutines",
		})
	}

	// Performance ideas for larger projects
	if g.analysis.TotalLines > 10000 {
		ideas = append(ideas, DreamIdea{
			Title:       "Performance Profiling Integration",
			Category:    "tooling",
			Description: "Add continuous performance benchmarking and profiling",
			Benefits:    []string{"Catch regressions early", "Identify bottlenecks", "Data-driven optimization"},
			Effort:      "small",
			Example:     "Use Go benchmarks + benchstat for regression detection",
		})

		ideas = append(ideas, DreamIdea{
			Title:       "Caching Layer",
			Category:    "architecture",
			Description: "Add strategic caching for expensive operations",
			Benefits:    []string{"Improved performance", "Reduced load", "Better scalability"},
			Effort:      "medium",
			Example:     "Use Redis or in-memory cache with TTL and eviction policies",
		})
	}

	return ideas
}

// Helper functions

func hasLanguage(analysis *CodebaseAnalysis, lang string) bool {
	_, exists := analysis.Languages[lang]
	return exists
}

// FormatIdea formats a dream idea as a string
func FormatIdea(idea DreamIdea) string {
	var sb strings.Builder

	// Title and category
	categoryIcon := categoryToIcon(idea.Category)
	sb.WriteString(fmt.Sprintf("%s %s [%s]\n", categoryIcon, idea.Title, idea.Effort))

	// Description
	sb.WriteString(fmt.Sprintf("  %s\n\n", idea.Description))

	// Benefits
	if len(idea.Benefits) > 0 {
		sb.WriteString("  Benefits:\n")
		for _, benefit := range idea.Benefits {
			sb.WriteString(fmt.Sprintf("    • %s\n", benefit))
		}
		sb.WriteString("\n")
	}

	// Dependencies
	if len(idea.Dependencies) > 0 {
		sb.WriteString("  Requires:\n")
		for _, dep := range idea.Dependencies {
			sb.WriteString(fmt.Sprintf("    - %s\n", dep))
		}
		sb.WriteString("\n")
	}

	// Example
	if idea.Example != "" {
		sb.WriteString(fmt.Sprintf("  Example: %s\n", idea.Example))
	}

	return sb.String()
}

func categoryToIcon(category string) string {
	switch category {
	case "feature":
		return "✨"
	case "architecture":
		return "🏗️"
	case "refactoring":
		return "♻️"
	case "tooling":
		return "🔧"
	case "testing":
		return "🧪"
	default:
		return "💡"
	}
}
