package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/diffsignal"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/verifyreceipt"
)

const verifyUsage = `usage:
  buckley verify run [--name NAME] [--paths GLOB]... [--dir DIR]
      Run the repository's declared verification commands here, in the real
      work tree, and record a receipt for each. Refuses a dirty tree.
  buckley verify record [--paths GLOB]... [--name NAME] [--dir DIR] -- COMMAND [ARGS...]
      Run COMMAND and record a receipt for it. Use this to wrap a test run you
      are doing anyway. --paths is required unless COMMAND matches a declared one.
  buckley verify list [--dir DIR]
      Show the receipts recorded for the current tree.`

func runVerifyCommand(args []string) error {
	if len(args) == 0 {
		return errors.New(verifyUsage)
	}
	switch args[0] {
	case "run":
		return runVerifyRun(args[1:])
	case "record":
		return runVerifyRecord(args[1:])
	case "list":
		return runVerifyList(args[1:])
	default:
		return fmt.Errorf("unknown verify subcommand %q\n%s", args[0], verifyUsage)
	}
}

func openVerifyLedger() (*evidence.SQLiteStore, error) {
	cfg, err := loadConfiguredConfig()
	if err != nil {
		return nil, err
	}
	path, err := resolveLedgerDBPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return nil, fmt.Errorf("prepare ledger directory: %w", err)
	}
	store, err := evidence.New(path)
	if err != nil {
		return nil, err
	}
	if _, err := store.SweepCount(context.Background(), evidence.DefaultRetentionPolicy(), time.Now()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("clean expired verification evidence: %w", err)
	}
	if _, err := store.CleanupOrphanBlobCount(context.Background()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("clean expired verification logs: %w", err)
	}
	if err := storage.ConfigureStorageBudget(context.Background(), store.DB(), cfg.Resources.StorageBytes); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("configure verification storage budget: %w", err)
	}
	return store, nil
}

func dirOf(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i > 0 {
		return path[:i]
	}
	return "."
}

type pathFlags []string

func (p *pathFlags) String() string     { return strings.Join(*p, ",") }
func (p *pathFlags) Set(v string) error { *p = append(*p, v); return nil }

func printReceipt(r verifyreceipt.Receipt) {
	fmt.Printf("receipt %s\n  %s  exit %d  tree %.12s  commit %.12s\n  command: %s\n  covers: %s\n  log: %s (sha256 %.16s, %d bytes)\n  on %s (%s/%s) at %s\n",
		r.ID, r.Status, r.ExitCode, r.Tree, r.Commit, r.Command(), strings.Join(r.Paths, ", "),
		r.LogEvidence, r.LogSHA256, r.LogBytes, r.Env.Hostname, r.Env.OS, r.Env.Arch, r.StartedAt.Format("2006-01-02 15:04:05Z07:00"))
}

func verifyContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runVerifyRecord(args []string) error {
	fs := flag.NewFlagSet("verify record", flag.ContinueOnError)
	var paths pathFlags
	fs.Var(&paths, "paths", "repository-relative glob the command covers (repeatable; '**' crosses directories)")
	name := fs.String("name", "", "label for the receipt")
	dir := fs.String("dir", ".", "directory inside the repository to run from")
	if err := fs.Parse(args); err != nil {
		return err
	}
	argv := fs.Args()
	if len(argv) == 0 {
		return errors.New("verify record needs a command after --\n" + verifyUsage)
	}
	cfg, _ := loadConfiguredConfig()
	if len(paths) == 0 {
		if cfg != nil {
			for _, d := range cfg.Review.Verification.Declared {
				if len(d.Command) > 0 && strings.Join(d.Command, "\x00") == strings.Join(argv, "\x00") {
					paths, *name = append(paths, d.Paths...), firstNonEmpty(*name, d.Name)
					break
				}
			}
		}
		if len(paths) == 0 {
			return errors.New("verify record needs --paths: the command matches no declared verification (review.verification.declared)")
		}
	}
	return recordAndPrint(*dir, argv, paths, *name, "record")
}

