package builtin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const containerStopGrace = 2 * time.Second

// Stdin is a per-invocation control channel. The user command has its own process
// group and no stdin, as with an ordinary noninteractive shell call.
const containerSupervisor = `set +e
set +u
if (( BASH_VERSINFO[0] < 4 )) || [[ ! -r /proc/self/stat ]]; then
 printf 'container supervision requires Bash 4 and readable /proc\n' >&2
 exit 125
fi
printf '\036%s:ready\037' "$2" >&2
IFS= read -r admission || { printf '\036%s:stopped\037\036%s:done\037' "$2" "$2" >&2; exit 125; }
[[ "$admission" = "$2" ]] || exit 125
set -m
bash -c "$1" </dev/null &
worker=$!
set +m
cancelled=0
while kill -0 "$worker" 2>/dev/null; do
 if IFS= read -r -t 0.1 control; then
  cancelled=1
  break
 else
  status=$?
  if [[ "$status" -eq 1 ]]; then cancelled=1; break; fi
 fi
done
if [[ "$cancelled" -eq 0 ]]; then
 printf '\036%s:finished\037' "$2" >&2
 if ! IFS= read -r decision || [[ "$decision" != "$2:finish" ]]; then cancelled=1; fi
fi
if [[ "$cancelled" -eq 1 ]]; then kill -KILL -- -"$worker" 2>/dev/null; fi
wait "$worker" 2>/dev/null
status=$?
if [[ "$cancelled" -eq 1 ]]; then
 group_status() {
  for stat in /proc/[0-9]*/stat; do
   if ! IFS= read -r record 2>/dev/null < "$stat"; then
    [[ -e "$stat" ]] && return 2
    continue
   fi
   record=${record##*) }
   read -r state parent group rest <<< "$record"
   [[ -n "$group" ]] || return 2
   if [[ "$group" = "$worker" && "$state" != Z && "$state" != X ]]; then return 0; fi
  done
  return 1
 }
 for (( attempt=0; attempt<20; attempt++ )); do
  group_status
  group_state=$?
  if [[ "$group_state" -eq 1 ]]; then printf '\036%s:stopped\037' "$2" >&2; break; fi
  [[ "$group_state" -eq 2 ]] && break
  sleep 0.05 || break
 done
fi
printf '\036%s:done\037' "$2" >&2
exit "$status"
`

type containerCommandState struct {
	attempted bool
	complete  bool
	accepted  bool
	stopped   bool
}

func (t *ShellCommandTool) runContainerCommand(ctx context.Context, source string, stdout, stderr io.Writer) (containerCommandState, error) {
	if err := ctx.Err(); err != nil {
		return containerCommandState{}, err
	}
	var nonceBytes [16]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return containerCommandState{}, fmt.Errorf("initialize container supervision: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes[:])
	reader, writer, err := os.Pipe()
	if err != nil {
		return containerCommandState{}, fmt.Errorf("initialize container control pipe: %w", err)
	}
	defer reader.Close()
	defer writer.Close()
	runCtx, forceCancel := context.WithCancel(context.WithoutCancel(ctx))
	defer forceCancel()
	args := []string{"compose", "-f", t.composeFile, "exec", "-T"}
	for _, pair := range envPairs(t.env) {
		args = append(args, "-e", pair)
	}
	service := t.containerService
	if service == "" {
		service = "dev"
	}
	args = append(args, service, "bash", "-c", containerSupervisor, "buckley-command", source, nonce)
	command := exec.CommandContext(runCtx, "docker", args...)
	command.Dir = strings.TrimSpace(t.workDir)
	command.Env = mergeEnv(command.Env, t.env)
	configureCommandCancellation(command)
	state := containerCommandState{}
	admitted := false
	frames := &containerFrames{target: stderr, frames: []containerFrame{
		{text: []byte("\x1e" + nonce + ":ready\x1f"), receive: func() {
			if !admitted && ctx.Err() == nil {
				admitted = true
				_, _ = io.WriteString(writer, nonce+"\n")
			}
		}},
		{text: []byte("\x1e" + nonce + ":finished\x1f"), receive: func() {
			if !state.accepted && ctx.Err() == nil {
				message := nonce + ":finish\n"
				n, err := io.WriteString(writer, message)
				state.accepted = err == nil && n == len(message)
			}
		}},
		{text: []byte("\x1e" + nonce + ":stopped\x1f"), receive: func() { state.stopped = true }},
		{text: []byte("\x1e" + nonce + ":done\x1f"), receive: func() { state.complete = true; _ = writer.Close() }},
	}}
	command.Stdin, command.Stdout, command.Stderr = reader, stdout, frames
	done := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-ctx.Done():
			_ = writer.Close()
			timer := time.NewTimer(containerStopGrace)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				forceCancel()
			}
		case <-done:
		}
	}()
	err = command.Run()
	close(done)
	<-watched
	frames.flush()
	state.attempted = command.Process != nil
	if !state.complete && err == nil {
		err = fmt.Errorf("container supervision did not acknowledge completion")
	}
	return state, err
}

type containerFrame struct {
	text    []byte
	receive func()
}

// Only partial private frames are retained; ordinary stderr streams directly.
type containerFrames struct {
	target  io.Writer
	frames  []containerFrame
	pending []byte
}

func (f *containerFrames) Write(p []byte) (int, error) {
	size := len(p)
	if len(f.pending) > 0 {
		p = append(f.pending, p...)
		f.pending = nil
	}
	for len(p) > 0 {
		index := bytes.IndexByte(p, '\x1e')
		if index < 0 {
			_, err := f.target.Write(p)
			return size, err
		}
		if index > 0 {
			if _, err := f.target.Write(p[:index]); err != nil {
				return 0, err
			}
			p = p[index:]
		}
		matched := false
		for _, frame := range f.frames {
			if bytes.HasPrefix(p, frame.text) {
				frame.receive()
				p = p[len(frame.text):]
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		for _, frame := range f.frames {
			if bytes.HasPrefix(frame.text, p) {
				f.pending = append(f.pending[:0], p...)
				return size, nil
			}
		}
		if _, err := f.target.Write(p[:1]); err != nil {
			return 0, err
		}
		p = p[1:]
	}
	return size, nil
}

func (f *containerFrames) flush() {
	if len(f.pending) > 0 {
		_, _ = f.target.Write(f.pending)
		f.pending = nil
	}
}
