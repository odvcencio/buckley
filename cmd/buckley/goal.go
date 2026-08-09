package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/durability"
	daprbackend "m31labs.dev/buckley/pkg/durability/dapr"
	"m31labs.dev/buckley/pkg/durability/goalrunner"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/ralph"
	"m31labs.dev/buckley/pkg/replay"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
	"m31labs.dev/buckley/pkg/tool"
)

// runGoalCommand implements `buckley goal <start|status|list>` (goal-loop
// design section 7.1, slice G6): durable goal intake onto the run ledger,
// and read-only status over the resulting tree. Execution attach/detach
// UX arrives with G9; this surface creates and inspects goals.
func runGoalCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: buckley goal <start|status|list|report|run|audit|replay|approve> [flags]")
	}
	switch args[0] {
	case "start":
		return runGoalStart(args[1:])
	case "status":
		return runGoalStatus(args[1:])
	case "list":
		return runGoalList(args[1:])
	case "report":
		return runGoalReport(args[1:])
	case "run":
		return runGoalRun(args[1:])
	case "audit":
		return runGoalAudit(args[1:])
	case "replay":
		return runGoalReplay(args[1:])
	case "approve":
		return runGoalApprove(args[1:])
	case "worker":
		return runGoalWorker(args[1:])
	default:
		return fmt.Errorf("unknown goal subcommand %q (want start, status, list, report, run, audit, replay, approve, or worker)", args[0])
	}
}

// runGoalWorker hosts durable goal activities as a standalone process:
// it registers the workflows against the Dapr sidecar and serves any
// goal on the ledger, resolving each run's engine on first use. This is
// the separately schedulable worker (spec decision 2): one pod runs
// `buckley goal worker` while `buckley goal run` elsewhere schedules
// and observes.
func runGoalWorker(args []string) error {
	fs := flag.NewFlagSet("goal worker", flag.ContinueOnError)
	endpoint := fs.String("endpoint", "", "Dapr sidecar gRPC endpoint (default: execution.dapr_grpc_endpoint, DAPR_GRPC_ENDPOINT, or localhost:50001)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: buckley goal worker [--endpoint host:port]")
	}

	cfg, mgr, store, err := initDependenciesFn()
	if err != nil {
		return err
	}
	defer store.Close()
	stores, cleanup, err := openGoalStores()
	if err != nil {
		return err
	}
	defer cleanup()
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}

	registry := tool.NewRegistry()
	tool.ApplyToolMiddlewareConfig(registry, cfg)
	registry.ConfigureContainers(cfg, workDir)
	registry.SetWorkDir(workDir)
	engine := newGoalTurnEngine(cfg, mgr, registry, stores.ledger, stores.evidence, workDir, "goal-worker")

	loop, err := goalloop.New(goalloop.Config{
		Ledger:      stores.ledger,
		Checkpoints: stores.checkpoints,
		Engine:      engine,
		SessionID:   "goal-worker",
	})
	if err != nil {
		return err
	}
	resolver := goalrunner.NewResolver(func(ctx context.Context, runID string) (*goalrunner.Runner, error) {
		goal, specs, err := loop.LoadGoal(ctx, runID)
		if err != nil {
			return nil, err
		}
		return goalrunner.New(loop, goal, specs), nil
	})

	sidecar := *endpoint
	if sidecar == "" {
		sidecar = cfg.Execution.DaprGRPCEndpoint
	}
	backend, err := daprbackend.New(sidecar)
	if err != nil {
		return err
	}
	defer backend.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	healthCtx, cancelHealth := context.WithTimeout(ctx, 5*time.Second)
	defer cancelHealth()
	if err := backend.Health(healthCtx); err != nil {
		return err
	}
	if err := backend.StartWorker(ctx, resolver); err != nil {
		return err
	}
	fmt.Println("Goal worker serving durable workflows; interrupt to stop.")
	<-ctx.Done()
	fmt.Println("Goal worker stopping; in-flight state is durable and resumes on the next worker.")
	return nil
}

