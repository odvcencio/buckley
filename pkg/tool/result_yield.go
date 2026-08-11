package tool

import (
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

// ResultYield is the compact, truthful result count a tool can report. A
// zero count with Observed set is a completed query that found nothing; it is
// deliberately distinct from a failed or unknown result.
type ResultYield struct {
	Observed bool
	Count    int
	Unit     string
}

// IsZero reports whether a tool completed with a measured empty result.
func (y ResultYield) IsZero() bool {
	return y.Observed && y.Count == 0
}

// Summary returns a human-readable count suitable for compact operation
// displays, for example "0 matches" or "1 file".
func (y ResultYield) Summary() string {
	if !y.Observed {
		return ""
	}
	unit := strings.TrimSpace(y.Unit)
	if unit == "" {
		unit = "result"
	}
	if y.Count == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %s", y.Count, pluralYieldUnit(unit))
}

func pluralYieldUnit(unit string) string {
	switch unit {
	case "entry":
		return "entries"
	case "match":
		return "matches"
	case "file", "result":
		return unit + "s"
	default:
		return unit + "s"
	}
}

// ResultYieldForTool extracts the result count only from repository
// exploration tools whose output contract defines a count. Callers can use
// it for progress projection without mistaking successful zero results for
// failures.
func ResultYieldForTool(toolName string, result *builtin.Result, execErr error) ResultYield {
	if execErr != nil || result == nil || !result.Success {
		return ResultYield{}
	}

	var unit string
	switch strings.TrimSpace(toolName) {
	case "search_text":
		unit = "match"
	case "find_files":
		unit = "file"
	case "list_directory":
		unit = "entry"
	default:
		return ResultYield{}
	}

	count, ok := resultCount(result.Data)
	if !ok || count < 0 {
		return ResultYield{}
	}
	return ResultYield{Observed: true, Count: count, Unit: unit}
}

func resultCount(data map[string]any) (int, bool) {
	if data == nil {
		return 0, false
	}
	value, ok := data["count"]
	if !ok {
		return 0, false
	}
	switch count := value.(type) {
	case int:
		return count, true
	case int8:
		return int(count), true
	case int16:
		return int(count), true
	case int32:
		return int(count), true
	case int64:
		return int(count), true
	case uint:
		return int(count), true
	case uint8:
		return int(count), true
	case uint16:
		return int(count), true
	case uint32:
		return int(count), true
	case uint64:
		return int(count), true
	case float64:
		if count == float64(int(count)) {
			return int(count), true
		}
	}
	return 0, false
}
