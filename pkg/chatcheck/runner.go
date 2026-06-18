package chatcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/model"
)

const (
	DefaultModel   = "xiaomi/mimo-v2.5-pro"
	defaultTimeout = 45 * time.Second
)

type Scenario struct {
	Description  string        `json:"description,omitempty"`
	Name         string        `json:"name,omitempty"`
	Tags         []string      `json:"tags,omitempty"`
	Model        string        `json:"model,omitempty"`
	SystemPrompt string        `json:"system_prompt,omitempty"`
	Turns        []Turn        `json:"turns,omitempty"`
	Timeout      time.Duration `json:"-"`
	MaxTokens    int           `json:"max_tokens,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
}

type Turn struct {
	User            string   `json:"user"`
	WantContains    []string `json:"want_contains,omitempty"`
	WantNotContains []string `json:"want_not_contains,omitempty"`
	WantRegex       []string `json:"want_regex,omitempty"`
	MinChars        int      `json:"min_chars,omitempty"`
	MaxChars        int      `json:"max_chars,omitempty"`
	MaxToolCalls    *int     `json:"max_tool_calls,omitempty"`
}

type Result struct {
	Name           string       `json:"name"`
	Model          string       `json:"model"`
	SessionID      string       `json:"session_id"`
	Passed         bool         `json:"passed"`
	Error          string       `json:"error,omitempty"`
	StartedAt      time.Time    `json:"started_at"`
	CompletedAt    time.Time    `json:"completed_at"`
	DurationMillis int64        `json:"duration_ms"`
	Usage          model.Usage  `json:"usage"`
	Turns          []TurnResult `json:"turns"`
}

type SuiteResult struct {
	Name            string      `json:"name"`
	Passed          bool        `json:"passed"`
	Error           string      `json:"error,omitempty"`
	StartedAt       time.Time   `json:"started_at"`
	CompletedAt     time.Time   `json:"completed_at"`
	DurationMillis  int64       `json:"duration_ms"`
	Usage           model.Usage `json:"usage"`
	ScenarioCount   int         `json:"scenario_count"`
	PassedScenarios int         `json:"passed_scenarios"`
	FailedScenarios int         `json:"failed_scenarios"`
	Results         []Result    `json:"results"`
}

type TurnResult struct {
	Index         int           `json:"index"`
	User          string        `json:"user"`
	Text          string        `json:"text"`
	Model         string        `json:"model"`
	Latency       time.Duration `json:"-"`
	LatencyMillis int64         `json:"latency_ms"`
	Usage         model.Usage   `json:"usage"`
	Finish        string        `json:"finish,omitempty"`
	Err           string        `json:"error,omitempty"`
	ToolCalls     int           `json:"tool_calls"`
	Reasoning     bool          `json:"reasoning"`
	CharLength    int           `json:"char_length"`
	Passed        bool          `json:"passed"`
	Checks        []CheckResult `json:"checks,omitempty"`
}

type CheckResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message,omitempty"`
}

type Runner struct {
	Client model.CompletionClient
}

type ScenarioSelector struct {
	NameContains []string
	Tags         []string
}

type scenarioFile struct {
	Description   string   `json:"description"`
	Name          string   `json:"name"`
	Tags          []string `json:"tags"`
	Model         string   `json:"model"`
	SystemPrompt  string   `json:"system_prompt"`
	Turns         []Turn   `json:"turns"`
	Timeout       string   `json:"timeout"`
	TimeoutMillis int64    `json:"timeout_ms"`
	MaxTokens     int      `json:"max_tokens"`
	SessionID     string   `json:"session_id"`
}