// runGoalApprove resolves a durable approval wait: it finds the parked
// task's waiting workflow instance on the run ledger and raises the
// approval event at it through the Dapr backend.
func runGoalApprove(args []string) error {
	fs := flag.NewFlagSet("goal approve", flag.ContinueOnError)
	deny := fs.Bool("deny", false, "deny instead of approve; the task stays parked")
	reason := fs.String("reason", "", "reason recorded with the decision")
	taskFlag := fs.String("task", "", "task ID when more than one task is waiting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: buckley goal approve [--deny] [--reason r] [--task task-id] <run-id>")
	}
	runID := strings.TrimSpace(fs.Arg(0))

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	stores, cleanup, err := openGoalStores()
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	events, err := stores.ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID})
	if err != nil {
		return err
	}
	// The latest unresolved wait per task is the approval surface.
	waiting := map[string]string{}
	for _, ev := range events {
		instance, _ := ev.Payload["workflow_instance_id"].(string)
		switch ev.Type {
		case runledger.EventDurableApprovalWaiting:
			waiting[ev.TaskID] = instance
		case runledger.EventDurableApprovalResolved:
			if waiting[ev.TaskID] == instance {
				delete(waiting, ev.TaskID)
			}
		}
	}
	if len(waiting) == 0 {
		return fmt.Errorf("no task in run %s is waiting for approval", runID)
	}
	taskID := strings.TrimSpace(*taskFlag)
	if taskID == "" {
		if len(waiting) > 1 {
			ids := make([]string, 0, len(waiting))
			for id := range waiting {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			return fmt.Errorf("multiple tasks are waiting (%s); pass --task", strings.Join(ids, ", "))
		}
		for id := range waiting {
			taskID = id
		}
	}
	instanceID, ok := waiting[taskID]
	if !ok || instanceID == "" {
		return fmt.Errorf("task %s is not waiting for approval", taskID)
	}

	backend, err := daprbackend.New(cfg.Execution.DaprGRPCEndpoint)
	if err != nil {
		return err
	}
	defer backend.Close()
	decision := durability.ApprovalDecision{Approved: !*deny, Reason: *reason}
	if err := backend.RaiseApproval(ctx, instanceID, decision); err != nil {
		return err
	}
	verb := "approved"
	if *deny {
		verb = "denied"
	}
	fmt.Printf("Task %s %s; the durable workflow resumes on its own.\n", taskID, verb)
	return nil
}

// runGoalAudit prints a run's full decision-and-capability trail from
// the ledger: controller decisions, capability calls, budget events,
// and task transitions, in order. This is the buckley-native full-truth
// view — no external observability system required.
func runGoalAudit(args []string) error {
	fs := flag.NewFlagSet("goal audit", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: buckley goal audit <run-id>")
	}
	stores, cleanup, err := openGoalStores()
	if err != nil {
		return err
	}
	defer cleanup()

	events, err := stores.ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: strings.TrimSpace(fs.Arg(0))})
	if err != nil {
		return err
	}
	shown := 0
	for _, ev := range events {
		var line string
		switch {
		case ev.Type == "capability.call":
			line = fmt.Sprintf("caps %-12s %-6s %s", ev.Payload["method"], ev.Payload["outcome"], truncate(fmt.Sprint(ev.Payload["params"]), 80))
		case ev.Type == runledger.EventControllerDecision:
			line = fmt.Sprintf("decide %-12s %s", ev.Payload["decision"], truncate(fmt.Sprint(ev.Payload["reason"]), 90))
		case strings.HasPrefix(ev.Type, "model.") || strings.HasPrefix(ev.Type, "tool."):
			line = fmt.Sprintf("%-28s step=%s attempt=%v", ev.Type, truncate(fmt.Sprint(ev.Payload["step_id"]), 72), ev.Payload["attempt"])
		case strings.HasPrefix(ev.Type, "budget."):
			line = fmt.Sprintf("%s spent=%.2f remaining=%.2f", ev.Type, floatFrom(ev.Payload["spent_usd"]), floatFrom(ev.Payload["remaining"]))
		case strings.HasPrefix(ev.Type, "task."):
			line = fmt.Sprintf("%s %s", ev.Type, ev.TaskID)
		default:
			continue
		}
		fmt.Printf("%s  %s\n", ev.Timestamp.Local().Format("15:04:05"), line)
		shown++
	}
	if shown == 0 {
		fmt.Println("No audited events for this run")
	}
	return nil
}

