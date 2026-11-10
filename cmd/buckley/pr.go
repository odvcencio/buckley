package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/diffsignal"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/terminal"
	"m31labs.dev/buckley/pkg/transparency"
)

const (
	gitLocalTimeout = 30 * time.Second
	gitPushTimeout  = 60 * time.Second
	ghAPITimeout    = 120 * time.Second
)

type prCommandOptions struct {
	dryRun       bool
	yes          bool
	push         bool
	verbose      bool
	showCost     bool
	base         string
	model        string
	effort       string
	backend      string
	timeout      time.Duration
	diffBudget   int
	contextFiles []string
	noUpdate     bool
	draft        bool
	// strict fails the run when the generated text states a decision or a
	// number that its sources do not contain; the default is to warn.
	strict bool
	// template writes the body in the repository's pull request template when
	// there is one.
	template bool
	// title replaces the title, generated or read from bodyFile.
	title string
	// output is a file that receives the title and body as shown.
	output string
	// bodyFile is a file whose title and body are posted as they are, without
	// a model call.
	bodyFile string
	// head is the --head value; empty means the checked-out branch.
	head string
	// dir is the -C/--dir value; empty means the launch directory.
	dir string
	// verificationLabel is the requirement to set on the PR after it is
	// created or updated; empty leaves labels alone.
	verificationLabel commands.VerificationMode
}

type prCommandRuntime struct {
	backend   string
	modelID   string
	ledger    *transparency.CostLedger
	framework *oneshot.Framework
}

type prRunResult struct {
	PR           *commands.PRResult
	Trace        *transparency.Trace
	ContextAudit *transparency.ContextAudit
	// Context is what the framework gathered for the prompt. The generated
	// text is checked against it.
	Context *oneshot.Context
	Error   error
}

// observedPRDefinition records the context the framework gathers for the
// prompt, so that the generated text can be checked against what the model
// was given without reading git a second time.
type observedPRDefinition struct {
	commands.PRDefinition
	gathered **oneshot.Context
}

func (d observedPRDefinition) BuildPrompt(ctx *oneshot.Context) string {
	*d.gathered = ctx
	return d.PRDefinition.BuildPrompt(ctx)
}

