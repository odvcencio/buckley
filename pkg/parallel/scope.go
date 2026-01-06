// Package parallel provides parallel agent execution with conflict-aware coordination.
package parallel

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ScopeValidator detects file scope conflicts between tasks before execution.
// It enables parallel execution of non-overlapping tasks while serializing
// tasks that would modify the same files.
type ScopeValidator struct {
	mu sync.RWMutex
}

// TaskScope defines the file scope for a task.
type TaskScope struct {
	TaskID string
	Files  []string // Explicit file paths
	Globs  []string // Glob patterns like "pkg/auth/..."
}

// Conflict represents a file scope conflict between two tasks.
type Conflict struct {
	TaskA        string   // First task ID
	TaskB        string   // Second task ID
	OverlapFiles []string // Files both tasks want to modify
	OverlapGlobs []string // Overlapping glob patterns
}

// TaskPartition groups tasks that can run in parallel without conflicts.
type TaskPartition struct {
	Group   int          // Partition group number (0 = first wave, 1 = second, etc.)
	TaskIDs []string     // Tasks in this partition
	WaitFor []string     // Task IDs that must complete before this partition starts
	Scopes  []*TaskScope // Scopes for tasks in this partition
}

// NewScopeValidator creates a new scope validator.
func NewScopeValidator() *ScopeValidator {
	return &ScopeValidator{}
}

// ExtractScope extracts the file scope from an AgentTask.
func (v *ScopeValidator) ExtractScope(task *AgentTask) *TaskScope {
	if task == nil {
		return nil
	}

	scope := &TaskScope{
		TaskID: task.ID,
	}
	if scope.TaskID == "" {
		scope.TaskID = strings.TrimSpace(task.Name)
	}

	// Extract from context if available
	if filesRaw, ok := task.Context["files"]; ok {
		for _, f := range strings.Split(filesRaw, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			if strings.Contains(f, "...") || strings.Contains(f, "*") {
				scope.Globs = append(scope.Globs, f)
			} else {
				scope.Files = append(scope.Files, filepath.Clean(f))
			}
		}
	}

	// Also check for explicit scope field
	if scopeRaw, ok := task.Context["scope"]; ok {
		for _, s := range strings.Split(scopeRaw, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if strings.Contains(s, "...") || strings.Contains(s, "*") {
				scope.Globs = append(scope.Globs, s)
			} else {
				scope.Files = append(scope.Files, filepath.Clean(s))
			}
		}
	}

	return scope
}

// CheckConflicts finds all conflicts between a set of task scopes.
func (v *ScopeValidator) CheckConflicts(scopes []*TaskScope) []Conflict {
	v.mu.RLock()
	defer v.mu.RUnlock()

	var conflicts []Conflict

	for i := 0; i < len(scopes); i++ {
		for j := i + 1; j < len(scopes); j++ {
			if conflict := v.checkPairConflict(scopes[i], scopes[j]); conflict != nil {
				conflicts = append(conflicts, *conflict)
			}
		}
	}

	return conflicts
}

// checkPairConflict checks if two scopes conflict.
func (v *ScopeValidator) checkPairConflict(a, b *TaskScope) *Conflict {
	if a == nil || b == nil {
		return nil
	}

	var overlapFiles []string
	var overlapGlobs []string

	// Check file-to-file overlap
	fileSetA := make(map[string]bool)
	for _, f := range a.Files {
		fileSetA[f] = true
	}
	for _, f := range b.Files {
		if fileSetA[f] {
			overlapFiles = append(overlapFiles, f)
		}
	}

	// Check glob-to-file overlap
	for _, glob := range a.Globs {
		for _, file := range b.Files {
			if matchGlob(glob, file) {
				overlapFiles = append(overlapFiles, file)
			}
		}
	}
	for _, glob := range b.Globs {
		for _, file := range a.Files {
			if matchGlob(glob, file) {
				overlapFiles = append(overlapFiles, file)
			}
		}
	}

	// Check glob-to-glob overlap
	for _, globA := range a.Globs {
		for _, globB := range b.Globs {
			if globsOverlap(globA, globB) {
				overlapGlobs = append(overlapGlobs, globA+" ∩ "+globB)
			}
		}
	}

	if len(overlapFiles) == 0 && len(overlapGlobs) == 0 {
		return nil
	}

	// Deduplicate
	overlapFiles = uniqueStrings(overlapFiles)
	overlapGlobs = uniqueStrings(overlapGlobs)

	return &Conflict{
		TaskA:        a.TaskID,
		TaskB:        b.TaskID,
		OverlapFiles: overlapFiles,
		OverlapGlobs: overlapGlobs,
	}
}

