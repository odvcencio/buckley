package commands

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/diffsignal"
	"m31labs.dev/buckley/pkg/verifyreceipt"
)

// VerificationMode says what verification evidence a review requires before it
// starts. A review never runs verification itself; it reads receipts and CI
// results.
type VerificationMode string

const (
	// VerifyNone runs the review whatever evidence exists and reports it.
	VerifyNone VerificationMode = "none"
	// VerifyReceipt requires receipts covering every changed source path.
	VerifyReceipt VerificationMode = "receipt"
	// VerifyReceiptOrCI accepts receipts or CI checks on the reviewed head.
	VerifyReceiptOrCI VerificationMode = "receipt-or-ci"
)

// ParseVerificationMode normalizes a mode name. Empty means none.
func ParseVerificationMode(value string) (VerificationMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none":
		return VerifyNone, nil
	case "receipt":
		return VerifyReceipt, nil
	case "receipt-or-ci", "receipt_or_ci":
		return VerifyReceiptOrCI, nil
	}
	return VerifyNone, fmt.Errorf("verification requirement %q must be none, receipt or receipt-or-ci", value)
}

// DeclaredVerification maps path globs to the command and CI checks that cover
// them.
type DeclaredVerification struct {
	Name     string
	Paths    []string
	Command  []string
	CIChecks []string
}

// CICheckState is a normalized CI check result.
type CICheckState string

const (
	CICheckPass    CICheckState = "pass"
	CICheckFail    CICheckState = "fail"
	CICheckPending CICheckState = "pending"
	CICheckSkipped CICheckState = "skipped"
)

// CICheck is one check run reported on the reviewed head.
type CICheck struct {
	Name  string
	State CICheckState
}

// NormalizeCICheck maps a PR check's status and conclusion to a CICheck.
func NormalizeCICheck(c PRCheck) CICheck {
	state := strings.ToUpper(strings.TrimSpace(firstPRString(c.Conclusion, c.Status)))
	out := CICheck{Name: c.Name, State: CICheckPending}
	switch state {
	case "SUCCESS", "PASS":
		out.State = CICheckPass
	case "NEUTRAL", "SKIPPED", "SKIPPING":
		out.State = CICheckSkipped
	case "FAILURE", "FAIL", "ERROR", "CANCELLED", "CANCELED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
		out.State = CICheckFail
	}
	return out
}

// VerificationInput is everything the evaluation reads.
type VerificationInput struct {
	Mode  VerificationMode
	Files []string
	// Receipts must already be filtered to the reviewed head's tree.
	Receipts []verifyreceipt.Receipt
	Rejected []string
	Checks   []CICheck
	// ChecksOnHead is true only when Checks were read for the exact reviewed
	// head commit. Checks from any other commit never count.
	ChecksOnHead bool
	Declared     []DeclaredVerification
}

// EvidenceItem is one receipt or CI result and the changed paths it covers.
type EvidenceItem struct {
	Kind   string // "receipt" or "ci"
	Label  string
	Detail string
	Paths  []string
}

// VerificationReport is the result of mapping evidence to changed paths.
type VerificationReport struct {
	Mode         VerificationMode
	PolicySource string
	Covered      []EvidenceItem
	Failing      []EvidenceItem
	Unverified   []string
	NotRequired  []string
	Rejected     []string
	Declared     []DeclaredVerification
}

// EvaluateVerification maps receipts and CI checks to the changed paths they
// cover. Documentation-only paths need no evidence.
func EvaluateVerification(in VerificationInput) VerificationReport {
	rep := VerificationReport{Mode: in.Mode, Rejected: in.Rejected, Declared: in.Declared}
	var required []string
	for _, f := range in.Files {
		f = normalizeReviewEvidencePath(f)
		switch {
		case f == "":
		case reviewDocumentationPath(f):
			rep.NotRequired = append(rep.NotRequired, f)
		default:
			required = append(required, f)
		}
	}
	sort.Strings(required)
	sort.Strings(rep.NotRequired)

	covered := map[string]bool{}
	failing := map[string]bool{}
	add := func(item EvidenceItem, pass bool) {
		sort.Strings(item.Paths)
		if pass {
			rep.Covered = append(rep.Covered, item)
			for _, p := range item.Paths {
				covered[p] = true
			}
			return
		}
		rep.Failing = append(rep.Failing, item)
		for _, p := range item.Paths {
			failing[p] = true
		}
	}

	for _, r := range newestReceipts(in.Receipts) {
		matched := matchFiles(required, r.Paths)
		if len(matched) == 0 {
			continue
		}
		item := EvidenceItem{
			Kind:  "receipt",
			Label: fmt.Sprintf("receipt %s: `%s`", shortID(r.ID), r.Command()),
			Detail: fmt.Sprintf("%s, exit %d, %s, on %s (%s/%s), %s",
				r.Status, r.ExitCode, r.StartedAt.UTC().Format(time.RFC3339), r.Env.Hostname, r.Env.OS, r.Env.Arch,
				time.Duration(r.DurationMS)*time.Millisecond),
			Paths: matched,
		}
		add(item, r.Status == verifyreceipt.StatusPass)
	}

	if in.Mode != VerifyReceipt && in.ChecksOnHead && len(in.Checks) > 0 {
		byName := map[string]CICheckState{}
		for _, c := range in.Checks {
			if prev, ok := byName[c.Name]; !ok || c.State == CICheckFail || (prev != CICheckFail && c.State != CICheckPass) {
				byName[c.Name] = c.State
			}
		}
		declaredCI := false
		for _, d := range in.Declared {
			if len(d.CIChecks) == 0 {
				continue
			}
			declaredCI = true
			matched := matchFiles(required, d.Paths)
			if len(matched) == 0 {
				continue
			}
			allPass := true
			for _, name := range d.CIChecks {
				switch byName[name] {
				case CICheckPass:
				case CICheckFail:
					allPass = false
					add(EvidenceItem{Kind: "ci", Label: "ci check `" + name + "`", Detail: "failed on the reviewed head", Paths: matched}, false)
				default:
					allPass = false
				}
			}
			if allPass {
				add(EvidenceItem{Kind: "ci", Label: "ci checks `" + strings.Join(d.CIChecks, "`, `") + "`",
					Detail: "passed on the reviewed head", Paths: matched}, true)
			}
		}
		if !declaredCI && len(required) > 0 {
			rep.Rejected = append(rep.Rejected, "CI checks have no declared path coverage; configure buckbot.verification.declared.ci_checks or record a verification receipt")
		}
	}

	for _, f := range required {
		if !covered[f] && !failing[f] {
			rep.Unverified = append(rep.Unverified, f)
		}
	}
	return rep
}

