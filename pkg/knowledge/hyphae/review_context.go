package hyphae

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/oneshot/commands"
)

const (
	defaultBinary    = "hypha"
	defaultMaxTokens = 600
	defaultTimeout   = 3 * time.Second
	maxQueryBytes    = 1_200
)

type commandRunner func(context.Context, string, ...string) ([]byte, error)

// ReviewContextProvider recalls bounded project decisions, constraints, and
// lessons from Hyphae. It is an optional infrastructure adapter: the review
// domain owns the provider port and does not depend on Hyphae.
type ReviewContextProvider struct {
	Binary    string
	MaxTokens int
	Timeout   time.Duration
	run       commandRunner
}

func NewReviewContextProvider() *ReviewContextProvider {
	return &ReviewContextProvider{
		Binary:    defaultBinary,
		MaxTokens: defaultMaxTokens,
		Timeout:   defaultTimeout,
		run:       runCommand,
	}
}

func (p *ReviewContextProvider) Name() string {
	return "hyphae"
}

// Required reports that Hyphae enriches review judgment but is not an
// authoritative merge gate. Its absence is disclosed without blocking review.
func (p *ReviewContextProvider) Required() bool {
	return false
}

func (p *ReviewContextProvider) Collect(
	parent context.Context,
	request commands.PRContextProviderRequest,
) ([]commands.PRContextEvidence, error) {
	if parent == nil {
		parent = context.Background()
	}
	binary := strings.TrimSpace(p.Binary)
	if binary == "" {
		binary = defaultBinary
	}
	maxTokens := p.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	run := p.run
	if run == nil {
		run = runCommand
	}

	query := buildReviewQuery(request)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	output, err := run(ctx, binary,
		"recall", query,
		"-shape", "summary+anchors",
		"-format", "text",
		"-max-tokens", strconv.Itoa(maxTokens),
	)
	if err != nil {
		return nil, fmt.Errorf("recalling project knowledge: %w", err)
	}
	body := strings.TrimSpace(string(output))
	if body == "" {
		return nil, nil
	}
	body = utf8Prefix(body, maxTokens*6)
	return []commands.PRContextEvidence{{
		Provider: "hyphae",
		Title:    "Prior project decisions and lessons",
		Body:     body,
		Priority: 80,
	}}, nil
}

func buildReviewQuery(request commands.PRContextProviderRequest) string {
	var sb strings.Builder
	sb.WriteString("Pull request review: recall prior architectural decisions, constraints, lessons, and known risks")
	if repository := strings.TrimSpace(request.PR.Repository); repository != "" {
		sb.WriteString(" for repository ")
		sb.WriteString(repository)
	}
	if title := strings.TrimSpace(request.PR.Title); title != "" {
		sb.WriteString("; title: ")
		sb.WriteString(utf8Prefix(title, 240))
	}
	if len(request.ChangedFiles) > 0 {
		sb.WriteString("; changed paths: ")
		limit := len(request.ChangedFiles)
		if limit > 20 {
			limit = 20
		}
		sb.WriteString(strings.Join(request.ChangedFiles[:limit], ", "))
	}
	return utf8Prefix(sb.String(), maxQueryBytes)
}

func runCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, binary, args...).Output()
}

func utf8Prefix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.ValidString(value[:maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}