func DefaultScenario(modelID string) Scenario {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		modelID = DefaultModel
	}
	return Scenario{
		Description: "Built-in Buckley multi-turn chat continuity check.",
		Name:        "multi-turn-chat",
		Tags:        []string{"chat", "smoke"},
		Model:       modelID,
		Timeout:     defaultTimeout,
		MaxTokens:   256,
		SessionID:   "buckley-chat-check",
		SystemPrompt: strings.Join([]string{
			"You are participating in a Buckley chat health check.",
			"Answer plainly and include requested sentinel tokens exactly.",
		}, " "),
		Turns: []Turn{
			{
				User:         "Reply with the exact token BUCKLEY_CHAT_CHECK_ONE and no markdown.",
				WantContains: []string{"BUCKLEY_CHAT_CHECK_ONE"},
				MinChars:     8,
			},
			{
				User:         "Name the exact token requested in the previous user message, then include BUCKLEY_CHAT_CHECK_TWO.",
				WantContains: []string{"BUCKLEY_CHAT_CHECK_ONE", "BUCKLEY_CHAT_CHECK_TWO"},
				MinChars:     16,
			},
		},
	}
}

func LoadScenarioFile(path string) (Scenario, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Scenario{}, fmt.Errorf("chat check scenario path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, fmt.Errorf("read chat check scenario: %w", err)
	}

	var file scenarioFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return Scenario{}, fmt.Errorf("parse chat check scenario: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values are not supported")
		}
		return Scenario{}, fmt.Errorf("parse chat check scenario: %w", err)
	}
	if file.TimeoutMillis < 0 {
		return Scenario{}, fmt.Errorf("chat check scenario timeout_ms cannot be negative")
	}
	if file.MaxTokens < 0 {
		return Scenario{}, fmt.Errorf("chat check scenario max_tokens cannot be negative")
	}
	for i, turn := range file.Turns {
		if err := validateTurn(turn, i+1); err != nil {
			return Scenario{}, err
		}
	}
	timeout, err := parseScenarioTimeout(file.Timeout, file.TimeoutMillis)
	if err != nil {
		return Scenario{}, err
	}
	if len(file.Turns) == 0 {
		return Scenario{}, fmt.Errorf("chat check scenario must define at least one turn")
	}
	return Scenario{
		Description:  file.Description,
		Name:         file.Name,
		Tags:         file.Tags,
		Model:        file.Model,
		SystemPrompt: file.SystemPrompt,
		Turns:        file.Turns,
		Timeout:      timeout,
		MaxTokens:    file.MaxTokens,
		SessionID:    file.SessionID,
	}, nil
}

func LoadScenarios(path string) ([]Scenario, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("chat check scenario path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat chat check scenario path: %w", err)
	}
	if !info.IsDir() {
		scenario, err := LoadScenarioFile(path)
		if err != nil {
			return nil, err
		}
		return []Scenario{scenario}, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read chat check scenario directory: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		files = append(files, filepath.Join(path, entry.Name()))
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("chat check scenario directory contains no JSON scenarios: %s", path)
	}

	scenarios := make([]Scenario, 0, len(files))
	for _, file := range files {
		scenario, err := LoadScenarioFile(file)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		scenarios = append(scenarios, scenario)
	}
	return scenarios, nil
}

func validateTurn(turn Turn, index int) error {
	if strings.TrimSpace(turn.User) == "" {
		return fmt.Errorf("chat check scenario turn %d user prompt is required", index)
	}
	if turn.MinChars < 0 {
		return fmt.Errorf("chat check scenario turn %d min_chars cannot be negative", index)
	}
	if turn.MaxChars < 0 {
		return fmt.Errorf("chat check scenario turn %d max_chars cannot be negative", index)
	}
	if turn.MaxToolCalls != nil && *turn.MaxToolCalls < 0 {
		return fmt.Errorf("chat check scenario turn %d max_tool_calls cannot be negative", index)
	}
	for _, pattern := range turn.WantRegex {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("chat check scenario turn %d invalid want_regex %q: %w", index, pattern, err)
		}
	}
	return nil
}

