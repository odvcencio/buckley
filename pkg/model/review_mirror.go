package model

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// reviewMirrorIdentity carries no credentials, remote URL, or mutable ref.
func reviewMirrorIdentity(ctx context.Context, root string, gitEnv []string) string {
	if value, err := reviewGitCommand(ctx, gitEnv, "-C", root, "config", "--get", "buckley.reviewRepository").Output(); err == nil && strings.TrimSpace(string(value)) != "" {
		return strings.TrimSpace(string(value))
	}
	remote, _ := reviewGitCommand(ctx, gitEnv, "-C", root, "config", "--get", "remote.origin.url").Output()
	value := strings.TrimSpace(string(remote))
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		value = parsed.Host + parsed.Path
	} else if at := strings.LastIndex(value, "@"); at >= 0 {
		value = value[at+1:]
	}
	value = strings.TrimSuffix(value, ".git")
	value = strings.ReplaceAll(value, ":", "/")
	if value != "" {
		return value
	}
	common, err := reviewGitCommand(ctx, gitEnv, "-C", root, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	directory := strings.TrimSpace(string(common))
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(root, directory)
	}
	sum := sha256.Sum256([]byte(filepath.Clean(directory)))
	return fmt.Sprintf("local/%x", sum[:16])
}
