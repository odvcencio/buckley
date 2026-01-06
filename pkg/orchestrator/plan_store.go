package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/odvcencio/buckley/pkg/paths"
)

// PlanStore defines persistence helpers for plans and related logs.
//
//go:generate mockgen -package=orchestrator -destination=mock_plan_store_test.go github.com/odvcencio/buckley/pkg/orchestrator PlanStore
type PlanStore interface {
	SavePlan(plan *Plan) error
	LoadPlan(planID string) (*Plan, error)
	ListPlans() ([]Plan, error)
	ReadLog(planID string, logKind string, limit int) ([]string, string, error)
}

// FilePlanStore persists plans as JSON/Markdown files under docs/plans.
type FilePlanStore struct {
	planDir string
}

// NewFilePlanStore constructs a file-backed plan store rooted at planDir.
func NewFilePlanStore(planDir string) *FilePlanStore {
	if strings.TrimSpace(planDir) == "" {
		planDir = filepath.Join("docs", "plans")
	}
	return &FilePlanStore{planDir: planDir}
}

func (s *FilePlanStore) ensureDir() error {
	return os.MkdirAll(s.planDir, 0o755)
}

// SavePlan writes both JSON + Markdown plan artifacts.
func (s *FilePlanStore) SavePlan(plan *Plan) error {
	if plan == nil {
		return fmt.Errorf("plan is nil")
	}
	if err := s.ensureDir(); err != nil {
		return fmt.Errorf("failed to create plans directory: %w", err)
	}

	assignPlanLogs(plan)

	jsonPath := filepath.Join(s.planDir, plan.ID+".json")
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal plan: %w", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write JSON plan: %w", err)
	}

	mdPath := filepath.Join(s.planDir, plan.ID+".md")
	content, err := renderPlanTemplate(plan)
	if err != nil {
		return fmt.Errorf("failed to render plan template: %w", err)
	}
	if err := os.WriteFile(mdPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write markdown plan: %w", err)
	}

	return nil
}

// LoadPlan reads a plan from the plan directory.
func (s *FilePlanStore) LoadPlan(planID string) (*Plan, error) {
	if strings.TrimSpace(planID) == "" {
		return nil, fmt.Errorf("plan id required")
	}
	path := filepath.Join(s.planDir, planID+".json")
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read plan: %w", err)
	}
	var plan Plan
	if err := json.Unmarshal(content, &plan); err != nil {
		return nil, fmt.Errorf("failed to parse plan: %w", err)
	}
	return &plan, nil
}

// ListPlans returns every saved plan under the plan directory.
func (s *FilePlanStore) ListPlans() ([]Plan, error) {
	if _, err := os.Stat(s.planDir); os.IsNotExist(err) {
		return []Plan{}, nil
	}
	entries, err := os.ReadDir(s.planDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read plans directory: %w", err)
	}
	var plans []Plan
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		planID := strings.TrimSuffix(entry.Name(), ".json")
		plan, err := s.LoadPlan(planID)
		if err != nil {
			continue
		}
		plans = append(plans, *plan)
	}
	return plans, nil
}

// ReadLog returns log entries for builder/review/research.
func (s *FilePlanStore) ReadLog(planID string, logKind string, limit int) ([]string, string, error) {
	plan, err := s.LoadPlan(planID)
	if err != nil {
		return nil, "", err
	}
	logPath, err := resolvePlanLogPath(plan, logKind)
	if err != nil {
		return nil, "", err
	}
	entries, err := readLogTail(logPath, limit)
	if err != nil {
		return nil, "", err
	}
	return entries, logPath, nil
}

func assignPlanLogs(plan *Plan) {
	if plan == nil {
		return
	}
	logDir := logsDirectoryForPlan(plan)
	now := time.Now()
	plan.Logs = PlanLogs{
		BaseDir:     logDir,
		BuilderLog:  filepath.Join(logDir, "builder.jsonl"),
		ReviewLog:   filepath.Join(logDir, "review.jsonl"),
		ResearchLog: filepath.Join(logDir, "research.jsonl"),
		UpdatedAt:   now,
	}
	if plan.Context.ResearchLogPath == "" {
		plan.Context.ResearchLogPath = plan.Logs.ResearchLog
	}
	if plan.Context.ResearchLoggedAt.IsZero() {
		plan.Context.ResearchLoggedAt = now
	}
}

func logsDirectoryForPlan(plan *Plan) string {
	identifier := SanitizeIdentifier(plan.ID)
	if identifier == "" {
		identifier = SanitizeIdentifier(plan.FeatureName)
	}
	if identifier == "" {
		identifier = "default"
	}
	return paths.BuckleyLogsDir(identifier)
}

func renderPlanTemplate(plan *Plan) (string, error) {
	type TemplateData struct {
		*Plan
		CompletedCount int
		RemainingCount int
	}
	completed := 0
	for _, task := range plan.Tasks {
		if task.Status == TaskCompleted {
			completed++
		}
	}
	data := TemplateData{
		Plan:           plan,
		CompletedCount: completed,
		RemainingCount: len(plan.Tasks) - completed,
	}
	tmpl, err := template.New("plan").Parse(planTemplateContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}
	return buf.String(), nil
}

func resolvePlanLogPath(plan *Plan, kind string) (string, error) {
	if plan == nil {
		return "", fmt.Errorf("plan not found")
	}
	if plan.Logs.BaseDir == "" {
		plan.Logs.BaseDir = logsDirectoryForPlan(plan)
	}
	switch kind {
	case "builder":
		if plan.Logs.BuilderLog != "" {
			return plan.Logs.BuilderLog, nil
		}
	case "review":
		if plan.Logs.ReviewLog != "" {
			return plan.Logs.ReviewLog, nil
		}
	case "research":
		if plan.Logs.ResearchLog != "" {
			return plan.Logs.ResearchLog, nil
		}
	default:
		return "", fmt.Errorf("unknown log kind: %s", kind)
	}
	return "", fmt.Errorf("log path not recorded for %s", kind)
}

func readLogTail(path string, limit int) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return []string{}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return []string{}, nil
	}
	lines := strings.Split(content, "\n")
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines, nil
}
