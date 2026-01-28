// pkg/ralph/schedule.go
package ralph

import (
	"strconv"
	"strings"
	"time"
)

// CronSpec represents a parsed cron expression.
// Supports: minute, hour, day-of-month, month, day-of-week.
type CronSpec struct {
	minute     fieldSpec
	hour       fieldSpec
	dayOfMonth fieldSpec
	month      fieldSpec
	dayOfWeek  fieldSpec
}

type fieldSpec struct {
	all      bool     // * wildcard
	interval int      // */N interval
	values   []int    // specific values
	ranges   [][2]int // ranges like 1-5
}

// ParseCron parses a cron expression.
// Supported formats:
//   - "* * * * *" (every minute)
//   - "*/5 * * * *" (every 5 minutes)
//   - "0 * * * *" (at minute 0 of every hour)
//   - "0 0 * * *" (at midnight)
//   - "0 0 * * 0" (at midnight on Sunday)
func ParseCron(expr string) (*CronSpec, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, &CronParseError{Expr: expr, Reason: "expected 5 fields"}
	}

	spec := &CronSpec{}
	var err error

	if spec.minute, err = parseField(parts[0], 0, 59); err != nil {
		return nil, err
	}
	if spec.hour, err = parseField(parts[1], 0, 23); err != nil {
		return nil, err
	}
	if spec.dayOfMonth, err = parseField(parts[2], 1, 31); err != nil {
		return nil, err
	}
	if spec.month, err = parseField(parts[3], 1, 12); err != nil {
		return nil, err
	}
	if spec.dayOfWeek, err = parseField(parts[4], 0, 6); err != nil {
		return nil, err
	}

	return spec, nil
}

// CronParseError indicates a cron parsing failure.
type CronParseError struct {
	Expr   string
	Reason string
}

func (e *CronParseError) Error() string {
	return "invalid cron expression: " + e.Expr + ": " + e.Reason
}

func parseField(s string, min, max int) (fieldSpec, error) {
	spec := fieldSpec{}

	// Handle "*"
	if s == "*" {
		spec.all = true
		return spec, nil
	}

	// Handle "*/N"
	if strings.HasPrefix(s, "*/") {
		n, err := strconv.Atoi(s[2:])
		if err != nil || n <= 0 {
			return spec, &CronParseError{Expr: s, Reason: "invalid interval"}
		}
		spec.interval = n
		return spec, nil
	}

	// Handle comma-separated values and ranges
	for _, part := range strings.Split(s, ",") {
		if strings.Contains(part, "-") {
			// Range like "1-5"
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				return spec, &CronParseError{Expr: s, Reason: "invalid range"}
			}
			start, err1 := strconv.Atoi(rangeParts[0])
			end, err2 := strconv.Atoi(rangeParts[1])
			if err1 != nil || err2 != nil || start > end || start < min || end > max {
				return spec, &CronParseError{Expr: s, Reason: "invalid range values"}
			}
			spec.ranges = append(spec.ranges, [2]int{start, end})
		} else {
			// Single value
			v, err := strconv.Atoi(part)
			if err != nil || v < min || v > max {
				return spec, &CronParseError{Expr: s, Reason: "invalid value"}
			}
			spec.values = append(spec.values, v)
		}
	}

	return spec, nil
}

// Matches checks if the given time matches this cron spec.
func (c *CronSpec) Matches(t time.Time) bool {
	if c == nil {
		return false
	}

	return c.minute.matches(t.Minute()) &&
		c.hour.matches(t.Hour()) &&
		c.dayOfMonth.matches(t.Day()) &&
		c.month.matches(int(t.Month())) &&
		c.dayOfWeek.matches(int(t.Weekday()))
}

func (f fieldSpec) matches(value int) bool {
	if f.all {
		return true
	}

	if f.interval > 0 {
		return value%f.interval == 0
	}

	for _, v := range f.values {
		if v == value {
			return true
		}
	}

	for _, r := range f.ranges {
		if value >= r[0] && value <= r[1] {
			return true
		}
	}

	return false
}

// WhenContext provides variables for evaluating "when" expressions.
type WhenContext struct {
	Iteration      int
	ErrorCount     int
	ConsecErrors   int // consecutive errors
	TotalCost      float64
	TotalTokens    int
	ElapsedMinutes int
	HasError       bool
}

// EvalWhen evaluates a simple "when" expression.
// Supported expressions:
//   - "iteration > 10"
//   - "error_count >= 3"
//   - "consec_errors >= 2"
//   - "cost > 5.0"
//   - "elapsed > 60" (minutes)
//   - "has_error"
//   - "iteration % 10 == 0"
func EvalWhen(expr string, ctx WhenContext) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false
	}

	// Handle simple boolean
	if expr == "has_error" {
		return ctx.HasError
	}

	parts := strings.Fields(expr)

	// Parse modulo comparison: "var % mod op value"
	if len(parts) == 5 && parts[1] == "%" {
		varValue, ok := whenValue(parts[0], ctx)
		if !ok {
			return false
		}
		modValue, err := strconv.Atoi(parts[2])
		if err != nil || modValue == 0 {
			return false
		}
		targetValue, err := strconv.Atoi(parts[4])
		if err != nil {
			return false
		}
		remainder := int(varValue) % modValue
		return compareValues(float64(remainder), parts[3], float64(targetValue))
	}

	// Parse comparison: "var op value"
	if len(parts) != 3 {
		return false
	}

	varName := parts[0]
	op := parts[1]
	valueStr := parts[2]

	varValue, ok := whenValue(varName, ctx)
	if !ok {
		return false
	}

	targetValue, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return false
	}

	return compareValues(varValue, op, targetValue)
}

func whenValue(name string, ctx WhenContext) (float64, bool) {
	switch name {
	case "iteration":
		return float64(ctx.Iteration), true
	case "error_count":
		return float64(ctx.ErrorCount), true
	case "consec_errors":
		return float64(ctx.ConsecErrors), true
	case "cost":
		return ctx.TotalCost, true
	case "tokens":
		return float64(ctx.TotalTokens), true
	case "elapsed":
		return float64(ctx.ElapsedMinutes), true
	default:
		return 0, false
	}
}

func compareValues(varValue float64, op string, targetValue float64) bool {
	switch op {
	case ">":
		return varValue > targetValue
	case ">=":
		return varValue >= targetValue
	case "<":
		return varValue < targetValue
	case "<=":
		return varValue <= targetValue
	case "==":
		return varValue == targetValue
	case "!=":
		return varValue != targetValue
	default:
		return false
	}
}