func FilterScenarios(scenarios []Scenario, selector ScenarioSelector) []Scenario {
	selector = normalizeScenarioSelector(selector)
	if len(selector.NameContains) == 0 && len(selector.Tags) == 0 {
		return append([]Scenario(nil), scenarios...)
	}

	filtered := make([]Scenario, 0, len(scenarios))
	for _, scenario := range scenarios {
		if len(selector.NameContains) > 0 && !scenarioNameMatches(scenario, selector.NameContains) {
			continue
		}
		if len(selector.Tags) > 0 && !scenarioTagsMatch(scenario, selector.Tags) {
			continue
		}
		filtered = append(filtered, scenario)
	}
	return filtered
}

func (r Runner) Run(ctx context.Context, scenario Scenario) (*Result, error) {
	if r.Client == nil {
		return nil, fmt.Errorf("chat check client is required")
	}
	scenario = NormalizeScenario(scenario)
	started := time.Now()
	result := &Result{
		Name:      scenario.Name,
		Model:     scenario.Model,
		SessionID: scenario.SessionID,
		StartedAt: started,
		Turns:     make([]TurnResult, 0, len(scenario.Turns)),
	}
	defer finalizeResult(result, started)

	messages := make([]model.Message, 0, len(scenario.Turns)*2+1)
	if strings.TrimSpace(scenario.SystemPrompt) != "" {
		messages = append(messages, model.Message{Role: "system", Content: strings.TrimSpace(scenario.SystemPrompt)})
	}

	for i, turn := range scenario.Turns {
		turn.User = strings.TrimSpace(turn.User)
		if turn.User == "" {
			err := fmt.Errorf("turn %d user prompt is required", i+1)
			return failTurn(result, TurnResult{Index: i + 1}, err)
		}

		messages = append(messages, model.Message{Role: "user", Content: turn.User})
		req := model.ChatRequest{
			Model:     scenario.Model,
			Messages:  append([]model.Message(nil), messages...),
			MaxTokens: scenario.MaxTokens,
			SessionID: scenario.SessionID,
		}

		start := time.Now()
		turnCtx := ctx
		cancel := func() {}
		if scenario.Timeout > 0 {
			turnCtx, cancel = context.WithTimeout(ctx, scenario.Timeout)
		}
		resp, err := r.Client.ChatCompletion(turnCtx, req)
		cancel()

		turnResult := TurnResult{
			Index:   i + 1,
			User:    turn.User,
			Model:   scenario.Model,
			Latency: time.Since(start),
		}
		turnResult.LatencyMillis = turnResult.Latency.Milliseconds()
		if err != nil {
			return failTurn(result, turnResult, fmt.Errorf("turn %d chat completion: %w", i+1, err))
		}
		if resp == nil {
			err := fmt.Errorf("turn %d chat completion: %w", i+1, model.NilChatResponseError(req))
			return failTurn(result, turnResult, err)
		}
		if strings.TrimSpace(resp.Model) != "" {
			turnResult.Model = resp.Model
		}
		turnResult.Usage = resp.Usage
		result.Usage.PromptTokens += resp.Usage.PromptTokens
		result.Usage.CompletionTokens += resp.Usage.CompletionTokens
		result.Usage.TotalTokens += resp.Usage.TotalTokens
		if len(resp.Choices) == 0 {
			err := fmt.Errorf("turn %d chat completion: %w", i+1, model.NoResponseChoicesError(req, resp))
			return failTurn(result, turnResult, err)
		}

		choice := resp.Choices[0]
		msg := choice.Message
		text, extractErr := model.ExtractTextContent(msg.Content)
		if extractErr != nil && strings.TrimSpace(msg.Reasoning) == "" {
			err := fmt.Errorf("turn %d extract response text: %w", i+1, extractErr)
			return failTurn(result, turnResult, err)
		}
		if strings.TrimSpace(text) == "" && strings.TrimSpace(msg.Reasoning) != "" {
			text = strings.TrimSpace(msg.Reasoning)
		}
		text = strings.TrimSpace(text)
		turnResult.Text = text
		turnResult.Finish = choice.FinishReason
		turnResult.ToolCalls = len(msg.ToolCalls)
		turnResult.Reasoning = strings.TrimSpace(msg.Reasoning) != "" || len(msg.ReasoningDetails) > 0
		turnResult.CharLength = len(text)

		if text == "" {
			err := fmt.Errorf("turn %d returned empty assistant text", i+1)
			turnResult.Checks = append(turnResult.Checks, CheckResult{
				Name:    "non_empty_text",
				Passed:  false,
				Message: "assistant text was empty",
			})
			return failTurn(result, turnResult, err)
		}
		turnResult.Checks = append(turnResult.Checks, CheckResult{Name: "non_empty_text", Passed: true})
		if turn.MinChars > 0 && len(text) < turn.MinChars {
			err := fmt.Errorf("turn %d response too short: got %d chars, want at least %d", i+1, len(text), turn.MinChars)
			turnResult.Checks = append(turnResult.Checks, CheckResult{
				Name:    "min_chars",
				Passed:  false,
				Message: fmt.Sprintf("got %d chars, want at least %d", len(text), turn.MinChars),
			})
			return failTurn(result, turnResult, err)
		}
		if turn.MinChars > 0 {
			turnResult.Checks = append(turnResult.Checks, CheckResult{
				Name:    "min_chars",
				Passed:  true,
				Message: fmt.Sprintf("got %d chars, want at least %d", len(text), turn.MinChars),
			})
		}
		if turn.MaxChars > 0 && len(text) > turn.MaxChars {
			err := fmt.Errorf("turn %d response too long: got %d chars, want at most %d", i+1, len(text), turn.MaxChars)
			turnResult.Checks = append(turnResult.Checks, CheckResult{
				Name:    "max_chars",
				Passed:  false,
				Message: fmt.Sprintf("got %d chars, want at most %d", len(text), turn.MaxChars),
			})
			return failTurn(result, turnResult, err)
		}
		if turn.MaxChars > 0 {
			turnResult.Checks = append(turnResult.Checks, CheckResult{
				Name:    "max_chars",
				Passed:  true,
				Message: fmt.Sprintf("got %d chars, want at most %d", len(text), turn.MaxChars),
			})
		}
		for _, want := range turn.WantContains {
			want = strings.TrimSpace(want)
			if want == "" {
				continue
			}
			if !strings.Contains(text, want) {
				err := fmt.Errorf("turn %d response missing %q", i+1, want)
				turnResult.Checks = append(turnResult.Checks, CheckResult{
					Name:    "contains",
					Passed:  false,
					Message: fmt.Sprintf("missing %q", want),
				})
				return failTurn(result, turnResult, err)
			}
			turnResult.Checks = append(turnResult.Checks, CheckResult{
				Name:    "contains",
				Passed:  true,
				Message: fmt.Sprintf("found %q", want),
			})
		}
		for _, forbidden := range turn.WantNotContains {
			forbidden = strings.TrimSpace(forbidden)
			if forbidden == "" {
				continue
			}
			if strings.Contains(text, forbidden) {
				err := fmt.Errorf("turn %d response included forbidden text %q", i+1, forbidden)
				turnResult.Checks = append(turnResult.Checks, CheckResult{
					Name:    "not_contains",
					Passed:  false,
					Message: fmt.Sprintf("found forbidden text %q", forbidden),
				})
				return failTurn(result, turnResult, err)
			}
			turnResult.Checks = append(turnResult.Checks, CheckResult{
				Name:    "not_contains",
				Passed:  true,
				Message: fmt.Sprintf("did not find %q", forbidden),
			})
		}
		for _, pattern := range turn.WantRegex {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				err := fmt.Errorf("turn %d invalid response regex %q: %w", i+1, pattern, err)
				turnResult.Checks = append(turnResult.Checks, CheckResult{
					Name:    "regex",
					Passed:  false,
					Message: err.Error(),
				})
				return failTurn(result, turnResult, err)
			}
			if !re.MatchString(text) {
				err := fmt.Errorf("turn %d response did not match regex %q", i+1, pattern)
				turnResult.Checks = append(turnResult.Checks, CheckResult{
					Name:    "regex",
					Passed:  false,
					Message: fmt.Sprintf("did not match %q", pattern),
				})
				return failTurn(result, turnResult, err)
			}
			turnResult.Checks = append(turnResult.Checks, CheckResult{
				Name:    "regex",
				Passed:  true,
				Message: fmt.Sprintf("matched %q", pattern),
			})
		}
		if turn.MaxToolCalls != nil && len(msg.ToolCalls) > *turn.MaxToolCalls {
			err := fmt.Errorf("turn %d used too many tool calls: got %d, want at most %d", i+1, len(msg.ToolCalls), *turn.MaxToolCalls)
			turnResult.Checks = append(turnResult.Checks, CheckResult{
				Name:    "max_tool_calls",
				Passed:  false,
				Message: fmt.Sprintf("got %d tool calls, want at most %d", len(msg.ToolCalls), *turn.MaxToolCalls),
			})
			return failTurn(result, turnResult, err)
		}
		if turn.MaxToolCalls != nil {
			turnResult.Checks = append(turnResult.Checks, CheckResult{
				Name:    "max_tool_calls",
				Passed:  true,
				Message: fmt.Sprintf("got %d tool calls, want at most %d", len(msg.ToolCalls), *turn.MaxToolCalls),
			})
		}

		turnResult.Passed = true
		result.Turns = append(result.Turns, turnResult)
		messages = append(messages, model.Message{
			Role:             "assistant",
			Content:          text,
			Reasoning:        msg.Reasoning,
			ReasoningDetails: msg.ReasoningDetails,
		})
	}

	return result, nil
}

