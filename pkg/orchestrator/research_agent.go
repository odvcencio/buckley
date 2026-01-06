package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/artifact"
	"github.com/odvcencio/buckley/pkg/encoding/toon"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/paths"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

// ResearchAgent builds research briefs before planning/execution.
type ResearchAgent struct {
	modelClient  ModelClient
	store        *storage.Store
	writer       *artifact.ResearchGenerator
	projectRoot  string
	summaryModel string
	loggers      map[string]*researchLogger
	workflow     *WorkflowManager
	useToon      bool
	notesCodec   *toon.Codec
}

// NewResearchAgent constructs an agent.
func NewResearchAgent(store *storage.Store, client ModelClient, projectRoot, outputDir, summaryModel string, workflow *WorkflowManager, useToon bool) *ResearchAgent {
	if store == nil || client == nil {
		return nil
	}
	if outputDir == "" {
		outputDir = filepath.Join("docs", "research")
	}
	writer := artifact.NewResearchGenerator(outputDir)
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}
	if summaryModel == "" {
		summaryModel = "planning"
	}
	return &ResearchAgent{
		modelClient:  client,
		store:        store,
		writer:       writer,
		projectRoot:  projectRoot,
		summaryModel: summaryModel,
		loggers:      make(map[string]*researchLogger),
		workflow:     workflow,
		useToon:      useToon,
		notesCodec:   toon.New(useToon),
	}
}

// Run executes research based on user goal.
func (r *ResearchAgent) Run(ctx context.Context, featureName, userGoal string) (*artifact.ResearchBrief, error) {
	r.logEvent(featureName, researchEventStart, map[string]string{
		"goal": strings.TrimSpace(userGoal),
	})
	r.sendProgress("🔬 Research agent analyzing %q", featureName)
	if r.workflow != nil {
		r.workflow.EmitResearchEvent(featureName, telemetry.EventResearchStarted, map[string]any{
			"goal": strings.TrimSpace(userGoal),
		})
	}

	files, err := r.store.SearchFiles(ctx, userGoal, "", 5)
	if err != nil {
		r.logEvent(featureName, researchEventFailed, map[string]string{
			"error": err.Error(),
		})
		r.sendProgress("⚠️ Research agent failed: %v", err)
		if r.workflow != nil {
			r.workflow.EmitResearchEvent(featureName, telemetry.EventResearchFailed, map[string]any{
				"error": err.Error(),
			})
		}
		return nil, err
	}
	r.sendProgress("🗂️ Research agent queued %d relevant file(s)", len(files))

	relevant := make([]artifact.RelevantFile, 0, len(files))
	for i, f := range files {
		r.sendProgress("  • Summarizing %s (%d/%d)", f.Path, i+1, len(files))
		summary := r.summarizeFile(ctx, f.Path)
		r.logEvent(featureName, researchEventFileSummarized, map[string]string{
			"path": f.Path,
		})
		relevant = append(relevant, artifact.RelevantFile{
			Path:    f.Path,
			Reason:  "Potentially impacted file",
			Summary: summary,
		})
	}

	insights := r.generateInsights(ctx, userGoal, relevant)
	r.logEvent(featureName, researchEventInsights, map[string]string{
		"risks":     fmt.Sprintf("%d", len(insights.Risks)),
		"decisions": fmt.Sprintf("%d", len(insights.Decisions)),
		"questions": fmt.Sprintf("%d", len(insights.Questions)),
	})
	r.sendProgress("🧠 Research insights ready (%d risk(s), %d question(s))", len(insights.Risks), len(insights.Questions))

	questions := make([]artifact.ResearchQuestion, 0, len(insights.Questions))
	for _, q := range insights.Questions {
		questions = append(questions, artifact.ResearchQuestion{
			Question: q.Question,
			Answer:   q.Answer,
		})
	}

	brief := &artifact.ResearchBrief{
		Feature:       featureName,
		UserGoal:      userGoal,
		RelevantFiles: relevant,
		Questions:     questions,
		Risks:         insights.Risks,
		Decisions:     insights.Decisions,
		Summary:       insights.Summary,
	}

	path, err := r.writer.Generate(brief)
	if err != nil {
		r.logEvent(featureName, researchEventFailed, map[string]string{
			"error": err.Error(),
		})
		r.sendProgress("⚠️ Failed to write research brief: %v", err)
		if r.workflow != nil {
			r.workflow.EmitResearchEvent(featureName, telemetry.EventResearchFailed, map[string]any{
				"error": err.Error(),
			})
		}
		return nil, err
	}
	r.sendProgress("🗒️ Research brief saved to %s", filepath.Base(path))

	r.logEvent(featureName, researchEventCompleted, map[string]string{
		"files":     fmt.Sprintf("%d", len(relevant)),
		"questions": fmt.Sprintf("%d", len(questions)),
	})
	r.sendProgress("✅ Research agent completed for %q", featureName)
	if r.workflow != nil {
		r.workflow.EmitResearchEvent(featureName, telemetry.EventResearchCompleted, map[string]any{
			"files":     len(relevant),
			"questions": len(questions),
			"risks":     len(insights.Risks),
		})
	}

	return brief, nil
}

