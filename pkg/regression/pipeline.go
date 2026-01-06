package regression

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/gitwatcher"
)

type Pipeline struct {
	cfg config.GitEventsConfig
}

func NewPipeline(cfg config.GitEventsConfig) *Pipeline {
	return &Pipeline{cfg: cfg}
}

func (p *Pipeline) HandleMerge(event gitwatcher.MergeEvent) {
	if strings.TrimSpace(p.cfg.RegressionCommand) == "" {
		return
	}
	env := map[string]string{
		"MERGE_REPO":   event.Repository,
		"MERGE_BRANCH": event.Branch,
		"MERGE_SHA":    event.SHA,
	}
	logf("Regression triggered for %s@%s (%s)", event.Repository, event.Branch, event.SHA)
	if err := p.runCommand("regression", p.cfg.RegressionCommand, env); err != nil {
		logf("Regression failed: %v", err)
		_ = p.runCommand("failure", p.cfg.FailureCommand, env)
		return
	}
	logf("Regression passed; rolling out release")
	_ = p.runCommand("release", p.cfg.ReleaseCommand, env)
}

func (p *Pipeline) runCommand(label, command string, env map[string]string) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), formatEnv(env)...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s command failed: %w", label, err)
	}
	return nil
}

func formatEnv(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, fmt.Sprintf("%s=%s", k, v))
	}
	return out
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stdout, "[regression] "+format+"\n", args...)
}
