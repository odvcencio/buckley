package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/artifact"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/paths"
	"github.com/odvcencio/buckley/pkg/personality"
	"github.com/odvcencio/buckley/pkg/prompts"
	"github.com/odvcencio/buckley/pkg/tool"
)

// ReviewAgent delegates code review to a dedicated model and persists artifacts.
type ReviewAgent struct {
	plan            *Plan
	config          *config.Config
	modelClient     ModelClient
	toolRegistry    *tool.Registry
	workflow        *WorkflowManager
	reviewGen       *artifact.ReviewGenerator
	logger          *reviewLogger
	schemaBlock     string
	personaProvider *personality.PersonaProvider
	ctx             context.Context
}

// SetPersonaProvider swaps the persona provider for upcoming review cycles.
func (a *ReviewAgent) SetPersonaProvider(provider *personality.PersonaProvider) {
	if a == nil {
		return
	}
	a.personaProvider = provider
}

// ReviewResult captures the outcome of a review cycle.
type ReviewResult struct {
	Approved       bool
	Summary        string
	ApprovalStatus string
	Issues         []artifact.Issue
	ArtifactPath   string
	Artifact       *artifact.ReviewArtifact
	Response       reviewAgentResponse
}

var reviewSchemaTemplate = map[string]any{
	"summary": "short overall summary",
	"validation_strategy": map[string]any{
		"critical_path":   []string{"ordered list of review focus areas"},
		"high_risk_areas": []string{"edges discovered during execution"},
	},
	"validation_results": []map[string]any{
		{
			"category": "Security|Correctness|Conventions|Architecture|Performance|Tests",
			"status":   "pass|fail|concern",
			"checks": []map[string]any{
				{
					"name":        "check description",
					"status":      "pass|fail|concern",
					"description": "context for the check",
					"issue": map[string]any{
						"severity":    "critical|quality|nit",
						"category":    "Security|Correctness|...",
						"title":       "short title",
						"description": "what is wrong and why",
						"location":    "file.go:42",
						"fix":         "precise fix suggestion",
					},
				},
			},
		},
	},
	"issues": []map[string]any{
		{
			"severity":    "critical|quality|nit",
			"category":    "Security|Correctness|...",
			"title":       "issue title",
			"description": "full explanation",
			"location":    "file.go:42",
			"fix":         "clear remediation guidance",
		},
	},
	"opportunistic_improvements": []map[string]any{
		{
			"category":    "Codebase Quality|Architecture|Performance|Documentation",
			"title":       "future improvement",
			"observation": "what you noticed",
			"suggestion":  "what to do",
			"impact":      "effort/benefit commentary",
			"files":       []string{"related.go"},
		},
	},
	"approval": map[string]any{
		"status":         "approved|approved_with_nits|changes_requested",
		"summary":        "plain-English verdict",
		"ready_for_pr":   true,
		"remaining_work": []string{"nit descriptions if any"},
	},
}

func reviewSchemaBlock(useToon bool) string {
	block := renderSchema(reviewSchemaTemplate, useToon)
	if strings.TrimSpace(block) != "" {
		return block
	}
	return renderSchema(reviewSchemaTemplate, false)
}

// NewReviewAgent builds a reviewer for the given plan.
func NewReviewAgent(plan *Plan, cfg *config.Config, client ModelClient, registry *tool.Registry, workflow *WorkflowManager) *ReviewAgent {
	if plan == nil || cfg == nil || client == nil {
		return nil
	}

	if workflow != nil && workflow.feature == "" {
		workflow.feature = plan.FeatureName
	}

	outputDir := cfg.Artifacts.ReviewDir
	if strings.TrimSpace(outputDir) == "" {
		outputDir = filepath.Join("docs", "reviews")
	}

	useToon := cfg.Encoding.UseToon

	projectRoot := ""
	if workflow != nil {
		projectRoot = workflow.projectRoot
	}
	personaProvider := BuildPersonaProvider(cfg, projectRoot)
	if workflow != nil && workflow.PersonaProvider() != nil {
		personaProvider = workflow.PersonaProvider()
	}
	return &ReviewAgent{
		plan:            plan,
		config:          cfg,
		modelClient:     client,
		toolRegistry:    registry,
		workflow:        workflow,
		reviewGen:       artifact.NewReviewGenerator(outputDir),
		logger:          newReviewLogger(plan),
		schemaBlock:     reviewSchemaBlock(useToon),
		personaProvider: personaProvider,
		ctx:             context.Background(),
	}
}