func parsePRCommandOptions(args []string) (prCommandOptions, error) {
	fs := flag.NewFlagSet("pr", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print the generated PR without creating it")
	yes := fs.Bool("yes", false, "skip confirmation prompts and create the PR")
	pushFlag := fs.Bool("push", true, "push the head branch before creating PR")
	baseFlag := fs.String("base", "", "base branch (default: BUCKLEY_PR_BASE, then the remote's default branch, then main, master or develop)")
	headFlag := fs.String("head", "", "branch the PR comes from (default: the checked-out branch); owner:branch names a fork, and owner must own the push remote's repository. The PR text is built from that branch's commits, so the worktree can be on another branch or dirty")
	var dir string
	fs.StringVar(&dir, "C", "", "run as if started in this directory, like git -C (uppercase; lowercase -c selects the config file). Relative --context-file, --config and --agent paths resolve against the directory you launched in")
	fs.StringVar(&dir, "dir", "", "same as -C")
	verbose := fs.Bool("verbose", false, "show model reasoning and full trace")
	showCost := fs.Bool("cost", true, "show token/cost breakdown")
	modelFlag := fs.String("model", "", "model to use (default: BUCKLEY_MODEL_PR or models.utility.pr for API backend)")
	effortFlag := fs.String("effort", "", "reasoning effort: low, medium, high, xhigh, or max (default: BUCKLEY_EFFORT_PR, BUCKLEY_ONESHOT_EFFORT, or config)")
	backendFlag := fs.String("backend", "", "backend to use: api, codex, or claude (default: BUCKLEY_PR_BACKEND, BUCKLEY_ONESHOT_BACKEND, or api)")
	timeout := fs.Duration("timeout", 2*time.Minute, "timeout for model request")
	diffBudget := fs.Int("diff-budget", diffsignal.PRDiffBudget, "total byte budget for gathered diff context (default: PR-scale budget, larger than the commit-message default)")
	var contextFileFlag stringSliceFlag
	fs.Var(&contextFileFlag, "context-file", "path to a file of author-supplied notes for the PR (repeatable, oldest first: a later file wins). The model reads the notes as data, not instructions. Sections headed Testing, Counters, Evidence, Not verified or Known gaps are copied into the PR body word for word")
	noUpdate := fs.Bool("no-update", false, "fail instead of silently overwriting an existing open PR's title/body")
	draft := fs.Bool("draft", false, "create the PR as a draft (passed through to gh pr create --draft)")
	title := fs.String("title", "", "use this PR title instead of the generated one or the one in --body-file")
	output := fs.String("output", "", "also write the title and body to this file, in the form --body-file reads; with --dry-run this saves the text for review")
	bodyFile := fs.String("body-file", "", "post this file as it is, without a model call: a first line \"Title: ...\" is the title and the rest is the body (the form --output writes)")
	template := fs.Bool("template", true, "write the body in the repository's pull request template when it has one (.github/PULL_REQUEST_TEMPLATE.md, read from the base branch); --template=false keeps Buckley's own sections")
	strict := fs.Bool("strict", false, "fail, and create nothing, when the generated text states a decision or a number that the notes, commits and diff do not contain (default: warn)")
	requireVerification := fs.Bool("require-verification", false, "label the PR buckbot:verify-required so Buckbot needs a receipt or passing CI before it reviews")
	requireReceipt := fs.Bool("require-receipt", false, "label the PR buckbot:verify-receipt (receipt only)")
	noRequireVerification := fs.Bool("no-require-verification", false, "label the PR buckbot:verify-optional")

	if err := fs.Parse(args); err != nil {
		return prCommandOptions{}, err
	}
	if operands := fs.Args(); len(operands) > 0 {
		msg := fmt.Sprintf("unexpected argument %q: buckley pr takes flags only (name the branch with --head %s)", operands[0], operands[0])
		if dropped, _, found := unreadFlag(fs, args); found {
			msg += fmt.Sprintf("; the flag %q after it would not have been read either, so put every flag first", dropped)
		}
		return prCommandOptions{}, errors.New(msg)
	}
	effort, err := resolveOneshotEffort("pr", *effortFlag)
	if err != nil {
		return prCommandOptions{}, err
	}
	backend, err := resolveOneshotBackend("pr", *backendFlag)
	if err != nil {
		return prCommandOptions{}, err
	}
	verificationLabel, err := verificationFlagMode(*requireVerification, *requireReceipt, *noRequireVerification)
	if err != nil {
		return prCommandOptions{}, err
	}
	if strings.ContainsAny(*title, "\r\n") {
		return prCommandOptions{}, errors.New("--title must be one line")
	}
	if *bodyFile != "" && len(contextFileFlag) > 0 {
		return prCommandOptions{}, errors.New("--body-file posts the file as it is, so --context-file has no effect; pass one or the other")
	}
	return prCommandOptions{
		verificationLabel: verificationLabel,
		dryRun:            *dryRun,
		yes:               *yes,
		push:              *pushFlag,
		verbose:           *verbose,
		showCost:          *showCost,
		base:              *baseFlag,
		model:             *modelFlag,
		effort:            effort,
		backend:           backend,
		timeout:           *timeout,
		diffBudget:        *diffBudget,
		contextFiles:      append([]string(nil), contextFileFlag...),
		noUpdate:          *noUpdate,
		draft:             *draft,
		strict:            *strict,
		template:          *template,
		title:             strings.TrimSpace(*title),
		output:            *output,
		bodyFile:          *bodyFile,
		head:              *headFlag,
		dir:               dir,
	}, nil
}

// prContextNotesMaxBytes bounds the total size of author-supplied
// --context-file content that reaches the prompt: enough for a slice
// breakdown and verification notes, not an unbounded attachment.
const prContextNotesMaxBytes = 16_000

// loadPRNotes reads the --context-file paths in the order given. A missing or
// unreadable file is an error (an explicitly-requested file vanishing silently
// would defeat the point of asking for it). An empty file is left out.
func loadPRNotes(paths []string) ([]commands.PRNote, error) {
	var notes []commands.PRNote
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read --context-file %s: %w", path, err)
		}
		if text := strings.TrimSpace(string(data)); text != "" {
			notes = append(notes, commands.PRNote{Path: path, Text: text})
		}
	}
	return notes, nil
}

// prNotesPrompt renders the notes for the prompt, each labeled by its path
// and, when there are several, by its position, because a later note is the
// current one. The text is bounded to prContextNotesMaxBytes; the size bound
// truncates rather than errors, since a legitimate note that merely ran long
// is still useful context. cut is the number of bytes that were left out, so
// that the caller can tell the author instead of dropping them in silence.
func prNotesPrompt(notes []commands.PRNote) (text string, cut int) {
	var b strings.Builder
	for i, note := range notes {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		switch {
		case len(notes) == 1:
			fmt.Fprintf(&b, "### %s\n\n%s", note.Path, note.Text)
		case i == len(notes)-1:
			fmt.Fprintf(&b, "### Note %d of %d, the latest: %s\n\n%s", i+1, len(notes), note.Path, note.Text)
		default:
			fmt.Fprintf(&b, "### Note %d of %d: %s\n\n%s", i+1, len(notes), note.Path, note.Text)
		}
	}

	out := b.String()
	if len(out) <= prContextNotesMaxBytes {
		return out, 0
	}
	// Cut at the end of a line when there is one in the last part of the
	// budget, and never inside a UTF-8 sequence.
	keep := prContextNotesMaxBytes
	if i := strings.LastIndexByte(out[:keep], '\n'); i > keep/2 {
		keep = i
	}
	for keep > 0 && !utf8.RuneStart(out[keep]) {
		keep--
	}
	return out[:keep] + "\n... (truncated)", len(out) - keep
}

