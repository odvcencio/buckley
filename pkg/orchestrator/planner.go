package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/personality"
	"github.com/odvcencio/buckley/pkg/storage"
)

type Planner struct {
	modelClient     ModelClient
	config          *config.Config
	store           *storage.Store
	workflow        *WorkflowManager
	planStore       PlanStore
	systemPrompt    string
	personaProvider *personality.PersonaProvider
}

type Plan struct {
	ID          string      `json:"id"`
	FeatureName string      `json:"feature_name"`
	CreatedAt   time.Time   `json:"created_at"`
	Description string      `json:"description"`
	Tasks       []Task      `json:"tasks"`
	Context     PlanContext `json:"context"`
	Logs        PlanLogs    `json:"logs,omitempty"`
}

type Task struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Description   string     `json:"description"`
	Type          TaskType   `json:"type"` // implementation, analysis, validation
	Files         []string   `json:"files"`
	Dependencies  []string   `json:"dependencies"`
	EstimatedTime string     `json:"estimated_time"`
	Verification  []string   `json:"verification"`
	Status        TaskStatus `json:"status"`
}

type TaskType string

const (
	TaskTypeImplementation TaskType = "implementation" // Creates or modifies files
	TaskTypeAnalysis       TaskType = "analysis"       // Runs commands, gathers information
	TaskTypeValidation     TaskType = "validation"     // Runs tests, checks quality
)

type TaskStatus int

const (
	TaskPending TaskStatus = iota
	TaskInProgress
	TaskCompleted
	TaskFailed
	TaskSkipped
)

type PlanContext struct {
	ProjectType      string    `json:"project_type"` // go, node, rust, mixed
	Dependencies     []string  `json:"dependencies"`
	RepoRoot         string    `json:"repo_root,omitempty"`
	GitBranch        string    `json:"git_branch"`
	GitRemoteURL     string    `json:"git_remote_url,omitempty"`
	Architecture     string    `json:"architecture"`
	ResearchSummary  string    `json:"research_summary,omitempty"`
	ResearchRisks    []string  `json:"research_risks,omitempty"`
	ResearchLogPath  string    `json:"research_log_path,omitempty"`
	ResearchLoggedAt time.Time `json:"research_logged_at,omitempty"`
}

type PlanLogs struct {
	BaseDir     string    `json:"base_dir"`
	BuilderLog  string    `json:"builder_log"`
	ReviewLog   string    `json:"review_log"`
	ResearchLog string    `json:"research_log"`
	UpdatedAt   time.Time `json:"updated_at"`
}

var planSchemaTemplate = map[string]any{
	"description":  "Overall feature description",
	"architecture": "High-level architecture overview",
	"tasks": []map[string]any{
		{
			"id":             "1",
			"title":          "Task title",
			"description":    "Detailed description",
			"type":           "implementation",
			"files":          []string{"path/to/file1.go", "path/to/file2.go"},
			"dependencies":   []string{},
			"estimated_time": "30m",
			"verification":   []string{"Run tests", "Manual test X"},
		},
	},
}

func defaultPlanningSystemPrompt(useToon bool, personaSection string) string {
	schema := renderSchema(planSchemaTemplate, useToon)
	var b strings.Builder
	b.WriteString("You are a software architect creating detailed implementation plans.\n\n")
	b.WriteString("Your task is to analyze a feature request and break it down into concrete, actionable tasks.\n\n")
	b.WriteString("For each task, provide:\n")
	b.WriteString("- A clear title\n")
	b.WriteString("- Detailed description of what needs to be done\n")
	b.WriteString("- Task type: \"implementation\" (creates/modifies files), \"analysis\" (runs commands, gathers info), or \"validation\" (runs tests/checks)\n")
	b.WriteString("- List of files that will be modified or created (empty for analysis/validation tasks)\n")
	b.WriteString("- Dependencies on other tasks (by task ID)\n")
	b.WriteString("- Estimated time to complete\n")
	b.WriteString("- Verification steps (how to test it works)\n\n")
	b.WriteString("Output your plan as JSON following this structure:\n")
	b.WriteString(schema)
	b.WriteString("\n\nTask type guidelines:\n")
	b.WriteString("- Use \"implementation\" for tasks that create or modify code files\n")
	b.WriteString("- Use \"analysis\" for tasks that run commands to gather information (coverage analysis, dependency checks, etc.)\n")
	b.WriteString("- Use \"validation\" for tasks that run tests or quality checks\n\n")
	b.WriteString("Be specific about file paths, function names, and implementation details.")
	if strings.TrimSpace(personaSection) != "" {
		b.WriteString("\n\n## Persona Voice\n")
		b.WriteString(personaSection)
	}
	return b.String()
}

