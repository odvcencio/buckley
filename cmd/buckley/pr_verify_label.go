package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"strings"

	"m31labs.dev/buckley/pkg/oneshot/commands"
)

// prLabelClient is the part of GitHub that label handling uses.
type prLabelClient interface {
	EnsureLabel(ctx context.Context, name, description, color string) error
	PRLabels(ctx context.Context, pr string) ([]string, error)
	EditLabels(ctx context.Context, pr string, add, remove []string) error
}

var verificationLabelInfo = map[string]struct{ description, color string }{
	commands.LabelVerifyRequired: {"Buckbot needs a verification receipt or passing CI on the head before it reviews", "0e8a16"},
	commands.LabelVerifyReceipt:  {"Buckbot needs a verification receipt on the head before it reviews", "1d76db"},
	commands.LabelVerifyOptional: {"Buckbot reviews without verification evidence and reports paths as unverified", "c5def5"},
}

// setVerificationLabel moves a PR to the label for mode. It creates the label
// when the repository lacks it, adds it, and removes only the other
// buckbot:verify-* labels. It never touches any other label.
func setVerificationLabel(ctx context.Context, client prLabelClient, pr string, mode commands.VerificationMode) (add, remove []string, err error) {
	current, err := client.PRLabels(ctx, pr)
	if err != nil {
		return nil, nil, fmt.Errorf("read PR labels: %w", err)
	}
	add, remove, err = commands.VerificationLabelPlan(mode, current)
	if err != nil {
		return nil, nil, err
	}
	for _, name := range add {
		info := verificationLabelInfo[name]
		if err := client.EnsureLabel(ctx, name, info.description, info.color); err != nil {
			return nil, nil, fmt.Errorf("create label %s: %w", name, err)
		}
	}
	if len(add) == 0 && len(remove) == 0 {
		return nil, nil, nil
	}
	if err := client.EditLabels(ctx, pr, add, remove); err != nil {
		return nil, nil, fmt.Errorf("update PR labels: %w", err)
	}
	return add, remove, nil
}

// ghLabelClient implements prLabelClient with the gh CLI.
type ghLabelClient struct{}

func (ghLabelClient) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (c ghLabelClient) EnsureLabel(ctx context.Context, name, description, color string) error {
	out, err := c.run(ctx, "label", "create", name, "--description", description, "--color", color)
	if err != nil && strings.Contains(strings.ToLower(string(out)), "already exists") {
		return nil
	}
	return err
}

func (c ghLabelClient) PRLabels(ctx context.Context, pr string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "gh", "pr", "view", pr, "--json", "labels").Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr view %s: %w", pr, err)
	}
	var doc struct {
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(doc.Labels))
	for _, l := range doc.Labels {
		names = append(names, l.Name)
	}
	return names, nil
}

func (c ghLabelClient) EditLabels(ctx context.Context, pr string, add, remove []string) error {
	args := []string{"pr", "edit", pr}
	if len(add) > 0 {
		args = append(args, "--add-label", strings.Join(add, ","))
	}
	if len(remove) > 0 {
		args = append(args, "--remove-label", strings.Join(remove, ","))
	}
	_, err := c.run(ctx, args...)
	return err
}

// runPRLabelCommand implements `buckley pr label`: it sets the verification
// label on an existing PR without regenerating its title or body.
func runPRLabelCommand(args []string) error {
	fs := flag.NewFlagSet("pr label", flag.ContinueOnError)
	require := fs.Bool("require-verification", false, "require a receipt or passing CI (label buckbot:verify-required)")
	receipt := fs.Bool("require-receipt", false, "require a verification receipt only (label buckbot:verify-receipt)")
	optional := fs.Bool("no-require-verification", false, "do not require evidence (label buckbot:verify-optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	mode, err := verificationFlagMode(*require, *receipt, *optional)
	if err != nil {
		return err
	}
	if mode == "" {
		return errors.New("usage: buckley pr label (--require-verification | --require-receipt | --no-require-verification) <pr number or URL>")
	}
	if fs.NArg() != 1 {
		return errors.New("usage: buckley pr label (--require-verification | --require-receipt | --no-require-verification) <pr number or URL>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), ghAPITimeout)
	defer cancel()
	add, remove, err := setVerificationLabel(ctx, ghLabelClient{}, fs.Arg(0), mode)
	if err != nil {
		return err
	}
	fmt.Printf("PR %s: verification policy %s (added %v, removed %v)\n", fs.Arg(0), mode, add, remove)
	return nil
}

// verificationFlagMode maps the three label flags to a mode. It returns an
// empty mode when none is set.
func verificationFlagMode(require, receipt, optional bool) (commands.VerificationMode, error) {
	n := 0
	var mode commands.VerificationMode
	for _, set := range []struct {
		on   bool
		mode commands.VerificationMode
	}{{require, commands.VerifyReceiptOrCI}, {receipt, commands.VerifyReceipt}, {optional, commands.VerifyNone}} {
		if set.on {
			n++
			mode = set.mode
		}
	}
	if n > 1 {
		return "", errors.New("choose one of --require-verification, --require-receipt, --no-require-verification")
	}
	return mode, nil
}
