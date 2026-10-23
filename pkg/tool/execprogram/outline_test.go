package execprogram

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestProgramOutline_EvidenceAndReuse(t *testing.T) {
	if execmode.DetectIsolation() != execmode.IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
	ctx := context.Background()
	root, bin := t.TempDir(), t.TempDir()
	prefix := "package sample\n" + strings.Repeat("// padding before the selected symbol\n", 10000)
	line := strings.Count(prefix, "\n") + 1
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte(prefix+"func Value() int { return 42 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stub := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' '{"format_version":1,"symbols":[{"name":"Value","start_line":%d,"end_line":%d}],"coverage":{"query_truncated":true},"next_cursor":"opaque"}'
`, line, line)
	if err := os.WriteFile(filepath.Join(bin, "canopy"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CANOPY_BIN", "")
	ev, err := evidence.New(filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatal(err)
	}
	run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "outline", Backend: "code-mode"})
	if err != nil {
		t.Fatal(err)
	}
	program, err := NewProgramTool(root, ledger, ev, run.RunID, "outline", nil)
	if err != nil {
		t.Fatal(err)
	}
	source := `package main
import("encoding/json";"fmt";"execprogram/caps")
func main(){
 raw,err:=caps.OutlineFile("source.go",1,"");if err!=nil{panic(err)}
 var outline struct{Symbols []struct{Start int ` + "`json:\"start_line\"`" + `;End int ` + "`json:\"end_line\"`" + `};Coverage map[string]any;NextCursor string ` + "`json:\"next_cursor\"`" + `}
 if err:=json.Unmarshal(raw,&outline);err!=nil{panic(err)}
 if len(outline.Symbols)!=1||outline.Coverage["query_truncated"]!=true||outline.NextCursor!="opaque"{panic("lost warnings or cursor")}
 s:=outline.Symbols[0];data,next,err:=caps.ReadFileLines("source.go",s.Start,s.End);if err!=nil{panic(err)}
 if next!=0{panic("symbol source needs another page")};fmt.Print(string(data))
}`
	params := map[string]any{"source": source}
	for i := 0; i < 2; i++ {
		result, err := program.ExecuteWithContext(ctx, params)
		if err != nil || result == nil || !result.Success {
			t.Fatalf("execution: result=%+v err=%v", result, err)
		}
		if result.Data["stdout"] != fmt.Sprintf("func Value() int { return %d }\n", 42+i) {
			t.Fatalf("stdout=%q", result.Data["stdout"])
		}
		output, err := ev.Get(ctx, result.Data["output_evidence"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(output.InlineBody), "func Value()") {
			t.Fatal("missing durable output")
		}
		params = map[string]any{"reuse": result.Data["program_evidence"]}
		if i == 0 {
			if err := os.WriteFile(filepath.Join(root, "source.go"), []byte(prefix+"func Value() int { return 43 }\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: run.RunID})
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	for _, event := range events {
		if event.Type == "capability.call" {
			if event.Payload["outcome"] != "ok" {
				t.Fatalf("capability failed: %+v", event)
			}
			methods = append(methods, event.Payload["method"].(string))
		}
	}
	if strings.Join(methods, ",") != "code.outline,files.read,code.outline,files.read" {
		t.Fatalf("audit methods=%v", methods)
	}
}
