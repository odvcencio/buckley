package commands

import (
	"fmt"
	"strings"
)

// Labels on a pull request that set its verification requirement.
const (
	LabelVerifyRequired = "buckbot:verify-required" // receipt-or-ci
	LabelVerifyReceipt  = "buckbot:verify-receipt"  // receipt only
	LabelVerifyOptional = "buckbot:verify-optional" // none
)

// PolicyInputs are the places a verification requirement can come from.
type PolicyInputs struct {
	// Flag is the --require-verification value, empty when not given.
	Flag string
	// Labels are the pull request's labels, when reviewing a PR.
	Labels []string
	// RepoConfig is buckbot.verification.require from ./.buckley/config.yaml.
	RepoConfig string
	// UserConfig is buckbot.verification.require from the user config.
	UserConfig string
}

// ResolveVerificationPolicy applies the precedence --require-verification flag,
// then PR label, then repository config, then user config, then none. It
// returns the mode and a short description of the source for the report. When
// a PR carries more than one verify label, the strictest wins.
func ResolveVerificationPolicy(in PolicyInputs) (VerificationMode, string, error) {
	if strings.TrimSpace(in.Flag) != "" {
		mode, err := ParseVerificationMode(in.Flag)
		return mode, "the --require-verification flag", err
	}
	if mode, label, ok := modeFromLabels(in.Labels); ok {
		return mode, "the PR label `" + label + "`", nil
	}
	if strings.TrimSpace(in.RepoConfig) != "" {
		mode, err := ParseVerificationMode(in.RepoConfig)
		return mode, "the repository config (buckbot.verification.require)", err
	}
	if strings.TrimSpace(in.UserConfig) != "" {
		mode, err := ParseVerificationMode(in.UserConfig)
		return mode, "the user config (buckbot.verification.require)", err
	}
	return VerifyNone, "the default (no requirement)", nil
}

func modeFromLabels(labels []string) (VerificationMode, string, bool) {
	rank := map[string]int{LabelVerifyOptional: 1, LabelVerifyReceipt: 3, LabelVerifyRequired: 2}
	best, bestRank := "", 0
	for _, l := range labels {
		l = strings.ToLower(strings.TrimSpace(l))
		if r := rank[l]; r > bestRank {
			best, bestRank = l, r
		}
	}
	switch best {
	case LabelVerifyRequired:
		return VerifyReceiptOrCI, best, true
	case LabelVerifyReceipt:
		return VerifyReceipt, best, true
	case LabelVerifyOptional:
		return VerifyNone, best, true
	}
	return VerifyNone, "", false
}

// VerificationLabelPlan lists the labels to add and remove to move a PR to a
// requirement. It touches only the three buckbot:verify-* labels.
func VerificationLabelPlan(mode VerificationMode, current []string) (add, remove []string, err error) {
	var want string
	switch mode {
	case VerifyReceiptOrCI:
		want = LabelVerifyRequired
	case VerifyReceipt:
		want = LabelVerifyReceipt
	case VerifyNone:
		want = LabelVerifyOptional
	default:
		return nil, nil, fmt.Errorf("unknown verification mode %q", mode)
	}
	has := map[string]bool{}
	for _, l := range current {
		has[strings.ToLower(strings.TrimSpace(l))] = true
	}
	if !has[want] {
		add = append(add, want)
	}
	for _, other := range []string{LabelVerifyRequired, LabelVerifyReceipt, LabelVerifyOptional} {
		if other != want && has[other] {
			remove = append(remove, other)
		}
	}
	return add, remove, nil
}
