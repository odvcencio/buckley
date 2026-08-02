package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

// runGoalCommand implements `buckley goal <start|status|list>` (goal-loop
// design section 7.1, slice G6): durable goal intake onto the run ledger,
// and read-only status over the resulting tree. Execution attach/detach
// UX arrives with G9; this surface creates and inspects goals.
func runGoalCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: buckley goal <start|status|list> [flags]")
	}
	switch args[0] {
	case "start":
		return runGoalStart(args[1:])
	case "status":
		return runGoalStatus(args[1:])
	case "list":
		return runGoalList(args[1:])
	default:
		return fmt.Errorf("unknown goal subcommand %q (want start, status, or list)", args[0])
	}
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: buckley goal start [flags] \"<statement>\"")
	}
	statement := strings.TrimSpace(strings.Join(fs.Args(), " "))

	loop, cleanup, err := newGoalLoop()
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: buckley goal status <run-id>")
	}
	runID := strings.TrimSpace(fs.Arg(0))

	stores, cleanup, err := openGoalStores()
	if err != nil {
		return err
	}
	defer cleanup()
	ctx := context.Background()

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

func firstGoalEvent(events []runledger.Event) (runledger.Event, bool) {
	for _, ev := range events {
		if ev.Type == runledger.EventTaskCreated {
			return ev, true
		}
	}
	return runledger.Event{}, false
}

// goalStores bundles the two durable stores the goal loop composes.
type goalStores struct {
	ledger      *runledger.SQLiteStore
	checkpoints *taskstate.Manager
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
	return &goalStores{ledger: ledger, checkpoints: checkpoints}, func() { _ = ev.Close() }, nil
}

func newGoalLoop() (*goalloop.Loop, func(), error) {
	stores, cleanup, err := openGoalStores()
	if err != nil {
		return nil, nil, err
	}
	loop, err := goalloop.New(goalloop.Config{
		Ledger:      stores.ledger,
		Checkpoints: stores.checkpoints,
		SessionID:   "goal-cli",
	})
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return loop, cleanup, nil
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
