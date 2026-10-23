package oneshot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/tool"
)

func TestAgentRunnerRun_ReviewInspectionCatalog(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		durable    bool
		iterations int
		wantTools  []string
	}{
		{"audited code mode", true, 2, []string{"exec_program"}},
		{"native fallback", false, 2, []string{"find_files", "read_file", "search_text"}},
		{"single turn", true, 1, []string{}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if scenario.durable && scenario.iterations > 1 && (execmode.DetectIsolation() != execmode.IsolationBwrap || !execmode.CompilerAvailable()) {
				t.Skip("requires bubblewrap and Go")
			}
			repo := newAgentRunnerSnapshotFixture(t)
			t.Chdir(repo)
			snapshot, err := model.CaptureReviewSnapshot(context.Background(), repo, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, "review.txt"), []byte("live workspace changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Tools []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tools"`
					Messages []struct {
						Role    string          `json:"role"`
						Content json.RawMessage `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					http.Error(w, "bad request", 400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) == 1 {
					names := make([]string, 0, len(request.Tools))
					for _, entry := range request.Tools {
						names = append(names, entry.Function.Name)
					}
					sort.Strings(names)
					if !reflect.DeepEqual(names, scenario.wantTools) {
						t.Errorf("catalog = %v, want %v", names, scenario.wantTools)
					}
					if scenario.iterations > 1 {
						name, args := "read_file", `{"path":"review.txt"}`
						if scenario.durable {
							name = "exec_program"
							encoded, _ := json.Marshal(map[string]string{"source": `package main
import ("fmt"; "execprogram/caps")
func main() { body, _, err := caps.ReadFile("review.txt"); if err != nil { panic(err) }; fmt.Print(body) }`})
							args = string(encoded)
							if !strings.Contains(string(request.Messages[0].Content), "standalone inspection tools are not advertised") {
								t.Error("prompt omitted active inspection interface")
							}
						}
						fmt.Fprintf(w, `{"id":"catalog-read","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"inspect","type":"function","function":{"name":%q,"arguments":%q}}]},"finish_reason":"tool_calls"}]}`, name, args)
						return
					}
				} else {
					found := false
					for _, message := range request.Messages {
						if message.Role == "tool" && strings.Contains(string(message.Content), "captured snapshot truth") {
							found = true
						}
					}
					if !found {
						t.Error("model did not receive captured snapshot content")
					}
				}
				fmt.Fprint(w, `{"id":"catalog-final","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"review complete"},"finish_reason":"stop"}]}`)
			}))
			t.Cleanup(server.Close)
			cfg := AgentRunnerConfig{Models: newAgentRunnerTestManager(t, server), Registry: tool.NewEmptyRegistry(), ModelID: "gpt-4o"}
			if scenario.durable {
				ev, err := evidence.New(filepath.Join(t.TempDir(), "review.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = ev.Close() })
				ledger, err := runledger.NewWithDB(ev.DB())
				if err != nil {
					t.Fatal(err)
				}
				cfg.ReviewRunLedger, cfg.ReviewEvidence = ledger, ev
			}
			result, err := NewAgentRunner(cfg).Run(context.Background(), "Read review.txt with read_file before reviewing.", "review", []string{"exec_program", "read_file", "find_files", "search_text"}, AgentExecutionOpts{ReviewSnapshot: snapshot, MaxIterations: scenario.iterations})
			if err != nil {
				t.Fatal(err)
			}
			if result.Incomplete || result.Response != "review complete" {
				t.Fatalf("result = %#v", result)
			}
			if got := calls.Load(); got != int32(scenario.iterations) {
				t.Fatalf("model calls = %d, want %d", got, scenario.iterations)
			}
		})
	}
}

func TestRegisterReviewCodeModeTool_PreservesVerification(t *testing.T) {
	if execmode.DetectIsolation() != execmode.IsolationBwrap || !execmode.CompilerAvailable() {
		t.Skip("requires bubblewrap and Go")
	}
	root := t.TempDir()
	ev, err := evidence.New(filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := newReviewSnapshotRegistry(root, []string{"exec_program", "read_file", "find_files", "search_text", "run_verification"}, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if _, err := registerReviewCodeModeTool(context.Background(), registry, root, ledger, ev, "review", "test-model", "test"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"exec_program", "run_verification"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("required tool %s missing", name)
		}
	}
	for _, name := range []string{"read_file", "find_files", "search_text"} {
		if _, ok := registry.Get(name); ok {
			t.Errorf("redundant tool %s remains", name)
		}
		if kind := registry.ToolKind(name); kind != "" {
			t.Errorf("removed tool %s retained kind %q", name, kind)
		}
	}
}
