package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestRunExecProgram_RejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{nil, {"--file", "x", "--reuse", "ev_x"}, {"--reuse", "ev_x", "extra"}, {"--reuse", "ev_x", "--format", "invalid"}, {"--reuse", "ev_x", "--timeout", "0"}, {"--reuse", "ev_x", "--timeout", "3m"}} {
		if err := runExecProgram(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, body := range []string{"", strings.Repeat("x", execmode.MaxSourceBytes+1)} {
		file := filepath.Join(t.TempDir(), "program.go")
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := runExecProgram(context.Background(), []string{"--file", file}, io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted source length %d", len(body))
		}
	}
}

func TestRunExecProgram_ReuseAfterCloseReadsFreshFiles(t *testing.T) {
	if execmode.DetectIsolation() != execmode.IsolationBwrap {
		t.Skip("bubblewrap unavailable")
	}
	t.Setenv("BUCKLEY_DATA_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	oldConfig := configPath
	configPath = ""
	t.Cleanup(func() { configPath = oldConfig })
	workspace := t.TempDir()
	input := filepath.Join(workspace, "input.txt")
	if err := os.WriteFile(input, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "program.go")
	source := `package main
import("fmt";"execprogram/caps")
func main(){text,truncated,err:=caps.ReadFile("input.txt");if err!=nil||truncated{panic("read failed")};fmt.Print(text)}`
	if err := os.WriteFile(file, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	var first, second bytes.Buffer
	if err := runExecProgram(context.Background(), []string{"--file", file, "--workdir", workspace, "--format", "json"}, &first, io.Discard); err != nil {
		t.Fatal(err)
	}
	var a, b struct {
		Success bool
		RunID   string `json:"run_id"`
		Data    map[string]any
	}
	if err := json.Unmarshal(first.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if !a.Success || a.Data["stdout"] != "first" {
		t.Fatalf("first=%s", first.Bytes())
	}
	if err := os.WriteFile(input, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runExecProgram(context.Background(), []string{"--reuse", a.Data["program_evidence"].(string), "--workdir", workspace, "--format", "json"}, &second, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if !b.Success || b.Data["stdout"] != "second" || a.RunID == b.RunID || a.Data["program_evidence"] != b.Data["program_evidence"] || a.Data["output_evidence"] == b.Data["output_evidence"] {
		t.Fatalf("first=%s second=%s", first.Bytes(), second.Bytes())
	}
	var plain bytes.Buffer
	if err := runExecProgram(context.Background(), []string{"--reuse", a.Data["program_evidence"].(string), "--workdir", workspace}, &plain, io.Discard); err != nil {
		t.Fatal(err)
	}
	if plain.String() != "second" {
		t.Fatalf("text output=%q", plain.String())
	}
	stores, cleanup, err := openGoalStores()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, id := range []string{a.RunID, b.RunID} {
		run, err := stores.ledger.GetRun(context.Background(), id)
		if err != nil || run.Status != "completed" {
			t.Fatalf("run=%+v err=%v", run, err)
		}
		events, err := stores.ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: id})
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		for _, event := range events {
			if event.Type == "capability.call" {
				calls++
				if event.Payload["method"] != "files.read" || event.Payload["outcome"] != "ok" {
					t.Fatalf("event=%+v", event)
				}
			}
		}
		if calls != 1 {
			t.Fatalf("calls=%d", calls)
		}
	}
}

func TestRunExecProgram_FailureAndTimeoutAreDurable(t *testing.T) {
	if execmode.DetectIsolation() != execmode.IsolationBwrap {
		t.Skip("bubblewrap unavailable")
	}
	t.Setenv("BUCKLEY_DATA_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	oldConfig := configPath
	configPath = ""
	t.Cleanup(func() { configPath = oldConfig })
	for _, tc := range []struct {
		name, source string
		timeout      time.Duration
	}{
		{"compile", "package main\nfunc main(){missing()}\n", time.Minute},
		{"deadline", "package main\nimport \"time\"\nfunc main(){time.Sleep(time.Minute)}\n", 100 * time.Millisecond},
		{"escape", `package main
import "execprogram/caps"
func main(){_,_,err:=caps.ReadFile("../outside");if err!=nil{panic(err)}}`, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "program.go")
			if err := os.WriteFile(file, []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err := runExecProgram(context.Background(), []string{"--file", file, "--workdir", t.TempDir(), "--format", "json", "--timeout", tc.timeout.String()}, &output, io.Discard)
			if err == nil {
				t.Fatal("expected failure")
			}
			var result struct {
				Success bool
				RunID   string `json:"run_id"`
			}
			if err := json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Success || result.RunID == "" {
				t.Fatalf("output=%s", output.Bytes())
			}
			stores, cleanup, err := openGoalStores()
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			run, err := stores.ledger.GetRun(context.Background(), result.RunID)
			if err != nil || run.Status != "failed" {
				t.Fatalf("run=%+v err=%v", run, err)
			}
		})
	}
}