// prTemplatePaths are the places where GitHub looks for a repository's single
// pull request template, in both spellings it accepts.
var prTemplatePaths = []string{
	".github/PULL_REQUEST_TEMPLATE.md", ".github/pull_request_template.md",
	"PULL_REQUEST_TEMPLATE.md", "pull_request_template.md",
	"docs/PULL_REQUEST_TEMPLATE.md", "docs/pull_request_template.md",
}

// prTemplateMaxBytes bounds a template. A larger file is not one a person
// fills in by hand, so it is ignored with a warning.
const prTemplateMaxBytes = 32 * 1024

// loadPRTemplate reads the repository's pull request template from git
// objects, like the rest of the PR evidence: from the base branch, where
// GitHub reads it, and from the head when the base has none (a branch that
// adds the template). It returns nil when there is no template.
func loadPRTemplate(plan *prPlan) *commands.PRTemplate {
	for _, ref := range []string{plan.Base.Ref, plan.Head.Ref} {
		if ref == "" {
			continue
		}
		for _, path := range prTemplatePaths {
			text, err := gitOutput("show", ref+":"+path)
			if err != nil {
				continue
			}
			if len(text) > prTemplateMaxBytes {
				prWarnf("%s is %d bytes, more than the %d a pull request template may have; the body uses Buckley's own sections", path, len(text), prTemplateMaxBytes)
				return nil
			}
			return commands.ParsePRTemplate(text)
		}
	}
	return nil
}

// templateHeadings lists the headings of a template; nil has none.
func templateHeadings(template *commands.PRTemplate) []string {
	if template == nil {
		return nil
	}
	headings := make([]string, 0, len(template.Sections))
	for _, section := range template.Sections {
		headings = append(headings, section.Heading)
	}
	return headings
}

// prContextOpts derives the git-diff gathering options for a PR run: a
// PR-scale byte budget (diffsignal.PRDiffBudget by default, overridable via
// --diff-budget) and PR-specific ranking (source first, tests next,
// docs/config after, dotdirs/scratch/handoff notes last) rather than the
// 80KB commit-message default and git's alphabetical file order that
// RunOpts{} previously left BuildContext to fall back to.
func prContextOpts(diffBudget int) oneshot.ContextOpts {
	if diffBudget <= 0 {
		diffBudget = diffsignal.PRDiffBudget
	}
	return oneshot.ContextOpts{
		MaxDiffBytes:  diffBudget,
		RankDiffForPR: true,
	}
}

// runPRCommand generates a structured PR via tool-use.
func runPRCommand(args []string) error {
	if len(args) > 0 && args[0] == "merge" {
		return runPRMergeCommand(args[1:])
	}
	if len(args) > 0 && args[0] == "label" {
		return runPRLabelCommand(args[1:])
	}

	opts, err := parsePRCommandOptions(args)
	if err != nil {
		return err
	}
	if opts.dir != "" {
		// Paths the caller typed belong to the launch directory.
		paths, err := enterWorkDir(opts.dir, append(append([]string(nil), opts.contextFiles...), opts.output, opts.bodyFile))
		if err != nil {
			return err
		}
		n := len(opts.contextFiles)
		opts.contextFiles, opts.output, opts.bodyFile = paths[:n], paths[n], paths[n+1]
	}

	if !stdoutIsTerminalFn() {
		// Standard output is a file or a pipe: it gets the title and body, in
		// the form --body-file reads, and then the PR's URL. Progress, the
		// model line, the cost and warnings go to stderr.
		previous := termOut
		termOut = terminal.NewWithOutput(os.Stderr)
		defer func() { termOut = previous }()
	}

	plan, err := preflightPR(opts, execGhCommand)
	if err != nil {
		return err
	}
	warnPRHeadState(opts, plan)

	var text prText
	if opts.bodyFile != "" {
		text, err = readPRText(opts.bodyFile, opts.title)
		if err == nil {
			printPR(nil, text, plan)
		}
	} else {
		text, err = generatePRText(opts, plan)
	}
	if err != nil {
		return err
	}
	if opts.output != "" {
		if err := os.WriteFile(opts.output, []byte(formatPRText(text)), 0o644); err != nil {
			return fmt.Errorf("write --output %s: %w", opts.output, err)
		}
		fmt.Fprintf(os.Stderr, "PR text written to %s; post it as it is with --body-file %s\n", opts.output, opts.output)
	}
	if opts.dryRun {
		return nil
	}

	if err := confirmPRCreation(opts); err != nil {
		return err
	}

	if opts.push && plan.Head.PushNeeded {
		if err := pushPRHead(plan.Head, plan.Remote); err != nil {
			return err
		}
	}
	warnIfPRHeadMoved(plan.Head)

	prURL, err := createPR(execGhCommand, text, plan, opts)
	if err != nil {
		return err
	}
	if !stdoutIsTerminalFn() {
		fmt.Println(prURL)
	}
	if opts.verificationLabel != "" {
		ctx, cancel := context.WithTimeout(context.Background(), ghAPITimeout)
		defer cancel()
		if _, _, err := setVerificationLabel(ctx, ghLabelClient{}, prURL, opts.verificationLabel); err != nil {
			return err
		}
		termOut.Dim("Verification policy label set: %s", opts.verificationLabel)
	}

	return nil
}

