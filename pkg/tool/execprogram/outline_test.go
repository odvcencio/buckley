package execprogram

import (
	"context"
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
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("package sample\nfunc Value() int { return 42 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stub := `#!/bin/sh
printf '%s\n' '{"format_version":1,"symbols":[{"name":"Value","start_line":2,"end_line":2}],"coverage":{"query_truncated":true},"next_cursor":"opaque"}'
`
	if err := os.WriteFile(filepath.Join(bin, "canopy"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
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
import("encoding/json";"fmt";"strings";"execprogram/caps")
func main(){
 raw,err:=caps.OutlineFile("source.go",1,"");if err!=nil{panic(err)}
 var outline struct{Symbols []struct{Start int ` + "`json:\"start_line\"`" + `;End int ` + "`json:\"end_line\"`" + `};Coverage map[string]any;NextCursor string ` + "`json:\"next_cursor\"`" + `}
 if err:=json.Unmarshal(raw,&outline);err!=nil{panic(err)}
 if len(outline.Symbols)!=1||outline.Coverage["query_truncated"]!=true||outline.NextCursor!="opaque"{panic("lost warnings or cursor")}
 text,truncated,err:=caps.ReadFile("source.go");if err!=nil||truncated{panic("source read failed")}
 s:=outline.Symbols[0];fmt.Println(strings.Join(strings.Split(text,"\n")[s.Start-1:s.End],"\n"))
}`
	params := map[string]any{"source": source}
	for i := 0; i < 2; i++ {
		result, err := program.ExecuteWithContext(ctx, params)
		if err != nil || result == nil || !result.Success {
			t.Fatalf("execution: result=%+v err=%v", result, err)
		}
		if result.Data["stdout"] != "func Value() int { return 42 }\n" {
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