func (a *ReviewAgent) baseContext() context.Context {
	if a == nil || a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

// SetContext updates the base context for review operations.
func (a *ReviewAgent) SetContext(ctx context.Context) {
	if a == nil || ctx == nil {
		return
	}
	a.ctx = ctx
}

// Review runs the review loop for a single task implementation.
func (a *ReviewAgent) Review(task *Task, builderResult *BuilderResult) (*ReviewResult, error) {
	if a == nil {
		return nil, fmt.Errorf("review agent not initialized")
	}
	if task == nil {
		return nil, fmt.Errorf("nil task provided to review agent")
	}
	if builderResult == nil {
		return nil, fmt.Errorf("builder result is required for review")
	}

	filePaths := a.combineFilePaths(task, builderResult)
	a.sendProgress("🕵️ Review agent evaluating %q (%d file(s))", task.Title, len(filePaths))
	contexts := a.loadFileContexts(filePaths)
	prompt := a.buildReviewPrompt(task, builderResult, contexts)

	var personaProfile *personality.PersonaProfile
	if a.personaProvider != nil {
		personaProfile = a.personaProvider.PersonaForPhase("reviewer")
		if personaProfile == nil {
			personaProfile = a.personaProvider.PersonaForPhase(string(prompts.PhaseReview))
		}
	}
	systemPrompt := prompts.ReviewPrompt(time.Now(), personaProfile)
	reqCtx, cancel := context.WithTimeout(a.baseContext(), 90*time.Second)
	defer cancel()

	req := model.ChatRequest{
		Model: a.config.Models.Review,
		Messages: []model.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.2,
	}

	if a.modelClient.SupportsReasoning(a.config.Models.Review) {
		req.Reasoning = &model.ReasoningConfig{Effort: "high"}
	}

	a.logEvent(task.ID, reviewEventStart, map[string]string{
		"files": fmt.Sprintf("%d", len(filePaths)),
	})
	a.logEvent(task.ID, reviewEventPrompt, map[string]string{
		"preview": truncateForLog(prompt, 400),
	})

	resp, err := a.modelClient.ChatCompletion(reqCtx, req)
	if err != nil {
		a.logFailure(task.ID, err)
		return nil, fmt.Errorf("review request failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("review model returned no choices")
	}

	content, err := model.ExtractTextContent(resp.Choices[0].Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to extract review content: %w", err)
	}

	parsed, err := parseReviewResponse(content)
	if err != nil {
		a.logFailure(task.ID, err)
		return nil, fmt.Errorf("failed to parse review response: %w", err)
	}

	now := time.Now()
	artifactPayload := a.buildArtifact(parsed, now)

	var artifactPath string
	if a.workflow == nil {
		path, err := a.reviewGen.Generate(artifactPayload)
		if err != nil {
			a.logFailure(task.ID, err)
			return nil, fmt.Errorf("failed to write review artifact: %w", err)
		}
		artifactPath = path
	} else {
		artifactPayload.FilePath = ""
	}

	allowNits := a.config.Workflow.AllowNitsInApproval

	approved := determineApproval(parsed, allowNits)
	summary := strings.TrimSpace(parsed.Summary)
	if summary == "" {
		summary = strings.TrimSpace(parsed.Approval.Summary)
	}
	if summary == "" {
		summary = "Review completed. See artifact for details."
	}

	result := &ReviewResult{
		Approved:       approved,
		Summary:        summary,
		ApprovalStatus: parsed.Approval.Status,
		Issues:         convertIssues(parsed.Issues),
		ArtifactPath:   artifactPath,
		Artifact:       artifactPayload,
		Response:       parsed,
	}

	details := map[string]string{
		"status": parsed.Approval.Status,
		"issues": fmt.Sprintf("%d", len(parsed.Issues)),
	}
	if artifactPath != "" {
		details["artifact"] = filepath.Base(artifactPath)
	}
	a.logEvent(task.ID, reviewEventCompleted, details)
	if approved {
		a.sendProgress("✅ Review approved %q (%s)", task.Title, parsed.Approval.Status)
	} else {
		a.sendProgress("⚠️ Review flagged %d issue(s) for %q (%s)", len(parsed.Issues), task.Title, parsed.Approval.Status)
	}

	return result, nil
}

func (a *ReviewAgent) sendProgress(format string, args ...any) {
	if a == nil || a.workflow == nil {
		return
	}
	a.workflow.SendProgress(fmt.Sprintf(format, args...))
}

func (a *ReviewAgent) combineFilePaths(task *Task, result *BuilderResult) []string {
	paths := map[string]struct{}{}
	for _, file := range task.Files {
		file = strings.TrimSpace(file)
		if file != "" {
			paths[file] = struct{}{}
		}
	}

	for _, file := range result.Files {
		if strings.TrimSpace(file.Path) != "" {
			paths[file.Path] = struct{}{}
		}
	}

	combined := make([]string, 0, len(paths))
	for path := range paths {
		combined = append(combined, path)
	}

	sort.Strings(combined)
	return combined
}

func (a *ReviewAgent) loadFileContexts(paths []string) []reviewFileContext {
	if len(paths) == 0 {
		return nil
	}

	var readTool tool.Tool
	if a.toolRegistry != nil {
		readTool, _ = a.toolRegistry.Get("read_file")
	}
	contexts := make([]reviewFileContext, 0, len(paths))

	for _, path := range paths {
		content := ""
		if readTool != nil {
			if result, err := readTool.Execute(map[string]any{"path": path}); err == nil && result.Success {
				if text, ok := result.Data["content"].(string); ok {
					content = text
				}
			}
		}

		if content == "" {
			data, err := os.ReadFile(path)
			if err != nil {
				content = fmt.Sprintf("Unable to read %s: %v", path, err)
			} else {
				content = string(data)
			}
		}

		contexts = append(contexts, reviewFileContext{
			Path:    path,
			Content: truncateContent(content, 8000, 400),
		})
	}

	return contexts
}

func (a *ReviewAgent) buildReviewPrompt(task *Task, result *BuilderResult, contexts []reviewFileContext) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Feature: %s\n", a.plan.FeatureName))
	b.WriteString(fmt.Sprintf("Plan Description: %s\n\n", a.plan.Description))

	b.WriteString(fmt.Sprintf("Task %s – %s\n", task.ID, task.Title))
	b.WriteString(fmt.Sprintf("Description: %s\n", task.Description))
	if len(task.Verification) > 0 {
		b.WriteString("\nVerification targets:\n")
		for _, step := range task.Verification {
			b.WriteString(fmt.Sprintf("- %s\n", step))
		}
	}

	if len(result.Files) > 0 {
		b.WriteString("\nFiles touched by builder:\n")
		for _, file := range result.Files {
			b.WriteString(fmt.Sprintf("- %s (approx +%d lines)\n", file.Path, file.LinesAdded))
		}
	}

	if strings.TrimSpace(result.Implementation) != "" {
		b.WriteString("\nBuilder implementation proposal:\n")
		b.WriteString(truncateContent(result.Implementation, 6000, 200))
		b.WriteString("\n")
	}

	if len(contexts) > 0 {
		b.WriteString("\nModified file contents (abridged):\n")
		for _, ctx := range contexts {
			b.WriteString(fmt.Sprintf("\n--- %s ---\n%s\n", ctx.Path, ctx.Content))
		}
	} else {
		b.WriteString("\nNo file contents could be loaded. Reason about the task description and builder implementation only.\n")
	}

	b.WriteString("\nRespond STRICTLY with JSON matching this schema (no markdown fences):\n")
	schema := a.schemaBlock
	if strings.TrimSpace(schema) == "" {
		schema = reviewSchemaBlock(a.config.Encoding.UseToon)
	}
	b.WriteString(schema)
	b.WriteString("\n")
	return b.String()
}

func (a *ReviewAgent) buildArtifact(resp reviewAgentResponse, now time.Time) *artifact.ReviewArtifact {
	planningPath := a.planArtifactPath()
	executionPath := a.executionArtifactPath()

	artifactPayload := &artifact.ReviewArtifact{
		Artifact: artifact.Artifact{
			Type:      artifact.ArtifactTypeReview,
			Feature:   a.plan.FeatureName,
			CreatedAt: now,
			UpdatedAt: now,
			Status:    defaultApprovalStatus(resp.Approval.Status),
		},
		PlanningArtifactPath:  planningPath,
		ExecutionArtifactPath: executionPath,
		ReviewedAt:            now,
		ReviewerModel:         a.config.Models.Review,
		ValidationStrategy: artifact.ValidationStrategy{
			CriticalPath:  append([]string{}, resp.ValidationStrategy.CriticalPath...),
			HighRiskAreas: append([]string{}, resp.ValidationStrategy.HighRiskAreas...),
		},
		ValidationResults:         convertValidationResults(resp.ValidationResults),
		IssuesFound:               convertIssues(resp.Issues),
		Iterations:                []artifact.ReviewIteration{{Number: 1, Timestamp: now, IssuesFound: len(resp.Issues), Status: defaultApprovalStatus(resp.Approval.Status), Notes: resp.Summary}},
		OpportunisticImprovements: convertImprovements(resp.OpportunisticImprovements),
	}

	if resp.Approval.Status != "" || resp.Approval.Summary != "" {
		remaining := resp.Approval.RemainingWork
		if len(remaining) == 0 {
			for _, issue := range resp.Issues {
				if strings.EqualFold(issue.Severity, "nit") {
					remaining = append(remaining, issue.Description)
				}
			}
		}
		artifactPayload.Approval = &artifact.Approval{
			Status:        defaultApprovalStatus(resp.Approval.Status),
			Timestamp:     now,
			RemainingWork: remaining,
			ReadyForPR:    resp.Approval.ReadyForPR,
			Summary:       resp.Approval.Summary,
		}
	}

	return artifactPayload
}

func (a *ReviewAgent) planArtifactPath() string {
	dir := a.config.Artifacts.PlanningDir
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join("docs", "plans")
	}

	fileName := a.plan.ID
	if strings.TrimSpace(fileName) == "" {
		fileName = SanitizeIdentifier(a.plan.FeatureName)
	}
	if fileName == "" {
		fileName = "plan"
	}
	return filepath.Join(dir, fmt.Sprintf("%s.md", fileName))
}