// PartitionTasks groups tasks into waves that can run in parallel.
// Tasks in the same partition have no overlapping scopes.
// Later partitions wait for earlier ones to complete.
func (v *ScopeValidator) PartitionTasks(scopes []*TaskScope) []TaskPartition {
	if len(scopes) == 0 {
		return nil
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	filtered := make([]*TaskScope, 0, len(scopes))
	for _, scope := range scopes {
		if scope == nil {
			continue
		}
		if strings.TrimSpace(scope.TaskID) == "" {
			scope.TaskID = fmt.Sprintf("task-%d", len(filtered)+1)
		}
		filtered = append(filtered, scope)
	}
	if len(filtered) == 0 {
		return nil
	}

	// Build conflict graph
	conflicts := make(map[string]map[string]bool)
	for _, s := range filtered {
		conflicts[s.TaskID] = make(map[string]bool)
	}

	for i := 0; i < len(filtered); i++ {
		for j := i + 1; j < len(filtered); j++ {
			if c := v.checkPairConflict(filtered[i], filtered[j]); c != nil {
				conflicts[filtered[i].TaskID][filtered[j].TaskID] = true
				conflicts[filtered[j].TaskID][filtered[i].TaskID] = true
			}
		}
	}

	// Greedy graph coloring for partitioning
	assigned := make(map[string]int)
	scopeByID := make(map[string]*TaskScope)
	for _, s := range filtered {
		scopeByID[s.TaskID] = s
	}

	// Sort by number of conflicts (descending) for better partitioning
	sortedScopes := make([]*TaskScope, len(filtered))
	copy(sortedScopes, filtered)
	sort.SliceStable(sortedScopes, func(i, j int) bool {
		return len(conflicts[sortedScopes[i].TaskID]) > len(conflicts[sortedScopes[j].TaskID])
	})

	maxGroup := 0
	for _, scope := range sortedScopes {
		// Find lowest group that doesn't conflict
		usedGroups := make(map[int]bool)
		for conflictID := range conflicts[scope.TaskID] {
			if group, ok := assigned[conflictID]; ok {
				usedGroups[group] = true
			}
		}

		group := 0
		for usedGroups[group] {
			group++
		}

		assigned[scope.TaskID] = group
		if group > maxGroup {
			maxGroup = group
		}
	}

	// Build partitions
	partitions := make([]TaskPartition, maxGroup+1)
	for i := range partitions {
		partitions[i].Group = i
	}

	for _, scope := range filtered {
		group := assigned[scope.TaskID]
		partitions[group].TaskIDs = append(partitions[group].TaskIDs, scope.TaskID)
		partitions[group].Scopes = append(partitions[group].Scopes, scopeByID[scope.TaskID])
	}

	// Set dependencies (each partition waits for all previous)
	for i := 1; i < len(partitions); i++ {
		for j := 0; j < i; j++ {
			partitions[i].WaitFor = append(partitions[i].WaitFor, partitions[j].TaskIDs...)
		}
	}

	return partitions
}

// HasConflicts returns true if any tasks have overlapping scopes.
func (v *ScopeValidator) HasConflicts(scopes []*TaskScope) bool {
	return len(v.CheckConflicts(scopes)) > 0
}

// matchGlob checks if a file matches a glob pattern.
// Supports "..." suffix for directory recursion.
func matchGlob(pattern, file string) bool {
	pattern = filepath.Clean(pattern)
	file = filepath.Clean(file)

	// Handle "..." suffix (recursive match)
	if strings.HasSuffix(pattern, "/...") {
		prefix := strings.TrimSuffix(pattern, "/...")
		return strings.HasPrefix(file, prefix+"/") || file == prefix
	}
	if strings.HasSuffix(pattern, "...") {
		prefix := strings.TrimSuffix(pattern, "...")
		return strings.HasPrefix(file, prefix)
	}

	// Standard glob match
	matched, _ := filepath.Match(pattern, file)
	if matched {
		return true
	}

	// Check if pattern matches directory prefix
	matched, _ = filepath.Match(pattern, filepath.Dir(file))
	return matched
}

// globsOverlap checks if two glob patterns could match the same files.
func globsOverlap(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)

	// Extract base paths
	baseA := strings.TrimSuffix(strings.TrimSuffix(a, "/..."), "...")
	baseB := strings.TrimSuffix(strings.TrimSuffix(b, "/..."), "...")

	// If one is prefix of other, they overlap
	return strings.HasPrefix(baseA, baseB) || strings.HasPrefix(baseB, baseA)
}

