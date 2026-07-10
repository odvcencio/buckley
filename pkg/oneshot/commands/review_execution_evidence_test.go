package commands

import (
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestClassifyReviewCommandEvidence(t *testing.T) {
	zero := 0
	one := 1
	base := model.CommandExecutionEvidence{
		ExitCode:         &zero,
		Status:           "completed",
		WorkingDirectory: "/snapshot",
		RepositoryRoot:   "/snapshot",
	}
	tests := []struct {
		name    string
		command string
		output  string
		mutate  func(*model.CommandExecutionEvidence)
		want    string
	}{
		{name: "shell wrapped go build", command: `/bin/bash -lc 'go build ./pkg/model'`, want: reviewEvidenceBuild},
		{name: "go compile-only test", command: `/bin/bash -lc "go test -run '^$' ./pkg/model"`, want: reviewEvidenceBuild},
		{name: "go package tests", command: "go test ./pkg/model ./pkg/rlm", output: "ok  model\nok  rlm", want: reviewEvidenceTest},
		{name: "go recursive tests with testless utility package", command: "go test ./...", output: "?   example.test/cmd/tool [no test files]\nok  example.test/pkg/core 0.123s", want: reviewEvidenceTest},
		{name: "cargo check", command: "cargo check --workspace", want: reviewEvidenceBuild},
		{name: "cargo tests", command: "cargo test --workspace", output: "running 2 tests\ntest result: ok", want: reviewEvidenceTest},
		{name: "pytest", command: "python3 -m pytest .", output: "12 passed", want: reviewEvidenceTest},
		{name: "npm build", command: "npm run build", want: reviewEvidenceBuild},
		{name: "pnpm tests", command: "pnpm test", output: "Tests: 3 passed", want: reviewEvidenceTest},
		{name: "make build", command: "make -j4 build", want: reviewEvidenceBuild},
		{name: "make tests", command: "make test", output: "PASS", want: reviewEvidenceTest},
		{name: "echo is not execution", command: `echo "go test ./..."`},
		{name: "chain can mask failure", command: "go test ./pkg/model || true"},
		{name: "pipe can mask failure", command: "go test ./pkg/model | tee out"},
		{name: "redirection hides semantics", command: "go test ./pkg/model >/dev/null"},
		{name: "subshell rejected", command: "(go test ./pkg/model)"},
		{name: "cd rejected", command: "cd pkg/model && go test ."},
		{name: "go test name filter", command: "go test -run TestOne ./pkg/model", output: "ok"},
		{name: "go focused test with execution proof", command: "go test -v -run TestOne ./pkg/model", output: "=== RUN   TestOne\n--- PASS: TestOne (0.00s)\nPASS", want: reviewEvidenceTest},
		{name: "go test exec override", command: "go test -exec /usr/bin/true ./pkg/model", output: "ok"},
		{name: "go test args escape", command: "go test ./pkg/model -args -test.list=.", output: "ok"},
		{name: "go count zero", command: "go test -count=0 ./pkg/model", output: "ok"},
		{name: "shell variable expansion", command: "go test $PACKAGE"},
		{name: "shell glob expansion", command: "go test ./pkg/*"},
		{name: "pytest test filter", command: "pytest -k one tests", output: "1 passed"},
		{name: "cargo test name filter", command: "cargo test one", output: "running 1 test"},
		{name: "node test filter", command: "npm test -- --testNamePattern=one", output: "1 passed"},
		{name: "arbitrary environment rejected", command: "GOFLAGS=-run=NoSuchTest go test ./pkg/model", output: "ok"},
		{name: "make directory change rejected", command: "make -C pkg test", output: "PASS"},
		{name: "go no test files", command: "go test ./pkg/empty", output: "? pkg/empty [no test files]"},
		{name: "go no tests to run", command: "go test ./pkg/model", output: "testing: warning: no tests to run"},
		{name: "pytest collected zero", command: "pytest tests", output: "collected 0 items"},
		{name: "cargo zero tests", command: "cargo test", output: "running 0 tests"},
		{name: "npm pass with no tests", command: "npm test", output: "No tests found, exiting with code 0"},
		{name: "nonzero exit", command: "go test ./pkg/model", mutate: func(e *model.CommandExecutionEvidence) { e.ExitCode = &one }},
		{name: "missing exit", command: "go test ./pkg/model", mutate: func(e *model.CommandExecutionEvidence) { e.ExitCode = nil }},
		{name: "unfinished", command: "go test ./pkg/model", mutate: func(e *model.CommandExecutionEvidence) { e.Status = "in_progress" }},
		{name: "outside root", command: "go test ./pkg/model", mutate: func(e *model.CommandExecutionEvidence) { e.WorkingDirectory = "/snapshot/pkg/model" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := base
			evidence.Command = test.command
			evidence.AggregatedOutput = test.output
			if test.mutate != nil {
				test.mutate(&evidence)
			}
			got, ok := classifyReviewCommandEvidence(evidence)
			if test.want == "" {
				if ok || got != "" {
					t.Fatalf("classified unsafe evidence as %q", got)
				}
				return
			}
			if !ok || got != test.want {
				t.Fatalf("classification=(%q,%t), want (%q,true)", got, ok, test.want)
			}
		})
	}
}