func NewPlanner(mgr ModelClient, cfg *config.Config, store *storage.Store, workflow *WorkflowManager, planStore PlanStore) *Planner {
	if planStore == nil {
		planDir := cfg.Artifacts.PlanningDir
		if strings.TrimSpace(planDir) == "" {
			planDir = filepath.Join("docs", "plans")
		}
		planStore = NewFilePlanStore(planDir)
	}
	useToon := false
	if cfg != nil {
		useToon = cfg.Encoding.UseToon
	}
	var personaProvider *personality.PersonaProvider
	if workflow != nil {
		personaProvider = workflow.PersonaProvider()
	}
	if personaProvider == nil {
		projectRoot := ""
		if workflow != nil {
			projectRoot = workflow.projectRoot
		}
		personaProvider = BuildPersonaProvider(cfg, projectRoot)
	}
	personaSection := ""
	if personaProvider != nil {
		personaSection = personaProvider.SectionForPhase("planning")
	}
	return &Planner{
		modelClient:     mgr,
		config:          cfg,
		store:           store,
		workflow:        workflow,
		planStore:       planStore,
		systemPrompt:    defaultPlanningSystemPrompt(useToon, personaSection),
		personaProvider: personaProvider,
	}
}

func (p *Planner) GeneratePlan(featureName, description string) (*Plan, error) {
	// Validate inputs
	if featureName == "" {
		return nil, fmt.Errorf("feature name cannot be empty")
	}
	if description == "" {
		return nil, fmt.Errorf("description cannot be empty")
	}

	p.sendProgress("🧭 Planning %q – gathering project context", featureName)

	// 1. Gather project context
	ctx := p.gatherContext()
	p.sendProgress("📎 Context: %s project on branch %s", ctx.ProjectType, ctx.GitBranch)

	// 2. Create planning prompt (augment with index context if available)
	p.sendProgress("📚 Querying project index for related files…")
	indexHints := p.lookupIndexContext(description, 5)
	if trimmed := strings.TrimSpace(indexHints); trimmed != "" {
		lines := strings.Count(trimmed, "\n") + 1
		p.sendProgress("📚 Found %d relevant files from the index", lines)
	}
	prompt := p.buildPlanningPrompt(featureName, description, ctx, indexHints)

	// 3. Call planning model (no timeout - let it run as long as needed)
	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.sendProgress("🤖 Asking %s to draft the plan…", safeModelName(p.config.Models.Planning))

	systemPrompt := p.systemPrompt
	if strings.TrimSpace(systemPrompt) == "" {
		section := ""
		if p.personaProvider != nil {
			section = p.personaProvider.SectionForPhase("planning")
		}
		systemPrompt = defaultPlanningSystemPrompt(false, section)
	}

	req := model.ChatRequest{
		Model: p.config.Models.Planning,
		Messages: []model.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.3, // Lower for more structured output
	}

	// Enable reasoning for planning models that support it
	if p.modelClient.SupportsReasoning(p.config.Models.Planning) {
		req.Reasoning = &model.ReasoningConfig{Effort: "high"}
	}

	resp, err := p.modelClient.ChatCompletion(reqCtx, req)
	if err != nil {
		return nil, fmt.Errorf("planning request failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from planning model")
	}

	p.sendProgress("📝 Processing plan response…")

	// 4. Parse plan from response
	content, err := model.ExtractTextContent(resp.Choices[0].Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to extract content: %w", err)
	}
	plan, err := p.parsePlan(content, featureName)
	if err != nil {
		return nil, fmt.Errorf("failed to parse plan: %w", err)
	}

	// Validate plan
	if len(plan.Tasks) == 0 {
		return nil, fmt.Errorf("plan must have at least one task")
	}

	p.enrichTasksWithIndex(plan)
	p.sendProgress("✅ Draft plan ready with %d tasks", len(plan.Tasks))

	plan.Context = ctx
	plan.CreatedAt = time.Now()
	plan.ID = fmt.Sprintf("%s-%s", plan.CreatedAt.Format("20060102-150405"), slugify(featureName))

	return plan, nil
}

