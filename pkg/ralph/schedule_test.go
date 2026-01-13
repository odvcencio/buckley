// pkg/ralph/schedule_test.go
package ralph

import (
	"testing"
	"time"
)

func TestParseCron_Valid(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"every minute", "* * * * *"},
		{"every 5 minutes", "*/5 * * * *"},
		{"at minute 0", "0 * * * *"},
		{"at midnight", "0 0 * * *"},
		{"at midnight on Sunday", "0 0 * * 0"},
		{"specific minutes", "0,15,30,45 * * * *"},
		{"minute range", "0-15 * * * *"},
		{"complex", "0,30 9-17 * * 1-5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := ParseCron(tt.expr)
			if err != nil {
				t.Errorf("ParseCron(%q) returned error: %v", tt.expr, err)
			}
			if spec == nil {
				t.Errorf("ParseCron(%q) returned nil spec", tt.expr)
			}
		})
	}
}

func TestParseCron_Invalid(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"empty", ""},
		{"too few fields", "* * * *"},
		{"too many fields", "* * * * * *"},
		{"invalid interval", "*/0 * * * *"},
		{"invalid minute", "60 * * * *"},
		{"invalid hour", "* 25 * * *"},
		{"invalid day", "* * 32 * *"},
		{"invalid month", "* * * 13 *"},
		{"invalid weekday", "* * * * 7"},
		{"invalid range", "5-3 * * * *"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCron(tt.expr)
			if err == nil {
				t.Errorf("ParseCron(%q) should return error", tt.expr)
			}
		})
	}
}

func TestCronSpec_Matches(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		time    time.Time
		matches bool
	}{
		{
			name:    "every minute matches any time",
			expr:    "* * * * *",
			time:    time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
			matches: true,
		},
		{
			name:    "every 5 minutes matches minute 0",
			expr:    "*/5 * * * *",
			time:    time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
			matches: true,
		},
		{
			name:    "every 5 minutes matches minute 15",
			expr:    "*/5 * * * *",
			time:    time.Date(2025, 1, 15, 10, 15, 0, 0, time.UTC),
			matches: true,
		},
		{
			name:    "every 5 minutes does not match minute 3",
			expr:    "*/5 * * * *",
			time:    time.Date(2025, 1, 15, 10, 3, 0, 0, time.UTC),
			matches: false,
		},
		{
			name:    "specific minute matches",
			expr:    "30 * * * *",
			time:    time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
			matches: true,
		},
		{
			name:    "specific minute does not match",
			expr:    "30 * * * *",
			time:    time.Date(2025, 1, 15, 10, 15, 0, 0, time.UTC),
			matches: false,
		},
		{
			name:    "midnight matches",
			expr:    "0 0 * * *",
			time:    time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
			matches: true,
		},
		{
			name:    "midnight does not match noon",
			expr:    "0 0 * * *",
			time:    time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
			matches: false,
		},
		{
			name:    "Sunday matches",
			expr:    "0 0 * * 0",
			time:    time.Date(2025, 1, 19, 0, 0, 0, 0, time.UTC), // Sunday
			matches: true,
		},
		{
			name:    "Sunday does not match Monday",
			expr:    "0 0 * * 0",
			time:    time.Date(2025, 1, 20, 0, 0, 0, 0, time.UTC), // Monday
			matches: false,
		},
		{
			name:    "range matches lower bound",
			expr:    "0 9-17 * * *",
			time:    time.Date(2025, 1, 15, 9, 0, 0, 0, time.UTC),
			matches: true,
		},
		{
			name:    "range matches upper bound",
			expr:    "0 9-17 * * *",
			time:    time.Date(2025, 1, 15, 17, 0, 0, 0, time.UTC),
			matches: true,
		},
		{
			name:    "range does not match outside",
			expr:    "0 9-17 * * *",
			time:    time.Date(2025, 1, 15, 8, 0, 0, 0, time.UTC),
			matches: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := ParseCron(tt.expr)
			if err != nil {
				t.Fatalf("ParseCron(%q) error: %v", tt.expr, err)
			}

			if got := spec.Matches(tt.time); got != tt.matches {
				t.Errorf("Matches(%v) = %v, want %v", tt.time, got, tt.matches)
			}
		})
	}
}

func TestCronSpec_Matches_Nil(t *testing.T) {
	var spec *CronSpec
	if spec.Matches(time.Now()) {
		t.Error("nil CronSpec should not match")
	}
}

func TestEvalWhen(t *testing.T) {
	tests := []struct {
		name   string
		expr   string
		ctx    WhenContext
		result bool
	}{
		{
			name:   "iteration greater than",
			expr:   "iteration > 10",
			ctx:    WhenContext{Iteration: 15},
			result: true,
		},
		{
			name:   "iteration not greater than",
			expr:   "iteration > 10",
			ctx:    WhenContext{Iteration: 5},
			result: false,
		},
		{
			name:   "error count >=",
			expr:   "error_count >= 3",
			ctx:    WhenContext{ErrorCount: 3},
			result: true,
		},
		{
			name:   "consec errors",
			expr:   "consec_errors >= 2",
			ctx:    WhenContext{ConsecErrors: 2},
			result: true,
		},
		{
			name:   "cost threshold",
			expr:   "cost > 5.0",
			ctx:    WhenContext{TotalCost: 6.5},
			result: true,
		},
		{
			name:   "cost below threshold",
			expr:   "cost > 5.0",
			ctx:    WhenContext{TotalCost: 4.0},
			result: false,
		},
		{
			name:   "elapsed minutes",
			expr:   "elapsed > 60",
			ctx:    WhenContext{ElapsedMinutes: 90},
			result: true,
		},
		{
			name:   "has_error true",
			expr:   "has_error",
			ctx:    WhenContext{HasError: true},
			result: true,
		},
		{
			name:   "has_error false",
			expr:   "has_error",
			ctx:    WhenContext{HasError: false},
			result: false,
		},
		{
			name:   "equals operator",
			expr:   "iteration == 10",
			ctx:    WhenContext{Iteration: 10},
			result: true,
		},
		{
			name:   "not equals operator",
			expr:   "iteration != 10",
			ctx:    WhenContext{Iteration: 5},
			result: true,
		},
		{
			name:   "less than operator",
			expr:   "iteration < 10",
			ctx:    WhenContext{Iteration: 5},
			result: true,
		},
		{
			name:   "less than or equal",
			expr:   "iteration <= 10",
			ctx:    WhenContext{Iteration: 10},
			result: true,
		},
		{
			name:   "empty expression",
			expr:   "",
			ctx:    WhenContext{},
			result: false,
		},
		{
			name:   "invalid expression",
			expr:   "foo bar",
			ctx:    WhenContext{},
			result: false,
		},
		{
			name:   "unknown variable",
			expr:   "unknown > 5",
			ctx:    WhenContext{},
			result: false,
		},
		{
			name:   "invalid operator",
			expr:   "iteration ~ 5",
			ctx:    WhenContext{},
			result: false,
		},
		{
			name:   "invalid value",
			expr:   "iteration > abc",
			ctx:    WhenContext{},
			result: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EvalWhen(tt.expr, tt.ctx); got != tt.result {
				t.Errorf("EvalWhen(%q, %+v) = %v, want %v", tt.expr, tt.ctx, got, tt.result)
			}
		})
	}
}

func TestCronParseError(t *testing.T) {
	err := &CronParseError{Expr: "bad", Reason: "test reason"}
	expected := "invalid cron expression: bad: test reason"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}