// runGoalReplay verifies a goal's durable replay contract without invoking a
// model, tool, or external process. It is deliberately read-only: a valid
// report means completed steps have stable identities and resolvable evidence,
// not that Buckley should execute them again.
func runGoalReplay(args []string) error {
	fs := flag.NewFlagSet("goal replay", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print the verification report as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: buckley goal replay [--json] <run-id>")
	}
	stores, cleanup, err := openGoalStores()
	if err != nil {
		return err
	}
	defer cleanup()

	runID := strings.TrimSpace(fs.Arg(0))
	report, err := replay.Verify(context.Background(), stores.ledger, stores.ledger, stores.evidence, runID)
	if err != nil {
		return err
	}
	if *jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			return err
		}
		if !report.Valid {
			return fmt.Errorf("replay verification failed for run %s", runID)
		}
		return nil
	}

	status := "INVALID"
	if report.Valid {
		status = "VALID"
	}
	fmt.Printf("Replay verification: %s\n", status)
	fmt.Printf("  run: %s (%s) · events: %d · tasks: %d · steps: %d · evidence: %d\n",
		report.RunID, report.RunStatus, report.EventCount, report.TaskCount, report.StepCount, report.EvidenceCount)
	for _, issue := range report.Issues {
		fmt.Printf("  [%s] %s: %s\n", issue.Severity, issue.Code, issue.Message)
	}
	if !report.Valid {
		return fmt.Errorf("replay verification failed for run %s", runID)
	}
	return nil
}

func floatFrom(v any) float64 {
	f, _ := v.(float64)
	return f
}

// runGoalRun drives a recorded goal against the live model stack until
// the queue drains, the budget parks the work, or the user interrupts.
// Interruption is safe by construction: every drive exit checkpoints, so
// the next `goal run` resumes from durable state.
func runGoalRun(args []string) error {
	fs := flag.NewFlagSet("goal run", flag.ContinueOnError)
	backendName := fs.String("backend", "", "delegate whole tasks to an external CLI backend (claude, codex) instead of the internal engine")
	execProgram := fs.Bool("exec-program", false, "offer the exec_program code-mode tool (read-only jailed capabilities, fully audited); internal engine only")
	execCaps := fs.String("exec-caps", "readonly", "capability grant for exec_program: readonly (read, list, search) | minimal (read, list)")
	durableFlag := fs.String("durable-backend", "", "override execution.durable_backend for this run: local | dapr")
	maxParallel := fs.Int("max-parallel", 1, "durable backend only: run up to this many claim-independent tasks concurrently")
	approvalWait := fs.Duration("approval-wait", 0, "durable backend only: hold parked tasks on a durable approval wait this long (0 disables; resolve with `buckley goal approve`)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: buckley goal run [--backend claude|codex] <run-id>")
	}
	runID := strings.TrimSpace(fs.Arg(0))

	cfg, mgr, store, err := initDependenciesFn()
	if err != nil {
		return err
	}
	defer store.Close()

	stores, cleanup, err := openGoalStores()
	if err != nil {
		return err
	}
	defer cleanup()

	workDir, err := os.Getwd()
	if err != nil {
		return err
	}

	var engine goalloop.TurnEngine
	if *backendName != "" {
		backend, err := goalBackendFor(*backendName)
		if err != nil {
			return err
		}
		engine, err = ralph.NewBackendTurnEngine(backend, stores.evidence, workDir)
		if err != nil {
			return err
		}
	} else {
		registry := tool.NewRegistry()
		tool.ApplyToolMiddlewareConfig(registry, cfg)
		registry.ConfigureContainers(cfg, workDir)
		registry.SetWorkDir(workDir)
		if *execProgram {
			capabilities := execmode.ReadOnlySet
			if *execCaps == "minimal" {
				capabilities = execmode.MinimalSet
			} else if *execCaps != "readonly" {
				return fmt.Errorf("unknown --exec-caps %q (want readonly or minimal)", *execCaps)
			}
			execTool, err := newExecProgramTool(workDir, stores.ledger, stores.evidence, runID, "goal-cli", capabilities)
			if err != nil {
				return err
			}
			registry.Register(execTool)
		}
		turnEngine := newGoalTurnEngine(cfg, mgr, registry, stores.ledger, stores.evidence, workDir, "goal-cli")
		turnEngine.codeMode = *execProgram
		engine = turnEngine
	}

	loop, err := goalloop.New(goalloop.Config{
		Ledger:      stores.ledger,
		Checkpoints: stores.checkpoints,
		Engine:      engine,
		SessionID:   "goal-cli",
	})
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	goal, specs, err := loop.LoadGoal(ctx, runID)
	if err != nil {
		return err
	}
	fmt.Printf("Running goal %s: %s\n", runID, goal.Statement)
	if goal.BudgetUSD > 0 {
		fmt.Printf("Budget: $%.2f · posture: %s\n", goal.BudgetUSD, goal.Posture)
	}

	durableBackend := strings.ToLower(strings.TrimSpace(*durableFlag))
	if durableBackend == "" {
		durableBackend = strings.ToLower(strings.TrimSpace(cfg.Execution.DurableBackend))
	}
	switch durableBackend {
	case "", config.DurableBackendLocal:
		// The in-process loop below is the default.
	case config.DurableBackendDapr:
		return runGoalDurable(ctx, cfg, loop, goal, specs, runID, *maxParallel, *approvalWait)
	default:
		return fmt.Errorf("unknown durable backend %q (want local or dapr)", durableBackend)
	}

	results, err := loop.Drain(ctx, runID, goal, specs)
	for _, result := range results {
		line := fmt.Sprintf("  [%s] %s — %d turn(s), $%.2f", result.Status, result.TaskID, result.Turns, result.SpentUSD)
		if result.Decision != "" {
			line += " (" + string(result.Decision) + ")"
		}
		fmt.Println(line)
	}
	if err != nil {
		if ctx.Err() != nil {
			fmt.Println("Interrupted; progress is checkpointed. Rerun `buckley goal run` to continue.")
			return nil
		}
		return err
	}
	fmt.Printf("Queue drained. Report: buckley goal report %s\n", runID)
	return nil
}

