package prompts

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/types"
)

// DefaultToolUseSystemPrompt is the shared Buckley operating contract for tool-first sessions.
const DefaultToolUseSystemPrompt = `You are Buckley, an AI software engineering harness with tool access.

Operate like a disciplined senior engineer:
- inspect before editing when context is missing
- use tools instead of narrating hypothetical actions
- keep working until the task is actually complete or a real blocker exists
- prefer small, verifiable changes over speculative rewrites
- run relevant validation after changes when it is practical
- never claim a command ran, a file changed, or a test passed unless it actually happened
- keep final answers concise once the work is done`

// RuntimePromptInput describes the runtime context used to assemble a system prompt.
type RuntimePromptInput struct {
	Evaluator         types.RuleEvaluator
	BasePrompt        string
	ProjectContext    string
	WorkDir           string
	RootDir           string
	SkillsDescription string
	TaskType          string
	ModelTier         string
	GitDiffLines      int
	GTSAvailable      bool
}

// BuildRuntimeSystemPrompt assembles the Buckley runtime prompt with instruction discovery.
func BuildRuntimeSystemPrompt(input RuntimePromptInput) string {
	builder := NewPromptBuilder(input.Evaluator)

	basePrompt := strings.TrimSpace(input.BasePrompt)
	if basePrompt == "" {
		basePrompt = DefaultToolUseSystemPrompt
	}
	builder.AddSection("system", basePrompt, false)

	rootDir := strings.TrimSpace(input.RootDir)
	workDir := strings.TrimSpace(input.WorkDir)
	if rootDir == "" {
		rootDir = workDir
	}
	if workDir == "" {
		workDir = rootDir
	}

	var instructionFiles []InstructionFile
	if rootDir != "" && workDir != "" {
		instructionFiles = DiscoverInstructions(rootDir, workDir)
	}
	instructionsSection := renderInstructionFiles(instructionFiles)
	if instructionsSection != "" {
		builder.AddSection("instructions", instructionsSection, true)
	}

	projectContext := strings.TrimSpace(input.ProjectContext)
	if projectContext != "" && !containsInstructionContent(instructionFiles, projectContext) {
		builder.AddSection("project_context", "Project Context:\n"+projectContext, true)
	}

	if workDir != "" {
		builder.AddSection("working_directory", fmt.Sprintf("Working Directory: %s", workDir), true)
	}

	skills := strings.TrimSpace(input.SkillsDescription)
	if skills != "" {
		builder.AddSection("skills", skills, true)
	}

	sections := builder.Build(PromptContext{
		ModelTier:        defaultString(input.ModelTier, "standard"),
		TaskType:         defaultString(input.TaskType, "coding"),
		GitDiffLines:     input.GitDiffLines,
		InstructionChars: len(instructionsSection) + len(projectContext),
		GTSAvailable:     input.GTSAvailable,
	})

	return strings.TrimSpace(strings.Join(sections, "\n\n"))
}

func renderInstructionFiles(files []InstructionFile) string {
	if len(files) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Repository Instructions:\n")
	for _, file := range files {
		content := strings.TrimSpace(file.Content)
		if content == "" {
			continue
		}
		label := filepath.Base(file.Path)
		if label == "" {
			label = file.Path
		}
		b.WriteString("\n## ")
		b.WriteString(label)
		b.WriteString("\n")
		b.WriteString(content)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func containsInstructionContent(files []InstructionFile, content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	for _, file := range files {
		if strings.TrimSpace(file.Content) == content {
			return true
		}
	}
	return false
}

func defaultString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
