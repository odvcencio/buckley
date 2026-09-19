package experiment

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// TerminalReporter renders experiment results with colors and charts.
type TerminalReporter struct {
	out        io.Writer
	comparator *Comparator
	noColor    bool

	// Styles
	successStyle  lipgloss.Style
	failedStyle   lipgloss.Style
	pendingStyle  lipgloss.Style
	headerStyle   lipgloss.Style
	dimStyle      lipgloss.Style
	boldStyle     lipgloss.Style
	barStyle      lipgloss.Style
	boxStyle      lipgloss.Style
	winnerStyle   lipgloss.Style
	costStyle     lipgloss.Style
	durationStyle lipgloss.Style
}

// NewTerminalReporter creates a reporter for terminal output.
func NewTerminalReporter(comparator *Comparator) *TerminalReporter {
	return NewTerminalReporterWithOutput(os.Stdout, comparator)
}

// NewTerminalReporterWithOutput creates a reporter with custom output.
func NewTerminalReporterWithOutput(out io.Writer, comparator *Comparator) *TerminalReporter {
	r := &TerminalReporter{
		out:        out,
		comparator: comparator,

		successStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#008000", Dark: "#55FF55"}),

		failedStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#D00000", Dark: "#FF5555"}),

		pendingStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#666666", Dark: "#888888"}),

		headerStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#333333", Dark: "#FFFFFF"}).
			Bold(true),

		dimStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#666666", Dark: "#888888"}),

		boldStyle: lipgloss.NewStyle().Bold(true),

		barStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#0066CC", Dark: "#5599FF"}),

		boxStyle: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.AdaptiveColor{Light: "#CCCCCC", Dark: "#444444"}).
			Padding(0, 1),

		winnerStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#008000", Dark: "#55FF55"}).
			Bold(true),

		costStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#B8860B", Dark: "#FFAA00"}),

		durationStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#0066CC", Dark: "#5599FF"}),
	}
	return r
}

// SetNoColor disables color output.
func (r *TerminalReporter) SetNoColor(noColor bool) {
	r.noColor = noColor
}

// RenderReport renders a full experiment report with charts.
func (r *TerminalReporter) RenderReport(exp *Experiment) error {
	if exp == nil {
		return fmt.Errorf("experiment is nil")
	}
	if r.comparator == nil {
		return fmt.Errorf("comparator unavailable")
	}

	report, err := r.comparator.Compare(exp)
	if err != nil {
		return err
	}

	// Header
	r.renderHeader(exp)

	// Results table
	r.renderResultsTable(report)

	// Bar charts
	r.renderCostChart(report)
	r.renderDurationChart(report)

	// Winner summary
	r.renderWinner(report)

	return nil
}

func (r *TerminalReporter) renderHeader(exp *Experiment) {
	width := r.terminalWidth()
	title := fmt.Sprintf("Experiment: %s", exp.Name)
	status := fmt.Sprintf("(%s)", exp.Status)

	header := r.style(r.headerStyle, title) + " " + r.statusStyle(exp.Status, status)
	fmt.Fprintln(r.out, header)
	fmt.Fprintln(r.out, r.style(r.dimStyle, strings.Repeat("─", min(width-2, 70))))

	if exp.Description != "" {
		fmt.Fprintln(r.out, r.style(r.dimStyle, exp.Description))
	}
	fmt.Fprintln(r.out)
}