func (a *ReviewAgent) executionArtifactPath() string {
	if a.workflow != nil && a.workflow.executionTracker != nil {
		if path := a.workflow.executionTracker.GetFilePath(); path != "" {
			return path
		}
	}
	dir := a.config.Artifacts.ExecutionDir
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join("docs", "execution")
	}
	return dir
}

type reviewFileContext struct {
	Path    string
	Content string
}

type reviewAgentResponse struct {
	Summary                   string                     `json:"summary"`
	ValidationStrategy        reviewStrategyPayload      `json:"validation_strategy"`
	ValidationResults         []reviewResultPayload      `json:"validation_results"`
	Issues                    []reviewIssuePayload       `json:"issues"`
	OpportunisticImprovements []reviewImprovementPayload `json:"opportunistic_improvements"`
	Approval                  reviewApprovalPayload      `json:"approval"`
}

type reviewStrategyPayload struct {
	CriticalPath  []string `json:"critical_path"`
	HighRiskAreas []string `json:"high_risk_areas"`
}

type reviewResultPayload struct {
	Category string               `json:"category"`
	Status   string               `json:"status"`
	Checks   []reviewCheckPayload `json:"checks"`
}

type reviewCheckPayload struct {
	Name        string              `json:"name"`
	Status      string              `json:"status"`
	Description string              `json:"description"`
	Issue       *reviewIssuePayload `json:"issue"`
}

