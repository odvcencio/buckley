package provision

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

const (
	trustedDockerBinary = "/usr/bin/docker"
	maxCommandOutput    = 4 << 20
)

type Runner interface {
	Run(context.Context, ...string) (string, error)
}

type ExecRunner struct {
	DockerConfig string
}

func (r ExecRunner) Run(ctx context.Context, args ...string) (string, error) {
	if err := validateTrustedExecutable(trustedDockerBinary); err != nil {
		return "", errors.New("trusted docker client is unavailable")
	}
	dockerConfig, err := canonicalDirectory(r.DockerConfig)
	if err != nil {
		return "", errors.New("isolated Docker client configuration is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, trustedDockerBinary, args...)
	cmd.Env = []string{
		"DOCKER_CONFIG=" + dockerConfig,
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"HOME=/nonexistent",
		"LC_ALL=C",
		"PATH=/usr/bin:/bin",
	}
	var stdout bytes.Buffer
	cmd.Stdout = &limitedBuffer{buffer: &stdout, limit: maxCommandOutput}
	cmd.Stderr = &limitedBuffer{limit: maxCommandOutput}
	if err := cmd.Run(); err != nil {
		return "", errors.New("docker operation failed")
	}
	return stdout.String(), nil
}
