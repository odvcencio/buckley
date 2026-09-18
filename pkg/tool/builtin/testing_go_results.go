package builtin

import (
	"encoding/json"
	"strings"
)

type goTestReport struct {
	passed, failed, skipped int
	complete                bool
	output                  string
	failures                []string
}

type goTestEvent struct {
	Action     string
	Package    string
	ImportPath string
	Test       string
	Output     string
}

func (e goTestEvent) scopeKey() string {
	pkg := e.Package
	if pkg == "" {
		pkg = e.ImportPath
	}
	return pkg + "\x00" + e.Test
}

// Go test -json separates test outcomes from text printed by the test program.
func parseGoTestOutput(raw string) goTestReport {
	var report goTestReport
	var output strings.Builder
	packages := make(map[string]bool)
	selected := make(map[string]string)
	var order []string
	omitted := false
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		var event goTestEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Action == "" || (event.Package == "" && event.ImportPath == "") {
			output.WriteString(line)
			output.WriteByte('\n')
			continue
		}
		if event.Output != "" {
			output.WriteString(event.Output)
		}
		if event.Action == "fail" || event.Action == "build-fail" {
			key := event.scopeKey()
			if _, ok := selected[key]; !ok {
				if len(selected) < 5 {
					selected[key] = ""
					order = append(order, key)
				} else {
					omitted = true
				}
			}
		}
		if event.Package == "" {
			continue // Build events carry ImportPath, not a test package outcome.
		}
		if _, exists := packages[event.Package]; !exists {
			packages[event.Package] = false
		}
		if event.Test != "" {
			switch event.Action {
			case "pass":
				report.passed++
			case "fail":
				report.failed++
			case "skip":
				report.skipped++
			}
		} else {
			switch event.Action {
			case "pass", "fail", "skip":
				packages[event.Package] = true
			}
		}
	}
	report.complete = len(packages) > 0
	for _, complete := range packages {
		report.complete = report.complete && complete
	}
	report.output = output.String()
	if len(order) > 0 {
		for _, line := range strings.Split(raw, "\n") {
			var event goTestEvent
			if err := json.Unmarshal([]byte(line), &event); err != nil || event.Action == "" {
				continue
			}
			key := event.scopeKey()
			if _, ok := selected[key]; ok {
				selected[key] = strings.Clone(outputTail(selected[key]+event.Output, 2048))
			}
		}
	}
	for _, key := range order {
		report.failures = append(report.failures, truncateString(strings.ReplaceAll(key, "\x00", "/"), 200)+"\n"+selected[key])
	}
	if omitted {
		report.failures = append(report.failures, "Additional failing scopes omitted.")
	}
	return report
}

func truncateString(value string, max int) string {
	if max <= 0 {
		return value
	}
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}
