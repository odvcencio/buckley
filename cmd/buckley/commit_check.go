package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/rules"
)

// runCommitCheckCommand checks an existing commit message (hand-written or from
// an agent) with the same safety and style rules that `buckley commit` applies
// to generated messages. It is the body of the commit-msg hook.
func runCommitCheckCommand(args []string) error {
	fs := flag.NewFlagSet("commit-check", flag.ContinueOnError)
	file := fs.String("file", "", "commit message file (default: read stdin; git passes this to commit-msg hooks)")
	strict := fs.Bool("strict", false, "fail on style violations too (default: warn)")
	noDiff := fs.Bool("no-diff", false, "skip the staged-diff checks (removed-line names)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var raw []byte
	var err error
	if *file != "" && *file != "-" {
		raw, err = os.ReadFile(*file)
	} else {
		raw, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return fmt.Errorf("read commit message: %w", err)
	}

	var diff string
	var stats oneshot.DiffStats
	if !*noDiff {
		diff = stagedDiffText()
		stats, _ = oneshot.StagedDiffStats(nil)
	}
	engine, engineErr := rules.NewDefaultEngine()
	if engineErr != nil {
		engine = nil // the built-in Go policy still applies
	}

	report := commands.CheckMessage(string(raw), diff, stats, engine)
	if report.Skipped != "" {
		return nil
	}
	for _, line := range report.Safety {
		fmt.Fprintf(os.Stderr, "buckley commit-check: safety: %s\n", line)
	}
	for _, line := range report.Style {
		fmt.Fprintf(os.Stderr, "buckley commit-check: style: %s\n", line)
	}
	switch {
	case report.Failed():
		return errors.New("commit message rejected: rewrite it to describe intent and effect, and say \"the old name\" instead of naming removed or renamed identifiers, people, or organizations")
	case *strict && len(report.Style) > 0:
		return errors.New("commit message rejected by --strict style checks")
	}
	return nil
}

// stagedDiffText returns the staged diff, or "" when git cannot produce it.
func stagedDiffText() string {
	out, err := exec.Command("git", "--no-pager", "diff", "--cached", "--no-ext-diff", "--no-color").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
