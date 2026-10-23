package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/verifyreceipt"
)

// declaredVerifications converts the configured declarations.
func declaredVerifications(cfg *config.Config) []commands.DeclaredVerification {
	if cfg == nil {
		return nil
	}
	out := make([]commands.DeclaredVerification, 0, len(cfg.Review.Verification.Declared))
	for _, d := range cfg.Review.Verification.Declared {
		out = append(out, commands.DeclaredVerification{Name: d.Name, Paths: d.Paths, Command: d.Command, CIChecks: d.CIChecks})
	}
	return out
}

// repoConfigVerificationRequire reads buckbot.verification.require from the
// repository's own ./.buckley/config.yaml, so the report can say whether the
// repository or the user config set the policy.
func repoConfigVerificationRequire() string {
	data, err := os.ReadFile(".buckley/config.yaml")
	if err != nil {
		return ""
	}
	var doc struct {
		Buckbot struct {
			Verification struct {
				Require string `yaml:"require"`
			} `yaml:"verification"`
		} `yaml:"buckbot"`
	}
	if yaml.Unmarshal(data, &doc) != nil {
		return ""
	}
	return strings.TrimSpace(doc.Buckbot.Verification.Require)
}

// verificationTarget describes what a review is about to read evidence for.
type verificationTarget struct {
	Files  []string
	Labels []string
	// HeadRev is the reviewed head commit. Receipts match on its tree hash.
	HeadRev string
	// LocalChanges is true when the reviewed content is not exactly HeadRev
	// (unstaged or untracked files are included), so no receipt can match.
	LocalChanges bool
	Checks       []commands.PRCheck
	// ChecksOnHead is true when Checks were read for HeadRev itself.
	ChecksOnHead bool
}

// loadVerificationReceipts opens the ledger read-only in effect: a missing
// ledger means no receipts, and the file is never created by a review.
func loadVerificationReceipts(ctx context.Context, tree string) (verifyreceipt.Loaded, error) {
	path, err := resolveLedgerDBPath()
	if err != nil || tree == "" {
		return verifyreceipt.Loaded{}, err
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return verifyreceipt.Loaded{}, nil
	}
	store, err := evidence.New(path)
	if err != nil {
		return verifyreceipt.Loaded{}, fmt.Errorf("open verification ledger: %w", err)
	}
	defer store.Close()
	return verifyreceipt.Load(ctx, store, tree)
}

// resolveVerificationReport decides the policy, reads receipts and CI checks
// for the reviewed head, and maps them to the changed paths. It runs before
// any model call. When the policy requires evidence that is missing or
// failing, it returns an error carrying exit status 16 or 17.
func (opts automatedReviewOptions) resolveVerificationReport(ctx context.Context, target verificationTarget) (*commands.VerificationReport, error) {
	mode, source, err := commands.ResolveVerificationPolicy(commands.PolicyInputs{
		Flag:       opts.requireVerification,
		Labels:     target.Labels,
		RepoConfig: repoConfigVerificationRequire(),
		UserConfig: opts.userVerificationRequire,
	})
	if err != nil {
		return nil, withExitCode(err, 2)
	}
	input := commands.VerificationInput{
		Mode:         mode,
		Files:        target.Files,
		Declared:     opts.declaredVerification,
		ChecksOnHead: target.ChecksOnHead,
	}
	for _, c := range target.Checks {
		input.Checks = append(input.Checks, commands.NormalizeCICheck(c))
	}
	if target.HeadRev != "" && !target.LocalChanges {
		wd, _ := os.Getwd()
		if tree, treeErr := verifyreceipt.TreeOf(ctx, wd, target.HeadRev); treeErr == nil {
			loaded, loadErr := loadVerificationReceipts(ctx, tree)
			if loadErr != nil {
				return nil, loadErr
			}
			input.Receipts, input.Rejected = loaded.Receipts, loaded.Rejected
		} else {
			input.Rejected = append(input.Rejected, "reviewed head "+target.HeadRev+" is not available locally, so receipts cannot be matched to its tree")
		}
	} else if target.LocalChanges {
		input.Rejected = append(input.Rejected, "the review includes uncommitted changes, so no receipt can match its tree")
	}
	report := commands.EvaluateVerification(input)
	report.PolicySource = source
	if code, msg := report.Gate(); code != commands.VerificationGateOK {
		return &report, withExitCode(errors.New(msg), code)
	}
	return &report, nil
}

// appendVerificationSection adds the deterministic verification section to a
// review, once.
func appendVerificationSection(review string, report *commands.VerificationReport) string {
	if report == nil || strings.Contains(review, "## Verification Evidence") {
		return review
	}
	return strings.TrimRight(review, "\n") + "\n\n" + report.Markdown()
}

// withVerificationPrompt appends the evidence to the model's task prompt.
func withVerificationPrompt(prompt string, report *commands.VerificationReport) string {
	if report == nil {
		return prompt
	}
	return strings.TrimRight(prompt, "\n") + "\n\n" + report.Markdown()
}
