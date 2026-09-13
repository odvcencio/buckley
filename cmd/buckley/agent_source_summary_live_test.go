package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

// This opt-in benchmark checks caller-directed answers to three known source
// questions, not arbitrary prose fidelity. It uses the same explicit routing
// inputs as TestAgentLive and never chooses credentials or a fallback model.
func TestAgentSourceSummaryLive(t *testing.T) {
	if os.Getenv("BUCKLEY_AGENT_LIVE") != "1" {
		t.Skip("set BUCKLEY_AGENT_LIVE=1 and explicit live-agent inputs")
	}
	inputs := map[string]string{}
	for _, name := range []string{"BIN", "CONFIG", "MODEL", "OUTPUT_DIR"} {
		inputs[name] = os.Getenv("BUCKLEY_AGENT_LIVE_" + name)
		if strings.TrimSpace(inputs[name]) == "" || (name != "MODEL" && !filepath.IsAbs(inputs[name])) {
			t.Fatalf("missing or invalid BUCKLEY_AGENT_LIVE_%s", name)
		}
	}
	recordRoot, err := os.MkdirTemp(inputs["OUTPUT_DIR"], "source-summary-")
	if err != nil {
		t.Fatal(err)
	}
	write := func(t *testing.T, path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locating source profile")
	}
	template := filepath.Join(filepath.Dir(source), "..", "..", "templates", "agents", "source-extractor.yaml")
	referencePath := filepath.Join(filepath.Dir(source), "oneshot_source_requirements.go")
	manifest := map[string]string{"requested_model": inputs["MODEL"]}
	for name, path := range map[string]string{"binary_sha256": inputs["BIN"], "config_sha256": inputs["CONFIG"], "profile_sha256": template, "test_source_sha256": source, "reference_source_sha256": referencePath} {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil {
			t.Fatal(copyErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		manifest[name] = hex.EncodeToString(digest.Sum(nil))
	}
	rawManifest, _ := json.Marshal(manifest)
	write(t, filepath.Join(recordRoot, "inputs.json"), rawManifest)
	t.Logf("source-summary records: %s; requested model: %s", recordRoot, inputs["MODEL"])
	reference, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	positions := token.NewFileSet()
	parsed, err := parser.ParseFile(positions, referencePath, reference, 0)
	if err != nil {
		t.Fatal(err)
	}
	fixture := ""
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "applySourceTextRequirements" {
			continue
		}
		start, end := positions.Position(function.Pos()).Line, positions.Position(function.End()).Line
		fixture = "// Source excerpt for an orchestration caller.\n" + strings.Join(strings.SplitAfter(string(reference), "\n")[start-1:end], "")
	}
	if fixture == "" {
		t.Fatal("source-coverage reference function missing")
	}
	fixtureEnd := len(strings.Split(strings.TrimSuffix(fixture, "\n"), "\n"))
	if fixtureEnd > 100 {
		t.Fatal("source question reference exceeds one bounded read page")
	}
	for _, tc := range []struct {
		name, question, answer string
		end                    int
		status                 artifactv1.ArtifactStatus
	}{
		{"empty-requirements", "Assuming validation/helper calls succeed, does applySourceTextRequirements change the input artifact when required is empty?", "UNCHANGED", fixtureEnd, artifactv1.StatusCompleted},
		{"nonempty-requirements", "Assuming validation, normalization and helper calls succeed, does applySourceTextRequirements change the input artifact when required is nonempty and the input has no required_source_text table?", "CHANGED", fixtureEnd, artifactv1.StatusCompleted},
		{"unobserved-definition", "Assuming validation/helper calls succeed, does applySourceTextRequirements change the input artifact when required is empty?", "UNKNOWN", 1, artifactv1.StatusIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			record := filepath.Join(recordRoot, tc.name)
			if err := os.Mkdir(record, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "source.txt")
			write(t, path, []byte(fixture))
			prompt := "Read only source.txt lines 1-" + strconv.Itoa(tc.end) + " exactly once. Do not search or read outside this range. Answer using only the source observed in that range: " + tc.question +
				" Set artifact.summary to exactly one token: UNCHANGED if the returned artifact equals the input, CHANGED if it differs, or UNKNOWN if the observed source cannot establish the result. No name, filename, punctuation, or other prose in summary. Treat this summary format as the caller's handoff contract. Report incomplete with a specific reason if the definition is unobserved; otherwise this read and answer are the entire requested task. Use top-level source_refs:[\"all\"] to retain the observed page."
			caseInputs, _ := json.Marshal(map[string]any{"task": prompt, "expected_answer": tc.answer, "expected_status": tc.status, "start_line": 1, "end_line": tc.end})
			write(t, filepath.Join(record, "inputs.json"), caseInputs)
			write(t, filepath.Join(record, "source.txt"), []byte(fixture))
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, inputs["BIN"], "--config", inputs["CONFIG"], "agent", "run", "--subagent", "extract", "--model", inputs["MODEL"], "--task-intent", "read_only", "--max-tool-calls", "4", "--max-output-tokens", "1800", "--max-elapsed-seconds", "60", template, prompt)
			cmd.Dir = dir
			cmd.WaitDelay = 2 * time.Second
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			started := time.Now()
			runErr := cmd.Run()
			write(t, filepath.Join(record, "artifact.json"), stdout.Bytes())
			write(t, filepath.Join(record, "stderr.txt"), stderr.Bytes())
			var artifact artifactv1.Artifact
			if err := json.Unmarshal(stdout.Bytes(), &artifact); err != nil {
				t.Fatalf("decode live artifact: %v (CLI error: %v)", err, runErr)
			}
			if err := artifact.ValidateStrict(); err != nil {
				t.Fatalf("invalid live artifact: %v", err)
			}
			if artifact.Status != tc.status || artifact.Summary != tc.answer || (tc.status == artifactv1.StatusCompleted && runErr != nil) || (tc.status == artifactv1.StatusIncomplete && len(artifact.IncompleteReasons) == 0) {
				t.Fatalf("caller answer=%q status=%s, want %q/%s; CLI error=%v", artifact.Summary, artifact.Status, tc.answer, tc.status, runErr)
			}
			if len(artifact.EvidenceRefs) != 1 || len(artifact.Blocks) != 1 || artifact.Blocks[0].Table == nil || len(artifact.Blocks[0].Table.Rows) != 1 {
				t.Fatal("answer lost or added source captures")
			}
			row := artifact.Blocks[0].Table.Rows[0]
			want := strings.Join(strings.SplitAfter(fixture, "\n")[:tc.end], "")
			if len(row) != 5 || row[0] != artifact.EvidenceRefs[0].ID || artifact.EvidenceRefs[0].Kind != "captured_source" || row[1] != path || row[2] != "1" || row[3] != strconv.Itoa(tc.end) || row[4] != want {
				t.Fatalf("source scope/bytes changed: %#v", row)
			}
			current, err := os.ReadFile(path)
			if err != nil || string(current) != fixture {
				t.Fatal("read-only source question modified its fixture")
			}
			outcome, _ := json.Marshal(map[string]any{"status": artifact.Status, "answer": artifact.Summary, "expected_answer": tc.answer, "duration_ms": time.Since(started).Milliseconds(), "known_answer_verified": true, "captured_scope_verified": true})
			write(t, filepath.Join(record, "outcome.json"), outcome)
			t.Logf("answer=%s status=%s known_answer_verified=true captured_scope_verified=true", artifact.Summary, artifact.Status)
		})
	}
}

func TestSourceSummaryReferenceAnswers(t *testing.T) {
	input := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusCompleted, "Reference", "Reference input")
	result, err := applySourceTextRequirements(input, nil)
	if err != nil {
		t.Fatalf("applySourceTextRequirements(nil): %v", err)
	}
	if !reflect.DeepEqual(result, input) {
		t.Fatalf("no requirements changed artifact: %#v", result)
	}
	result, err = applySourceTextRequirements(input, []string{"not-observed"})
	if err != nil {
		t.Fatalf("applySourceTextRequirements(not-observed): %v", err)
	}
	if reflect.DeepEqual(result, input) {
		t.Fatal("missing requirement did not change artifact")
	}
	if result.Status != artifactv1.StatusIncomplete {
		t.Fatalf("status=%s, want incomplete", result.Status)
	}
}
