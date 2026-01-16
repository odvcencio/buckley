package buckley

import (
	"math"
	"strings"
)

var ringPhases = []rune{'○', '◔', '◑', '◕', '●'}

func progressGlyph(percent int) rune {
	if percent <= 0 {
		return ringPhases[0]
	}
	if percent >= 100 {
		return ringPhases[len(ringPhases)-1]
	}
	step := float64(percent) / 100.0 * float64(len(ringPhases)-1)
	idx := int(math.Round(step))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ringPhases) {
		idx = len(ringPhases) - 1
	}
	return ringPhases[idx]
}

func sparkline(samples []float64, maxWidth int) string {
	if len(samples) == 0 || maxWidth <= 0 {
		return ""
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	start := 0
	if len(samples) > maxWidth {
		start = len(samples) - maxWidth
	}
	var b strings.Builder
	for _, v := range samples[start:] {
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		idx := int(math.Round(v * float64(len(levels)-1)))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(levels) {
			idx = len(levels) - 1
		}
		b.WriteRune(levels[idx])
	}
	return b.String()
}

func trendArrow(delta float64) string {
	switch {
	case delta > 0.02:
		return "↗"
	case delta < -0.02:
		return "↘"
	default:
		return "→"
	}
}
