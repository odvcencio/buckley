package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/chatcheck"
)

const envBuckleyChatCheckModel = "BUCKLEY_CHAT_CHECK_MODEL"

func runDoctorCommand(args []string) error {
	subCmd := "check"
	if len(args) > 0 {
		subCmd = strings.TrimSpace(args[0])
	}

	switch subCmd {
	case "", "check", "config":
		return runConfigCommand([]string{"check"})
	case "chat":
		return runDoctorChatCommand(args[1:])
	default:
		return fmt.Errorf("unknown doctor command: %s (use check or chat)", subCmd)
	}
}

func runDoctorChatCommand(args []string) error {
	defaultModel := strings.TrimSpace(os.Getenv(envBuckleyChatCheckModel))
	if defaultModel == "" {
		defaultModel = chatcheck.DefaultModel
	}

	fs := flag.NewFlagSet("doctor chat", flag.ContinueOnError)
	modelID := fs.String("model", defaultModel, "model to use for the multi-turn chat check")
	timeout := fs.Duration("timeout", 45*time.Second, "per-turn timeout")
	scenarioPath := fs.String("scenario", "", "JSON scenario file or directory for the chat check")
	var tagFilters stringListFlag
	var nameFilters stringListFlag
	fs.Var(&tagFilters, "tag", "only run/list scenarios with this tag (repeatable, comma-separated)")
	fs.Var(&nameFilters, "name", "only run/list scenarios whose name or description contains this text (repeatable, comma-separated)")
	listScenarios := fs.Bool("list", false, "list resolved chat check scenarios and exit without running them")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON report")
	outPath := fs.String("out", "", "write machine-readable JSON report to a file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected doctor chat argument: %s", fs.Arg(0))
	}

	scenarios, err := resolveDoctorChatScenarios(*modelID, *timeout, *scenarioPath, flagWasSet(fs, "model"), flagWasSet(fs, "timeout"))
	if err != nil {
		return err
	}
	selector := chatcheck.ScenarioSelector{
		NameContains: nameFilters.Values(),
		Tags:         tagFilters.Values(),
	}
	scenarios = chatcheck.FilterScenarios(scenarios, selector)
	if len(scenarios) == 0 {
		return fmt.Errorf("no chat check scenarios matched filters: %s", formatScenarioSelector(selector))
	}
	if *listScenarios {
		inventory := buildDoctorChatScenarioInventory(scenarios)
		if *outPath != "" {
			if err := writeChatCheckReport(*outPath, inventory); err != nil {
				return err
			}
		}
		if *jsonOutput {
			return printChatCheckJSON(os.Stdout, inventory)
		}
		printDoctorChatScenarioInventory(os.Stdout, inventory)
		return nil
	}

	_, mgr, store, err := initDependenciesFn()
	if err != nil {
		return err
	}
	defer store.Close()

	if *jsonOutput {
		printChatCheckStart(os.Stderr, scenarios)
	} else {
		printChatCheckStart(os.Stdout, scenarios)
	}
	report, runErr := runDoctorChatCheck(context.Background(), chatcheck.Runner{Client: mgr}, scenarios)
	if *outPath != "" {
		if err := writeChatCheckReport(*outPath, report); err != nil {
			return err
		}
	}
	if *jsonOutput {
		if err := printChatCheckJSON(os.Stdout, report); err != nil {
			return err
		}
	} else {
		printChatCheckReport(os.Stdout, report)
	}
	if runErr != nil {
		return withExitCode(runErr, 1)
	}
	if !*jsonOutput {
		if len(scenarios) == 1 {
			fmt.Println("Chat health check passed")
		} else {
			fmt.Println("Chat health check suite passed")
		}
	}
	return nil
}

func resolveDoctorChatScenario(modelID string, timeout time.Duration, scenarioPath string, modelSet bool, timeoutSet bool) (chatcheck.Scenario, error) {
	scenarios, err := resolveDoctorChatScenarios(modelID, timeout, scenarioPath, modelSet, timeoutSet)
	if err != nil {
		return chatcheck.Scenario{}, err
	}
	if len(scenarios) != 1 {
		return chatcheck.Scenario{}, fmt.Errorf("expected one chat check scenario, got %d", len(scenarios))
	}
	return scenarios[0], nil
}