// uniqueStrings removes duplicates from a string slice.
func uniqueStrings(s []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(s))
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}

// ConflictReport generates a human-readable conflict report.
func (v *ScopeValidator) ConflictReport(conflicts []Conflict) string {
	if len(conflicts) == 0 {
		return "No conflicts detected. All tasks can run in parallel."
	}

	var b strings.Builder
	b.WriteString("## Scope Conflicts Detected\n\n")
	b.WriteString("The following tasks have overlapping file scopes and cannot run in parallel:\n\n")

	for i, c := range conflicts {
		b.WriteString(fmt.Sprintf("### Conflict %d\n", i+1))
		b.WriteString("- **Task A:** ")
		b.WriteString(c.TaskA)
		b.WriteString("\n")
		b.WriteString("- **Task B:** ")
		b.WriteString(c.TaskB)
		b.WriteString("\n")

		if len(c.OverlapFiles) > 0 {
			b.WriteString("- **Overlapping files:**\n")
			for _, f := range c.OverlapFiles {
				b.WriteString("  - ")
				b.WriteString(f)
				b.WriteString("\n")
			}
		}
		if len(c.OverlapGlobs) > 0 {
			b.WriteString("- **Overlapping patterns:**\n")
			for _, g := range c.OverlapGlobs {
				b.WriteString("  - ")
				b.WriteString(g)
				b.WriteString("\n")
			}
		}

		if i < len(conflicts)-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}

// PartitionReport generates a human-readable partition report.
func (v *ScopeValidator) PartitionReport(partitions []TaskPartition) string {
	if len(partitions) == 0 {
		return "No tasks to partition."
	}

	var b strings.Builder
	b.WriteString("## Execution Plan\n\n")

	if len(partitions) == 1 {
		b.WriteString("All tasks can run in parallel (no conflicts).\n\n")
	} else {
		b.WriteString(fmt.Sprintf("Tasks will run in %d waves due to file scope conflicts.\n\n", len(partitions)))
	}

	for _, p := range partitions {
		b.WriteString(fmt.Sprintf("### Wave %d\n", p.Group+1))

		if len(p.WaitFor) > 0 {
			b.WriteString("*Waits for: ")
			b.WriteString(strings.Join(p.WaitFor, ", "))
			b.WriteString("*\n\n")
		}

		b.WriteString("| Task | Scope |\n")
		b.WriteString("|------|-------|\n")

		scopeByID := make(map[string]*TaskScope, len(p.Scopes))
		for _, scope := range p.Scopes {
			if scope != nil {
				scopeByID[scope.TaskID] = scope
			}
		}

		for _, taskID := range p.TaskIDs {
			scope := ""
			if taskScope := scopeByID[taskID]; taskScope != nil {
				var parts []string
				parts = append(parts, taskScope.Files...)
				parts = append(parts, taskScope.Globs...)
				scope = strings.Join(parts, ", ")
			}
			b.WriteString("| ")
			b.WriteString(taskID)
			b.WriteString(" | ")
			b.WriteString(scope)
			b.WriteString(" |\n")
		}
		b.WriteString("\n")
	}

	return b.String()
}
