package widgets

import (
	"math"

	"github.com/odvcencio/fluffy-ui/backend"
	"github.com/odvcencio/fluffy-ui/runtime"
)

type ringPoint struct {
	x int
	y int
}

var ringPoints = []ringPoint{
	{1, 0}, {2, 0}, {3, 0},
	{4, 1}, {4, 2}, {4, 3},
	{3, 4}, {2, 4}, {1, 4},
	{0, 3}, {0, 2}, {0, 1},
}

const progressRingSize = 5

func drawProgressRing(buf *runtime.Buffer, x, y, percent int, full, empty, edge backend.Style) {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	if buf == nil {
		return
	}

	total := len(ringPoints)
	if total == 0 {
		return
	}
	filled := int(math.Round(float64(percent) / 100 * float64(total)))
	if filled < 0 {
		filled = 0
	}
	if filled > total {
		filled = total
	}

	for i, pt := range ringPoints {
		style := empty
		ch := '○'
		if i < filled {
			ch = '●'
			style = full
			if i == filled-1 && filled < total {
				style = edge
			}
		}
		buf.Set(x+pt.x, y+pt.y, ch, style)
	}
}