func (r Runner) RunSuite(ctx context.Context, name string, scenarios []Scenario) (*SuiteResult, error) {
	if r.Client == nil {
		return nil, fmt.Errorf("chat check client is required")
	}
	if len(scenarios) == 0 {
		return nil, fmt.Errorf("chat check suite requires at least one scenario")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "chat-check-suite"
	}
	started := time.Now()
	suite := &SuiteResult{
		Name:          name,
		StartedAt:     started,
		ScenarioCount: len(scenarios),
		Results:       make([]Result, 0, len(scenarios)),
	}
	defer finalizeSuiteResult(suite, started)

	failures := make([]string, 0)
	for _, scenario := range scenarios {
		normalized := NormalizeScenario(scenario)
		result, err := r.Run(ctx, normalized)
		if result == nil {
			result = &Result{
				Name:      normalized.Name,
				Model:     normalized.Model,
				SessionID: normalized.SessionID,
				Error:     errString(err),
				StartedAt: time.Now(),
			}
			finalizeResult(result, result.StartedAt)
		}
		suite.Results = append(suite.Results, *result)
		suite.Usage.PromptTokens += result.Usage.PromptTokens
		suite.Usage.CompletionTokens += result.Usage.CompletionTokens
		suite.Usage.TotalTokens += result.Usage.TotalTokens
		if err != nil || !result.Passed {
			failures = append(failures, normalized.Name)
		}
	}
	if len(failures) > 0 {
		return suite, fmt.Errorf("chat check suite failed: %d of %d scenarios failed: %s", len(failures), len(scenarios), strings.Join(failures, ", "))
	}
	return suite, nil
}

