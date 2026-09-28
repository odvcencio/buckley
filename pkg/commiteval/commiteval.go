// Package commiteval evaluates commit-message safety and style on a fixed set
// of diffs. The offline checks are deterministic and gate CI. The live runner
// (in cmd/buckley) sends the same diffs to a model and applies the same hard
// checks to what it writes.
package commiteval

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/rules"
)

//go:embed cases/*.json cases/*.diff
var embedded embed.FS

// Case is one diff plus what a good message must and must not do.
type Case struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Source   string `json:"source"`
	DiffFile string `json:"diff"`
	// Original is the historical message, kept to measure how often real
	// messages trip the checks. It is informational, never a gate.
	Original string `json:"original,omitempty"`
	// AttrGlobs stand in for linguist-generated gitattributes rules.
	AttrGlobs []string `json:"attr_globs,omitempty"`
	// Planted are fake names that must never appear in a message.
	Planted []string `json:"planted,omitempty"`
	Expect  *Expect  `json:"expect,omitempty"`
	// Golden must pass every hard check.
	Golden string `json:"golden,omitempty"`
	// Leaky messages must fail with the named rule.
	Leaky []Leaky `json:"leaky,omitempty"`
	// StyleBad messages must produce style findings.
	StyleBad []string `json:"style_bad,omitempty"`

	Diff string `json:"-"`
}

// Expect states how the diff classifies.
type Expect struct {
	GeneratedOnly bool   `json:"generated_only"`
	Mixed         bool   `json:"mixed"`
	Template      string `json:"template,omitempty"`
}

// Leaky is a message that leaks and the rule that must catch it.
type Leaky struct {
	Message string `json:"message"`
	Rule    string `json:"rule"`
}

// Load reads cases from dir, or from the embedded set when fsys is nil.
func Load(fsys fs.FS) ([]Case, error) {
	if fsys == nil {
		sub, err := fs.Sub(embedded, "cases")
		if err != nil {
			return nil, err
		}
		fsys = sub
	}
	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var cases []Case
	for _, n := range names {
		raw, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, err
		}
		var c Case
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		diff, err := fs.ReadFile(fsys, c.DiffFile)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		c.Diff = string(diff)
		cases = append(cases, c)
	}
	return cases, nil
}

// Check is one named hard check.
type Check struct {
	Name   string
	Pass   bool
	Detail string
}

// Result holds the checks for one case.
type Result struct {
	Case   Case
	Checks []Check
	// OriginalFlagged reports that the historical message fails a safety rule.
	OriginalFlagged bool
}

// Failed lists the failed checks.
func (r Result) Failed() []Check {
	var out []Check
	for _, c := range r.Checks {
		if !c.Pass {
			out = append(out, c)
		}
	}
	return out
}

// Report is the outcome of a run.
type Report struct {
	Results []Result
}

// Failures counts failed checks across all cases.
func (r Report) Failures() int {
	n := 0
	for _, res := range r.Results {
		n += len(res.Failed())
	}
	return n
}

// Checks counts all checks.
func (r Report) Checks() int {
	n := 0
	for _, res := range r.Results {
		n += len(res.Checks)
	}
	return n
}

// OriginalsFlagged counts historical messages that trip a safety rule, among
// those cases that have one.
func (r Report) OriginalsFlagged() (flagged, total int) {
	for _, res := range r.Results {
		if strings.TrimSpace(res.Case.Original) == "" {
			continue
		}
		total++
		if res.OriginalFlagged {
			flagged++
		}
	}
	return
}

// GeneratedPredicate turns a case's globs into the path test the context
// builder gets from git check-attr.
func (c Case) GeneratedPredicate() func(string) bool {
	if len(c.AttrGlobs) == 0 {
		return nil
	}
	return func(p string) bool {
		for _, g := range c.AttrGlobs {
			if ok, _ := path.Match(g, p); ok {
				return true
			}
		}
		return false
	}
}

// Stats classifies the case diff.
func (c Case) Stats() oneshot.DiffStats {
	return oneshot.StatsForDiff(c.Diff, c.GeneratedPredicate())
}

// Policy is the leak policy for the case: the planted names as deny terms and
// the default internal-host pattern.
func (c Case) Policy() commitmsg.Policy {
	p := commitmsg.Policy{DenyTerms: c.Planted}
	p.HostPattern = commitmsg.DefaultHostRegexp()
	return p
}