func (r *TerminalReporter) renderResultsTable(report *ComparisonReport) {
	r.renderProvenanceNote()
	reportIndex := newVariantReportIndex(report.Variants)

	// Table header
	fmt.Fprintf(r.out, "%s │ %s │ %s │ %s │ %s │ %s │ %s │ %s │ %s\n",
		r.style(r.boldStyle, "Model"),
		r.style(r.boldStyle, "Run"),
		r.style(r.boldStyle, "Score"),
		r.style(r.boldStyle, "Cost"),
		r.style(r.boldStyle, "Duration"),
		r.style(r.boldStyle, "Tokens"),
		r.style(r.boldStyle, "Input"),
		r.style(r.boldStyle, "Execution"),
		r.style(r.boldStyle, "Evidence"),
	)
	fmt.Fprintln(r.out, strings.Repeat("─", 70))

	for _, ranking := range report.Rankings {
		v := reportIndex.find(ranking.RunID)
		if v == nil {
			continue
		}

		// Status indicator
		var indicator string
		switch v.Status {
		case RunCompleted:
			indicator = r.style(r.successStyle, "✓")
		case RunFailed:
			indicator = r.style(r.failedStyle, "✗")
		default:
			indicator = r.style(r.pendingStyle, "○")
		}

		// Score
		score := formatComparisonScore(v)

		// Cost
		cost := formatCostEvidence(v.CostEvidence, v.Metrics)

		// Duration
		duration := formatDurationMs(v.Metrics.DurationMs)

		// Tokens
		tokens := fmt.Sprintf("%d", formatRunTokens(v.Metrics))

		fmt.Fprintf(r.out, "%s %s │ %s │ %s │ %s │ %s │ %s │ %s │ %s │ %s\n",
			indicator, v.ModelID, v.RunID, score, cost, duration, tokens, compactDigest(v.InputDigest), formatExecutionIdentitySummary(v.ModelExecutions), v.VerificationStatus)
	}
	fmt.Fprintln(r.out)
}

func (r *TerminalReporter) renderCostChart(report *ComparisonReport) {
	fmt.Fprintln(r.out, r.style(r.boldStyle, "Cost Comparison:"))

	// Collect costs and find max
	type costEntry struct {
		label string
		cost  float64
	}
	var entries []costEntry
	var maxCost float64

	for _, v := range report.Variants {
		cost := v.Metrics.TotalCost
		if !v.CostEvidence.Comparable || v.Status != RunCompleted || !finiteNonNegativeFloat(cost) ||
			(cost == 0 && v.CostEvidence.Status != CostEvidenceKnown) {
			fmt.Fprintf(r.out, "%s / %s cost unknown or incomplete (not compared)\n", v.ModelID, v.RunID)
			continue
		}
		entries = append(entries, costEntry{label: comparisonRunLabel(v), cost: v.Metrics.TotalCost})
		if v.Metrics.TotalCost > maxCost {
			maxCost = v.Metrics.TotalCost
		}
	}
	if len(entries) == 0 {
		fmt.Fprintln(r.out, "No comparable cost evidence.")
		fmt.Fprintln(r.out)
		return
	}

	// Sort by cost descending
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].cost > entries[j].cost
	})

	// Render bars
	barWidth := r.chartBarWidth()
	for _, e := range entries {
		r.renderBar(e.label, e.cost, maxCost, barWidth, "$%.4f")
	}
	fmt.Fprintln(r.out)
}

func (r *TerminalReporter) renderDurationChart(report *ComparisonReport) {
	fmt.Fprintln(r.out, r.style(r.boldStyle, "Duration Comparison:"))

	// Collect durations and find max
	type durationEntry struct {
		label string
		ms    int64
	}
	var entries []durationEntry
	var maxMs int64

	for _, v := range report.Variants {
		entries = append(entries, durationEntry{label: comparisonRunLabel(v), ms: v.Metrics.DurationMs})
		if v.Metrics.DurationMs > maxMs {
			maxMs = v.Metrics.DurationMs
		}
	}

	// Sort by duration descending
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ms > entries[j].ms
	})

	// Render bars
	barWidth := r.chartBarWidth()
	for _, e := range entries {
		bar := r.buildBar(float64(e.ms), float64(maxMs), barWidth)
		duration := formatDurationMs(e.ms)
		fmt.Fprintf(r.out, "%s %s %s\n", e.label, r.style(r.barStyle, bar), r.style(r.durationStyle, duration))
	}
	fmt.Fprintln(r.out)
}

func (r *TerminalReporter) renderBar(label string, value, maxValue float64, width int, format string) {
	bar := r.buildBar(value, maxValue, width)
	valueStr := fmt.Sprintf(format, value)
	fmt.Fprintf(r.out, "%s %s %s\n", label, r.style(r.barStyle, bar), r.style(r.costStyle, valueStr))
}