// runGoalDurable drives one goal through the Dapr workflow backend
// (spec.durable-execution-dapr, Phase 1). The worker runs in this
// process; Dapr owns scheduling, so an interrupt leaves the workflow
// resumable and a later `goal run` re-attaches to the same instance.
func runGoalDurable(ctx context.Context, cfg *config.Config, loop *goalloop.Loop, goal goalloop.Goal, specs map[string]goalloop.TaskSpec, runID string, maxParallel int, approvalWait time.Duration) error {
	backend, err := daprbackend.New(cfg.Execution.DaprGRPCEndpoint)
	if err != nil {
		return err
	}
	defer backend.Close()

	healthCtx, cancelHealth := context.WithTimeout(ctx, 5*time.Second)
	defer cancelHealth()
	if err := backend.Health(healthCtx); err != nil {
		return err
	}
	if err := backend.StartWorker(ctx, goalrunner.New(loop, goal, specs)); err != nil {
		return err
	}
	instanceID, err := backend.StartGoal(ctx, durability.GoalStart{RunID: runID, MaxParallel: maxParallel, ApprovalWaitMS: approvalWait.Milliseconds()})
	if err != nil {
		return err
	}
	fmt.Printf("Durable workflow %s running on the dapr backend\n", instanceID)

	status, err := backend.WaitForGoal(ctx, instanceID)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Println("Interrupted; the workflow stays durable under Dapr. Rerun `buckley goal run` to re-attach.")
			return nil
		}
		return err
	}
	for _, task := range status.Result.Tasks {
		line := fmt.Sprintf("  [%s] %s — %d turn(s), $%.2f", task.Status, task.TaskID, task.Turns, task.SpentUSD)
		if task.Decision != "" {
			line += " (" + task.Decision + ")"
		}
		fmt.Println(line)
	}
	if status.Failure != "" {
		return fmt.Errorf("durable goal %s failed: %s", instanceID, status.Failure)
	}
	fmt.Printf("Workflow %s %s. Report: buckley goal report %s\n", instanceID, strings.ToLower(status.RuntimeStatus), runID)
	return nil
}

// runGoalReport prints the goal's morning report (design 7.3): the
// durable roll-up the overnight posture leaves for the user.
func runGoalReport(args []string) error {
	fs := flag.NewFlagSet("goal report", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: buckley goal report <run-id>")
	}

	loop, cleanup, err := newGoalLoop()
	if err != nil {
		return err
	}
	defer cleanup()

	report, err := loop.Report(context.Background(), strings.TrimSpace(fs.Arg(0)))
	if err != nil {
		return err
	}
	fmt.Print(goalloop.RenderReport(report))
	return nil
}

// goalStringList collects a repeatable string flag.
type goalStringList []string

