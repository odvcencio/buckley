package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type commandTransport struct {
	command *exec.Cmd
	limit   int
	grace   time.Duration
	reader  *messageLimitReader
	stream  *commandStream
}

func (t *commandTransport) Connect(ctx context.Context) (sdkmcp.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stdin, input, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer stdin.Close()
	output, stdout, err := os.Pipe()
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	defer stdout.Close()
	t.command.Stdin, t.command.Stdout = stdin, stdout
	configureMCPProcess(t.command)
	if err := t.command.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, err
	}
	grace := t.grace
	if grace <= 0 {
		grace = 5 * time.Second
	}
	s := &commandStream{command: t.command, input: input, output: output, done: make(chan struct{}), grace: grace}
	t.stream = s
	go func() {
		s.waitErr = s.command.Wait()
		reapMCPDescendants(s.command)
		close(s.done)
	}()
	t.reader = &messageLimitReader{source: output, limit: t.limit}
	s.reader = t.reader
	return (&sdkmcp.IOTransport{Reader: s, Writer: s}).Connect(ctx)
}

func (t *commandTransport) connectionError(err error) error {
	if t != nil && t.reader != nil && t.reader.exceeded.Load() && !errors.Is(err, ErrMessageTooLarge) {
		return errors.Join(t.reader.limitError(), err)
	}
	return err
}

func (t *commandTransport) close() {
	if t.stream != nil {
		_ = t.stream.Close()
	}
}

type commandStream struct {
	command       *exec.Cmd
	input, output *os.File
	reader        *messageLimitReader
	done          chan struct{}
	grace         time.Duration
	waitErr       error
	closeOnce     sync.Once
	closeErr      error
}

func (s *commandStream) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *commandStream) Write(p []byte) (int, error) { return s.input.Write(p) }

func (s *commandStream) Close() error {
	s.closeOnce.Do(func() {
		_ = s.input.Close()
		defer s.output.Close()
		if s.reader.exceeded.Load() {
			_ = s.output.Close()
		}
		wait := func() bool {
			timer := time.NewTimer(s.grace)
			defer timer.Stop()
			select {
			case <-s.done:
				s.closeErr = s.waitErr
				return true
			case <-timer.C:
				return false
			}
		}
		if wait() {
			return
		}
		if signalMCPProcess(s.command, false) == nil && wait() {
			return
		}
		_ = signalMCPProcess(s.command, true)
		if !wait() {
			s.closeErr = fmt.Errorf("mcp: server did not exit after forced shutdown")
		}
	})
	return s.closeErr
}
