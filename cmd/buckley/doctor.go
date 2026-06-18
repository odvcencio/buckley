package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/chatcheck"
)

const envBuckleyChatCheckModel = "BUCKLEY_CHAT_CHECK_MODEL"
const projectChatCheckScenarioDir = ".buckley/chatchecks"
const projectEvalScenarioDir = "evals"
const defaultChatCheckArtifactRoot = ".buckley/chatchecks/runs"

func runDoctorCommand(args []string) error {
	subCmd := "check"
	if len(args) > 0 {
		subCmd = strings.TrimSpace(args[0])
	}

	switch subCmd {
	case "", "check", "config":
		return runConfigCommand([]string{"check"})
	case "chat":
		if len(args) > 1 && strings.TrimSpace(args[1]) == "init" {
			return runDoctorChatInitCommand(args[2:])
		}
		return runDoctorChatCommand(args[1:])
	default:
		return fmt.Errorf("unknown doctor command: %s (use check or chat)", subCmd)
	}
}

type doctorChatInitResult struct {
	Name        string   `json:"name"`
	Root        string   `json:"root"`
	Path        string   `json:"path"`
	Created     []string `json:"created,omitempty"`
	Existing    []string `json:"existing,omitempty"`
	Overwritten []string `json:"overwritten,omitempty"`
	DryRun      bool     `json:"dry_run,omitempty"`
}

type doctorChatInitOptions struct {
	Root        string
	Name        string
	Description string
	Model       string
	Tags        []string
	Force       bool
	DryRun      bool
}

