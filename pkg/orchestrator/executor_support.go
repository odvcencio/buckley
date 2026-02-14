package orchestrator

import (
	"fmt"
	"strings"
)

func (e *Executor) dependenciesMet(task *Task) bool {
	for _, depID := range task.Dependencies {
		// Find dependency task
		depMet := false
		for _, t := range e.plan.Tasks {
			if t.ID == depID && t.Status == TaskCompleted {
				depMet = true
				break
			}
		}
		if !depMet {
			return false
		}
	}
	return true
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (e *Executor) detectBusinessAmbiguity(task *Task) error {
	if e.workflow == nil || e.config == nil || !e.config.Workflow.PauseOnBusinessAmbiguity {
		return nil
	}

	text := strings.ToLower(strings.TrimSpace(task.Title + " " + task.Description))
	if text == "" {
		return nil
	}

	keywords := []string{"clarify", "unknown", "decide", "unsure", "not sure", "tbd", "???"}
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return e.workflow.pauseWorkflow("Business Ambiguity",
				fmt.Sprintf("Task %s may require clarification: \"%s\"", task.ID, snippet(task.Description, 160)))
		}
	}

	if strings.Count(task.Description, "?") >= 2 {
		return e.workflow.pauseWorkflow("Business Ambiguity",
			fmt.Sprintf("Task %s contains unresolved questions: \"%s\"", task.ID, snippet(task.Description, 160)))
	}

	return nil
}

func snippet(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" || len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

func (e *Executor) saveProgress() error {
	// Save plan updates
	return e.planner.UpdatePlan(e.plan)
}

func (e *Executor) GetProgress() (completed int, total int) {
	total = len(e.plan.Tasks)
	completed = 0
	for _, task := range e.plan.Tasks {
		if task.Status == TaskCompleted {
			completed++
		}
	}
	return completed, total
}

// captureFileState captures the current state of task files for change detection
func (e *Executor) captureFileState(task *Task) map[string]bool {
	state := make(map[string]bool)
	if task == nil {
		return state
	}

	readTool, ok := e.toolRegistry.Get("read_file")
	if !ok {
		return state
	}

	for _, filePath := range task.Files {
		// Try to read the file
		params := map[string]any{
			"path": filePath,
		}
		result, err := readTool.Execute(params)
		if err == nil && result.Success {
			// File exists
			state[filePath] = true
		}
	}

	return state
}

// filesChanged checks if files changed between two states
func (e *Executor) filesChanged(before, after map[string]bool) bool {
	// Check if any new files appeared
	for file := range after {
		if !before[file] {
			return true
		}
	}

	// Check if any files disappeared (less common but possible)
	for file := range before {
		if !after[file] {
			return true
		}
	}

	// For implementation tasks, file existence change indicates progress
	// For analysis tasks, no file changes is expected
	return false
}
