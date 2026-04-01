package prompts

import (
	"bytes"
	"hash/fnv"
	"os"
	"path/filepath"

	"github.com/odvcencio/buckley/pkg/types"
)

const (
	MaxInstructionFileChars  = 4000
	MaxTotalInstructionChars = 12000
)

// PromptContext provides facts for arbiter-governed prompt assembly.
type PromptContext struct {
	ModelTier        string
	TaskType         string
	GitDiffLines     int
	InstructionChars int
	GTSAvailable     bool
}

// PromptSection is a named section of the system prompt.
type PromptSection struct {
	Name    string
	Content string
	Dynamic bool
}

// InstructionFile is a discovered instruction file.
type InstructionFile struct {
	Path    string
	Content string
}

// PromptBuilder assembles prompts with arbiter-governed section inclusion.
type PromptBuilder struct {
	sections  []PromptSection
	evaluator types.RuleEvaluator
}

// NewPromptBuilder creates a prompt builder. evaluator may be nil (include all sections).
func NewPromptBuilder(evaluator types.RuleEvaluator) *PromptBuilder {
	return &PromptBuilder{evaluator: evaluator}
}

// AddSection adds a named section to the builder.
func (b *PromptBuilder) AddSection(name, content string, dynamic bool) {
	b.sections = append(b.sections, PromptSection{
		Name: name, Content: content, Dynamic: dynamic,
	})
}

// Build assembles the final prompt sections, governed by session/prompt_assembly.arb.
func (b *PromptBuilder) Build(ctx PromptContext) []string {
	if b.evaluator != nil {
		result, err := b.evaluator.EvalStrategy("session/prompt_assembly", "assembly_policy", map[string]any{
			"model_tier":        ctx.ModelTier,
			"task_type":         ctx.TaskType,
			"git_diff_lines":    ctx.GitDiffLines,
			"instruction_chars": ctx.InstructionChars,
			"gts_available":     ctx.GTSAvailable,
		})
		if err == nil {
			return b.buildWithPolicy(result)
		}
	}
	return b.buildAll()
}

func (b *PromptBuilder) buildWithPolicy(result types.StrategyResult) []string {
	var out []string
	totalChars := 0
	maxChars := result.Int("max_chars")
	if maxChars <= 0 {
		maxChars = MaxTotalInstructionChars
	}

	for _, section := range b.sections {
		if result.Bool("omit_" + section.Name) {
			continue
		}
		content := section.Content
		if totalChars+len(content) > maxChars {
			remaining := maxChars - totalChars
			if remaining <= 0 {
				break
			}
			content = content[:remaining] + "\n[truncated]"
		}
		totalChars += len(content)
		out = append(out, content)
	}
	return out
}

func (b *PromptBuilder) buildAll() []string {
	var out []string
	totalChars := 0
	for _, section := range b.sections {
		content := section.Content
		if totalChars+len(content) > MaxTotalInstructionChars {
			remaining := MaxTotalInstructionChars - totalChars
			if remaining <= 0 {
				break
			}
			content = content[:remaining] + "\n[truncated]"
		}
		totalChars += len(content)
		out = append(out, content)
	}
	return out
}

// DiscoverInstructions walks from cwd to root, finding and deduplicating instruction files.
func DiscoverInstructions(root, cwd string) []InstructionFile {
	var files []InstructionFile
	seen := map[uint64]bool{}
	candidates := []string{"CLAUDE.md", "CLAUDE.local.md", ".claude/instructions.md", "AGENTS.md"}

	for dir := cwd; ; dir = filepath.Dir(dir) {
		for _, name := range candidates {
			path := filepath.Join(dir, name)
			content, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			h := fnv.New64a()
			h.Write(bytes.TrimSpace(content))
			hash := h.Sum64()
			if seen[hash] {
				continue
			}
			seen[hash] = true
			files = append(files, InstructionFile{Path: path, Content: string(content)})
		}
		if dir == root || dir == filepath.Dir(dir) {
			break
		}
	}
	return files
}
