package session

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// DetermineSessionID determines the session ID for the current working directory
func DetermineSessionID(cwd string) string {
	if info := getGitMetadata(cwd); info.valid {
		branch := info.branch
		if branch == "" {
			branch = "unknown"
		}
		return fmt.Sprintf("%s-%s", info.repoName, branch)
	}

	// Non-git: directory name + path hash
	dirName := filepath.Base(cwd)
	pathHash := shortHash(cwd)
	return fmt.Sprintf("%s-%s", dirName, pathHash)
}

// detectGitRepo detects if the current directory is a git repository
// Returns the repository name if found, empty string otherwise
func detectGitRepo(cwd string) string {
	return getGitMetadata(cwd).repoName
}

// getCurrentBranch returns the current git branch name
func getCurrentBranch(cwd string) string {
	branch := getGitMetadata(cwd).branch
	if branch == "" {
		return "unknown"
	}
	return branch
}

// shortHash generates a short hash of a string
func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:4])
}

// GetProjectPath returns the project path (git root or cwd)
func GetProjectPath(cwd string) string {
	if info := getGitMetadata(cwd); info.valid && info.rootPath != "" {
		return info.rootPath
	}
	return cwd
}

// GetGitInfo returns git repository and branch information
func GetGitInfo(cwd string) (repo string, branch string) {
	repo = detectGitRepo(cwd)
	if repo != "" {
		branch = getCurrentBranch(cwd)
	}
	return
}

// DefaultSessionID returns the default session ID for the current working directory
func DefaultSessionID() string {
	cwd, err := os.Getwd()
	if err != nil {
		// Fallback to a random-looking ID
		return fmt.Sprintf("default-%s", shortHash(fmt.Sprintf("%d", os.Getpid())))
	}
	return DetermineSessionID(cwd)
}

var sessionNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9\-]`)
var ulidEntropy = ulid.Monotonic(cryptorand.Reader, 0)

// GenerateSessionID returns a unique session ID using the provided base name
func GenerateSessionID(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "session"
	}
	base = strings.ToLower(strings.ReplaceAll(base, " ", "-"))
	base = sessionNameSanitizer.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "session"
	}

	id := ulid.MustNew(ulid.Timestamp(time.Now()), ulidEntropy).String()
	return fmt.Sprintf("%s-%s", base, strings.ToLower(id))
}

type gitMetadata struct {
	repoName string
	branch   string
	rootPath string
	valid    bool
}

//go:generate mockgen -package=session -destination=mock_git_runner_test.go github.com/odvcencio/buckley/pkg/session gitCommandRunner
type gitCommandRunner interface {
	Run(ctx context.Context, dir string, args ...string) ([]byte, error)
}

type execGitRunner struct{}

func (execGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

type gitDetector struct {
	timeout time.Duration
	runner  gitCommandRunner
	cache   sync.Map
}

const defaultGitTimeout = 3 * time.Second

var defaultGitDetector = newGitDetector()

func newGitDetector() *gitDetector {
	return &gitDetector{
		timeout: defaultGitTimeout,
		runner:  execGitRunner{},
	}
}

func getGitMetadata(cwd string) gitMetadata {
	return defaultGitDetector.metadata(cwd)
}

func (d *gitDetector) metadata(cwd string) gitMetadata {
	if d == nil || cwd == "" {
		return gitMetadata{}
	}
	if cached, ok := d.cache.Load(cwd); ok {
		if info, ok := cached.(gitMetadata); ok {
			return info
		}
	}

	info := gitMetadata{}

	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	rootOutput, err := d.runner.Run(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		d.cache.Store(cwd, info)
		return info
	}
	root := strings.TrimSpace(string(rootOutput))
	if root == "" {
		d.cache.Store(cwd, info)
		return info
	}
	info.rootPath = root
	info.repoName = filepath.Base(root)
	info.valid = true

	branchCtx, branchCancel := context.WithTimeout(context.Background(), d.timeout)
	defer branchCancel()
	branchOutput, err := d.runner.Run(branchCtx, cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil {
		info.branch = strings.TrimSpace(string(branchOutput))
	}

	d.cache.Store(cwd, info)
	return info
}

// setGitDetector allows tests to replace the default detector.
func setGitDetector(det *gitDetector) func() {
	prev := defaultGitDetector
	defaultGitDetector = det
	return func() {
		defaultGitDetector = prev
	}
}
