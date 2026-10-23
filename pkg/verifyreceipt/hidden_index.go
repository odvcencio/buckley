package verifyreceipt

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// hiddenTrackedChanges checks ignored index entries in a private index copy.
// Git status alone omits assume-unchanged and skip-worktree modifications.
func hiddenTrackedChanges(ctx context.Context, root string) (bool, error) {
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(scanCtx, "git", "-C", root, "-c", "core.fsmonitor=false", "ls-files", "-v", "--stage", "-z")
	var stderr limitedBuffer
	stderr.max = 4096
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, err
	}
	defer func() {
		if cmd.ProcessState == nil {
			cancel()
			_ = cmd.Wait()
		}
	}()
	var scratch string
	defer func() {
		if scratch != "" {
			_ = os.RemoveAll(scratch)
		}
	}()
	var paths *os.File
	reader := bufio.NewReader(stdout)
	firstChunk, hidden := true, false
	for {
		entry, readErr := reader.ReadSlice(0)
		if firstChunk {
			hidden = len(entry) > 2 && (entry[0] == 'S' || entry[0] >= 'a' && entry[0] <= 'z')
			if len(entry) >= 2 {
				entry = entry[2:]
			}
		}
		if hidden {
			if paths == nil {
				scratch, err = os.MkdirTemp("", "buckley-verify-index-*")
				if err != nil {
					return false, err
				}
				paths, err = os.OpenFile(filepath.Join(scratch, "paths"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
				if err != nil {
					return false, err
				}
				defer paths.Close()
			}
			if _, err := paths.Write(entry); err != nil {
				return false, err
			}
		}
		firstChunk = !errors.Is(readErr, bufio.ErrBufferFull)
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return false, readErr
		}
	}
	if err := cmd.Wait(); err != nil {
		return false, fmt.Errorf("verify: inspect index flags: %w: %s", err, strings.TrimSpace(string(stderr.Bytes())))
	}
	if paths == nil {
		return false, nil
	}
	index, err := git(ctx, root, "rev-parse", "--git-path", "index")
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(index) {
		index = filepath.Join(root, index)
	}
	source, err := os.Open(index)
	if err != nil {
		return false, err
	}
	defer source.Close()
	privateIndex := filepath.Join(scratch, "index")
	copy, err := os.OpenFile(privateIndex, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	_, copyErr := io.Copy(copy, source)
	closeErr := copy.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return false, err
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_INDEX_FILE=") {
			env = append(env, value)
		}
	}
	env = append(env, "GIT_INDEX_FILE="+privateIndex)
	run := func(stdin io.Reader, args ...string) error {
		command := exec.CommandContext(ctx, "git", "-C", root, "-c", "core.fsmonitor=false", "-c", "core.splitIndex=false")
		command.Args = append(command.Args, args...)
		command.Env, command.Stdin = env, stdin
		var output limitedBuffer
		output.max = 4096
		command.Stdout, command.Stderr = &output, &output
		if err := command.Run(); err != nil {
			return fmt.Errorf("verify: inspect private index: %w: %s", err, strings.TrimSpace(string(output.Bytes())))
		}
		return nil
	}
	if err := run(nil, "update-index", "--no-split-index"); err != nil {
		return false, err
	}
	if _, err := paths.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if err := run(paths, "update-index", "-z", "--index-info"); err != nil {
		return false, err
	}
	err = run(nil, "update-index", "--really-refresh")
	if err == nil {
		return false, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}