func failTurn(result *Result, turn TurnResult, err error) (*Result, error) {
	if err == nil {
		err = fmt.Errorf("chat check failed")
	}
	turn.Err = err.Error()
	turn.Passed = false
	if result != nil {
		result.Error = err.Error()
		result.Turns = append(result.Turns, turn)
	}
	return result, err
}

func finalizeSuiteResult(result *SuiteResult, started time.Time) {
	if result == nil {
		return
	}
	completed := time.Now()
	result.CompletedAt = completed
	result.DurationMillis = completed.Sub(started).Milliseconds()
	result.PassedScenarios = 0
	result.FailedScenarios = 0
	for _, scenario := range result.Results {
		if scenario.Passed {
			result.PassedScenarios++
		} else {
			result.FailedScenarios++
		}
	}
	result.Passed = result.ScenarioCount > 0 && result.FailedScenarios == 0 && len(result.Results) == result.ScenarioCount
	if !result.Passed && result.Error == "" {
		result.Error = fmt.Sprintf("%d of %d scenarios failed", result.FailedScenarios, result.ScenarioCount)
	}
}

func finalizeResult(result *Result, started time.Time) {
	if result == nil {
		return
	}
	completed := time.Now()
	result.CompletedAt = completed
	result.DurationMillis = completed.Sub(started).Milliseconds()
	if result.Error != "" || len(result.Turns) == 0 {
		result.Passed = false
		return
	}
	for _, turn := range result.Turns {
		if !turn.Passed || strings.TrimSpace(turn.Err) != "" {
			result.Passed = false
			return
		}
	}
	result.Passed = true
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func NormalizeScenario(scenario Scenario) Scenario {
	scenario.Description = strings.TrimSpace(scenario.Description)
	scenario.Name = strings.TrimSpace(scenario.Name)
	if scenario.Name == "" {
		scenario.Name = "chat-check"
	}
	scenario.Tags = normalizeTags(scenario.Tags)
	scenario.Model = strings.TrimSpace(scenario.Model)
	if scenario.Model == "" {
		scenario.Model = DefaultModel
	}
	scenario.SystemPrompt = strings.TrimSpace(scenario.SystemPrompt)
	if scenario.Timeout <= 0 {
		scenario.Timeout = defaultTimeout
	}
	if scenario.MaxTokens <= 0 {
		scenario.MaxTokens = 256
	}
	scenario.SessionID = strings.TrimSpace(scenario.SessionID)
	if scenario.SessionID == "" {
		scenario.SessionID = "buckley-chat-check"
	}
	if len(scenario.Turns) == 0 {
		defaults := DefaultScenario(scenario.Model)
		scenario.Turns = defaults.Turns
	}
	return scenario
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	normalized := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		normalized = append(normalized, tag)
	}
	sort.Strings(normalized)
	return normalized
}

