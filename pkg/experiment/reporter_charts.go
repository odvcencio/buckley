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
	// Table header
	fmt.Fprintf(r.out, "%-20s │ %6s │ %9s │ %10s │ %8s\n",
		r.style(r.boldStyle, "Model"),
		r.style(r.boldStyle, "Score"),
		r.style(r.boldStyle, "Cost"),
		r.style(r.boldStyle, "Duration"),
		r.style(r.boldStyle, "Tokens"),
	)
	fmt.Fprintln(r.out, strings.Repeat("─", 20)+"─┼"+strings.Repeat("─", 8)+"┼"+
		strings.Repeat("─", 11)+"┼"+strings.Repeat("─", 12)+"┼"+strings.Repeat("─", 10))

	for _, ranking := range report.Rankings {
		v := findVariantReport(report.Variants, ranking.VariantID)
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

		// Model name (truncate if too long)
		modelName := truncateString(v.ModelID, 18)

		// Score
		score := fmt.Sprintf("%.0f%%", ranking.Score*100)

		// Cost
		cost := "-"
		if v.Metrics.TotalCost > 0 {
			cost = fmt.Sprintf("$%.4f", v.Metrics.TotalCost)
		}

		// Duration
		duration := formatDurationMs(v.Metrics.DurationMs)

		// Tokens
		tokens := fmt.Sprintf("%d", v.Metrics.PromptTokens+v.Metrics.CompletionTokens)

		fmt.Fprintf(r.out, "%s %-18s │ %6s │ %9s │ %10s │ %8s\n",
			indicator, modelName, score, cost, duration, tokens)
	}
	fmt.Fprintln(r.out)
}

func (r *TerminalReporter) renderCostChart(report *ComparisonReport) {
	fmt.Fprintln(r.out, r.style(r.boldStyle, "Cost Comparison:"))

	// Collect costs and find max
	type costEntry struct {
		modelID string
		cost    float64
	}
	var entries []costEntry
	var maxCost float64

	for _, v := range report.Variants {
		entries = append(entries, costEntry{modelID: v.ModelID, cost: v.Metrics.TotalCost})
		if v.Metrics.TotalCost > maxCost {
			maxCost = v.Metrics.TotalCost
		}
	}

	// Sort by cost descending
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].cost > entries[j].cost
	})

	// Render bars
	barWidth := r.chartBarWidth()
	for _, e := range entries {
		r.renderBar(e.modelID, e.cost, maxCost, barWidth, "$%.4f")
	}
	fmt.Fprintln(r.out)
}

func (r *TerminalReporter) renderDurationChart(report *ComparisonReport) {
	fmt.Fprintln(r.out, r.style(r.boldStyle, "Duration Comparison:"))

	// Collect durations and find max
	type durationEntry struct {
		modelID string
		ms      int64
	}
	var entries []durationEntry
	var maxMs int64

	for _, v := range report.Variants {
		entries = append(entries, durationEntry{modelID: v.ModelID, ms: v.Metrics.DurationMs})
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
		label := truncateString(e.modelID, 14)
		bar := r.buildBar(float64(e.ms), float64(maxMs), barWidth)
		duration := formatDurationMs(e.ms)
		fmt.Fprintf(r.out, "%-14s %s %s\n", label, r.style(r.barStyle, bar), r.style(r.durationStyle, duration))
	}
	fmt.Fprintln(r.out)
}

func (r *TerminalReporter) renderBar(modelID string, value, maxValue float64, width int, format string) {
	label := truncateString(modelID, 14)
	bar := r.buildBar(value, maxValue, width)
	valueStr := fmt.Sprintf(format, value)
	if value == 0 {
		valueStr = "$0.00"
	}
	fmt.Fprintf(r.out, "%-14s %s %s\n", label, r.style(r.barStyle, bar), r.style(r.costStyle, valueStr))
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
	if len(report.Rankings) == 0 {
		return
	}

	winner := report.Rankings[0]
	v := findVariantReport(report.Variants, winner.VariantID)
	if v == nil {
		return
	}

	// Determine winning reason
	var reasons []string
	if winner.Score >= 1.0 {
		reasons = append(reasons, "100% score")
	}

	// Check if lowest cost among successful variants
	lowestCost := true
	for _, other := range report.Variants {
		if other.VariantID != winner.VariantID &&
			other.Status == RunCompleted &&
			other.Metrics.TotalCost < v.Metrics.TotalCost &&
			other.Metrics.TotalCost > 0 {
			lowestCost = false
			break
		}
	}
	if lowestCost && v.Metrics.TotalCost > 0 {
		reasons = append(reasons, "lowest cost")
	}

	// Check if fastest
	fastest := true
	for _, other := range report.Variants {
		if other.VariantID != winner.VariantID &&
			other.Status == RunCompleted &&
			other.Metrics.DurationMs < v.Metrics.DurationMs {
			fastest = false
			break
		}
	}
	if fastest && v.Metrics.DurationMs > 0 {
		reasons = append(reasons, "fastest")
	}

	reasonStr := ""
	if len(reasons) > 0 {
		reasonStr = " (" + strings.Join(reasons, ", ") + ")"
	}

	fmt.Fprintf(r.out, "%s %s%s\n",
		r.style(r.winnerStyle, "Winner:"),
		r.style(r.boldStyle, v.ModelID),
		r.style(r.dimStyle, reasonStr),
	)
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

	for _, ranking := range report.Rankings {
		v := findVariantReport(report.Variants, ranking.VariantID)
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

		cost := ""
		if v.Metrics.TotalCost > 0 {
			cost = r.style(r.costStyle, fmt.Sprintf("$%.4f", v.Metrics.TotalCost))
		}

		fmt.Fprintf(r.out, "  %s %s %.0f%% %s %s\n",
			indicator,
			truncateString(v.ModelID, 20),
			ranking.Score*100,
			formatDurationMs(v.Metrics.DurationMs),
			cost,
		)
	}

	return nil
}