type reviewIssuePayload struct {
	Severity    string `json:"severity"`
	Category    string `json:"category"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Location    string `json:"location"`
	Fix         string `json:"fix"`
}

type reviewImprovementPayload struct {
	Category    string   `json:"category"`
	Title       string   `json:"title"`
	Observation string   `json:"observation"`
	Suggestion  string   `json:"suggestion"`
	Impact      string   `json:"impact"`
	Files       []string `json:"files"`
}

type reviewApprovalPayload struct {
	Status        string   `json:"status"`
	Summary       string   `json:"summary"`
	RemainingWork []string `json:"remaining_work"`
	ReadyForPR    bool     `json:"ready_for_pr"`
}

func parseReviewResponse(content string) (reviewAgentResponse, error) {
	var parsed reviewAgentResponse
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```JSON")
		trimmed = strings.Trim(trimmed, "`")
		trimmed = strings.TrimSpace(trimmed)
		if strings.HasSuffix(trimmed, "```") {
			trimmed = strings.TrimSuffix(trimmed, "```")
		}
	}

	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
		return parsed, nil
	}

	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		sub := trimmed[start : end+1]
		if err := json.Unmarshal([]byte(sub), &parsed); err == nil {
			return parsed, nil
		}
	}

	return parsed, fmt.Errorf("unable to parse review JSON")
}

func convertIssues(issues []reviewIssuePayload) []artifact.Issue {
	result := make([]artifact.Issue, 0, len(issues))
	for i, issue := range issues {
		result = append(result, artifact.Issue{
			ID:          i + 1,
			Severity:    strings.ToLower(issue.Severity),
			Category:    issue.Category,
			Title:       issue.Title,
			Description: issue.Description,
			Location:    issue.Location,
			Fix:         issue.Fix,
		})
	}
	return result
}