// SavePlan persists the given plan using the configured plan store.
func (p *Planner) SavePlan(plan *Plan) error {
	if p.planStore == nil {
		return fmt.Errorf("plan store not initialized")
	}
	return p.planStore.SavePlan(plan)
}

// LoadPlan loads an existing plan from the plan store.
func (p *Planner) LoadPlan(planID string) (*Plan, error) {
	if p.planStore == nil {
		return nil, fmt.Errorf("plan store not initialized")
	}
	return p.planStore.LoadPlan(planID)
}

// ListPlans returns all saved plans.
func (p *Planner) ListPlans() ([]Plan, error) {
	if p.planStore == nil {
		return nil, fmt.Errorf("plan store not initialized")
	}
	return p.planStore.ListPlans()
}

// UpdatePlan re-saves the plan and refreshes metadata.
func (p *Planner) UpdatePlan(plan *Plan) error {
	return p.SavePlan(plan)
}

func (p *Planner) sendProgress(format string, args ...any) {
	if p == nil || p.workflow == nil {
		return
	}
	message := fmt.Sprintf(format, args...)
	p.workflow.SendProgress(message)
}

func safeModelName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "the planning model"
	}
	return name
}

func (p *Planner) gatherContext() PlanContext {
	projectDir := ""
	if p.workflow != nil {
		projectDir = strings.TrimSpace(p.workflow.projectRoot)
	}
	repoRoot := getRepoRoot(projectDir)
	ctx := PlanContext{
		ProjectType:  detectProjectType(projectDir),
		Dependencies: detectDependencies(projectDir),
		RepoRoot:     repoRoot,
		GitBranch:    getCurrentBranch(projectDir),
		GitRemoteURL: sanitizeRemoteURL(getRemoteURL(projectDir, "origin")),
		Architecture: "",
	}
	return ctx
}

func (p *Planner) buildPlanningPrompt(featureName, description string, ctx PlanContext, indexHints string) string {
	var b strings.Builder

	b.WriteString("Create an implementation plan for this feature:\n\n")
	b.WriteString(fmt.Sprintf("**Feature:** %s\n\n", featureName))
	b.WriteString(fmt.Sprintf("**Description:** %s\n\n", description))
	b.WriteString("**Project Context:**\n")
	b.WriteString(fmt.Sprintf("- Project Type: %s\n", ctx.ProjectType))
	b.WriteString(fmt.Sprintf("- Git Branch: %s\n", ctx.GitBranch))

	if len(ctx.Dependencies) > 0 {
		b.WriteString("- Key Dependencies:\n")
		for _, dep := range ctx.Dependencies {
			b.WriteString(fmt.Sprintf("  - %s\n", dep))
		}
	}

	if indexHints != "" {
		b.WriteString("\n**Relevant files from project index:**\n")
		b.WriteString(indexHints)
	}

	b.WriteString("\nBreak this down into specific, actionable tasks with file paths and verification steps.")

	return b.String()
}