func (r *ResearchAgent) summarizeFile(ctx context.Context, relPath string) string {
	fullPath := filepath.Join(r.projectRoot, relPath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Sprintf("Unable to read file: %v", err)
	}

	content := string(data)
	if len(content) > 6000 {
		content = content[:6000] + "\n..."
	}

	resp, err := r.callModel(ctx, fmt.Sprintf(`Summarize the following Go source file and describe why it might matter for the requested feature.
Output 2 short bullet points.

File: %s
Content:
"""%s"""`, relPath, content))
	if err != nil {
		return contentSnippet(content, 400)
	}
	return resp
}

func (r *ResearchAgent) generateInsights(ctx context.Context, goal string, files []artifact.RelevantFile) insights {
	if len(files) == 0 {
		return insights{
			Summary: fmt.Sprintf("Research completed for goal: %s", goal),
		}
	}

	plainNotes := formatPlainNotes(files)
	notesBlock := plainNotes
	if r.useToon && r.notesCodec != nil {
		if encoded, err := r.notesCodec.Marshal(newNoteEntries(files)); err == nil {
			notesBlock = string(encoded)
		}
	}

	prompt := fmt.Sprintf(`You are preparing research notes before coding.
Given the user's goal and early file summaries, answer in compact JSON:
{
  "summary": "...",
  "risks": ["..."],
  "decisions": ["..."],
  "questions": [{"question": "...", "answer": "..."}]
}

Goal: %s
Notes:
%s`, goal, notesBlock)

	resp, err := r.callModel(ctx, prompt)
	if err != nil {
		return insights{Summary: plainNotes}
	}

	var parsed insights
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		return insights{Summary: resp}
	}
	return parsed
}

func newNoteEntries(files []artifact.RelevantFile) []map[string]string {
	entries := make([]map[string]string, 0, len(files))
	for _, file := range files {
		entries = append(entries, map[string]string{
			"path":    file.Path,
			"summary": file.Summary,
		})
	}
	return entries
}

func formatPlainNotes(files []artifact.RelevantFile) string {
	var builder strings.Builder
	for _, file := range files {
		builder.WriteString(fmt.Sprintf("- %s: %s\n", file.Path, file.Summary))
	}
	return builder.String()
}

func (r *ResearchAgent) callModel(ctx context.Context, prompt string) (string, error) {
	req := model.ChatRequest{
		Model: r.summaryModel,
		Messages: []model.Message{
			{Role: "system", Content: "You are a senior software research assistant."},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.2,
	}
	resp, err := r.modelClient.ChatCompletion(ctx, req)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from model")
	}
	return model.ExtractTextContent(resp.Choices[0].Message.Content)
}

func contentSnippet(content string, max int) string {
	if len(content) <= max {
		return content
	}
	return content[:max] + "..."
}

type insights struct {
	Summary   string            `json:"summary"`
	Risks     []string          `json:"risks"`
	Decisions []string          `json:"decisions"`
	Questions []insightQuestion `json:"questions"`
}

type insightQuestion struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type researchEventType string

const (
	researchEventStart          researchEventType = "research.start"
	researchEventFileSummarized researchEventType = "research.file.summarized"
	researchEventInsights       researchEventType = "research.insights"
	researchEventCompleted      researchEventType = "research.completed"
	researchEventFailed         researchEventType = "research.failed"
)

type researchEvent struct {
	Timestamp time.Time         `json:"timestamp"`
	Feature   string            `json:"feature"`
	Type      researchEventType `json:"type"`
	Details   map[string]string `json:"details,omitempty"`
}

type researchLogger struct {
	path string
	mu   sync.Mutex
}

func (r *ResearchAgent) logEvent(feature string, eventType researchEventType, details map[string]string) {
	logger := r.loggerFor(feature)
	if logger == nil {
		return
	}

	logger.record(researchEvent{
		Timestamp: time.Now(),
		Feature:   feature,
		Type:      eventType,
		Details:   details,
	})
}

func (r *ResearchAgent) loggerFor(feature string) *researchLogger {
	if r == nil {
		return nil
	}
	if r.loggers == nil {
		r.loggers = make(map[string]*researchLogger)
	}

	identifier := SanitizeIdentifier(feature)
	if identifier == "" {
		identifier = "default"
	}

	if logger, ok := r.loggers[identifier]; ok {
		return logger
	}

	logDir := paths.BuckleyLogsDir(identifier)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil
	}

	logger := &researchLogger{
		path: filepath.Join(logDir, "research.jsonl"),
	}
	r.loggers[identifier] = logger
	return logger
}

func (r *ResearchAgent) sendProgress(format string, args ...any) {
	if r == nil || r.workflow == nil {
		return
	}
	r.workflow.SendProgress(fmt.Sprintf(format, args...))
}

func (l *researchLogger) record(event researchEvent) {
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
