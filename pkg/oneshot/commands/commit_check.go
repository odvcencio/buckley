package commands

import (
	"regexp"
	"strings"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/rules"
)

// CheckReport is the outcome of checking an existing commit message.
type CheckReport struct {
	// Skipped is set for messages the checks do not apply to (merges, fixups).
	Skipped string
	// Safety lists leak findings. They never echo the offending text.
	Safety []string
	// Style lists style violations. Empty when the header is not in the
	// conventional action form, because hand-written messages may use other
	// shapes.
	Style []string
	// Rules names the failed safety rules (commitmsg.Rule*), never their text.
	Rules []string
	// Action is the arbiter outcome: allow, repair, or block. Without an
	// engine, or when it cannot evaluate, it is derived from Safety.
	Action string
}

// Failed reports whether the safety check rejects the message.
func (r CheckReport) Failed() bool { return r.Action == "repair" || r.Action == "block" }

var (
	checkHeaderRe = regexp.MustCompile(`^([A-Za-z]+)(?:\(([^)]*)\))?(!)?: (.+)$`)
	trailerRe     = regexp.MustCompile(`^(?i)(co-authored-by|signed-off-by|reviewed-by|acked-by|refs|fixes|closes|buckley-[a-z-]+):`)
	skipHeaderRe  = regexp.MustCompile(`^(Merge |Revert |fixup! |squash! |amend! )`)
)

// CheckMessage runs the commit checks on message. diff is the staged diff and
// stats describes it; either may be empty. engine may be nil.
func CheckMessage(message, diff string, stats oneshot.DiffStats, engine *rules.Engine) CheckReport {
	return CheckMessageWithPolicy(message, diff, stats, engine, commitPolicyLoader())
}

// CheckMessageWithPolicy is CheckMessage with an explicit leak policy, so
// evaluations can plant deny terms without reading private files.
func CheckMessageWithPolicy(message, diff string, stats oneshot.DiffStats, engine *rules.Engine, policy commitmsg.Policy) CheckReport {
	header, bullets, body := parseMessage(message)
	if skipHeaderRe.MatchString(header) {
		return CheckReport{Skipped: "merge, revert, or fixup message", Action: "allow"}
	}

	report := CheckReport{Action: "allow"}
	cr := CommitResult{Subject: header, Body: StringList(bullets)}
	if m := checkHeaderRe.FindStringSubmatch(header); m != nil {
		cr = CommitResult{Action: m[1], Scope: m[2], Subject: m[4], Body: StringList(bullets), Breaking: m[3] == "!"}
		if m[1] != strings.ToLower(m[1]) {
			report.Style = append(report.Style, "action must be lowercase")
		}
		if err := commitmsg.ValidateStyle(cr.Action, cr.Scope, cr.Subject, bullets); err != nil {
			report.Style = append(report.Style, err.Error())
		}
	}

	ctx := &oneshot.Context{Sources: map[string]string{"git_diff:staged": diff}, Diff: stats}
	safetyText := header + "\n" + body
	findings := policy.Check(safetyText, diff)
	if stats.GeneratedRatio() >= GeneratedRatioLimit {
		findings = withoutRule(findings, commitmsg.RuleRemovedEcho)
	}
	for _, f := range findings {
		report.Safety = append(report.Safety, f.Detail)
		report.Rules = append(report.Rules, f.Rule)
	}
	if len(findings) > 0 {
		report.Action = "repair"
	}

	if engine != nil {
		if action, ok := evalCommitPolicy(engine, ctx, cr, safetyText, policy); ok {
			report.Action = action
		}
	}
	return report
}

// evalCommitPolicy asks commit_message.arb for the outcome. Facts come from the
// same code path as generation. ok is false when the engine cannot decide.
func evalCommitPolicy(engine *rules.Engine, ctx *oneshot.Context, cr CommitResult, text string, policy commitmsg.Policy) (string, bool) {
	// Facts are computed over the full text so prose paragraphs count, not just
	// the parsed bullets.
	req := policyFactsFor(policy, ctx, cr)
	// PolicyFacts formats the parsed result; add findings for prose paragraphs
	// that the bullet parse dropped.
	if extra := policy.Check(text, ctx.Sources["git_diff:staged"]); len(extra) > 0 {
		var deny, echo, sensitive int
		for _, f := range extra {
			switch f.Rule {
			case commitmsg.RuleDenyList:
				deny += f.Count
			case commitmsg.RuleRemovedEcho:
				echo += f.Count
			default:
				sensitive += f.Count
			}
		}
		if ctx.Diff.GeneratedRatio() >= GeneratedRatioLimit {
			echo = 0
		}
		req.Facts["deny_hits"], req.Facts["removed_echo"], req.Facts["sensitive_hits"] = deny, echo, sensitive
	}
	res, err := engine.EvalStrategy(req.Domain, req.Strategy, req.Facts)
	if err != nil {
		return "", false
	}
	action, _ := res.Params["action"].(string)
	switch action {
	case "allow", "repair", "block":
		return action, true
	}
	return "", false
}

// parseMessage splits a message into its header, its "- " bullets, and the body
// text without trailers and comment lines.
func parseMessage(message string) (header string, bullets []string, body string) {
	var kept []string
	for _, line := range strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			continue // git strips comment lines
		}
		if trailerRe.MatchString(strings.TrimSpace(line)) {
			continue
		}
		kept = append(kept, line)
	}
	for len(kept) > 0 && strings.TrimSpace(kept[0]) == "" {
		kept = kept[1:]
	}
	if len(kept) == 0 {
		return "", nil, ""
	}
	header = strings.TrimSpace(kept[0])
	body = strings.TrimSpace(strings.Join(kept[1:], "\n"))
	for _, line := range kept[1:] {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") {
			bullets = append(bullets, t)
		}
	}
	return header, bullets, body
}
