package diffsignal

import (
	"bytes"
	"os/exec"
	"path"
	"strings"
	"sync/atomic"
)

// GeneratedResolver builds the "is this path generated" predicate for the
// paths of one diff. It lets a caller that knows the repository (for its
// .gitattributes) and a configured glob list classify files without passing
// a predicate through every diff-reading call.
type GeneratedResolver func(paths []string) func(path string) bool

var defaultResolver atomic.Pointer[GeneratedResolver]

// SetGeneratedResolver installs the process-wide resolver used when a caller
// passes no predicate of its own. Pass nil to remove it. Buckley's review
// commands set it once at startup, before any diff is read.
func SetGeneratedResolver(r GeneratedResolver) {
	if r == nil {
		defaultResolver.Store(nil)
		return
	}
	defaultResolver.Store(&r)
}

func resolveDefaultGenerated(paths []string) func(string) bool {
	if r := defaultResolver.Load(); r != nil {
		return (*r)(paths)
	}
	return nil
}

// NewGeneratedResolver combines the repository's gitattributes (a path with
// linguist-generated set, or with diff unset) with a list of glob patterns.
// workDir is the repository directory for git check-attr.
func NewGeneratedResolver(workDir string, globs []string) GeneratedResolver {
	byGlob := GlobMatcher(globs)
	return func(paths []string) func(string) bool {
		byAttr := attrGenerated(workDir, paths)
		if byGlob == nil && byAttr == nil {
			return nil
		}
		return func(p string) bool {
			return (byAttr != nil && byAttr[p]) || (byGlob != nil && byGlob(p))
		}
	}
}

// GlobMatcher matches slash-separated repository paths against patterns.
// "*" and "?" stay within one path element, "**" crosses elements, and a
// pattern without a slash matches the base name at any depth. It returns nil
// when no pattern is usable.
func GlobMatcher(patterns []string) func(string) bool {
	var clean []string
	for _, p := range patterns {
		if p = strings.TrimSpace(p); p != "" {
			clean = append(clean, p)
		}
	}
	if len(clean) == 0 {
		return nil
	}
	return func(p string) bool {
		for _, pat := range clean {
			if !strings.Contains(pat, "/") {
				if ok, _ := path.Match(pat, path.Base(p)); ok {
					return true
				}
				continue
			}
			if matchSegments(strings.Split(pat, "/"), strings.Split(p, "/")) {
				return true
			}
		}
		return false
	}
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			if len(rest) == 0 {
				return len(name) > 0
			}
			for i := 0; i <= len(name); i++ {
				if matchSegments(rest, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

func attrGenerated(workDir string, paths []string) map[string]bool {
	if len(paths) == 0 || workDir == "" {
		return nil
	}
	cmd := exec.Command("git", "--no-pager", "check-attr", "-z", "--stdin", "linguist-generated", "diff")
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	fields := bytes.Split(out, []byte{0})
	generated := map[string]bool{}
	for i := 0; i+2 < len(fields); i += 3 {
		p, attr, value := string(fields[i]), string(fields[i+1]), string(fields[i+2])
		if (attr == "linguist-generated" && (value == "set" || value == "true")) || (attr == "diff" && value == "unset") {
			generated[p] = true
		}
	}
	if len(generated) == 0 {
		return nil
	}
	return generated
}