// generatePRText asks the model for the PR text, checks it against its
// sources, prints it and returns the title and body as they will be posted.
func generatePRText(opts prCommandOptions, plan *prPlan) (prText, error) {
	warnPRGitVersion()

	notes, err := loadPRNotes(opts.contextFiles)
	if err != nil {
		return prText{}, err
	}
	contextNotes, cut := prNotesPrompt(notes)
	if cut > 0 {
		prWarnf("the notes are %d bytes too long for the prompt, so the model does not read their end; sections headed Testing, Counters, Evidence, Not verified or Known gaps still reach the PR body in full", cut)
	}
	var template *commands.PRTemplate
	if opts.template {
		template = loadPRTemplate(plan)
	}
	sections := commands.AuthorSections(notes, templateHeadings(template)...)

	runtime, cleanup, err := newPRCommandRuntimeFn(opts)
	defer cleanup()
	if err != nil {
		return prText{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	if !quietMode {
		termOut.Dim("Using %s", describeOneshotBackend(runtime.backend, runtime.modelID))
	}

	result, err := runPRGeneration(ctx, runtime.framework, plan, opts.diffBudget, contextNotes, sections, template)
	if err != nil {
		return prText{}, err
	}
	return renderPRGenerationResult(opts, plan, result, runtime.ledger, notes, sections, template)
}

// formatPRText is the file form of a PR text: a "Title:" line, a blank line
// and the body. parsePRText reads it back to the same title and body.
func formatPRText(text prText) string {
	return "Title: " + text.Title + "\n\n" + text.Body + "\n"
}

// parsePRText reads the file form of a PR text. A first line that starts with
// "Title:" is the title; without one the whole content is the body.
func parsePRText(content string) prText {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	first, rest, _ := strings.Cut(content, "\n")
	if title, ok := strings.CutPrefix(first, "Title:"); ok {
		return prText{Title: strings.TrimSpace(title), Body: strings.TrimSpace(rest)}
	}
	return prText{Body: strings.TrimSpace(content)}
}

// readPRText reads the text that --body-file names. title, when set, replaces
// the file's title. A text without a title or without a body is an error: the
// file is posted as it is, so nothing can fill a gap.
func readPRText(path, title string) (prText, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return prText{}, fmt.Errorf("read --body-file %s: %w", path, err)
	}
	text := parsePRText(string(data))
	if title != "" {
		text.Title = title
	}
	if text.Title == "" {
		return prText{}, fmt.Errorf("--body-file %s has no title: start it with a line \"Title: ...\" or pass --title", path)
	}
	if text.Body == "" {
		return prText{}, fmt.Errorf("--body-file %s has no body", path)
	}
	return text, nil
}

// newPRCommandRuntimeFn builds the model runtime. Tests replace it.
var newPRCommandRuntimeFn = newPRCommandRuntime

func newPRCommandRuntime(opts prCommandOptions) (*prCommandRuntime, func(), error) {
	cfg, mgr, store, err := initOneshotDependencies(opts.backend)
	cleanup := func() {
		if store != nil {
			store.Close()
		}
	}
	if err != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("init dependencies: %w", err)
	}

	modelID, cfg := resolveOneshotModelWithEffort(cfg, opts.effort, func(cfg *config.Config) string {
		return resolvePRModelID(opts.model, cfg, opts.backend)
	})
	if opts.backend == oneshotBackendAPI && modelID == "" {
		cleanup()
		return nil, func() {}, fmt.Errorf("no model configured (set BUCKLEY_MODEL_PR or configure models.utility.pr)")
	}

	ledger := transparency.NewCostLedger()
	invoker, err := newOneshotToolInvoker(opts.backend, "pr", modelID, cfg, mgr, ledger)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}

	return &prCommandRuntime{
		backend:   opts.backend,
		modelID:   modelID,
		ledger:    ledger,
		framework: withUtilityValidationFallbacks(oneshot.NewFramework(invoker, nil), opts.backend, "pr", modelID, cfg, mgr, ledger),
	}, cleanup, nil
}

