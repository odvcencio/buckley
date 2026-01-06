package artifact

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExecutionTracker manages execution artifacts with incremental updates
type ExecutionTracker struct {
	outputDir string
	artifact  *ExecutionArtifact
	filePath  string
}

// NewExecutionTracker creates a new execution tracker
func NewExecutionTracker(outputDir string, planningArtifactPath string, feature string, totalTasks int) *ExecutionTracker {
	now := time.Now()
	return &ExecutionTracker{
		outputDir: outputDir,
		artifact: &ExecutionArtifact{
			Artifact: Artifact{
				Type:      ArtifactTypeExecution,
				Feature:   feature,
				CreatedAt: now,
				UpdatedAt: now,
				Status:    "in_progress",
			},
			PlanningArtifactPath: planningArtifactPath,
			StartedAt:            now,
			CurrentTask:          0,
			TotalTasks:           totalTasks,
			ProgressLog:          []TaskProgress{},
			Pauses:               []ExecutionPause{},
			DeviationSummary:     []Deviation{},
			ReviewChecklist:      []string{},
		},
	}
}

// Initialize creates the initial execution artifact file
func (t *ExecutionTracker) Initialize() error {
	// Ensure output directory exists
	if err := os.MkdirAll(t.outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Generate filename: YYYY-MM-DD-{feature}-execution.md
	now := time.Now()
	filename := fmt.Sprintf("%s-%s-execution.md", now.Format("2006-01-02"), t.artifact.Feature)
	t.filePath = filepath.Join(t.outputDir, filename)
	t.artifact.FilePath = t.filePath

	// Write initial content
	return t.save()
}

// AddTaskProgress adds progress for a completed task
func (t *ExecutionTracker) AddTaskProgress(progress TaskProgress) error {
	t.artifact.ProgressLog = append(t.artifact.ProgressLog, progress)
	t.artifact.CurrentTask = progress.TaskID
	t.artifact.UpdatedAt = time.Now()

	// Add deviations to summary
	for _, deviation := range progress.Deviations {
		t.artifact.DeviationSummary = append(t.artifact.DeviationSummary, deviation)
	}

	return t.save()
}

// AddPause records when execution paused for user input
func (t *ExecutionTracker) AddPause(pause ExecutionPause) error {
	pause.Number = len(t.artifact.Pauses) + 1
	pause.Timestamp = time.Now()
	t.artifact.Pauses = append(t.artifact.Pauses, pause)
	t.artifact.UpdatedAt = time.Now()

	return t.save()
}

// ResolvePause updates the latest pause entry with user feedback.
func (t *ExecutionTracker) ResolvePause(userResponse, resolution string) error {
	if t == nil || t.artifact == nil || len(t.artifact.Pauses) == 0 {
		return fmt.Errorf("no pause to resolve")
	}

	idx := len(t.artifact.Pauses) - 1
	if userResponse != "" {
		t.artifact.Pauses[idx].UserResponse = userResponse
	}
	if resolution != "" {
		t.artifact.Pauses[idx].Resolution = resolution
	}
	t.artifact.UpdatedAt = time.Now()
	return t.save()
}

// AddReviewChecklistItem adds a high-risk area for review
func (t *ExecutionTracker) AddReviewChecklistItem(item string) error {
	t.artifact.ReviewChecklist = append(t.artifact.ReviewChecklist, item)
	t.artifact.UpdatedAt = time.Now()

	return t.save()
}

// Complete marks the execution as completed
func (t *ExecutionTracker) Complete() error {
	t.artifact.Status = "completed"
	t.artifact.UpdatedAt = time.Now()

	return t.save()
}

// GetFilePath returns the file path of the execution artifact
func (t *ExecutionTracker) GetFilePath() string {
	return t.filePath
}

// save writes the current artifact state to disk
func (t *ExecutionTracker) save() error {
	content := t.generateMarkdown()
	return os.WriteFile(t.filePath, []byte(content), 0644)
}

// generateMarkdown converts the execution artifact to markdown
func (t *ExecutionTracker) generateMarkdown() string {
	var b strings.Builder

	// Header with chain link
	b.WriteString(fmt.Sprintf("# Execution: %s\n\n", formatFeatureName(t.artifact.Feature)))
	b.WriteString(fmt.Sprintf("**Planning Artifact:** [%s](%s)\n",
		filepath.Base(t.artifact.PlanningArtifactPath),
		t.relativePath(t.artifact.PlanningArtifactPath)))
	b.WriteString(fmt.Sprintf("**Started:** %s\n", t.artifact.StartedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("**Status:** %s (Task %d/%d)\n\n",
		strings.Title(t.artifact.Status), t.artifact.CurrentTask, t.artifact.TotalTasks))

	// Progress Log
	if len(t.artifact.ProgressLog) > 0 {
		b.WriteString("## Progress Log\n\n")

		for _, progress := range t.artifact.ProgressLog {
			b.WriteString(fmt.Sprintf("### Task %d: %s\n", progress.TaskID, progress.Description))
			b.WriteString(fmt.Sprintf("**Status:** %s %s\n", statusIcon(progress.Status), progress.Status))

			if progress.Duration != "" {
				b.WriteString(fmt.Sprintf("**Duration:** %s\n", progress.Duration))
			}

			if len(progress.FilesModified) > 0 {
				b.WriteString("**Files Modified:**\n")
				for _, file := range progress.FilesModified {
					b.WriteString(fmt.Sprintf("- `%s` ", file.Path))
					if file.LinesAdded > 0 {
						b.WriteString(fmt.Sprintf("(+%d lines)", file.LinesAdded))
					}
					b.WriteString("\n")
				}
			}

			if progress.ImplementationNotes != "" {
				b.WriteString("\n#### Implementation Notes\n")
				b.WriteString(progress.ImplementationNotes)
				b.WriteString("\n")
			}

			if len(progress.Deviations) > 0 {
				b.WriteString("\n#### Deviations from Plan\n")
				for _, dev := range progress.Deviations {
					b.WriteString(fmt.Sprintf("- **%s:** %s\n", dev.Type, dev.Description))
					if dev.Rationale != "" {
						b.WriteString(fmt.Sprintf("  - **Rationale:** %s\n", dev.Rationale))
					}
				}
			}

			if len(progress.TestsAdded) > 0 {
				b.WriteString("\n#### Tests Added\n")
				for _, test := range progress.TestsAdded {
					b.WriteString(fmt.Sprintf("- `%s` - %s", test.Name, statusIcon(test.Status)))
					if test.Coverage > 0 {
						b.WriteString(fmt.Sprintf(" (%.1f%% coverage)", test.Coverage))
					}
					b.WriteString("\n")
				}
			}

			if progress.CodeSnippet != "" {
				b.WriteString("\n#### Code Snippet\n```go\n")
				b.WriteString(progress.CodeSnippet)
				b.WriteString("\n```\n")
			}

			b.WriteString("\n")
		}
	}

	// Pauses and Decisions
	if len(t.artifact.Pauses) > 0 {
		b.WriteString("## Pauses and Decisions\n\n")
		for _, pause := range t.artifact.Pauses {
			b.WriteString(fmt.Sprintf("### Pause #%d: %s (Task %d)\n", pause.Number, pause.Reason, pause.TaskID))
			b.WriteString(fmt.Sprintf("**Question:** %s\n\n", pause.Question))
			b.WriteString(fmt.Sprintf("**User Response:** %s\n\n", pause.UserResponse))
			b.WriteString(fmt.Sprintf("**Resolution:** %s\n\n", pause.Resolution))
		}
	}

	// Running Deviation Summary
	if len(t.artifact.DeviationSummary) > 0 {
		b.WriteString("## Summary of Deviations\n\n")
		b.WriteString("| Task | Type | Description | Impact |\n")
		b.WriteString("|------|------|-------------|--------|\n")
		for _, dev := range t.artifact.DeviationSummary {
			b.WriteString(fmt.Sprintf("| %d | %s | %s | %s |\n",
				dev.TaskID, dev.Type, dev.Description, dev.Impact))
		}
		b.WriteString("\n")
	}

	// Review Preparation Checklist
	if len(t.artifact.ReviewChecklist) > 0 {
		b.WriteString("## High-Risk Areas for Review\n\n")
		for _, item := range t.artifact.ReviewChecklist {
			b.WriteString(fmt.Sprintf("- [ ] %s\n", item))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// relativePath converts an absolute path to a relative path for linking
func (t *ExecutionTracker) relativePath(targetPath string) string {
	// If target is in docs/plans and we're in docs/execution, use ../plans/filename
	targetDir := filepath.Dir(targetPath)
	targetBase := filepath.Base(targetPath)

	if strings.Contains(targetDir, "plans") {
		return filepath.Join("../plans", targetBase)
	}

	return targetPath
}

// statusIcon returns an icon for a status
func statusIcon(status string) string {
	switch strings.ToLower(status) {
	case "completed", "pass":
		return "✓"
	case "failed", "fail":
		return "✗"
	case "in_progress":
		return "⚙"
	case "pending":
		return "○"
	default:
		return "•"
	}
}