func resolveDoctorChatScenarios(modelID string, timeout time.Duration, scenarioPath string, modelSet bool, timeoutSet bool) ([]chatcheck.Scenario, error) {
	scenario := chatcheck.DefaultScenario(modelID)
	scenario.Timeout = timeout
	if strings.TrimSpace(scenarioPath) == "" {
		return []chatcheck.Scenario{chatcheck.NormalizeScenario(scenario)}, nil
	}

	loaded, err := chatcheck.LoadScenarios(scenarioPath)
	if err != nil {
		return nil, err
	}
	for i := range loaded {
		if modelSet || strings.TrimSpace(loaded[i].Model) == "" {
			loaded[i].Model = modelID
		}
		if timeoutSet || loaded[i].Timeout <= 0 {
			loaded[i].Timeout = timeout
		}
		loaded[i] = chatcheck.NormalizeScenario(loaded[i])
	}
	return loaded, nil
}

type doctorChatScenarioInventory struct {
	ScenarioCount int                         `json:"scenario_count"`
	Scenarios     []doctorChatScenarioSummary `json:"scenarios"`
}

type doctorChatScenarioSummary struct {
	Description     string   `json:"description,omitempty"`
	Name            string   `json:"name"`
	Tags            []string `json:"tags,omitempty"`
	Model           string   `json:"model"`
	SessionID       string   `json:"session_id"`
	Turns           int      `json:"turns"`
	TimeoutMillis   int64    `json:"timeout_ms"`
	MaxTokens       int      `json:"max_tokens"`
	SystemPrompt    bool     `json:"system_prompt"`
	ExpectedMatches int      `json:"expected_matches"`
	ForbiddenChecks int      `json:"forbidden_checks"`
	RegexChecks     int      `json:"regex_checks"`
	MinCharChecks   int      `json:"min_char_checks"`
	MaxCharChecks   int      `json:"max_char_checks"`
	MaxToolChecks   int      `json:"max_tool_call_checks"`
}

func buildDoctorChatScenarioInventory(scenarios []chatcheck.Scenario) doctorChatScenarioInventory {
	inventory := doctorChatScenarioInventory{
		ScenarioCount: len(scenarios),
		Scenarios:     make([]doctorChatScenarioSummary, 0, len(scenarios)),
	}
	for _, scenario := range scenarios {
		summary := doctorChatScenarioSummary{
			Description:   scenario.Description,
			Name:          scenario.Name,
			Tags:          append([]string(nil), scenario.Tags...),
			Model:         scenario.Model,
			SessionID:     scenario.SessionID,
			Turns:         len(scenario.Turns),
			TimeoutMillis: scenario.Timeout.Milliseconds(),
			MaxTokens:     scenario.MaxTokens,
			SystemPrompt:  strings.TrimSpace(scenario.SystemPrompt) != "",
		}
		for _, turn := range scenario.Turns {
			summary.ExpectedMatches += countNonEmptyStrings(turn.WantContains)
			summary.ForbiddenChecks += countNonEmptyStrings(turn.WantNotContains)
			summary.RegexChecks += countNonEmptyStrings(turn.WantRegex)
			if turn.MinChars > 0 {
				summary.MinCharChecks++
			}
			if turn.MaxChars > 0 {
				summary.MaxCharChecks++
			}
			if turn.MaxToolCalls != nil {
				summary.MaxToolChecks++
			}
		}
		inventory.Scenarios = append(inventory.Scenarios, summary)
	}
	return inventory
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		*f = append(*f, part)
	}
	return nil
}

func (f *stringListFlag) Values() []string {
	if f == nil {
		return nil
	}
	return append([]string(nil), *f...)
}

func countNonEmptyStrings(values []string) int {
	count := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	return count
}

func formatScenarioSelector(selector chatcheck.ScenarioSelector) string {
	parts := make([]string, 0, 2)
	if len(selector.Tags) > 0 {
		parts = append(parts, "tag="+strings.Join(selector.Tags, ","))
	}
	if len(selector.NameContains) > 0 {
		parts = append(parts, "name="+strings.Join(selector.NameContains, ","))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	if fs == nil {
		return false
	}
	wasSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			wasSet = true
		}
	})
	return wasSet
}

func runDoctorChatCheck(ctx context.Context, runner chatcheck.Runner, scenarios []chatcheck.Scenario) (any, error) {
	if len(scenarios) == 1 {
		return runner.Run(ctx, scenarios[0])
	}
	return runner.RunSuite(ctx, "chat-check-suite", scenarios)
}

func printChatCheckStart(w io.Writer, scenarios []chatcheck.Scenario) {
	if len(scenarios) == 1 {
		scenario := scenarios[0]
		fmt.Fprintf(w, "Running chat health check with %s (%d turns)\n", scenario.Model, len(scenario.Turns))
		return
	}
	fmt.Fprintf(w, "Running chat health check suite with %d scenarios\n", len(scenarios))
}

