package builtin

import (
	"encoding/json"
	"strings"
)

type goTestReport struct {
	passed, failed, skipped int
	complete                bool
	output                  string
}

// Go test -json separates test outcomes from text printed by the test program.
func parseGoTestOutput(raw string) goTestReport {
	var report goTestReport
	var output strings.Builder
	packages := make(map[string]bool)
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		var event struct {
			Action     string
			Package    string
			ImportPath string
			Test       string
			Output     string
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Action == "" || (event.Package == "" && event.ImportPath == "") {
			output.WriteString(line)
			output.WriteByte('\n')
			continue
		}
		if event.Output != "" {
			output.WriteString(event.Output)
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
	return report
}
