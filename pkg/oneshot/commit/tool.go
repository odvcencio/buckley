// Package commit implements the generate_commit tool for buckley commit.
package commit

import (
	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/tools"
)

// CommitActions are the allowed action verbs for commits.
var CommitActions = []string{
	"add",
	"fix",
	"update",
	"refactor",
	"remove",
	"improve",
	"rename",
	"move",
	"revert",
	"merge",
	"bump",
	"release",
	"format",
	"optimize",
	"simplify",
	"extract",
	"inline",
	"document",
	"test",
	"build",
	"ci",
}

// GenerateCommitTool defines the structured contract for commit generation.
// The model calls this tool with structured parameters - no text parsing needed.
var GenerateCommitTool = tools.Definition{
	Name:        "generate_commit",
	Description: "Generate a structured git commit message based on staged changes. Returns action-style commit with header and body bullets.",
	Parameters: tools.ObjectSchema(
		map[string]tools.Property{
			"action": tools.StringEnumProperty(
				"The action verb describing what this commit does",
				CommitActions...,
			),
			"scope": tools.StringProperty(
				"The component, package, or area affected (optional, e.g., 'api', 'ui', 'config')",
			),
			"subject": {
				Type:        "string",
				Description: "Short summary of the change, imperative mood, no period, max 50 chars",
				MaxLength:   72,
			},
			"body": tools.ArrayProperty(
				"Bullet points explaining WHAT changed and WHY (not how). Each item describes a logical change.",
				tools.StringProperty("A single bullet point"),
			),
			"breaking": tools.BoolProperty(
				"Whether this commit introduces a breaking change",
			),
			"issues": tools.ArrayProperty(
				"Related issue numbers without # prefix (e.g., '123', '456')",
				tools.StringProperty("Issue number"),
			),
		},
		"action", "subject", "body", // required fields
	),
}

// CommitResult is the strongly-typed result of generate_commit.
// No parsing required - the model fills this structure directly.
type CommitResult struct {
	Action   string   `json:"action"`
	Scope    string   `json:"scope,omitempty"`
	Subject  string   `json:"subject"`
	Body     []string `json:"body"`
	Breaking bool     `json:"breaking,omitempty"`
	Issues   []string `json:"issues,omitempty"`
}

// Header formats the commit header line.
func (cr CommitResult) Header() string {
	if cr.Scope != "" {
		return cr.Action + "(" + cr.Scope + "): " + cr.Subject
	}
	return cr.Action + ": " + cr.Subject
}

// Format returns the full commit message.
func (cr CommitResult) Format() string {
	msg := cr.Header() + "\n\n"

	for _, bullet := range cr.Body {
		msg += "- " + bullet + "\n"
	}

	if cr.Breaking {
		msg += "\nBREAKING CHANGE: " + cr.Subject + "\n"
	}

	if len(cr.Issues) > 0 {
		msg += "\n"
		for _, issue := range cr.Issues {
			msg += "Closes #" + issue + "\n"
		}
	}

	return msg
}

// Validate checks if the result meets requirements.
func (cr CommitResult) Validate() error {
	if cr.Action == "" {
		return &ValidationError{Field: "action", Message: "action is required"}
	}
	if cr.Subject == "" {
		return &ValidationError{Field: "subject", Message: "subject is required"}
	}
	if len(cr.Body) == 0 {
		return &ValidationError{Field: "body", Message: "body requires at least one bullet"}
	}
	return nil
}

// ValidationError indicates a result didn't meet requirements.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}

func init() {
	// Register the tool in the tools registry
	tools.MustRegister(GenerateCommitTool)

	// Register the command in the oneshot registry
	oneshot.MustRegisterBuiltin(
		"commit",
		"Generate a structured git commit message from staged changes",
		GenerateCommitTool,
	)
}