func (l *goalStringList) String() string { return strings.Join(*l, "; ") }
func (l *goalStringList) Set(v string) error {
	v = strings.TrimSpace(v)
	if v != "" {
		*l = append(*l, v)
	}
	return nil
}

func runGoalStart(args []string) error {
	fs := flag.NewFlagSet("goal start", flag.ContinueOnError)
	budget := fs.Float64("budget", 0, "dollar ceiling for the goal (0 = no budget)")
	posture := fs.String("posture", "interactive", "budget posture: interactive | frugal | overnight")
	approval := fs.String("approval", "safe", "approval envelope while unattended (ADR 0006 tier)")
	var criteria goalStringList
	fs.Var(&criteria, "criteria", "acceptance criterion (repeatable)")
	var constraints goalStringList
	fs.Var(&constraints, "constraint", "constraint (repeatable)")
	var tasks goalStringList
	fs.Var(&tasks, "task", "explicit task, in queue order (repeatable; omit to run the goal as one task)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: buckley goal start [flags] \"<statement>\"")
	}
	statement := strings.TrimSpace(strings.Join(fs.Args(), " "))

	loop, cleanup, err := newGoalLoopWithTasks(tasks)
	if err != nil {
		return err
	}
	defer cleanup()

	intake, err := loop.Start(context.Background(), goalloop.Goal{
		Statement:          statement,
		AcceptanceCriteria: criteria,
		Constraints:        constraints,
		BudgetUSD:          *budget,
		Posture:            *posture,
		ApprovalMode:       *approval,
	})
	if err != nil {
		return err
	}

	fmt.Printf("Goal recorded: run %s\n", intake.RunID)
	fmt.Printf("  %s\n", intake.Goal.Statement)
	if *budget > 0 {
		fmt.Printf("  budget: $%.2f · posture: %s · approval: %s\n", *budget, *posture, *approval)
	}
	fmt.Println("Tasks:")
	for i, task := range intake.Tasks {
		fmt.Printf("  %d. %s (%s)\n", i+1, task.Spec.Title, task.TaskID)
	}
	fmt.Printf("Inspect with: buckley goal status %s\n", intake.RunID)
	return nil
}

func runGoalStatus(args []string) error {
	fs := flag.NewFlagSet("goal status", flag.ContinueOnError)
	watch := fs.Bool("watch", false, "re-render every few seconds until interrupted")
	interval := fs.Duration("interval", 10*time.Second, "watch refresh interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: buckley goal status [--watch] <run-id>")
	}
	runID := strings.TrimSpace(fs.Arg(0))

	stores, cleanup, err := openGoalStores()
	if err != nil {
		return err
	}
	defer cleanup()

	if !*watch {
		return printGoalStatus(context.Background(), stores, runID)
	}

	// Watch mode: a second terminal's cheap observation loop while
	// `goal run` drives in the first. Reads only durable state, so it
	// never disturbs the run.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	for {
		if err := printGoalStatus(ctx, stores, runID); err != nil {
			return err
		}
		fmt.Println("---")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*interval):
		}
	}
}

func printGoalStatus(ctx context.Context, stores *goalStores, runID string) error {
	tree, err := runledger.LoadGoalTree(ctx, stores.ledger, runID)
	if err != nil {
		return fmt.Errorf("load goal %s: %w", runID, err)
	}

	fmt.Printf("Run %s status=%s started=%s\n", tree.Run.RunID, tree.Run.Status, tree.Run.StartedAt.Local().Format(time.RFC822))
	for taskID, task := range tree.State.Tasks {
		created, ok := firstGoalEvent(task.Events)
		label := taskID
		if ok {
			if title, _ := created.Payload["title"].(string); title != "" {
				label = title
			} else if statement, _ := created.Payload["statement"].(string); statement != "" {
				label = "goal: " + statement
			}
		}
		status := task.Status
		var debt int
		if resumed, err := stores.checkpoints.Resume(ctx, taskID); err == nil {
			status = resumed.State.Status
			debt = resumed.State.VerificationDebt()
		}
		line := fmt.Sprintf("  [%s] %s (%s)", status, label, taskID)
		if debt > 0 {
			line += fmt.Sprintf(" — verification debt %d", debt)
		}
		fmt.Println(line)
	}
	return nil
}