func runPRGeneration(ctx context.Context, framework *oneshot.Framework, plan *prPlan, diffBudget int, contextNotes string, sections []commands.AuthorSection, template *commands.PRTemplate) (*prRunResult, error) {
	spinner := terminal.NewSpinnerWithOutput(prStatusWriter(), "Generating PR...")
	spinner.Start()

	var gathered *oneshot.Context
	def := observedPRDefinition{gathered: &gathered, PRDefinition: commands.PRDefinition{
		BaseBranch:     plan.Base.Ref,
		BaseName:       plan.Base.Label,
		HeadRef:        plan.Head.Ref,
		HeadName:       plan.Head.Name,
		ContextNotes:   contextNotes,
		AuthorSections: sections,
		Template:       template,
	}}
	fwResult, err := framework.Run(ctx, def, oneshot.RunOpts{
		ContextOpts: prContextOpts(diffBudget),
	})
	result := prRunResultFromFramework(fwResult)
	result.Context = gathered
	if err != nil {
		result.Error = err
		spinner.StopWithError(err.Error())
		return nil, fmt.Errorf("PR generation failed: %w", err)
	}
	if result.Error != nil {
		spinner.StopWithError(result.Error.Error())
	} else {
		spinner.StopWithSuccess("Generated PR")
	}
	return result, nil
}

func prRunResultFromFramework(fwResult *oneshot.RunResult) *prRunResult {
	result := &prRunResult{}
	if fwResult == nil {
		return result
	}
	result.Trace = fwResult.Trace
	result.ContextAudit = fwResult.ContextAudit
	if fwResult.Value == nil {
		return result
	}
	pr, ok := fwResult.Value.(*commands.PRResult)
	if !ok {
		result.Error = fmt.Errorf("unexpected result type: %T", fwResult.Value)
		return result
	}
	result.PR = pr
	return result
}

// prText is the PR title and body exactly as they are shown and posted.
type prText struct {
	Title string
	Body  string
}

func renderPRGenerationResult(opts prCommandOptions, plan *prPlan, result *prRunResult, ledger *transparency.CostLedger, notes []commands.PRNote, sections []commands.AuthorSection, template *commands.PRTemplate) (prText, error) {
	if opts.verbose && result.ContextAudit != nil {
		printContextAudit(result.ContextAudit)
	}
	if opts.verbose && result.Trace != nil && result.Trace.Reasoning != "" {
		printReasoning(result.Trace.Reasoning)
	}
	if result.Error != nil {
		printError(result.Error, result.Trace)
		return prText{}, result.Error
	}
	if result.PR == nil {
		return prText{}, fmt.Errorf("no PR generated")
	}

	for _, section := range sections {
		if section.Kind == commands.SectionTesting {
			// The author's testing record replaces the model's steps.
			result.PR.Testing = nil
		}
	}
	claims := checkPRAgainstSources(result.PR, commands.NewPRSources(notes, result.Context))
	text := prText{Title: opts.title, Body: result.PR.Body(commands.PRBodyOptions{
		Sections:         sections,
		StateNotVerified: true,
		HasNotes:         len(notes) > 0,
		Template:         template,
	})}
	if text.Title == "" {
		text.Title = result.PR.Header()
	}
	printPR(result.PR, text, plan)
	if opts.showCost && result.Trace != nil {
		printCost(result.Trace, ledger)
	}
	if opts.strict && claims > 0 {
		return prText{}, fmt.Errorf("the generated text states %d thing(s) without a source (see the warnings); nothing was created (--strict)", claims)
	}
	return text, nil
}

// checkPRAgainstSources compares the model's text with what it was written
// from. It removes each testing step whose command the sources do not contain
// as written, and says so. It warns about decisions and numbers that have no
// source, and returns how many of those there are: code cannot rewrite prose,
// so the author decides, or --strict stops the run.
func checkPRAgainstSources(pr *commands.PRResult, src commands.PRSources) (claims int) {
	for _, step := range pr.DropUnbackedTesting(src) {
		prWarnf("left out the testing step %q: its command is not in the notes, the commits, the diff or the project guidelines", step)
	}
	unbacked := pr.UnbackedClaims(src)
	for _, claim := range unbacked {
		prWarnf("%s", claim)
	}
	return len(unbacked)
}

var errPRNonInteractive = errors.New("refusing to create PR without confirmation in non-interactive mode (use --dry-run or --yes)")

func confirmPRCreation(opts prCommandOptions) error {
	if opts.yes {
		return nil
	}
	if !stdinIsTerminalFn() {
		return errPRNonInteractive
	}
	fmt.Fprint(prStatusWriter(), "\nCreate this PR? [y/N] ")
	var response string
	fmt.Scanln(&response)
	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		return fmt.Errorf("aborted")
	}
	return nil
}

// prPlan is everything the preflight resolves before the model call.
type prPlan struct {
	Head    prHead
	Base    prBase
	Remote  string
	Commits int
}

// explicitBase returns the base branch only when the caller chose it, which is
// the only case where an open PR with another base is a conflict.
func (p *prPlan) explicitBase() string {
	if p.Base.Explicit {
		return p.Base.Short
	}
	return ""
}