func (r *TerminalReporter) buildBar(value, maxValue float64, width int) string {
	if maxValue == 0 {
		return strings.Repeat("░", width)
	}

	filled := int(value / maxValue * float64(width))
	if filled > width {
		filled = width
	}
	if value > 0 && filled == 0 {
		filled = 1 // Minimum visibility
	}

	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func (r *TerminalReporter) renderWinner(report *ComparisonReport) {
	if report == nil || report.Summary == "" {
		return
	}

	fmt.Fprintln(r.out, report.Summary)
}

func (r *TerminalReporter) style(s lipgloss.Style, text string) string {
	if r.noColor {
		return text
	}
	return s.Render(text)
}

func (r *TerminalReporter) statusStyle(status ExperimentStatus, text string) string {
	switch status {
	case ExperimentCompleted:
		return r.style(r.successStyle, text)
	case ExperimentFailed:
		return r.style(r.failedStyle, text)
	case ExperimentRunning:
		return r.style(r.durationStyle, text)
	default:
		return r.style(r.pendingStyle, text)
	}
}

func (r *TerminalReporter) terminalWidth() int {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width == 0 {
		return 80
	}
	return width
}

func (r *TerminalReporter) chartBarWidth() int {
	width := r.terminalWidth()
	// Label (14) + space + bar + space + value (~10)
	barWidth := width - 14 - 12
	if barWidth < 10 {
		barWidth = 10
	}
	if barWidth > 40 {
		barWidth = 40
	}
	return barWidth
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}

// RenderCompact renders a compact one-line summary per variant.
func (r *TerminalReporter) RenderCompact(exp *Experiment) error {
	if exp == nil {
		return fmt.Errorf("experiment is nil")
	}
	if r.comparator == nil {
		return fmt.Errorf("comparator unavailable")
	}

	report, err := r.comparator.Compare(exp)
	if err != nil {
		return err
	}

	fmt.Fprintf(r.out, "%s ", r.style(r.boldStyle, exp.Name))
	fmt.Fprintf(r.out, "%s\n", r.statusStyle(exp.Status, fmt.Sprintf("(%s)", exp.Status)))
	r.renderProvenanceNote()

	reportIndex := newVariantReportIndex(report.Variants)
	for _, ranking := range report.Rankings {
		v := reportIndex.find(ranking.RunID)
		if v == nil {
			continue
		}

		var indicator string
		switch v.Status {
		case RunCompleted:
			indicator = r.style(r.successStyle, "✓")
		case RunFailed:
			indicator = r.style(r.failedStyle, "✗")
		default:
			indicator = r.style(r.pendingStyle, "○")
		}

		cost := r.style(r.costStyle, formatCostEvidence(v.CostEvidence, v.Metrics))

		rank := "-"
		if ranking.Rank > 0 {
			rank = fmt.Sprintf("%d", ranking.Rank)
		}
		tokens := formatRunTokens(v.Metrics)
		fmt.Fprintf(r.out, "  %s #%s %s %s %s %s tokens=%d %s input=%s exec=%s %s\n",
			indicator,
			rank,
			v.ModelID,
			v.RunID,
			formatComparisonScore(v),
			formatDurationMs(v.Metrics.DurationMs),
			tokens,
			cost,
			compactDigest(v.InputDigest),
			formatExecutionIdentitySummary(v.ModelExecutions),
			v.VerificationStatus,
		)
	}

	return nil
}

func comparisonRunLabel(v VariantReport) string {
	if v.RunID == "" {
		return v.ModelID
	}
	return fmt.Sprintf("%s / %s", v.ModelID, v.RunID)
}

func (r *TerminalReporter) renderProvenanceNote() {
	fmt.Fprintln(r.out, r.style(r.dimStyle, "Provenance: requested inputs only; backend, repository baseline, harness, and tool versions are not captured."))
	fmt.Fprintln(r.out, r.style(r.dimStyle, "Execution identity: observed response identities only; not a complete attempt audit or backend revision proof."))
}

func compactDigest(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