// newestReceipts keeps only the latest receipt for each distinct command,
// directory and path set on the tree. A rerun that passes after an earlier
// failure on the same tree (a flaky test, a fixed environment) supersedes it,
// and the reverse also holds: the newest result is the one that counts.
func newestReceipts(receipts []verifyreceipt.Receipt) []verifyreceipt.Receipt {
	type key struct{ argv, dir, paths string }
	latest := map[key]verifyreceipt.Receipt{}
	var order []key
	for _, r := range receipts {
		paths := append([]string(nil), r.Paths...)
		sort.Strings(paths)
		k := key{strings.Join(r.Argv, "\x00"), r.Dir, strings.Join(paths, "\x00")}
		prev, seen := latest[k]
		if !seen {
			order = append(order, k)
		}
		if !seen || r.StartedAt.After(prev.StartedAt) {
			latest[k] = r
		}
	}
	out := make([]verifyreceipt.Receipt, 0, len(order))
	for _, k := range order {
		out = append(out, latest[k])
	}
	return out
}

func matchFiles(files, globs []string) []string {
	match := diffsignal.GlobMatcher(globs)
	if match == nil {
		return nil
	}
	var out []string
	for _, f := range files {
		if match(f) {
			out = append(out, f)
		}
	}
	return out
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "ev_")
	if len(id) > 10 {
		id = id[:10]
	}
	return id
}

// Verification gate results. They double as process exit statuses.
const (
	VerificationGateOK      = 0
	VerificationGateMissing = 16
	VerificationGateFailing = 17
)

// Gate decides whether the review may start. With mode none it always may.
// Otherwise failing evidence stops it with VerificationGateFailing and
// uncovered paths stop it with VerificationGateMissing. The message is short
// and lists the commands that would cover the missing paths.
func (r VerificationReport) Gate() (int, string) {
	if r.Mode == VerifyNone || r.Mode == "" {
		return VerificationGateOK, ""
	}
	var b strings.Builder
	if len(r.Failing) > 0 {
		fmt.Fprintf(&b, "Verification evidence is failing (policy %s, set by %s). No model call was made.\n", r.Mode, r.sourceLabel())
		for _, f := range r.Failing {
			fmt.Fprintf(&b, "  - %s: %s; paths (%d): %s\n", f.Label, f.Detail, len(f.Paths), strings.Join(limitList(f.Paths, 4), ", "))
		}
		return VerificationGateFailing, strings.TrimRight(b.String(), "\n")
	}
	if len(r.Unverified) > 0 {
		fmt.Fprintf(&b, "Verification evidence missing (policy %s, set by %s). No model call was made.\n", r.Mode, r.sourceLabel())
		fmt.Fprintf(&b, "Uncovered paths (%d):\n", len(r.Unverified))
		for _, p := range limitList(r.Unverified, 20) {
			fmt.Fprintf(&b, "  - %s\n", p)
		}
		b.WriteString("Run, from the reviewed commit with a clean tree:\n")
		for _, c := range r.RecordCommands() {
			fmt.Fprintf(&b, "  %s\n", c)
		}
		return VerificationGateMissing, strings.TrimRight(b.String(), "\n")
	}
	return VerificationGateOK, ""
}

func (r VerificationReport) sourceLabel() string {
	if r.PolicySource == "" {
		return "default"
	}
	return r.PolicySource
}

func limitList(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	out := append([]string(nil), items[:n]...)
	return append(out, fmt.Sprintf("... and %d more", len(items)-n))
}