func normalizeScenarioSelector(selector ScenarioSelector) ScenarioSelector {
	selector.Tags = normalizeTags(selector.Tags)
	selector.NameContains = normalizeSelectorTerms(selector.NameContains)
	return selector
}

func normalizeSelectorTerms(terms []string) []string {
	if len(terms) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(terms))
	normalized := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		normalized = append(normalized, term)
	}
	sort.Strings(normalized)
	return normalized
}

func scenarioNameMatches(scenario Scenario, terms []string) bool {
	name := strings.ToLower(strings.TrimSpace(scenario.Name))
	description := strings.ToLower(strings.TrimSpace(scenario.Description))
	for _, term := range terms {
		if strings.Contains(name, term) || strings.Contains(description, term) {
			return true
		}
	}
	return false
}

func scenarioTagsMatch(scenario Scenario, tags []string) bool {
	if len(tags) == 0 {
		return true
	}
	scenarioTags := normalizeTags(scenario.Tags)
	if len(scenarioTags) == 0 {
		return false
	}
	tagSet := make(map[string]struct{}, len(scenarioTags))
	for _, tag := range scenarioTags {
		tagSet[tag] = struct{}{}
	}
	for _, tag := range tags {
		if _, ok := tagSet[tag]; !ok {
			return false
		}
	}
	return true
}

func parseScenarioTimeout(timeoutText string, timeoutMillis int64) (time.Duration, error) {
	timeoutText = strings.TrimSpace(timeoutText)
	if timeoutText != "" && timeoutMillis > 0 {
		return 0, fmt.Errorf("chat check scenario cannot set both timeout and timeout_ms")
	}
	if timeoutText != "" {
		timeout, err := time.ParseDuration(timeoutText)
		if err != nil {
			return 0, fmt.Errorf("parse chat check scenario timeout: %w", err)
		}
		return timeout, nil
	}
	if timeoutMillis > 0 {
		return time.Duration(timeoutMillis) * time.Millisecond, nil
	}
	return 0, nil
}
