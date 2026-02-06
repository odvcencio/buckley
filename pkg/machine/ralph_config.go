package machine

import (
	"fmt"
	"strings"
)

// RalphSpec describes a Ralph task specification.
type RalphSpec struct {
	// Goal is the natural-language description of what to build/fix.
	Goal string

	// Files lists the files that the agent should focus on.
	Files []string

	// VerifyCommand is the shell command to run for verification (e.g., "go test ./...").
	VerifyCommand string

	// MaxIterations limits the number of code-verify-fix cycles (0 = use default).
	MaxIterations int
}

// Validate returns an error if the spec is incomplete.
func (s *RalphSpec) Validate() error {
	if strings.TrimSpace(s.Goal) == "" {
		return fmt.Errorf("ralph spec: goal is required")
	}
	if strings.TrimSpace(s.VerifyCommand) == "" {
		return fmt.Errorf("ralph spec: verify command is required")
	}
	return nil
}

// SystemPrompt builds the system prompt injected at each iteration.
func (s *RalphSpec) SystemPrompt(iteration int, lastError string) string {
	var b strings.Builder
	b.WriteString("You are a stateless coding agent running in Ralph mode.\n\n")
	b.WriteString("## Goal\n")
	b.WriteString(strings.TrimSpace(s.Goal))
	b.WriteString("\n")

	if len(s.Files) > 0 {
		b.WriteString("\n## Files\n")
		for _, f := range s.Files {
			b.WriteString("- ")
			b.WriteString(f)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n## Verification\n")
	b.WriteString("After making changes, they will be verified with: `")
	b.WriteString(s.VerifyCommand)
	b.WriteString("`\n")

	b.WriteString("\n## Iteration\n")
	b.WriteString(fmt.Sprintf("This is iteration %d.", iteration))

	if lastError != "" {
		b.WriteString("\n\n## Previous Error\nThe last verification failed with:\n```\n")
		b.WriteString(lastError)
		b.WriteString("\n```\nFix the issue and try again.\n")
	}

	return b.String()
}

// ParseRalphSpec parses a simple text spec format.
// Format:
//
//	goal: <text>
//	files: <comma-separated paths>
//	verify: <command>
//	max_iterations: <number>
func ParseRalphSpec(text string) (*RalphSpec, error) {
	spec := &RalphSpec{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(strings.ToLower(key))
		value = strings.TrimSpace(value)

		switch key {
		case "goal":
			spec.Goal = value
		case "files":
			for _, f := range strings.Split(value, ",") {
				f = strings.TrimSpace(f)
				if f != "" {
					spec.Files = append(spec.Files, f)
				}
			}
		case "verify":
			spec.VerifyCommand = value
		case "max_iterations":
			n := 0
			fmt.Sscanf(value, "%d", &n)
			spec.MaxIterations = n
		}
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return spec, nil
}