func runGoalList(args []string) error {
	fs := flag.NewFlagSet("goal list", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "max goals to list")
	if err := fs.Parse(args); err != nil {
		return err
	}

	stores, cleanup, err := openGoalStores()
	if err != nil {
		return err
	}
	defer cleanup()

	runs, err := stores.ledger.ListRuns(context.Background(), runledger.RunQuery{Limit: *limit * 4})
	if err != nil {
		return fmt.Errorf("list runs: %w", err)
	}
	shown := 0
	for _, run := range runs {
		if run.Backend != "goalloop" {
			continue
		}
		fmt.Printf("%-28s %-10s %s\n", run.RunID, run.Status, run.StartedAt.Local().Format(time.RFC822))
		if shown++; shown >= *limit {
			break
		}
	}
	if shown == 0 {
		fmt.Println("No goals recorded")
	}
	return nil
}

// goalBackendFor builds a named external backend preset (design section
// 8: external backends as task executors). Unknown names run as a bare
// command receiving the prompt as its single argument.
func goalBackendFor(name string) (ralph.Backend, error) {
	switch name {
	case "claude":
		return ralph.NewExternalBackend("claude", "claude", []string{"-p", "{prompt}"}, nil), nil
	case "codex":
		return ralph.NewExternalBackend("codex", "codex", []string{"exec", "{prompt}"}, nil), nil
	default:
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("backend name is required")
		}
		return ralph.NewExternalBackend(name, name, []string{"{prompt}"}, nil), nil
	}
}

func firstGoalEvent(events []runledger.Event) (runledger.Event, bool) {
	for _, ev := range events {
		if ev.Type == runledger.EventTaskCreated {
			return ev, true
		}
	}
	return runledger.Event{}, false
}

// goalStores bundles the durable stores the goal loop composes.
type goalStores struct {
	ledger      *runledger.SQLiteStore
	checkpoints *taskstate.Manager
	evidence    *evidence.SQLiteStore
}

// openGoalStores opens the shared ledger database: evidence and run
// ledger share one SQLite file so checkpoint evidence references are
// enforced. Path: BUCKLEY_DATA_DIR/ledger.db or ~/.buckley/ledger.db.
func openGoalStores() (*goalStores, func(), error) {
	path, err := resolveLedgerDBPath()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, fmt.Errorf("prepare ledger directory: %w", err)
	}
	ev, err := evidence.New(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open evidence store: %w", err)
	}
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		_ = ev.Close()
		return nil, nil, fmt.Errorf("open run ledger: %w", err)
	}
	checkpoints, err := taskstate.NewManager(ledger, ev)
	if err != nil {
		_ = ev.Close()
		return nil, nil, err
	}
	return &goalStores{ledger: ledger, checkpoints: checkpoints, evidence: ev}, func() { _ = ev.Close() }, nil
}

func newGoalLoop() (*goalloop.Loop, func(), error) {
	return newGoalLoopWithTasks(nil)
}

// newGoalLoopWithTasks wires the loop with an explicit manual
// decomposition when tasks are given: each --task line becomes one
// TaskSpec, priority following queue order. The model-driven planner
// stays a port for later; manual decomposition is the daily-use path.
func newGoalLoopWithTasks(tasks []string) (*goalloop.Loop, func(), error) {
	stores, cleanup, err := openGoalStores()
	if err != nil {
		return nil, nil, err
	}
	cfg := goalloop.Config{
		Ledger:      stores.ledger,
		Checkpoints: stores.checkpoints,
		SessionID:   "goal-cli",
	}
	if len(tasks) > 0 {
		specs := make([]goalloop.TaskSpec, 0, len(tasks))
		for i, title := range tasks {
			specs = append(specs, goalloop.TaskSpec{Title: title, Priority: i + 1})
		}
		cfg.Planner = staticGoalPlanner{specs: specs}
	}
	loop, err := goalloop.New(cfg)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return loop, cleanup, nil
}

// staticGoalPlanner returns a fixed decomposition (the --task flags).
type staticGoalPlanner struct {
	specs []goalloop.TaskSpec
}

func (p staticGoalPlanner) Decompose(context.Context, goalloop.Goal) ([]goalloop.TaskSpec, error) {
	return p.specs, nil
}

// resolveLedgerDBPath locates the run ledger database, following the same
// convention as resolveDBPath: BUCKLEY_DATA_DIR first, then ~/.buckley.
func resolveLedgerDBPath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(envBuckleyDataDir)); dir != "" {
		dir, err := expandHomePath(dir)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "ledger.db"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".buckley", "ledger.db"), nil
}