func (p *Planner) lookupIndexContext(query string, limit int) string {
	if p.store == nil || strings.TrimSpace(query) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	files, err := p.store.SearchFiles(ctx, query, "", limit)
	if err != nil || len(files) == 0 {
		return ""
	}

	var b strings.Builder
	for _, file := range files {
		b.WriteString(fmt.Sprintf("- %s", file.Path))
		if file.Summary != "" {
			b.WriteString(fmt.Sprintf(" — %s", truncateSummary(file.Summary, 200)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func truncateSummary(text string, max int) string {
	if len(text) <= max {
		return text
	}
	if max <= 3 {
		return "..."
	}
	return text[:max-3] + "..."
}

func (p *Planner) enrichTasksWithIndex(plan *Plan) {
	if p.store == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	for i := range plan.Tasks {
		task := &plan.Tasks[i]
		if len(task.Files) > 0 {
			continue
		}

		query := fmt.Sprintf("%s %s", task.Title, task.Description)
		files, err := p.store.SearchFiles(ctx, query, "", 3)
		if err != nil || len(files) == 0 {
			continue
		}

		for _, file := range files {
			task.Files = append(task.Files, file.Path)
			if len(task.Files) >= 3 {
				break
			}
		}
	}
}

func (p *Planner) parsePlan(content, featureName string) (*Plan, error) {
	// Extract JSON from markdown code blocks if present
	jsonStr := content
	if strings.Contains(content, "```json") {
		start := strings.Index(content, "```json") + 7
		end := strings.Index(content[start:], "```")
		if end > 0 {
			jsonStr = content[start : start+end]
		}
	} else if strings.Contains(content, "```") {
		start := strings.Index(content, "```") + 3
		end := strings.Index(content[start:], "```")
		if end > 0 {
			jsonStr = content[start : start+end]
		}
	}

	// Sanitize JSON string to fix common LLM errors
	jsonStr = sanitizeJSONString(jsonStr)

	// Parse JSON
	var planData struct {
		Description  string `json:"description"`
		Architecture string `json:"architecture"`
		Tasks        []Task `json:"tasks"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &planData); err != nil {
		// Save full JSON to file for debugging
		debugFile := "/tmp/buckley-plan-debug.json"
		if writeErr := os.WriteFile(debugFile, []byte(jsonStr), 0644); writeErr == nil {
			fmt.Fprintf(os.Stderr, "Failed to parse plan JSON. Full JSON saved to: %s\n", debugFile)
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "Failed to parse plan JSON. First 500 chars:\n%s\n", truncateString(jsonStr, 500))
		}
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Set default task types for tasks that don't have them
	for i := range planData.Tasks {
		task := &planData.Tasks[i]
		if task.Type == "" {
			// Infer task type based on characteristics
			if len(task.Files) > 0 {
				task.Type = TaskTypeImplementation
			} else if containsAnalysisKeywords(task.Title, task.Description) {
				task.Type = TaskTypeAnalysis
			} else if containsValidationKeywords(task.Title, task.Description) {
				task.Type = TaskTypeValidation
			} else {
				// Default to implementation
				task.Type = TaskTypeImplementation
			}
		}
	}

	// Build plan
	plan := &Plan{
		FeatureName: featureName,
		Description: planData.Description,
		Tasks:       planData.Tasks,
	}

	if planData.Architecture != "" {
		plan.Context.Architecture = planData.Architecture
	}

	return plan, nil
}

// Helper functions

func detectProjectType(projectDir string) string {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		if wd, err := os.Getwd(); err == nil {
			projectDir = wd
		}
	}
	// Check for Go
	if _, err := os.Stat(filepath.Join(projectDir, "go.mod")); err == nil {
		return "go"
	}

	// Check for Node
	if _, err := os.Stat(filepath.Join(projectDir, "package.json")); err == nil {
		return "node"
	}

	// Check for Rust
	if _, err := os.Stat(filepath.Join(projectDir, "Cargo.toml")); err == nil {
		return "rust"
	}

	return "unknown"
}

func detectDependencies(projectDir string) []string {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		if wd, err := os.Getwd(); err == nil {
			projectDir = wd
		}
	}
	deps := []string{}

	// For Go projects
	goModPath := filepath.Join(projectDir, "go.mod")
	if _, err := os.Stat(goModPath); err == nil {
		content, err := os.ReadFile(goModPath)
		if err == nil {
			lines := strings.Split(string(content), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "github.com/") ||
					strings.HasPrefix(line, "golang.org/") {
					parts := strings.Fields(line)
					if len(parts) >= 1 {
						deps = append(deps, parts[0])
					}
				}
			}
		}
	}

	// Limit to top 5 most relevant
	if len(deps) > 5 {
		deps = deps[:5]
	}

	return deps
}

func getCurrentBranch(projectDir string) string {
	cmd := exec.Command("git", "branch", "--show-current")
	if strings.TrimSpace(projectDir) != "" {
		cmd.Dir = strings.TrimSpace(projectDir)
	}
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

func getRepoRoot(projectDir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	if strings.TrimSpace(projectDir) != "" {
		cmd.Dir = strings.TrimSpace(projectDir)
	}
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func getRemoteURL(projectDir string, remoteName string) string {
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		remoteName = "origin"
	}
	cmd := exec.Command("git", "remote", "get-url", remoteName)
	if strings.TrimSpace(projectDir) != "" {
		cmd.Dir = strings.TrimSpace(projectDir)
	}
	output, err := cmd.Output()
	if err != nil {
		cmd = exec.Command("git", "config", "--get", fmt.Sprintf("remote.%s.url", remoteName))
		if strings.TrimSpace(projectDir) != "" {
			cmd.Dir = strings.TrimSpace(projectDir)
		}
		output, err = cmd.Output()
		if err != nil {
			return ""
		}
	}
	return strings.TrimSpace(string(output))
}

func sanitizeRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.User = nil
	return strings.TrimSpace(u.String())
}

func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	// Remove non-alphanumeric except dashes
	var result strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// sanitizeJSONString fixes common invalid escape sequences from LLM responses
func sanitizeJSONString(jsonStr string) string {
	// First pass: fix common specific invalid escapes
	replacements := map[string]string{
		`\|`: `|`, // Vertical bar doesn't need escaping
		`\(`: `(`, // Parentheses don't need escaping
		`\)`: `)`,
		`\[`: `[`, // Brackets don't need escaping
		`\]`: `]`,
		`\{`: `{`, // Braces are only valid when actually escaping JSON structure
		`\}`: `}`,
		`\<`: `<`, // Angle brackets don't need escaping
		`\>`: `>`,
		`\-`: `-`, // Dash doesn't need escaping
		`\_`: `_`, // Underscore doesn't need escaping
		`\*`: `*`, // Asterisk doesn't need escaping
		`\#`: `#`, // Hash doesn't need escaping
		`\@`: `@`, // At symbol doesn't need escaping
		`\&`: `&`, // Ampersand doesn't need escaping in JSON strings
		`\=`: `=`, // Equals doesn't need escaping
		`\+`: `+`, // Plus doesn't need escaping
		`\:`: `:`, // Colon doesn't need escaping
		`\;`: `;`, // Semicolon doesn't need escaping
		`\!`: `!`, // Exclamation doesn't need escaping
		`\?`: `?`, // Question mark doesn't need escaping
		`\.`: `.`, // Period doesn't need escaping
		`\,`: `,`, // Comma doesn't need escaping (outside of JSON structure)
	}

	result := jsonStr
	for invalid, valid := range replacements {
		result = strings.ReplaceAll(result, invalid, valid)
	}

	// Second pass: catch any remaining invalid escapes using character class
	// Valid JSON escapes are: \" \\ \/ \b \f \n \r \t \uXXXX
	// Replace any \X where X is not one of these valid escape chars
	var cleaned strings.Builder
	i := 0
	for i < len(result) {
		if i < len(result)-1 && result[i] == '\\' {
			next := result[i+1]
			// Check if it's a valid JSON escape
			if next == '"' || next == '\\' || next == '/' ||
				next == 'b' || next == 'f' || next == 'n' ||
				next == 'r' || next == 't' || next == 'u' {
				// Valid escape, keep both characters
				cleaned.WriteByte(result[i])
				cleaned.WriteByte(next)
				i += 2
			} else {
				// Invalid escape, skip the backslash
				cleaned.WriteByte(next)
				i += 2
			}
		} else {
			cleaned.WriteByte(result[i])
			i++
		}
	}

	return cleaned.String()
}

// truncateString truncates a string to maxLen characters
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// containsAnalysisKeywords checks if task description indicates it's an analysis task
func containsAnalysisKeywords(title, description string) bool {
	text := strings.ToLower(title + " " + description)
	keywords := []string{
		"analyze", "analysis", "coverage", "report", "investigate",
		"examine", "check status", "run coverage", "gather", "collect",
		"measure", "metrics", "statistics", "scan", "detect",
	}
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// containsValidationKeywords checks if task description indicates it's a validation task
func containsValidationKeywords(title, description string) bool {
	text := strings.ToLower(title + " " + description)
	keywords := []string{
		"test", "validate", "verify", "check", "lint", "quality",
		"run tests", "ensure", "confirm", "assert", "review",
	}
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// SetPersonaProvider updates the planner to use a refreshed persona profile.
func (p *Planner) SetPersonaProvider(provider *personality.PersonaProvider) {
	if p == nil {
		return
	}
	p.personaProvider = provider
	useToon := false
	if p.config != nil {
		useToon = p.config.Encoding.UseToon
	}
	section := ""
	if provider != nil {
		section = provider.SectionForPhase("planning")
	}
	p.systemPrompt = defaultPlanningSystemPrompt(useToon, section)
}