// summary renders "head -> base (N commits, tip abc1234)".
func (p *prPlan) summary() string {
	noun := "commits"
	if p.Commits == 1 {
		noun = "commit"
	}
	from := ""
	if !p.Head.Local {
		from = ", read from " + strings.TrimPrefix(p.Head.Ref, "refs/remotes/")
	}
	return fmt.Sprintf("%s → %s (%d %s, tip %s%s)", p.Head.Name, p.Base.Short, p.Commits, noun, shortSHA(p.Head.SHA), from)
}

// lookPathFn finds an executable on PATH. Tests replace it.
var lookPathFn = exec.LookPath

var errGhNotFound = errors.New("gh CLI not found (install from https://cli.github.com)")

// preflightPR runs every check that can fail without a model: the
// confirmation channel, gh, the head and base branches, the commit count and
// conflicts with an open PR. A failure here costs no tokens. A dry run skips
// the checks that need gh, the network or the push remote's URL, so it previews
// in a repository whose remote is missing or is not a GitHub URL.
func preflightPR(opts prCommandOptions, run ghCommandRunner) (*prPlan, error) {
	if !opts.yes && !opts.dryRun && !stdinIsTerminalFn() {
		return nil, errPRNonInteractive
	}
	if !opts.dryRun {
		if _, err := lookPathFn("gh"); err != nil {
			return nil, errGhNotFound
		}
	}

	remote, err := prRemoteName()
	if err != nil {
		return nil, err
	}
	head, err := resolvePRHead(prHeadOptions{Flag: opts.head, Remote: remote, Push: opts.push, CheckRemote: !opts.dryRun})
	if err != nil {
		return nil, err
	}
	base, err := resolvePRBase(opts.base, remote)
	if err != nil {
		return nil, err
	}
	if head.Name == base.Short {
		return nil, fmt.Errorf("head and base are both %s; pass --head <branch> or --base <branch>", head.Name)
	}
	commits, err := countPRCommits(base.Ref, head.Ref)
	if err != nil {
		return nil, err
	}
	if commits == 0 {
		return nil, fmt.Errorf("%s has no commits that %s lacks, so there is no PR to open", head.Name, base.Label)
	}
	plan := &prPlan{Head: head, Base: base, Remote: remote, Commits: commits}

	if opts.dryRun {
		return plan, nil
	}
	if plan.Head.Owner, err = prHeadOwner(head, remote); err != nil {
		return nil, err
	}
	existing, err := openPRForHead(run, plan.Head, plan.explicitBase())
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return plan, nil
	}
	if _, err := resolvePRUpdatePolicy(existing.URL, true, opts.noUpdate); err != nil {
		return nil, err
	}
	if !base.Explicit && existing.Base != base.Short {
		prWarnf("the open PR %s targets %s, but the PR text is written against %s; pass --base %s to match", existing.URL, existing.Base, base.Short, existing.Base)
	}
	return plan, nil
}

// warnPRHeadState reports head states the preflight accepts but the caller
// should know about.
func warnPRHeadState(opts prCommandOptions, plan *prPlan) {
	head, remoteBranch := plan.Head, plan.Remote+"/"+plan.Head.Name
	if head.RemoteErr != nil {
		prWarnf("could not compare %s with %s (%v); the PR text describes the local branch and may not match what the PR will contain", head.Name, remoteBranch, head.RemoteErr)
	}
	if head.Behind > 0 {
		prWarnf("%s is %d commit(s) behind %s; the PR text describes the remote branch and nothing is pushed", head.Name, head.Behind, remoteBranch)
	}
	if !opts.push && head.Ahead > 0 {
		prWarnf("%s has %d commit(s) that %s lacks and --push=false; the PR text describes %s and leaves them out", head.Name, head.Ahead, remoteBranch, remoteBranch)
	}
}

// warnPRGitVersion says so when the installed git cannot read .gitattributes
// from the head commit. The diff then follows the working tree's attributes,
// which may belong to another branch or hold uncommitted edits.
func warnPRGitVersion() {
	if ok, installed := oneshot.HeadAttributesSupported(); !ok {
		prWarnf("the installed git (%s) cannot read .gitattributes from the head commit; it needs 2.40 or newer. The diff follows the working tree's .gitattributes, so the PR text may not match the commits", installed)
	}
}

// prStatusWriter is where `buckley pr` writes progress. On a terminal that is
// standard output, next to the text. When standard output is a file or a
// pipe, it is stderr, so that the file holds the text and nothing else.
func prStatusWriter() *os.File {
	if stdoutIsTerminalFn() {
		return os.Stdout
	}
	return os.Stderr
}

