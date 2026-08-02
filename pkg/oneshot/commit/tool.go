// Package commit implements the generate_commit tool for buckley commit.
package commit

import (
	"log"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/tools"
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
				Description: "Short summary of the change, imperative mood, no period. The FULL header (action + scope + subject) must be <= 72 chars total, so budget accordingly (e.g., 'add(ui): ' is 9 chars, leaving 63 for subject)",
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
				"Issue numbers this change RELATES TO, without # prefix (e.g., '123', '456'). "+
					"These are rendered as references ('Refs #N'), NOT as close directives — "+
					"populating this does not and must not close any issue. Do not add an issue "+
					"number just because it appears in the diff text (e.g. a changelog '(roadmap: #14)').",
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
//
// Related issues are rendered in a reference-only form ("Refs #N"), never
// as a GitHub close directive: the model's issues field carries issues
// the change relates to, not issues it is declared to close, and emitting
// "Closes #N" here has silently auto-closed unrelated issues/PRs on merge.
// Body bullets are sanitized for the same reason, in case a close keyword
// slips into free text. See pkg/commitmsg.
func (cr CommitResult) Format() string {
	msg := cr.Header() + "\n\n"

	for _, bullet := range cr.Body {
		msg += "- " + commitmsg.NeutralizeCloseDirectives(bullet) + "\n"
	}

	if cr.Breaking {
		msg += "\nBREAKING CHANGE: " + cr.Subject + "\n"
	}

	if len(cr.Issues) > 0 {
		msg += "\n"
		for _, issue := range cr.Issues {
			msg += commitmsg.IssueRefLine(issue) + "\n"
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
	if err := tools.Register(GenerateCommitTool); err != nil {
		log.Printf("register commit tool: %v", err)
	}

	// Register the command in the oneshot registry
	if err := oneshot.RegisterBuiltin(
		"commit",
		"Generate a structured git commit message from staged changes",
		GenerateCommitTool,
	); err != nil {
		log.Printf("register commit command: %v", err)
	}
}
