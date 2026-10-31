// Package gitversion reads the version of the git on PATH, so a caller can use
// an option only when the installed git has it.
package gitversion

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Unknown is the version text AtLeast returns when git's version cannot be read.
const Unknown = "unknown"

// versionLine matches the line `git version` prints, for example
// "git version 2.43.0", "git version 2.39.5 (Apple Git-154)" or
// "git version 2.44.0.windows.1".
var versionLine = regexp.MustCompile(`^git version ((\d+)\.(\d+)\S*)`)

// AtLeast reports whether the git on PATH is release major.minor or newer, and
// returns the version git printed, for messages. When git cannot run or prints
// a version this package does not recognize, ok is false and installed is
// Unknown, so a caller takes the path that works on every git.
func AtLeast(major, minor int) (ok bool, installed string) {
	out, err := exec.Command("git", "version").Output()
	if err != nil {
		return false, Unknown
	}
	return atLeast(string(out), major, minor)
}

func atLeast(output string, major, minor int) (bool, string) {
	match := versionLine.FindStringSubmatch(strings.TrimSpace(output))
	if match == nil {
		return false, Unknown
	}
	gotMajor, errMajor := strconv.Atoi(match[2])
	gotMinor, errMinor := strconv.Atoi(match[3])
	if errMajor != nil || errMinor != nil {
		return false, Unknown
	}
	return gotMajor > major || (gotMajor == major && gotMinor >= minor), match[1]
}