func printPR(pr *commands.PRResult, text prText, plan *prPlan) {
	if !stdoutIsTerminalFn() {
		// The text once, as --output writes it: no header, no box, no styling.
		fmt.Print(formatPRText(text))
		return
	}
	termOut.Newline()
	termOut.Header("PULL REQUEST: " + plan.summary())

	// Build content for the box. A text from --body-file and a body in the
	// repository's template have no summary and changes to preview.
	if pr != nil && pr.Summary != "" {
		var content strings.Builder
		content.WriteString(text.Title)
		content.WriteString("\n\n")
		content.WriteString(pr.Summary)
		content.WriteString("\n\nChanges:")
		for _, change := range pr.Changes {
			content.WriteString("\n  - ")
			content.WriteString(change)
		}
		if pr.Breaking {
			content.WriteString("\n\n⚠️  BREAKING CHANGES")
		}

		termOut.Box("", content.String())
	}

	// Also print the full body for piping
	fmt.Println("Title:", text.Title)
	fmt.Println()
	fmt.Println(text.Body)
}

// createPR creates the PR for the plan, or updates the open PR for the same
// head, and returns its URL. The lookup keys on the head branch, so the PR
// that belongs to whatever branch the worktree has checked out is never touched.
func createPR(run ghCommandRunner, text prText, plan *prPlan, opts prCommandOptions) (string, error) {
	existing, err := openPRForHead(run, plan.Head, plan.explicitBase())
	if err != nil {
		return "", err
	}
	existingURL := ""
	if existing != nil {
		existingURL = existing.URL
	}
	update, err := resolvePRUpdatePolicy(existingURL, existing != nil, opts.noUpdate)
	if err != nil {
		return "", err
	}
	if update {
		if err := updatePR(run, existingURL, text.Title, text.Body); err != nil {
			return "", err
		}
		return existingURL, nil
	}

	spinner := terminal.NewSpinnerWithOutput(prStatusWriter(), "Creating PR...")
	spinner.Start()

	output, err := run(buildGhPRCreateArgs(text.Title, text.Body, plan.Base.Short, plan.Head.GhHead, opts.draft)...)
	if err != nil {
		spinner.StopWithError(fmt.Sprintf("failed: %s", err))
		return "", fmt.Errorf("gh pr create failed: %w", err)
	}

	prURL := lastLine(output)
	if !prURLPattern.MatchString(prURL) {
		spinner.StopWithError("gh printed no PR URL")
		return "", fmt.Errorf("gh pr create exited 0 but its last output line is not a PR URL (%q); check whether the PR was created", prURL)
	}
	spinner.StopWithSuccess(fmt.Sprintf("PR created: %s", prURL))
	return prURL, nil
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// resolvePRUpdatePolicy decides whether createPR should update an existing
// open PR in place, create a new one, or refuse. Kept pure (no gh
// invocation) so the --no-update guard is unit-testable directly.
//
// Default (noUpdate=false) preserves the pre-existing behavior: re-running
// `buckley pr` after new commits refreshes the open PR instead of failing
// with "already exists". --no-update opts into the stricter, less
// surprising behavior of refusing to touch a PR this run did not create.
func resolvePRUpdatePolicy(existingURL string, existingFound bool, noUpdate bool) (update bool, err error) {
	if !existingFound {
		return false, nil
	}
	if noUpdate {
		return false, fmt.Errorf("an open PR already exists at %s; refusing to overwrite its title/body (--no-update)", existingURL)
	}
	return true, nil
}

// buildGhPRCreateArgs assembles the `gh pr create` argument list. head is
// passed as --head only when the caller gave one; --draft is passed through
// when requested.
func buildGhPRCreateArgs(title, body, baseBranch, head string, draft bool) []string {
	args := []string{"pr", "create",
		"--title", title,
		"--body", body,
		"--base", baseBranch,
	}
	if head != "" {
		args = append(args, "--head", head)
	}
	if draft {
		args = append(args, "--draft")
	}
	return args
}

// openPullRequest is an open PR as `gh pr list` reports it.
type openPullRequest struct {
	Number int
	URL    string
	Base   string
}

// prURLPattern matches the URL of a pull request, the last line gh pr create
// prints.
var prURLPattern = regexp.MustCompile(`^https?://\S+/pull/[0-9]+$`)

// errEmptyPRSelector stops an empty PR argument before it reaches gh, which
// reads "" as "the PR for the current branch": a PR the caller did not mean.
var errEmptyPRSelector = errors.New("no PR given: gh would read an empty PR argument as the PR for the current branch")

// openPRForHead finds the open PR whose head is the branch head.Name of the
// repository head.Owner owns. Closed and merged PRs stay untouched: a fresh
// push after a merge gets a fresh PR. It returns nil when there is none and an
// error when gh fails, so a transient failure never reads as "no PR".
//
// gh filters by branch name only, so the listing holds every open PR with that
// head name: one from the base repository and one from each fork. Only the
// head repository's owner tells them apart, so the match needs it. In a fork
// checkout gh lists the parent repository, and the caller's own PR is the one
// whose head owner is the fork. A base other than "" is the base the caller
// chose: an open PR for the head with a different base is refused rather than
// edited, because editing it would change a PR the caller did not name.
func openPRForHead(run ghCommandRunner, head prHead, base string) (*openPullRequest, error) {
	out, err := run("pr", "list", "--head", head.Name, "--state", "open",
		"--json", "number,url,baseRefName,headRefName,headRepositoryOwner")
	if err != nil {
		return nil, fmt.Errorf("look up open PRs for %s: %w", head.Name, err)
	}
	var listed []struct {
		Number              int    `json:"number"`
		URL                 string `json:"url"`
		BaseRefName         string `json:"baseRefName"`
		HeadRefName         string `json:"headRefName"`
		HeadRepositoryOwner struct {
			Login string `json:"login"`
		} `json:"headRepositoryOwner"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return nil, fmt.Errorf("decode open PRs for %s: %w", head.Name, err)
	}
	var matches []openPullRequest
	for _, pr := range listed {
		// No login means the head repository was deleted; nothing can be
		// pushed to it, so the PR cannot be ours.
		login := pr.HeadRepositoryOwner.Login
		if pr.HeadRefName != head.Name || login == "" || !strings.EqualFold(login, head.Owner) {
			continue
		}
		if pr.Number <= 0 || !prURLPattern.MatchString(pr.URL) {
			return nil, fmt.Errorf("gh listed an open PR for %s without a usable number and URL (#%d %q)", head.Name, pr.Number, pr.URL)
		}
		matches = append(matches, openPullRequest{Number: pr.Number, URL: pr.URL, Base: pr.BaseRefName})
	}
	if len(matches) == 0 {
		return nil, nil
	}
	if base != "" {
		for i := range matches {
			if matches[i].Base == base {
				return &matches[i], nil
			}
		}
		return nil, fmt.Errorf("an open PR for %s targets %s, not %s; refusing to retarget it (%s)", head.Name, matches[0].Base, base, openPRURLs(matches))
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("%s has %d open PRs (%s); pass --base to pick one", head.Name, len(matches), openPRURLs(matches))
	}
	return &matches[0], nil
}

func openPRURLs(prs []openPullRequest) string {
	parts := make([]string, len(prs))
	for i, pr := range prs {
		parts[i] = fmt.Sprintf("#%d %s", pr.Number, pr.URL)
	}
	return strings.Join(parts, ", ")
}

// keepPreviousPRText saves the title and body that an update is about to
// replace, in the form --body-file reads, under the git directory. An update
// writes a newly generated text over the open PR. A body that someone edited
// on GitHub or with `gh pr edit` after an earlier run was lost without a
// trace; now it can be put back. A failure to read or save the old text does
// not stop the update: it is reported, and the caller asked for the update.
func keepPreviousPRText(run ghCommandRunner, url string, next prText) {
	out, err := run("pr", "view", url, "--json", "title,body")
	if err != nil {
		prWarnf("could not read the current text of %s (%v); it is replaced without a copy", url, err)
		return
	}
	var current struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if json.Unmarshal(out, &current) != nil {
		return
	}
	previous := prText{Title: strings.TrimSpace(current.Title), Body: strings.TrimSpace(strings.ReplaceAll(current.Body, "\r\n", "\n"))}
	if previous.Body == "" || previous == next {
		return
	}
	number := url[strings.LastIndex(url, "/")+1:]
	name := fmt.Sprintf("buckley/pr-%s-%s.md", number, time.Now().UTC().Format("20060102T150405Z"))
	path, err := gitOutput("rev-parse", "--git-path", name)
	if err == nil {
		path, err = filepath.Abs(path)
	}
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			err = os.WriteFile(path, []byte(formatPRText(previous)), 0o644)
		}
	}
	if err != nil {
		prWarnf("could not save the current text of %s (%v); it is replaced without a copy", url, err)
		return
	}
	fmt.Fprintf(os.Stderr, "The PR's previous title and body are saved in %s; to put them back: buckley pr --yes --body-file %s\n", path, path)
}

// updatePR regenerates the existing open PR's title and body in place,
// so re-running `buckley pr` after new commits refreshes the PR instead
// of failing with "already exists".
func updatePR(run ghCommandRunner, url, title, body string) error {
	if strings.TrimSpace(url) == "" {
		return errEmptyPRSelector
	}
	keepPreviousPRText(run, url, prText{Title: title, Body: body})
	spinner := terminal.NewSpinnerWithOutput(prStatusWriter(), "Updating PR...")
	spinner.Start()

	if _, err := run("pr", "edit", url, "--title", title, "--body", body); err != nil {
		spinner.StopWithError(fmt.Sprintf("failed: %s", err))
		return fmt.Errorf("gh pr edit failed: %w", err)
	}
	spinner.StopWithSuccess(fmt.Sprintf("PR updated: %s", url))
	return nil
}