// RecordCommands returns the `buckley verify record` commands that would cover
// the unverified paths: declared commands when a glob matches, otherwise a
// template for the user's own test command.
func (r VerificationReport) RecordCommands() []string {
	var out []string
	remaining := append([]string(nil), r.Unverified...)
	for _, d := range r.Declared {
		if len(d.Command) == 0 || len(matchFiles(remaining, d.Paths)) == 0 {
			continue
		}
		out = append(out, recordCommand(d.Paths, d.Command))
		remaining = subtract(remaining, matchFiles(remaining, d.Paths))
	}
	if len(remaining) > 0 {
		dirs := map[string]bool{}
		for _, f := range remaining {
			dirs[topDir(f)] = true
		}
		var globs []string
		for d := range dirs {
			globs = append(globs, d)
		}
		sort.Strings(globs)
		out = append(out, recordCommand(globs, []string{"<your test command>"}))
	}
	return out
}

func recordCommand(paths, argv []string) string {
	var b strings.Builder
	b.WriteString("buckley verify record")
	for _, p := range paths {
		b.WriteString(" --paths '" + p + "'")
	}
	b.WriteString(" --")
	for _, a := range argv {
		b.WriteString(" " + a)
	}
	return b.String()
}

func topDir(file string) string {
	if i := strings.Index(file, "/"); i > 0 {
		return file[:i] + "/**"
	}
	return file
}

func subtract(all, remove []string) []string {
	skip := map[string]bool{}
	for _, r := range remove {
		skip[r] = true
	}
	var out []string
	for _, a := range all {
		if !skip[a] {
			out = append(out, a)
		}
	}
	return out
}

// FailingSummary lists failing evidence for validation messages. A nil report
// has no failing evidence.
func (r *VerificationReport) FailingSummary() []string {
	if r == nil {
		return nil
	}
	var out []string
	for _, f := range r.Failing {
		out = append(out, f.Label+" ("+f.Detail+")")
	}
	return out
}

// Qualifier is "verified" when every required path is covered by passing
// evidence, "unverified" when some are not, and "failing" when evidence fails.
func (r VerificationReport) Qualifier() string {
	switch {
	case len(r.Failing) > 0:
		return "failing"
	case len(r.Unverified) > 0:
		return "unverified"
	default:
		return "verified"
	}
}

// Markdown renders the verification section. It is deterministic: Buckbot
// appends it to the review and gives it to the model as input.
func (r VerificationReport) Markdown() string {
	var b strings.Builder
	b.WriteString("## Verification Evidence\n\n")
	b.WriteString("The reviewer does not run build or test commands. Evidence below comes from verification receipts bound to the reviewed tree and from CI checks on the reviewed head.\n\n")
	fmt.Fprintf(&b, "- Policy: `%s` (set by %s)\n", orNone(string(r.Mode)), r.sourceLabel())
	fmt.Fprintf(&b, "- Verification: **%s**\n\n", r.Qualifier())
	if len(r.Covered) > 0 {
		b.WriteString("### Covered\n\n")
		for _, c := range r.Covered {
			fmt.Fprintf(&b, "- %s (%s)\n  - Covers: %s\n", c.Label, c.Detail, strings.Join(limitList(c.Paths, 25), ", "))
		}
		b.WriteString("\n")
	}
	if len(r.Failing) > 0 {
		b.WriteString("### Failing evidence (a finding)\n\n")
		for _, c := range r.Failing {
			fmt.Fprintf(&b, "- %s (%s)\n  - Affects: %s\n", c.Label, c.Detail, strings.Join(limitList(c.Paths, 25), ", "))
		}
		b.WriteString("\n")
	}
	if len(r.Unverified) > 0 {
		b.WriteString("### Unverified paths\n\n")
		for _, p := range limitList(r.Unverified, 40) {
			fmt.Fprintf(&b, "- %s\n", p)
		}
		b.WriteString("\n")
	}
	if len(r.NotRequired) > 0 {
		fmt.Fprintf(&b, "Documentation-only paths needing no verification: %d.\n\n", len(r.NotRequired))
	}
	if len(r.Rejected) > 0 {
		b.WriteString("### Rejected receipts\n\n")
		for _, x := range r.Rejected {
			fmt.Fprintf(&b, "- %s\n", x)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// ReviewPromptGuidance tells the model how to treat the evidence.
const ReviewPromptGuidance = `VERIFICATION POLICY:
- This policy supersedes instructions above to execute verification or require passing tests for approval. You do not run build or test commands, and run_verification is not available.
- The Verification Evidence section lists receipts and CI checks that cover changed paths. Report Build and Tests as PASS only when it reports verified coverage with passing evidence. Report FAIL only with supplied failing evidence. Report UNAVAILABLE when evidence is missing or coverage is incomplete. Missing verification is not a defect and does not lower the grade; say "unverified" in the verdict instead of withholding it.
- Failing evidence is a finding: report it, and do not approve while it stands.
- Cite each receipt or CI check you rely on by its label.`
