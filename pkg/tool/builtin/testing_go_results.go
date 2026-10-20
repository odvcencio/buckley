package builtin

import (
	"encoding/json"
	"strings"
)

type goTestReport struct {
	passed, failed, skipped int
	complete, buildOnly     bool
	output                  string
	failures                []string
}
type goTestEvent struct {
	Action, Package, ImportPath, Test, Output string
}

func (e goTestEvent) scopeKey() string {
	pkg := e.Package
	if pkg == "" {
		pkg = e.ImportPath
	}
	return pkg + "\x00" + e.Test
}

type goPackageState struct{ complete, noTests bool }
type testScopeTail struct{ data []byte }

func (b *testScopeTail) write(text string) {
	const limit = 2048
	if len(text) >= limit {
		b.data = append(b.data[:0], text[len(text)-limit:]...)
		return
	}
	if extra := len(b.data) + len(text) - limit; extra > 0 {
		copy(b.data, b.data[extra:])
		b.data = b.data[:len(b.data)-extra]
	}
	b.data = append(b.data, text...)
}

type goTestStream struct {
	report       goTestReport
	packages     map[string]goPackageState
	active       map[string]*testScopeTail
	selected     map[string]*testScopeTail
	order        []string
	omitted      bool
	problem      string
	failedRun    bool
	packageBytes int
}

func (g *goTestStream) event(raw []byte, output *testText, newline bool) {
	if len(raw) == 0 {
		return
	}
	var e goTestEvent
	if err := json.Unmarshal(raw, &e); err != nil || e.Action == "" || (e.Package == "" && e.ImportPath == "") {
		_, _ = output.Write(raw)
		if newline {
			_, _ = output.Write([]byte("\n"))
		}
		if strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
			g.problem = "go test produced an invalid structured event"
		}
		return
	}
	_, _ = output.Write([]byte(e.Output))
	if len(e.Package) > 8192 || len(e.ImportPath) > 8192 || len(e.Test) > 8192 {
		g.problem = "go test scope exceeds metadata budget"
		return
	}
	if g.packages == nil {
		g.packages = make(map[string]goPackageState)
		g.active = make(map[string]*testScopeTail)
		g.selected = make(map[string]*testScopeTail)
	}
	key := e.scopeKey()
	if e.Output != "" {
		tail := g.selected[key]
		if tail == nil {
			tail = g.active[key]
		}
		if tail == nil && len(g.active) < 128 {
			tail = &testScopeTail{}
			g.active[key] = tail
		}
		if tail != nil {
			tail.write(e.Output)
		}
	}
	if e.Action == "fail" || e.Action == "build-fail" {
		if e.Test == "" {
			g.failedRun = true
		}
		if _, ok := g.selected[key]; !ok {
			if len(g.order) < 5 {
				g.order = append(g.order, strings.Clone(key))
				detail, ok := g.active[key]
				if !ok {
					detail = &testScopeTail{}
					detail.write("Earlier output for this failing scope was not retained.")
				}
				g.selected[key] = detail
			} else {
				g.omitted = true
			}
		}
	}
	switch e.Action {
	case "pass", "fail", "skip", "build-fail":
		delete(g.active, key)
	}
	if e.Package == "" {
		return
	}
	state, exists := g.packages[e.Package]
	if !exists && len(g.packages) >= 10000 {
		g.problem = "go test package count exceeds capture budget; narrow the path"
		return
	}
	if !exists {
		if g.packageBytes+len(e.Package) > DefaultTestMetadataBytes {
			g.problem = "go test package metadata exceeds capture budget; narrow the path"
			return
		}
		g.packageBytes += len(e.Package)
	}
	if e.Test == "" {
		if strings.Contains(e.Output, "[no test files]") {
			state.noTests = true
		}
		switch e.Action {
		case "pass", "fail", "skip":
			state.complete = true
		}
	} else {
		switch e.Action {
		case "pass":
			g.report.passed++
		case "fail":
			g.report.failed++
		case "skip":
			g.report.skipped++
		}
	}
	g.packages[e.Package] = state
}
func (g *goTestStream) finish(output string) goTestReport {
	r := g.report
	r.output = output
	r.complete = len(g.packages) > 0 && g.problem == ""
	r.buildOnly = r.complete
	for _, state := range g.packages {
		r.complete = r.complete && state.complete
		r.buildOnly = r.buildOnly && state.noTests
	}
	r.buildOnly = r.buildOnly && r.complete
	for _, key := range g.order {
		r.failures = append(r.failures, truncateString(strings.ReplaceAll(key, "\x00", "/"), 200)+"\n"+string(g.selected[key].data))
	}
	if g.omitted {
		r.failures = append(r.failures, "Additional failing scopes omitted.")
	}
	return r
}

// Retained evidence is bounded even when the caller already owns a raw transcript.
func parseGoTestOutput(raw string) goTestReport {
	stream := testStream{framework: "go", goReport: &goTestStream{}}
	_, _ = stream.Write([]byte(raw))
	stream.finish()
	return stream.goReport.finish(stream.text.String())
}
func truncateString(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max] + "..."
}