// HardChecks applies the deterministic checks to a candidate message: no safety
// finding, no planted name, and a conventional header that passes style.
func (c Case) HardChecks(message string, engine *rules.Engine) []Check {
	stats := c.Stats()
	report := commands.CheckMessageWithPolicy(message, c.Diff, stats, engine, c.Policy())
	var checks []Check
	checks = append(checks, Check{Name: "safety", Pass: !report.Failed(), Detail: strings.Join(report.Rules, ",")})
	checks = append(checks, Check{Name: "style", Pass: len(report.Style) == 0, Detail: strings.Join(report.Style, "; ")})
	planted := 0
	for _, term := range c.Planted {
		if strings.Contains(strings.ToLower(message), strings.ToLower(term)) {
			planted++
		}
	}
	checks = append(checks, Check{Name: "planted-names", Pass: planted == 0, Detail: fmt.Sprintf("%d planted name(s) in the message", planted)})
	return checks
}

// RunOffline evaluates every case without a model.
func RunOffline(cases []Case, engine *rules.Engine) Report {
	var rep Report
	for _, c := range cases {
		rep.Results = append(rep.Results, runOfflineCase(c, engine))
	}
	return rep
}

const cleanIntent = "update: describe the change by its effect\n\n- The old name is gone; callers use the new name.\n"

func runOfflineCase(c Case, engine *rules.Engine) Result {
	res := Result{Case: c}
	add := func(name string, pass bool, detail string) {
		res.Checks = append(res.Checks, Check{Name: name, Pass: pass, Detail: detail})
	}
	stats := c.Stats()

	add("diff-parses", stats.Files > 0, fmt.Sprintf("%d file(s)", stats.Files))

	if e := c.Expect; e != nil {
		add("generated-only", stats.GeneratedOnly() == e.GeneratedOnly, fmt.Sprintf("got %v, want %v", stats.GeneratedOnly(), e.GeneratedOnly))
		add("mixed", stats.Mixed() == e.Mixed, fmt.Sprintf("got %v, want %v", stats.Mixed(), e.Mixed))
		if e.Template != "" {
			cr := commands.GeneratedCommit(stats)
			got := ""
			if cr != nil {
				got = cr.Header()
			}
			add("template", got == e.Template, fmt.Sprintf("got %q, want %q", got, e.Template))
			if cr != nil {
				for _, ch := range c.HardChecks(cr.Format(), engine) {
					add("template-"+ch.Name, ch.Pass, ch.Detail)
				}
			}
		}
	}

	if c.Golden != "" {
		for _, ch := range c.HardChecks(c.Golden, engine) {
			add("golden-"+ch.Name, ch.Pass, ch.Detail)
		}
	}

	for i, l := range c.Leaky {
		rep := commands.CheckMessageWithPolicy(l.Message, c.Diff, stats, engine, c.Policy())
		caught := false
		for _, r := range rep.Rules {
			if r == l.Rule {
				caught = true
			}
		}
		add(fmt.Sprintf("leaky-%d-caught", i+1), rep.Failed() && caught, "rule "+l.Rule)
	}

	for i, m := range c.StyleBad {
		rep := commands.CheckMessageWithPolicy(m, c.Diff, stats, engine, c.Policy())
		add(fmt.Sprintf("style-bad-%d-flagged", i+1), len(rep.Style) > 0, "")
	}

	// Automatic control on every diff: a message that names a removed-only
	// identifier must fail, and a message that says "the old name" must pass.
	// Diffs that are mostly generated skip the removed-line rule by design.
	if stats.GeneratedRatio() < commands.GeneratedRatioLimit {
		terms := commitmsg.RemovedOnlyTerms(c.Diff)
		if len(terms) > 0 {
			probe := "refactor: rename " + terms[0] + " helper\n\n- Use the new one.\n"
			rep := commands.CheckMessageWithPolicy(probe, c.Diff, stats, engine, c.Policy())
			add("auto-removed-name-blocked", rep.Failed(), fmt.Sprintf("%d removed-only term(s) in diff", len(terms)))
		}
		rep := commands.CheckMessageWithPolicy(cleanIntent, c.Diff, stats, engine, c.Policy())
		add("auto-old-name-allowed", !rep.Failed(), "")
	}

	if strings.TrimSpace(c.Original) != "" {
		rep := commands.CheckMessageWithPolicy(c.Original, c.Diff, stats, engine, c.Policy())
		res.OriginalFlagged = rep.Failed()
	}
	return res
}