func convertValidationResults(results []reviewResultPayload) []artifact.ValidationResult {
	out := make([]artifact.ValidationResult, 0, len(results))
	for _, res := range results {
		checks := make([]artifact.ValidationCheck, 0, len(res.Checks))
		for _, check := range res.Checks {
			var issue *artifact.Issue
			if check.Issue != nil {
				converted := convertIssues([]reviewIssuePayload{*check.Issue})
				if len(converted) > 0 {
					issue = &converted[0]
				}
			}
			checks = append(checks, artifact.ValidationCheck{
				Name:        check.Name,
				Status:      strings.ToLower(check.Status),
				Description: check.Description,
				Issue:       issue,
			})
		}
		out = append(out, artifact.ValidationResult{
			Category: res.Category,
			Status:   strings.ToLower(res.Status),
			Checks:   checks,
		})
	}
	return out
}

func convertImprovements(items []reviewImprovementPayload) []artifact.Improvement {
	result := make([]artifact.Improvement, 0, len(items))
	for _, item := range items {
		result = append(result, artifact.Improvement{
			Category:    item.Category,
			Title:       item.Title,
			Observation: item.Observation,
			Suggestion:  item.Suggestion,
			Impact:      item.Impact,
			Files:       append([]string{}, item.Files...),
		})
	}
	return result
}

func determineApproval(resp reviewAgentResponse, allowNits bool) bool {
	status := strings.ToLower(resp.Approval.Status)
	switch status {
	case "approved":
		return true
	case "approved_with_nits":
		return allowNits
	case "changes_requested":
		return false
	}

	hasCritical := false
	hasQuality := false
	for _, issue := range resp.Issues {
		switch strings.ToLower(issue.Severity) {
		case "critical":
			hasCritical = true
		case "quality":
			hasQuality = true
		}
	}

	if hasCritical || hasQuality {
		return false
	}

	if len(resp.Issues) == 0 {
		return true
	}

	// Only nits left
	return allowNits
}

func truncateContent(value string, maxChars int, maxLines int) string {
	if value == "" {
		return value
	}

	lines := strings.Split(value, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], fmt.Sprintf("... (%d more lines truncated)", len(lines)-maxLines))
	}

	result := strings.Join(lines, "\n")
	if len(result) > maxChars {
		result = result[:maxChars] + "\n... (content truncated)"
	}
	return result
}

func truncateForLog(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}

func defaultApprovalStatus(status string) string {
	switch strings.ToLower(status) {
	case "approved", "approved_with_nits":
		return strings.ToLower(status)
	case "changes_requested":
		return "changes_requested"
	default:
		return "changes_requested"
	}
}

// --- Telemetry logging ------------------------------------------------------

type reviewEventType string

const (
	reviewEventStart     reviewEventType = "review.start"
	reviewEventPrompt    reviewEventType = "review.prompt"
	reviewEventCompleted reviewEventType = "review.completed"
	reviewEventFailed    reviewEventType = "review.failed"
)

type reviewEvent struct {
	Timestamp time.Time         `json:"timestamp"`
	PlanID    string            `json:"plan_id,omitempty"`
	TaskID    string            `json:"task_id,omitempty"`
	Type      reviewEventType   `json:"type"`
	Details   map[string]string `json:"details,omitempty"`
}

type reviewLogger struct {
	path string
	mu   sync.Mutex
}

func newReviewLogger(plan *Plan) *reviewLogger {
	if plan == nil {
		return nil
	}

	identifier := SanitizeIdentifier(plan.ID)
	if identifier == "" {
		identifier = SanitizeIdentifier(plan.FeatureName)
	}
	if identifier == "" {
		identifier = "default"
	}

	logDir := paths.BuckleyLogsDir(identifier)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil
	}

	return &reviewLogger{
		path: filepath.Join(logDir, "review.jsonl"),
	}
}

func (l *reviewLogger) record(event reviewEvent) {
	if l == nil || l.path == "" {
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = f.Write(append(data, '\n'))
}

func (a *ReviewAgent) logEvent(taskID string, eventType reviewEventType, details map[string]string) {
	if a == nil || a.logger == nil {
		return
	}
	a.logger.record(reviewEvent{
		Timestamp: time.Now(),
		PlanID:    safePlanID(a.plan),
		TaskID:    taskID,
		Type:      eventType,
		Details:   details,
	})
}

func (a *ReviewAgent) logFailure(taskID string, err error) {
	a.logEvent(taskID, reviewEventFailed, map[string]string{
		"error": err.Error(),
	})
}
