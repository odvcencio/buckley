package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/commiteval"
	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/rules"
)

// runEvalCommitCommand implements `buckley eval commit`. Offline (default) it
// runs the deterministic safety and style checks on the fixed diffs and exits
// non-zero on any failure, so CI can gate on it. With --live it also asks a
// model to write a message for each diff and applies the same hard checks.
func runEvalCommitCommand(args []string) error {
	fs := flag.NewFlagSet("eval commit", flag.ContinueOnError)
	casesDir := fs.String("cases", "", "directory of case files (default: the built-in set)")
	only := fs.String("case", "", "run only the case with this name")
	live := fs.Bool("live", false, "also generate a message per diff with a model and hard-check it")
	modelFlag := fs.String("model", "", "model for --live (default: the commit model)")
	backendFlag := fs.String("backend", "", "backend for --live: api, codex, or claude")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	timeout := fs.Duration("timeout", 2*time.Minute, "timeout per live case")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var fsys = os.DirFS(*casesDir)
	if *casesDir == "" {
		fsys = nil
	}
	cases, err := commiteval.Load(fsys)
	if err != nil {
		return fmt.Errorf("load cases: %w", err)
	}
	if *only != "" {
		var keep []commiteval.Case
		for _, c := range cases {
			if c.Name == *only {
				keep = append(keep, c)
			}
		}
		if len(keep) == 0 {
			return fmt.Errorf("no case named %q", *only)
		}
		cases = keep
	}

	engine, engineErr := rules.NewDefaultEngine()
	if engineErr != nil {
		engine = nil
	}
	report := commiteval.RunOffline(cases, engine)

	var liveRows []liveRow
	if *live {
		liveRows, err = runLiveCommitEval(cases, engine, *modelFlag, *backendFlag, *timeout)
		if err != nil {
			return err
		}
	}

	flagged, withOriginal := report.OriginalsFlagged()
	if *asJSON {
		out := map[string]any{
			"cases":     len(report.Results),
			"checks":    report.Checks(),
			"failures":  report.Failures(),
			"originals": map[string]int{"flagged": flagged, "total": withOriginal},
			"live":      liveRows,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return err
		}
	} else {
		for _, res := range report.Results {
			mark := "ok  "
			if len(res.Failed()) > 0 {
				mark = "FAIL"
			}
			fmt.Printf("%s %-24s %-8s %d checks\n", mark, res.Case.Name, res.Case.Kind, len(res.Checks))
			for _, c := range res.Failed() {
				fmt.Printf("       failed %s: %s\n", c.Name, c.Detail)
			}
		}
		fmt.Printf("\n%d cases, %d checks, %d failed\n", len(report.Results), report.Checks(), report.Failures())
		fmt.Printf("historical messages that trip a safety rule: %d of %d (informational)\n", flagged, withOriginal)
		for _, r := range liveRows {
			status := "ok  "
			if r.Blocked {
				status = "BLOCK"
			} else if !r.Pass {
				status = "FAIL"
			}
			fmt.Printf("live %s %-24s %s\n", status, r.Case, r.Detail)
		}
	}

	liveFailed := 0
	for _, r := range liveRows {
		if !r.Pass {
			liveFailed++
		}
	}
	if report.Failures() > 0 || liveFailed > 0 {
		return fmt.Errorf("commit eval failed: %d offline check(s), %d live case(s)", report.Failures(), liveFailed)
	}
	return nil
}

type liveRow struct {
	Case   string `json:"case"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
	Header string `json:"header,omitempty"`
	// Blocked marks a case where the safety loop stopped the commit.
	Blocked bool `json:"blocked,omitempty"`
}

// evalCommitDefinition runs CommitDefinition on a fixed diff instead of the
// staged one and validates against the case's planted names.
type evalCommitDefinition struct {
	commands.CommitDefinition
	c     commiteval.Case
	stats oneshot.DiffStats
}

func (evalCommitDefinition) ContextSources() []oneshot.ContextSource { return nil }

func (d evalCommitDefinition) BuildPrompt(*oneshot.Context) string {
	return d.CommitDefinition.BuildPrompt(&oneshot.Context{Sources: map[string]string{"git_diff:staged": d.c.Diff}})
}

// PolicyFacts opts out of the arbiter path so the case's policy applies.
func (evalCommitDefinition) PolicyFacts(*oneshot.Context, json.RawMessage) (*oneshot.PolicyRequest, error) {
	return nil, nil
}

func (d evalCommitDefinition) ValidateWithContext(_ *oneshot.Context, raw json.RawMessage) error {
	var cr commands.CommitResult
	if err := json.Unmarshal(raw, &cr); err != nil {
		return err
	}
	rep := commands.CheckMessageWithPolicy(cr.Format(), d.c.Diff, d.stats, nil, d.c.Policy())
	if !rep.Failed() {
		return nil
	}
	var findings []commitmsg.Finding
	for i, detail := range rep.Safety {
		rule := ""
		if i < len(rep.Rules) {
			rule = rep.Rules[i]
		}
		findings = append(findings, commitmsg.Finding{Rule: rule, Detail: detail})
	}
	return &commitmsg.LeakError{Findings: findings}
}

func runLiveCommitEval(cases []commiteval.Case, engine *rules.Engine, model, backend string, timeout time.Duration) ([]liveRow, error) {
	cliArgs := []string{}
	if model != "" {
		cliArgs = append(cliArgs, "--model", model)
	}
	if backend != "" {
		cliArgs = append(cliArgs, "--backend", backend)
	}
	opts, err := parseCommitCommandOptions(cliArgs)
	if err != nil {
		return nil, err
	}
	var rows []liveRow
	for _, c := range cases {
		stats := c.Stats()
		if cr := commands.GeneratedCommit(stats); cr != nil {
			// Generated-only diffs never reach a model.
			rows = append(rows, evalRow(c, cr.Format(), engine))
			continue
		}
		def := evalCommitDefinition{c: c, stats: stats}
		runtime, cleanup, err := newCommitCommandRuntime(opts, def)
		if err != nil {
			return rows, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		res, runErr := runtime.runner.Run(ctx)
		cancel()
		cleanup()
		switch {
		case runErr != nil:
			rows = append(rows, liveRow{Case: c.Name, Detail: "run failed: " + runErr.Error()})
		case res.Error != nil:
			// Retries exhausted: the safety loop blocked the message.
			// The commit would not have been made or pushed: safe, but the
			// model never produced a passing message. Reported, not a leak.
			var leak *commitmsg.LeakError
			if errors.As(res.Error, &leak) {
				rows = append(rows, liveRow{Case: c.Name, Pass: true, Blocked: true, Detail: "blocked: no message passed the safety check in 3 attempts"})
			} else {
				rows = append(rows, liveRow{Case: c.Name, Detail: "no message passed validation: " + res.Error.Error()})
			}
		case res.Commit == nil:
			rows = append(rows, liveRow{Case: c.Name, Detail: "no commit generated"})
		default:
			rows = append(rows, evalRow(c, res.Commit.Format(), engine))
		}
	}
	return rows, nil
}

func evalRow(c commiteval.Case, message string, engine *rules.Engine) liveRow {
	row := liveRow{Case: c.Name, Pass: true, Header: strings.SplitN(message, "\n", 2)[0]}
	var failed []string
	for _, ch := range c.HardChecks(message, engine) {
		if !ch.Pass {
			row.Pass = false
			failed = append(failed, ch.Name)
		}
	}
	if row.Pass {
		row.Detail = row.Header
	} else {
		row.Detail = "failed " + strings.Join(failed, ", ")
	}
	return row
}