func printDoctorChatScenarioInventory(w io.Writer, inventory doctorChatScenarioInventory) {
	fmt.Fprintf(w, "Chat check scenarios: %d\n", inventory.ScenarioCount)
	for _, scenario := range inventory.Scenarios {
		fmt.Fprintf(w, "  - %s: %d turns, model=%s, timeout=%dms, max_tokens=%d", scenario.Name, scenario.Turns, scenario.Model, scenario.TimeoutMillis, scenario.MaxTokens)
		if len(scenario.Tags) > 0 {
			fmt.Fprintf(w, ", tags=%s", strings.Join(scenario.Tags, ","))
		}
		if scenario.ExpectedMatches > 0 {
			fmt.Fprintf(w, ", contains=%d", scenario.ExpectedMatches)
		}
		if scenario.ForbiddenChecks > 0 {
			fmt.Fprintf(w, ", not_contains=%d", scenario.ForbiddenChecks)
		}
		if scenario.RegexChecks > 0 {
			fmt.Fprintf(w, ", regex=%d", scenario.RegexChecks)
		}
		if scenario.MinCharChecks > 0 {
			fmt.Fprintf(w, ", min_chars=%d", scenario.MinCharChecks)
		}
		if scenario.MaxCharChecks > 0 {
			fmt.Fprintf(w, ", max_chars=%d", scenario.MaxCharChecks)
		}
		if scenario.MaxToolChecks > 0 {
			fmt.Fprintf(w, ", max_tool_calls=%d", scenario.MaxToolChecks)
		}
		if scenario.SystemPrompt {
			fmt.Fprint(w, ", system_prompt=true")
		}
		if scenario.SessionID != "" {
			fmt.Fprintf(w, ", session_id=%s", scenario.SessionID)
		}
		if scenario.Description != "" {
			fmt.Fprintf(w, ", description=%q", scenario.Description)
		}
		fmt.Fprintln(w)
	}
}

func printChatCheckReport(w io.Writer, report any) {
	switch result := report.(type) {
	case *chatcheck.Result:
		printChatCheckResult(w, result)
	case *chatcheck.SuiteResult:
		printChatCheckSuiteResult(w, result)
	}
}

func printChatCheckResult(w io.Writer, result *chatcheck.Result) {
	if result == nil {
		return
	}
	for _, turn := range result.Turns {
		status := "ok"
		if strings.TrimSpace(turn.Err) != "" {
			status = "fail"
		}
		fmt.Fprintf(w, "  [%s] turn %d: %s, %d chars", status, turn.Index, turn.Latency.Round(time.Millisecond), turn.CharLength)
		if turn.Model != "" {
			fmt.Fprintf(w, ", model=%s", turn.Model)
		}
		if turn.Finish != "" {
			fmt.Fprintf(w, ", finish=%s", turn.Finish)
		}
		if turn.ToolCalls > 0 {
			fmt.Fprintf(w, ", tool_calls=%d", turn.ToolCalls)
		}
		if turn.Reasoning {
			fmt.Fprint(w, ", reasoning=true")
		}
		if turn.Err != "" {
			fmt.Fprintf(w, ", error=%s", turn.Err)
		}
		fmt.Fprintln(w)
	}
}

func printChatCheckSuiteResult(w io.Writer, result *chatcheck.SuiteResult) {
	if result == nil {
		return
	}
	for i := range result.Results {
		scenario := &result.Results[i]
		status := "ok"
		if !scenario.Passed {
			status = "fail"
		}
		fmt.Fprintf(w, "  [%s] scenario %q: %d turns, %d ms", status, scenario.Name, len(scenario.Turns), scenario.DurationMillis)
		if scenario.Model != "" {
			fmt.Fprintf(w, ", model=%s", scenario.Model)
		}
		if scenario.Error != "" {
			fmt.Fprintf(w, ", error=%s", scenario.Error)
		}
		fmt.Fprintln(w)
		printChatCheckResult(w, scenario)
	}
	fmt.Fprintf(w, "  suite: %d passed, %d failed\n", result.PassedScenarios, result.FailedScenarios)
}

func printChatCheckJSON(w io.Writer, report any) error {
	if report == nil {
		return nil
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func writeChatCheckReport(path string, report any) error {
	path = strings.TrimSpace(path)
	if path == "" || report == nil {
		return nil
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create chat check report directory: %w", err)
		}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal chat check report: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write chat check report: %w", err)
	}
	return nil
}