func recordAndPrint(dir string, argv, paths []string, name, recorder string) error {
	store, err := openVerifyLedger()
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, stop := verifyContext()
	defer stop()
	receipt, err := verifyreceipt.Record(ctx, store, verifyreceipt.RecordOptions{
		Dir: dir, Argv: argv, Paths: paths, Name: name, Recorder: recorder, Output: os.Stderr,
	})
	if err != nil {
		return err
	}
	printReceipt(receipt)
	if receipt.Status != verifyreceipt.StatusPass {
		return withExitCode(fmt.Errorf("verification failed: %s (exit %d); the failing receipt is recorded", receipt.Command(), receipt.ExitCode), 1)
	}
	return nil
}

func runVerifyRun(args []string) error {
	fs := flag.NewFlagSet("verify run", flag.ContinueOnError)
	var paths pathFlags
	fs.Var(&paths, "paths", "only run declared verifications whose globs overlap these globs (repeatable)")
	name := fs.String("name", "", "only run the declared verification with this name")
	dir := fs.String("dir", ".", "directory inside the repository to run from")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfiguredConfig()
	if err != nil {
		return err
	}
	var selected []config.DeclaredVerification
	for _, d := range cfg.Review.Verification.Declared {
		if len(d.Command) == 0 || (*name != "" && d.Name != *name) {
			continue
		}
		if len(paths) > 0 && !globsOverlap(paths, d.Paths) {
			continue
		}
		selected = append(selected, d)
	}
	if len(selected) == 0 {
		return errors.New("no declared verification matches; declare commands under review.verification.declared in .buckley/config.yaml, or use `buckley verify record -- COMMAND`")
	}
	var failed []string
	for _, d := range selected {
		fmt.Fprintf(os.Stderr, "== %s: %s\n", firstNonEmpty(d.Name, "verification"), strings.Join(d.Command, " "))
		if err := recordAndPrint(*dir, d.Command, d.Paths, d.Name, "run"); err != nil {
			var coded exitCoder
			if errors.As(err, &coded) {
				failed = append(failed, strings.Join(d.Command, " "))
				continue
			}
			return err
		}
	}
	if len(failed) > 0 {
		return withExitCode(fmt.Errorf("%d verification(s) failed: %s", len(failed), strings.Join(failed, "; ")), 1)
	}
	return nil
}

// globsOverlap reports whether any requested glob and any declared glob cover
// a common path: they are equal, one is a directory glob ("dir/**") holding the
// other's literal prefix, or one matches the other as a path. So `--paths
// client/**` selects `client/**`, and `--paths pkg/sub/**` selects `pkg/**`.
func globsOverlap(requested, declared []string) bool {
	for _, r := range requested {
		for _, d := range declared {
			if strings.TrimSpace(r) == "" || strings.TrimSpace(d) == "" {
				continue
			}
			if r == d || globContains(r, d) || globContains(d, r) ||
				diffsignal.GlobMatcher([]string{d})(r) || diffsignal.GlobMatcher([]string{r})(d) {
				return true
			}
		}
	}
	return false
}

// globContains reports whether outer is a directory glob ("dir/**") whose
// directory holds inner's literal prefix.
func globContains(outer, inner string) bool {
	dir, ok := strings.CutSuffix(outer, "**")
	return ok && strings.HasPrefix(strings.TrimSuffix(inner, "**"), dir)
}

func runVerifyList(args []string) error {
	fs := flag.NewFlagSet("verify list", flag.ContinueOnError)
	dir := fs.String("dir", ".", "directory inside the repository")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := verifyContext()
	defer stop()
	state, err := verifyreceipt.StateOf(ctx, *dir)
	if err != nil {
		return err
	}
	loaded, err := loadVerificationReceipts(ctx, state.Tree)
	if err != nil {
		return err
	}
	fmt.Printf("tree %.12s (commit %.12s)%s\n", state.Tree, state.Commit, map[bool]string{true: ", working tree has uncommitted changes", false: ""}[state.Dirty])
	if len(loaded.Receipts) == 0 {
		fmt.Println("no receipts for this tree")
	}
	for _, r := range loaded.Receipts {
		printReceipt(r)
	}
	for _, x := range loaded.Rejected {
		fmt.Printf("rejected: %s\n", x)
	}
	return nil
}
