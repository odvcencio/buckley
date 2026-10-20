//go:build linux

package builtin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func containerShellFixture(t *testing.T, delay string) *ShellCommandTool {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is required")
	}
	root := t.TempDir()
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
 if [ "$1" = dev-go ]; then
  shift
 ` + delay + `
  exec "$@"
 fi
 shift
done
exit 1
`
	if err := os.WriteFile(filepath.Join(root, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	tool := &ShellCommandTool{}
	tool.SetWorkDir(root)
	tool.ConfigureContainerMode(filepath.Join(root, "compose.yml"), "dev-go", root)
	return tool
}

func TestShellCommandTool_ContainerCancellationStopsDescendants(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			tool := containerShellFixture(t, "")
			marker := filepath.Join(tool.workDir, "late.txt")
			pidPath := filepath.Join(tool.workDir, "child.pid")
			command := fmt.Sprintf("printf '%%s' \"$$\" > '%s'; printf started; sleep 2; printf late > '%s'", pidPath, marker)
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			}
			t.Cleanup(cancel)
			ready := make(chan struct{}, 1)
			ctx = WithShellOutputSink(ctx, func(stream, text string) {
				if stream == "stdout" && strings.Contains(text, "started") {
					select {
					case ready <- struct{}{}:
					default:
					}
				}
			})
			done := make(chan *Result, 1)
			go func() { res, _ := tool.ExecuteWithContext(ctx, map[string]any{"command": command}); done <- res }()
			select {
			case <-ready:
			case <-time.After(3 * time.Second):
				t.Fatal("remote command did not start")
			}
			pid := cancellationProbePID(t, pidPath)
			t.Cleanup(func() {
				if cancellationProbeRunning(pid) {
					_ = exec.Command("kill", "-KILL", fmt.Sprint(pid)).Run()
				}
			})
			if !deadline {
				cancel()
			}
			var result *Result
			select {
			case result = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation did not complete")
			}
			flag := "canceled"
			if deadline {
				flag = "timed_out"
			}
			if result == nil || result.Success || result.Data[flag] != true || result.Data["remote_stopped"] != true || result.Data["outcome_unknown"] != true {
				t.Fatalf("unverified cancellation: %+v", result)
			}
			if strings.Contains(result.Error, "120") {
				t.Fatalf("reported configured timeout instead of parent deadline: %s", result.Error)
			}
			if cancellationProbeRunning(pid) {
				t.Fatalf("descendant still running: %d", pid)
			}
			time.Sleep(1200 * time.Millisecond)
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("command wrote after cancellation: %v", err)
			}
		})
	}
}

func TestShellCommandTool_ContainerCancelBeforeAdmission(t *testing.T) {
	tool := containerShellFixture(t, "sleep 0.4")
	marker := filepath.Join(tool.workDir, "never.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, err := tool.ExecuteWithContext(ctx, map[string]any{"command": "printf forbidden > '" + marker + "'"})
	if err != nil || result == nil || result.Success || result.Data["remote_stopped"] != true {
		t.Fatalf("late admission not stopped: %+v %v", result, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command ran after cancellation before admission: %v", err)
	}
}

func TestShellCommandTool_ContainerKeepsExitAndOutput(t *testing.T) {
	tool := containerShellFixture(t, "")
	result, err := tool.Execute(map[string]any{"command": "printf hello; printf diagnostic >&2; exit 7"})
	if err != nil || result == nil || result.Success || result.Data["exit_code"] != 7 || result.Data["stdout"] != "hello" || result.Data["stderr"] != "diagnostic" {
		t.Fatalf("lost command result: %+v %v", result, err)
	}
}

func TestShellCommandTool_ContainerMissingAcknowledgement(t *testing.T) {
	tool := containerShellFixture(t, "printf pretend-success; exit 0")
	result, err := tool.Execute(map[string]any{"command": "printf actual"})
	if err != nil || result == nil || result.Success || result.Data["outcome_unknown"] != true || result.Data["remote_stopped"] != false {
		t.Fatalf("invented confirmed outcome: %+v %v", result, err)
	}
}

func TestShellCommandTool_ContainerCancellationKeepsConcurrentCommand(t *testing.T) {
	tool := containerShellFixture(t, "")
	siblingMarker := filepath.Join(tool.workDir, "sibling.txt")
	siblingReady := make(chan struct{}, 1)
	siblingCtx := WithShellOutputSink(context.Background(), func(stream, text string) {
		if stream == "stdout" && strings.Contains(text, "sibling-start") {
			select {
			case siblingReady <- struct{}{}:
			default:
			}
		}
	})
	siblingDone := make(chan *Result, 1)
	go func() {
		res, _ := tool.ExecuteWithContext(siblingCtx, map[string]any{"command": "printf sibling-start; sleep 0.6; printf finished > '" + siblingMarker + "'"})
		siblingDone <- res
	}()
	select {
	case <-siblingReady:
	case <-time.After(3 * time.Second):
		t.Fatal("sibling did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithShellOutputSink(ctx, func(stream, text string) {
		if stream == "stdout" && strings.Contains(text, "own-start") {
			cancel()
		}
	})
	res, err := tool.ExecuteWithContext(ctx, map[string]any{"command": "printf own-start; sleep 2"})
	if err != nil || res == nil || res.Success || res.Data["remote_stopped"] != true {
		t.Fatalf("own command did not stop: %+v %v", res, err)
	}
	select {
	case sibling := <-siblingDone:
		if sibling == nil || !sibling.Success {
			t.Fatalf("cancellation killed another invocation: %+v", sibling)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("sibling remained blocked")
	}
	if data, err := os.ReadFile(siblingMarker); err != nil || string(data) != "finished" {
		t.Fatalf("sibling lost work: %q %v", data, err)
	}
}

func TestShellCommandTool_ContainerAlreadyCancelledNeverStarts(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	tool := containerShellFixture(t, "printf invoked > '"+called+"'")
	marker := filepath.Join(tool.workDir, "never.txt")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := tool.ExecuteWithContext(ctx, map[string]any{"command": "printf forbidden > '" + marker + "'"})
	if err != nil || result == nil || result.Success || result.Data["canceled"] != true {
		t.Fatalf("lost cancellation: %+v %v", result, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("pre-cancelled command ran: %v", err)
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatalf("pre-cancelled invocation started a Docker client: %v", err)
	}
}

func TestShellCommandTool_ContainerCannotConfirmStop(t *testing.T) {
	tool := containerShellFixture(t, "sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := tool.ExecuteWithContext(ctx, map[string]any{"command": "printf forbidden"})
	if err != nil || result == nil || result.Success || result.Data["remote_stopped"] != false || result.Data["outcome_unknown"] != true {
		t.Fatalf("invented termination confirmation: %+v %v", result, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("client cleanup was not bounded")
	}
}

func TestShellCommandTool_ContainerCompletionCancellationRace(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint(late), func(t *testing.T) {
			hook := "printf cancel-hook >&2\n"
			before, after := hook, ""
			if late {
				before, after = "", hook
			}
			peer := `nonce=$6
printf '\036%s:ready\037' "$nonce" >&2
IFS= read -r admission || exit 125
` + before + `printf '\036%s:finished\037' "$nonce" >&2
if ! IFS= read -r finish; then
 printf '\036%s:stopped\037\036%s:done\037' "$nonce" "$nonce" >&2
 exit 125
fi
` + after + `sleep 0.1
printf '\036%s:done\037' "$nonce" >&2
exit 0
`
			tool := containerShellFixture(t, peer)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = WithShellOutputSink(ctx, func(stream, text string) {
				if stream == "stderr" && strings.Contains(text, "cancel-hook") {
					cancel()
				}
			})
			result, err := tool.ExecuteWithContext(ctx, map[string]any{"command": "true"})
			if err != nil || result == nil || result.Success != late {
				t.Fatalf("incorrect completion race outcome late=%t: %+v %v", late, result, err)
			}
			if !late && (result.Data["canceled"] != true || result.Data["remote_stopped"] != true) {
				t.Fatalf("lost pre-completion cancellation: %+v", result)
			}
			if late && result.Data["canceled"] == true {
				t.Fatalf("completion was accepted before cancellation: %+v", result)
			}
		})
	}
}
