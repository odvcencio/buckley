package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

// Opt in with BUCKLEY_AGENT_LIVE=1 and explicit BIN, CONFIG, MODEL, OUTPUT_DIR
// variables under the same prefix. The caller chooses credentials and routing.
// Captures, fixture outcomes and test execution are checked; prose is not graded.
func TestAgentLive(t *testing.T) {
	if os.Getenv("BUCKLEY_AGENT_LIVE") != "1" {
		t.Skip("set BUCKLEY_AGENT_LIVE=1 and explicit live-agent inputs")
	}
	inputs := map[string]string{}
	for _, name := range []string{"BIN", "CONFIG", "MODEL", "OUTPUT_DIR"} {
		inputs[name] = os.Getenv("BUCKLEY_AGENT_LIVE_" + name)
		if strings.TrimSpace(inputs[name]) == "" {
			t.Fatalf("missing BUCKLEY_AGENT_LIVE_%s", name)
		}
		if name != "MODEL" && !filepath.IsAbs(inputs[name]) {
			t.Fatalf("BUCKLEY_AGENT_LIVE_%s must be absolute", name)
		}
	}
	for _, name := range []string{"BIN", "CONFIG"} {
		info, err := os.Stat(inputs[name])
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("BUCKLEY_AGENT_LIVE_%s must name a regular file", name)
		}
	}
	recordRoot, err := os.MkdirTemp(inputs["OUTPUT_DIR"], "run-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("live records: %s; requested model: %s", recordRoot, inputs["MODEL"])
	write := func(t *testing.T, path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint := func(path string) string {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		digest := sha256.New()
		if _, err := io.Copy(digest, file); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(digest.Sum(nil))
	}
	manifest, _ := json.Marshal(map[string]string{"requested_model": inputs["MODEL"], "binary_sha256": fingerprint(inputs["BIN"]), "config_sha256": fingerprint(inputs["CONFIG"])})
	write(t, filepath.Join(recordRoot, "inputs.json"), manifest)
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locating templates")
	}
	templates := filepath.Join(filepath.Dir(source), "..", "..", "templates", "agents")
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "BUCKLEY_AGENT_LIVE_WITNESS=") {
			env = append(env, entry)
		}
	}
	cases := []struct {
		name   string
		status artifactv1.ArtifactStatus
	}{
		{"source", artifactv1.StatusCompleted},
		{"source-missing", artifactv1.StatusIncomplete},
		{"edit", artifactv1.StatusCompleted},
		{"execution-pass", artifactv1.StatusCompleted},
		{"execution-fail", artifactv1.StatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			record := filepath.Join(recordRoot, tc.name)
			if err := os.Mkdir(record, 0700); err != nil {
				t.Fatal(err)
			}
			key := agentLiveBuggyKey
			if tc.name == "execution-pass" {
				key = strings.Replace(key, "strings.ToLower(key)", "strings.ToLower(strings.TrimSpace(key))", 1)
			}
			baseline := map[string]string{"go.mod": "module example.com/agentlive\n\ngo 1.22\n", "key.go": key, "key_test.go": agentLiveKeyTest}
			for name, content := range baseline {
				write(t, filepath.Join(dir, name), []byte(content))
			}
			// A fresh local Git baseline lets the CLI observe the intended mutation.
			for _, args := range [][]string{{"init", "-q"}, {"add", "go.mod", "key.go", "key_test.go"}, {"-c", "user.name=Agent Live", "-c", "user.email=agent-live@buckley.local", "commit", "-qm", "fixture"}} {
				if _, err := agentLiveCommand(dir, env, "git", args...); err != nil {
					t.Fatalf("fixture git setup: %v", err)
				}
			}
			hostTests := func(wantPass bool) {
				output, err := agentLiveCommand(dir, env, "go", "test", "-v", "-count=1", "-run", "^TestNormalizeKey$", "./...")
				if wantPass {
					if err != nil || !bytes.Contains(output, []byte("--- PASS: TestNormalizeKey")) {
						t.Fatalf("host verification expected passing test: %v\n%s", err, output)
					}
				} else {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 1 || !bytes.Contains(output, []byte("--- FAIL: TestNormalizeKey")) {
						t.Fatalf("host verification expected test assertion failure: %v\n%s", err, output)
					}
				}
			}
			isSource := strings.HasPrefix(tc.name, "source")
			if !isSource {
				hostTests(tc.name == "execution-pass")
			}
			args := []string{"--config", inputs["CONFIG"], "agent", "run", "--model", inputs["MODEL"], "--max-output-tokens", "3500", "--max-elapsed-seconds", "90"}
			prompt := ""
			switch tc.name {
			case "source", "source-missing":
				args = append(args, "--subagent", "extract", "--task-intent", "read_only", "--max-tool-calls", "2", "--require-source-text", "func NormalizeKey(")
				prompt = "Read only key.go and summarize NormalizeKey for an orchestration caller. This one file is the complete requested scope; no tests or other files are needed. Use top-level source_refs:[\"all\"] to preserve the observed page."
				if tc.name == "source-missing" {
					args = append(args, "--require-source-text", "func MissingNormalizeKey(")
					prompt += " Also report whether MissingNormalizeKey appears in the observed file; report missing source honestly."
				}
				args = append(args, filepath.Join(templates, "source-extractor.yaml"))
			case "edit":
				args = append(args, "--subagent", "edit", "--task-intent", "mutation", "--max-tool-calls", "8", filepath.Join(templates, "scoped-editor.yaml"))
				prompt = "Fix NormalizeKey in key.go to trim surrounding whitespace and lowercase the result. Change key.go only; preserve go.mod and key_test.go exactly. Run Go TestNormalizeKey with run_tests and report the observed verification. Those edits and that test are the complete scope. Use top-level source_refs:[]; include kind,status,title,summary in the final artifact."
			default:
				args = append(args, "--subagent", "edit", "--task-intent", "read_only", "--tool-tier", "read_only", "--max-tool-calls", "3", filepath.Join(templates, "scoped-editor.yaml"))
				prompt = "Run Go TestNormalizeKey with run_tests exactly once and report the observed outcome. No edits or source gathering are requested. Use completed for passing verification and failed for failing verification. Use top-level source_refs:[] and include kind,status,title,summary."
			}
			args = append(args, prompt)
			witness := filepath.Join(record, "test.witness")
			ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, inputs["BIN"], args...)
			cmd.Dir = dir
			cmd.Env = append(append([]string(nil), env...), "BUCKLEY_AGENT_LIVE_WITNESS="+witness)
			cmd.WaitDelay = 2 * time.Second
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			start := time.Now()
			runErr := cmd.Run()
			duration := time.Since(start)
			exitCode := 0
			if runErr != nil {
				var exit *exec.ExitError
				exitCode = -1
				if errors.As(runErr, &exit) {
					exitCode = exit.ExitCode()
				}
			}
			write(t, filepath.Join(record, "artifact.json"), stdout.Bytes())
			write(t, filepath.Join(record, "stderr.txt"), stderr.Bytes())
			var artifact artifactv1.Artifact
			verified := false
			t.Cleanup(func() {
				report, _ := json.Marshal(map[string]any{"case": tc.name, "artifact_status": artifact.Status, "cli_exit": exitCode, "duration_ms": duration.Milliseconds(), "host_verified": verified})
				write(t, filepath.Join(record, "outcome.json"), report)
			})
			if ctx.Err() != nil || exitCode < 0 || exitCode > 1 {
				t.Fatalf("agent process did not finish normally; see %s", record)
			}
			if stdout.Len() > artifactv1.MaxProviderBytes {
				t.Fatal("artifact exceeds byte limit")
			}
			decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&artifact); err != nil {
				t.Fatalf("invalid artifact JSON: %v; see %s", err, record)
			}
			var trailing any
			if decoder.Decode(&trailing) != io.EOF {
				t.Fatal("artifact has trailing output")
			}
			if err := artifact.ValidateStrict(); err != nil {
				t.Fatalf("invalid artifact: %v", err)
			}
			if artifact.Status != tc.status {
				t.Fatalf("artifact status=%s, want=%s (CLI exit=%d); see %s", artifact.Status, tc.status, exitCode, record)
			}
			for name, content := range baseline {
				after, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				if tc.name == "edit" && name == "key.go" {
					if string(after) == content {
						t.Fatal("requested edit did not occur")
					}
					continue
				}
				if string(after) != content {
					t.Fatalf("unexpected change to %s", name)
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != ".git" {
					if _, ok := baseline[entry.Name()]; !ok {
						t.Fatalf("unexpected fixture entry %s", entry.Name())
					}
				}
			}
			if isSource {
				refs := map[string]bool{}
				for _, ref := range artifact.EvidenceRefs {
					if ref.Kind == "captured_source" {
						refs[ref.ID] = true
					}
				}
				lines := strings.SplitAfter(key, "\n")
				if lines[len(lines)-1] == "" {
					lines = lines[:len(lines)-1]
				}
				found := false
				missingChecked := false
				for _, block := range artifact.Blocks {
					if block.Table == nil {
						continue
					}
					if slices.Equal(block.Table.Headers, []string{"required_source_text", "evidence_status", "source_ref", "start_line", "end_line"}) {
						for _, row := range block.Table.Rows {
							if row[0] == "func MissingNormalizeKey(" && row[1] == "not_observed" {
								missingChecked = true
							}
						}
					}
					if !slices.Equal(block.Table.Headers, []string{"source_ref", "path", "start_line", "end_line", "content"}) {
						continue
					}
					for _, row := range block.Table.Rows {
						first, firstErr := strconv.Atoi(row[2])
						last, lastErr := strconv.Atoi(row[3])
						if !refs[row[0]] || row[1] != filepath.Join(dir, "key.go") || firstErr != nil || lastErr != nil || first < 1 || last < first || last > len(lines) || last-first >= 100 {
							t.Fatal("capture identity or range mismatch")
						}
						if row[4] != strings.Join(lines[first-1:last], "") {
							t.Fatal("captured bytes differ from baseline")
						}
						if strings.Contains(row[4], "func NormalizeKey(") {
							found = true
						}
					}
				}
				if !found {
					t.Fatal("required declaration absent from captured source")
				}
				if tc.name == "source-missing" && (!missingChecked || len(artifact.IncompleteReasons) == 0) {
					t.Fatal("missing declaration not reported")
				}
			} else {
				marker, err := os.ReadFile(witness)
				if err != nil || string(marker) != "TestNormalizeKey ran\n" {
					t.Fatal("Buckley test execution was not observed before host verification")
				}
				hostTests(tc.name != "execution-fail")
			}
			verified = true
			t.Logf("status=%s cli_exit=%d duration=%s host_verified=true", artifact.Status, exitCode, duration.Round(time.Millisecond))
		})
	}
}

func agentLiveCommand(dir string, env []string, program string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = 2 * time.Second
	return cmd.CombinedOutput()
}

const agentLiveBuggyKey = `package agentlive

import "strings"

const KeyLimit = 64

func NormalizeKey(key string) string {
 return strings.ToLower(key)
}
`

const agentLiveKeyTest = `package agentlive

import (
 "os"
 "testing"
)

func TestNormalizeKey(t *testing.T) {
 if witness:=os.Getenv("BUCKLEY_AGENT_LIVE_WITNESS");witness!=""{
  if err:=os.WriteFile(witness,[]byte("TestNormalizeKey ran\n"),0600);err!=nil{t.Fatal(err)}
 }
 for _,tc:=range []struct{input,want string}{{"  MiXeD  ","mixed"},{" \t ",""},{"Keep","keep"}}{
  if got:=NormalizeKey(tc.input);got!=tc.want{t.Fatalf("NormalizeKey(%q)=%q, want %q",tc.input,got,tc.want)}
 }
}
`