func runDoctorChatInitCommand(args []string) error {
	fs := flag.NewFlagSet("doctor chat init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	pathFlag := fs.String("path", ".", "project root where .buckley/chatchecks should be created")
	description := fs.String("description", "", "scenario description")
	modelID := fs.String("model", chatcheck.DefaultModel, "model to use when running this scenario")
	var tags stringListFlag
	fs.Var(&tags, "tag", "scenario tag (repeatable, comma-separated)")
	force := fs.Bool("force", false, "overwrite scenario file if it already exists")
	dryRun := fs.Bool("dry-run", false, "show what would be created without writing files")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: buckley doctor chat init [--path <dir>] [--description <text>] [--model <id>] [--tag <tag>] [--force] [--dry-run] [--json|--format json] <scenario>")
	}
	if err := normalizeJSONFormatFlag(*format, jsonOutput); err != nil {
		return err
	}
	result, err := initProjectChatCheck(doctorChatInitOptions{
		Root:        *pathFlag,
		Name:        fs.Arg(0),
		Description: *description,
		Model:       *modelID,
		Tags:        tags.Values(),
		Force:       *force,
		DryRun:      *dryRun,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	printDoctorChatInitResult(os.Stdout, result)
	return nil
}

func runDoctorChatCommand(args []string) error {
	defaultModel := strings.TrimSpace(os.Getenv(envBuckleyChatCheckModel))
	if defaultModel == "" {
		defaultModel = chatcheck.DefaultModel
	}

	fs := flag.NewFlagSet("doctor chat", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage of doctor chat [scenario-id ...]:\n")
		fs.PrintDefaults()
	}
	modelID := fs.String("model", defaultModel, "model to use for the multi-turn chat check")
	timeout := fs.Duration("timeout", 45*time.Second, "per-turn timeout")
	scenarioPath := fs.String("scenario", "", "JSON scenario file or directory for the chat check")
	projectScenarios := fs.Bool("project", false, "use project chat check scenarios from .buckley/chatchecks")
	var tagFilters stringListFlag
	var nameFilters stringListFlag
	fs.Var(&tagFilters, "tag", "only run/list scenarios with this tag (repeatable, comma-separated)")
	fs.Var(&nameFilters, "name", "only run/list scenarios whose name or description contains this text (repeatable, comma-separated)")
	listScenarios := fs.Bool("list", false, "list resolved chat check scenarios and exit without running them")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON report")
	outPath := fs.String("out", "", "write machine-readable JSON report to a file")
	junitPath := fs.String("junit", "", "write JUnit XML report to a file")
	writeArtifacts := fs.Bool("artifacts", false, "write run artifacts under .buckley/chatchecks/runs")
	writeFailureArtifacts := fs.Bool("artifacts-on-failure", true, "write run artifacts automatically when a chat check fails")
	artifactsDir := fs.String("artifacts-dir", defaultChatCheckArtifactRoot, "directory for chat check run artifacts")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *listScenarios && strings.TrimSpace(*junitPath) != "" {
		return fmt.Errorf("doctor chat -junit cannot be used with -list")
	}
	if *listScenarios && *writeArtifacts {
		return fmt.Errorf("doctor chat -artifacts cannot be used with -list")
	}
	resolvedScenarioPath, err := resolveDoctorChatScenarioPath(*scenarioPath, *projectScenarios)
	if err != nil {
		return err
	}

	scenarios, err := resolveDoctorChatScenarios(*modelID, *timeout, resolvedScenarioPath, flagWasSet(fs, "model"), flagWasSet(fs, "timeout"))
	if err != nil {
		return err
	}
	selector := chatcheck.ScenarioSelector{
		IDs:          fs.Args(),
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
	if *junitPath != "" {
		if err := writeChatCheckJUnitReport(*junitPath, report); err != nil {
			return err
		}
	}
	artifactRunDir := ""
	if shouldWriteChatCheckArtifacts(report, *writeArtifacts, *writeFailureArtifacts) {
		var err error
		artifactRoot := resolveChatCheckArtifactRoot(*artifactsDir, *projectScenarios, resolvedScenarioPath, flagWasSet(fs, "artifacts-dir"))
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
		artifactContext := buildDoctorChatArtifactContext(cwd, resolvedScenarioPath, artifactRoot, *projectScenarios, selector, scenarios)
		artifactRunDir, err = writeChatCheckArtifacts(artifactRoot, report, time.Now(), artifactContext)
		if err != nil {
			return err
		}
	}
	if *jsonOutput {
		if artifactRunDir != "" {
			fmt.Fprintf(os.Stderr, "Artifacts: %s\n", artifactRunDir)
		}
		if err := printChatCheckJSON(os.Stdout, report); err != nil {
			return err
		}
	} else {
		printChatCheckReport(os.Stdout, report)
		if artifactRunDir != "" {
			fmt.Fprintf(os.Stdout, "Artifacts: %s\n", artifactRunDir)
		}
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

func resolveDoctorChatScenarioPath(scenarioPath string, useProject bool) (string, error) {
	scenarioPath = strings.TrimSpace(scenarioPath)
	if !useProject {
		return scenarioPath, nil
	}
	if scenarioPath != "" {
		return "", fmt.Errorf("doctor chat -project cannot be combined with -scenario")
	}
	return findProjectChatCheckScenarioDir(".")
}

func findProjectChatCheckScenarioDir(start string) (string, error) {
	start = strings.TrimSpace(start)
	if start == "" {
		start = "."
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve project chat check start: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("stat project chat check start: %w", err)
	}
	if !info.IsDir() {
		dir = filepath.Dir(dir)
	}

	for {
		for _, rel := range []string{projectChatCheckScenarioDir, projectEvalScenarioDir} {
			candidate := filepath.Join(dir, rel)
			info, err := os.Stat(candidate)
			if err == nil {
				if !info.IsDir() {
					return "", fmt.Errorf("project chat check scenario path is not a directory: %s", candidate)
				}
				return candidate, nil
			}
			if !os.IsNotExist(err) {
				return "", fmt.Errorf("stat project chat check scenarios: %w", err)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("project chat check scenarios not found: %s or %s", projectChatCheckScenarioDir, projectEvalScenarioDir)
}

func initProjectChatCheck(opts doctorChatInitOptions) (doctorChatInitResult, error) {
	name, err := cleanDoctorChatScenarioName(opts.Name)
	if err != nil {
		return doctorChatInitResult{}, err
	}
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return doctorChatInitResult{}, fmt.Errorf("resolve chat check init path: %w", err)
	}
	if info, err := os.Stat(absRoot); err == nil {
		if !info.IsDir() {
			return doctorChatInitResult{}, fmt.Errorf("chat check init path is not a directory: %s", absRoot)
		}
	} else if !os.IsNotExist(err) {
		return doctorChatInitResult{}, fmt.Errorf("stat chat check init path: %w", err)
	}

	scenarioDir := filepath.Join(absRoot, projectChatCheckScenarioDir)
	scenarioFile := filepath.Join(scenarioDir, filepath.FromSlash(name)+".yaml")
	result := doctorChatInitResult{
		Name:   name,
		Root:   absRoot,
		Path:   scenarioFile,
		DryRun: opts.DryRun,
	}
	if _, _, _, err := ensureChatCheckInitPath(absRoot, "", true, false, opts.DryRun); err != nil {
		return doctorChatInitResult{}, err
	}
	for _, dir := range []string{filepath.Join(absRoot, ".buckley"), scenarioDir, filepath.Dir(scenarioFile)} {
		created, existing, _, err := ensureChatCheckInitPath(dir, "", true, false, opts.DryRun)
		if err != nil {
			return doctorChatInitResult{}, err
		}
		rel := agentInitRelativePath(absRoot, dir, true)
		if created {
			result.Created = append(result.Created, rel)
		} else if existing {
			result.Existing = append(result.Existing, rel)
		}
	}

	content := renderChatCheckScenarioYAML(opts, name)
	created, existing, overwritten, err := ensureChatCheckInitPath(scenarioFile, content, false, opts.Force, opts.DryRun)
	if err != nil {
		return doctorChatInitResult{}, err
	}
	rel := agentInitRelativePath(absRoot, scenarioFile, false)
	switch {
	case overwritten:
		result.Overwritten = append(result.Overwritten, rel)
	case created:
		result.Created = append(result.Created, rel)
	case existing:
		result.Existing = append(result.Existing, rel)
	}
	return result, nil
}

func ensureChatCheckInitPath(path, content string, dir, force, dryRun bool) (created bool, existing bool, overwritten bool, err error) {
	info, statErr := os.Stat(path)
	if statErr == nil {
		if dir {
			if !info.IsDir() {
				return false, false, false, fmt.Errorf("chat check init path exists and is not a directory: %s", path)
			}
			return false, true, false, nil
		}
		if info.IsDir() {
			return false, false, false, fmt.Errorf("chat check init path exists and is a directory: %s", path)
		}
		if !force {
			return false, true, false, nil
		}
		if dryRun {
			return false, false, true, nil
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return false, false, false, fmt.Errorf("write chat check scenario: %w", err)
		}
		return false, false, true, nil
	}
	if !os.IsNotExist(statErr) {
		return false, false, false, fmt.Errorf("stat chat check init path: %w", statErr)
	}
	if dryRun {
		return true, false, false, nil
	}
	if dir {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return false, false, false, fmt.Errorf("create chat check directory: %w", err)
		}
		return true, false, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, false, false, fmt.Errorf("create chat check parent directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, false, false, fmt.Errorf("write chat check scenario: %w", err)
	}
	return true, false, false, nil
}

func cleanDoctorChatScenarioName(value string) (string, error) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	switch strings.ToLower(filepath.Ext(value)) {
	case ".yaml", ".yml", ".json":
		value = strings.TrimSuffix(value, filepath.Ext(value))
	}
	return cleanAgentSkillName(value)
}

func renderChatCheckScenarioYAML(opts doctorChatInitOptions, name string) string {
	modelID := strings.TrimSpace(opts.Model)
	if modelID == "" {
		modelID = chatcheck.DefaultModel
	}
	description := strings.TrimSpace(opts.Description)
	if description == "" {
		description = "Checks Buckley multi-turn chat continuity."
	}
	tags := normalizeChatCheckInitTags(opts.Tags)
	scenario := chatcheck.DefaultScenario(modelID)
	scenario.Name = name
	scenario.Description = description
	scenario.Tags = tags
	scenario.SessionID = "buckley-chat-check-" + strings.ReplaceAll(filepath.Base(filepath.ToSlash(name)), "_", "-")

	var b strings.Builder
	fmt.Fprintf(&b, "description: %s\n", quoteYAMLString(scenario.Description))
	fmt.Fprintf(&b, "name: %s\n", quoteYAMLString(scenario.Name))
	b.WriteString("tags:\n")
	for _, tag := range scenario.Tags {
		fmt.Fprintf(&b, "  - %s\n", quoteYAMLString(tag))
	}
	fmt.Fprintf(&b, "model: %s\n", quoteYAMLString(scenario.Model))
	b.WriteString("timeout: \"45s\"\n")
	fmt.Fprintf(&b, "max_tokens: %d\n", scenario.MaxTokens)
	fmt.Fprintf(&b, "session_id: %s\n", quoteYAMLString(scenario.SessionID))
	fmt.Fprintf(&b, "system_prompt: %s\n", quoteYAMLString(scenario.SystemPrompt))
	b.WriteString("turns:\n")
	for _, turn := range scenario.Turns {
		fmt.Fprintf(&b, "  - user: %s\n", quoteYAMLString(turn.User))
		if len(turn.WantContains) > 0 {
			b.WriteString("    want_contains:\n")
			for _, want := range turn.WantContains {
				fmt.Fprintf(&b, "      - %s\n", quoteYAMLString(want))
			}
		}
		if turn.MinChars > 0 {
			fmt.Fprintf(&b, "    min_chars: %d\n", turn.MinChars)
		}
	}
	return b.String()
}

func normalizeChatCheckInitTags(values []string) []string {
	if len(values) == 0 {
		return []string{"chat", "smoke"}
	}
	return normalizeChatCheckTags(values)
}

func normalizeChatCheckTags(values []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			tag := strings.ToLower(strings.TrimSpace(part))
			if tag == "" {
				continue
			}
			if _, ok := seen[tag]; ok {
				continue
			}
			seen[tag] = struct{}{}
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"chat", "smoke"}
	}
	return out
}

func printDoctorChatInitResult(w io.Writer, result doctorChatInitResult) {
	action := "Created"
	if result.DryRun {
		action = "Would create"
	}
	fmt.Fprintf(w, "%s chat check scenario %s at %s\n", action, result.Name, result.Path)
	printPathList(w, "Created", "Would create", result.Created, result.DryRun)
	printPathList(w, "Overwritten", "Would overwrite", result.Overwritten, result.DryRun)
	printPathList(w, "Existing", "Existing", result.Existing, false)
	fmt.Fprintln(w, "Next: buckley doctor chat -project -list")
}

func resolveChatCheckArtifactRoot(configured string, useProject bool, scenarioPath string, explicit bool) string {
	configured = strings.TrimSpace(configured)
	if explicit || !useProject {
		return configured
	}
	scenarioPath = strings.TrimSpace(scenarioPath)
	if scenarioPath == "" {
		return configured
	}
	return filepath.Join(scenarioPath, "runs")
}

type junitTestSuite struct {
	XMLName   xml.Name        `xml:"testsuite"`
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	Errors    int             `xml:"errors,attr"`
	Time      string          `xml:"time,attr"`
	TestCases []junitTestCase `xml:"testcase"`
}

type junitTestCase struct {
	ClassName string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

type doctorChatArtifactsManifest struct {
	GeneratedAt time.Time                  `json:"generated_at"`
	Context     doctorChatArtifactContext  `json:"context"`
	Report      string                     `json:"report"`
	Results     []doctorChatArtifactEntry  `json:"results"`
	Artifacts   doctorChatArtifactLocation `json:"artifacts"`
}

type doctorChatArtifactContext struct {
	WorkDir       string                        `json:"workdir,omitempty"`
	ScenarioPath  string                        `json:"scenario_path,omitempty"`
	Project       bool                          `json:"project"`
	ArtifactRoot  string                        `json:"artifact_root,omitempty"`
	Selector      doctorChatArtifactSelector    `json:"selector"`
	ScenarioCount int                           `json:"scenario_count"`
	Scenarios     []doctorChatScenarioSummary   `json:"scenarios,omitempty"`
	Git           *doctorChatArtifactGitContext `json:"git,omitempty"`
}

type doctorChatArtifactSelector struct {
	IDs          []string `json:"ids,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	NameContains []string `json:"name_contains,omitempty"`
}

type doctorChatArtifactGitContext struct {
	Branch string `json:"branch,omitempty"`
	SHA    string `json:"sha,omitempty"`
}

type doctorChatArtifactLocation struct {
	Report         string `json:"report"`
	Summary        string `json:"summary"`
	ResultsDir     string `json:"results_dir"`
	TranscriptsDir string `json:"transcripts_dir,omitempty"`
}

type doctorChatArtifactEntry struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Transcript     string `json:"transcript,omitempty"`
	Passed         bool   `json:"passed"`
	Error          string `json:"error,omitempty"`
	Turns          int    `json:"turns"`
	DurationMillis int64  `json:"duration_ms"`
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

func buildDoctorChatArtifactContext(workDir, scenarioPath, artifactRoot string, project bool, selector chatcheck.ScenarioSelector, scenarios []chatcheck.Scenario) doctorChatArtifactContext {
	inventory := buildDoctorChatScenarioInventory(scenarios)
	return doctorChatArtifactContext{
		WorkDir:      strings.TrimSpace(workDir),
		ScenarioPath: strings.TrimSpace(scenarioPath),
		Project:      project,
		ArtifactRoot: strings.TrimSpace(artifactRoot),
		Selector: doctorChatArtifactSelector{
			IDs:          append([]string(nil), selector.IDs...),
			Tags:         append([]string(nil), selector.Tags...),
			NameContains: append([]string(nil), selector.NameContains...),
		},
		ScenarioCount: inventory.ScenarioCount,
		Scenarios:     inventory.Scenarios,
		Git:           resolveDoctorChatArtifactGitContext(),
	}
}

func resolveDoctorChatArtifactGitContext() *doctorChatArtifactGitContext {
	sha, _ := gitOutput("rev-parse", "HEAD")
	branch, _ := gitOutput("rev-parse", "--abbrev-ref", "HEAD")
	branch = strings.TrimSpace(branch)
	if branch == "HEAD" {
		branch = ""
	}
	sha = strings.TrimSpace(sha)
	if branch == "" && sha == "" {
		return nil
	}
	return &doctorChatArtifactGitContext{Branch: branch, SHA: sha}
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
	parts := make([]string, 0, 3)
	if len(selector.IDs) > 0 {
		parts = append(parts, "id="+strings.Join(selector.IDs, ","))
	}
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

func shouldWriteChatCheckArtifacts(report any, explicit bool, onFailure bool) bool {
	if report == nil {
		return false
	}
	if explicit {
		return true
	}
	return onFailure && chatCheckReportFailed(report)
}

func chatCheckReportFailed(report any) bool {
	switch result := report.(type) {
	case *chatcheck.Result:
		return result != nil && !result.Passed
	case *chatcheck.SuiteResult:
		return result != nil && !result.Passed
	default:
		return false
	}
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
	if err := ensureParentDir(path, "chat check report"); err != nil {
		return err
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

func writeChatCheckJUnitReport(path string, report any) error {
	path = strings.TrimSpace(path)
	if path == "" || report == nil {
		return nil
	}
	if err := ensureParentDir(path, "chat check JUnit report"); err != nil {
		return err
	}
	suite, err := chatCheckJUnitSuite(report)
	if err != nil {
		return err
	}
	data, err := xml.MarshalIndent(suite, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal chat check JUnit report: %w", err)
	}
	data = append([]byte(xml.Header), data...)
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write chat check JUnit report: %w", err)
	}
	return nil
}

func writeChatCheckArtifacts(root string, report any, now time.Time, artifactContext doctorChatArtifactContext) (string, error) {
	if report == nil {
		return "", nil
	}
	root = strings.TrimSpace(root)
	if root == "" {
		root = defaultChatCheckArtifactRoot
	}
	if now.IsZero() {
		now = time.Now()
	}
	runDir := filepath.Join(root, now.UTC().Format("20060102T150405.000000000Z"))
	if err := os.MkdirAll(filepath.Join(runDir, "results"), 0o755); err != nil {
		return "", fmt.Errorf("create chat check artifact directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(runDir, "transcripts"), 0o755); err != nil {
		return "", fmt.Errorf("create chat check transcript directory: %w", err)
	}
	if err := writeChatCheckReport(filepath.Join(runDir, "report.json"), report); err != nil {
		return "", err
	}

	results, err := chatCheckReportResults(report)
	if err != nil {
		return "", err
	}
	manifest := doctorChatArtifactsManifest{
		GeneratedAt: now.UTC(),
		Context:     artifactContext,
		Report:      "report.json",
		Results:     make([]doctorChatArtifactEntry, 0, len(results)),
		Artifacts: doctorChatArtifactLocation{
			Report:         "report.json",
			Summary:        "summary.json",
			ResultsDir:     "results",
			TranscriptsDir: "transcripts",
		},
	}
	if strings.TrimSpace(manifest.Context.ArtifactRoot) == "" {
		manifest.Context.ArtifactRoot = root
	}
	seenResultPaths := map[string]int{}
	seenTranscriptPaths := map[string]int{}
	for _, result := range results {
		relPath := filepath.Join("results", chatCheckResultArtifactPath(result.Name))
		relPath = uniqueChatCheckArtifactPath(relPath, seenResultPaths)
		if err := writeChatCheckReport(filepath.Join(runDir, relPath), result); err != nil {
			return "", err
		}
		transcriptPath := filepath.Join("transcripts", chatCheckTranscriptArtifactPath(result.Name))
		transcriptPath = uniqueChatCheckArtifactPath(transcriptPath, seenTranscriptPaths)
		if err := writeChatCheckTranscript(filepath.Join(runDir, transcriptPath), result); err != nil {
			return "", err
		}
		manifest.Results = append(manifest.Results, doctorChatArtifactEntry{
			Name:           result.Name,
			Path:           filepath.ToSlash(relPath),
			Transcript:     filepath.ToSlash(transcriptPath),
			Passed:         result.Passed,
			Error:          result.Error,
			Turns:          len(result.Turns),
			DurationMillis: result.DurationMillis,
		})
	}
	if err := writeChatCheckReport(filepath.Join(runDir, "summary.json"), manifest); err != nil {
		return "", err
	}
	return runDir, nil
}

func writeChatCheckTranscript(path string, result chatcheck.Result) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if err := ensureParentDir(path, "chat check transcript"); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(chatCheckTranscriptMarkdown(result)), 0o644); err != nil {
		return fmt.Errorf("write chat check transcript: %w", err)
	}
	return nil
}

func chatCheckTranscriptMarkdown(result chatcheck.Result) string {
	var b strings.Builder
	name := strings.TrimSpace(result.Name)
	if name == "" {
		name = "chat-check"
	}
	fmt.Fprintf(&b, "# Chat Check Transcript: %s\n\n", name)
	fmt.Fprintf(&b, "- Passed: %t\n", result.Passed)
	if result.Model != "" {
		fmt.Fprintf(&b, "- Model: %s\n", result.Model)
	}
	if result.SessionID != "" {
		fmt.Fprintf(&b, "- Session: %s\n", result.SessionID)
	}
	if result.DurationMillis > 0 {
		fmt.Fprintf(&b, "- Duration: %d ms\n", result.DurationMillis)
	}
	if result.Usage.TotalTokens > 0 {
		fmt.Fprintf(&b, "- Tokens: prompt=%d completion=%d total=%d\n", result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens)
	}
	if strings.TrimSpace(result.Error) != "" {
		fmt.Fprintf(&b, "- Error: %s\n", strings.TrimSpace(result.Error))
	}
	b.WriteString("\n")
	for _, turn := range result.Turns {
		fmt.Fprintf(&b, "## Turn %d\n\n", turn.Index)
		if turn.Model != "" {
			fmt.Fprintf(&b, "- Model: %s\n", turn.Model)
		}
		if turn.LatencyMillis > 0 {
			fmt.Fprintf(&b, "- Latency: %d ms\n", turn.LatencyMillis)
		}
		if turn.Finish != "" {
			fmt.Fprintf(&b, "- Finish: %s\n", turn.Finish)
		}
		fmt.Fprintf(&b, "- Tool calls: %d\n", turn.ToolCalls)
		if turn.Reasoning {
			b.WriteString("- Reasoning: true\n")
		}
		if strings.TrimSpace(turn.Err) != "" {
			fmt.Fprintf(&b, "- Error: %s\n", strings.TrimSpace(turn.Err))
		}
		b.WriteString("\n### User\n\n")
		writeTranscriptBlock(&b, turn.User)
		b.WriteString("\n### Assistant\n\n")
		writeTranscriptBlock(&b, turn.Text)
		if len(turn.Checks) > 0 {
			b.WriteString("\n### Checks\n\n")
			for _, check := range turn.Checks {
				marker := " "
				if check.Passed {
					marker = "x"
				}
				fmt.Fprintf(&b, "- [%s] %s", marker, transcriptLabel(check.Name, "check"))
				if strings.TrimSpace(check.Message) != "" {
					fmt.Fprintf(&b, ": %s", strings.TrimSpace(check.Message))
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func writeTranscriptBlock(b *strings.Builder, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "(empty)"
	}
	b.WriteString("```text\n")
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("```\n")
}

func transcriptLabel(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func chatCheckReportResults(report any) ([]chatcheck.Result, error) {
	switch result := report.(type) {
	case *chatcheck.Result:
		if result == nil {
			return nil, fmt.Errorf("chat check result is nil")
		}
		return []chatcheck.Result{*result}, nil
	case *chatcheck.SuiteResult:
		if result == nil {
			return nil, fmt.Errorf("chat check suite result is nil")
		}
		return append([]chatcheck.Result(nil), result.Results...), nil
	default:
		return nil, fmt.Errorf("unsupported chat check report type %T", report)
	}
}

func chatCheckResultArtifactPath(name string) string {
	return chatCheckNamedArtifactPath(name, ".json")
}

func chatCheckTranscriptArtifactPath(name string) string {
	return chatCheckNamedArtifactPath(name, ".md")
}

func chatCheckNamedArtifactPath(name string, ext string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "chat-check"
	}
	parts := strings.Split(filepath.ToSlash(name), "/")
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if segment := safeArtifactPathSegment(part); segment != "" {
			clean = append(clean, segment)
		}
	}
	if len(clean) == 0 {
		clean = append(clean, "chat-check")
	}
	if strings.TrimSpace(ext) == "" {
		ext = ".json"
	}
	clean[len(clean)-1] += ext
	return filepath.Join(clean...)
}

func uniqueChatCheckArtifactPath(path string, seen map[string]int) string {
	if seen == nil {
		return path
	}
	count := seen[path]
	if count == 0 {
		seen[path] = 1
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for {
		count++
		candidate := fmt.Sprintf("%s-%d%s", base, count, ext)
		if seen[candidate] == 0 {
			seen[path] = count
			seen[candidate] = 1
			return candidate
		}
	}
}

func safeArtifactPathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	segment := strings.Trim(b.String(), ".-")
	if segment == "" || segment == "." || segment == ".." {
		return ""
	}
	return segment
}

func chatCheckJUnitSuite(report any) (junitTestSuite, error) {
	switch result := report.(type) {
	case *chatcheck.Result:
		if result == nil {
			return junitTestSuite{}, fmt.Errorf("chat check result is nil")
		}
		return junitSuiteFromResults("buckley.doctor.chat", []chatcheck.Result{*result}), nil
	case *chatcheck.SuiteResult:
		if result == nil {
			return junitTestSuite{}, fmt.Errorf("chat check suite result is nil")
		}
		return junitSuiteFromResults(result.Name, result.Results), nil
	default:
		return junitTestSuite{}, fmt.Errorf("unsupported chat check report type %T", report)
	}
}

func junitSuiteFromResults(name string, results []chatcheck.Result) junitTestSuite {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "buckley.doctor.chat"
	}
	suite := junitTestSuite{
		Name:      name,
		Tests:     len(results),
		TestCases: make([]junitTestCase, 0, len(results)),
	}
	var totalMillis int64
	for _, result := range results {
		totalMillis += result.DurationMillis
		testCase := junitTestCase{
			ClassName: "buckley.doctor.chat",
			Name:      result.Name,
			Time:      secondsString(result.DurationMillis),
			SystemOut: chatCheckJUnitSystemOut(result),
		}
		if !result.Passed {
			suite.Failures++
			message := strings.TrimSpace(result.Error)
			if message == "" {
				message = "chat check failed"
			}
			testCase.Failure = &junitFailure{
				Message: message,
				Text:    chatCheckJUnitFailureText(result),
			}
		}
		suite.TestCases = append(suite.TestCases, testCase)
	}
	suite.Time = secondsString(totalMillis)
	return suite
}

func chatCheckJUnitSystemOut(result chatcheck.Result) string {
	lines := make([]string, 0, len(result.Turns)+1)
	lines = append(lines, fmt.Sprintf("model=%s session_id=%s passed=%v", result.Model, result.SessionID, result.Passed))
	for _, turn := range result.Turns {
		status := "passed"
		if !turn.Passed || strings.TrimSpace(turn.Err) != "" {
			status = "failed"
		}
		lines = append(lines, fmt.Sprintf("turn=%d status=%s chars=%d latency_ms=%d finish=%s tool_calls=%d reasoning=%v", turn.Index, status, turn.CharLength, turn.LatencyMillis, turn.Finish, turn.ToolCalls, turn.Reasoning))
	}
	return strings.Join(lines, "\n")
}

func chatCheckJUnitFailureText(result chatcheck.Result) string {
	lines := make([]string, 0, len(result.Turns)+1)
	if strings.TrimSpace(result.Error) != "" {
		lines = append(lines, result.Error)
	}
	for _, turn := range result.Turns {
		if strings.TrimSpace(turn.Err) != "" {
			lines = append(lines, fmt.Sprintf("turn %d: %s", turn.Index, turn.Err))
		}
		for _, check := range turn.Checks {
			if !check.Passed {
				lines = append(lines, fmt.Sprintf("turn %d %s: %s", turn.Index, check.Name, check.Message))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func secondsString(millis int64) string {
	if millis < 0 {
		millis = 0
	}
	return fmt.Sprintf("%.3f", float64(millis)/1000)
}

func ensureParentDir(path string, label string) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s directory: %w", label, err)
		}
	}
	return nil
}
