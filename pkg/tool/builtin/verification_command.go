package builtin

import "strings"

var verificationShellCommandPrefixes = []string{
	"go build", "go vet", "go test", "golangci-lint run", "staticcheck",
	"npm test", "npm run test", "npm run build", "npm run lint",
	"cargo build", "cargo test", "cargo check", "cargo clippy",
	"pytest", "python -m pytest", "python3 -m pytest",
	"make test", "make check", "make build", "make vet", "make lint",
}

var verificationNoOpCommands = map[string]bool{"true": true, "false": true, "echo": true, "printf": true, ":": true}

// verificationCommandExecFlags are flags that make a build/test tool run an
// arbitrary program or write outside the workspace, so a "verification"
// command carrying one is no longer read-only.
var verificationCommandExecFlags = []string{
	"-exec", "-toolexec", "-vettool", "-overlay", "-modfile", "-o",
	"--config", "--target-dir", "--manifest-path",
	// Flags that load build rules or code from another file or directory:
	// make -f/-C/-I, npm --prefix, pytest -c/--rootdir.
	"-f", "--file", "--makefile", "-c", "--directory", "-i", "--include-dir",
	"--prefix", "--rootdir",
	"--help", "-h", "--version", "--dry-run", "--ignore-scripts",
	"--if-present", "--collect-only", "--no-run", "--list", "--print",
	"--just-print", "--recon", "--question", "--touch",
	"--eval", "--environment-overrides",
}

// isWorkspaceRelativeArg reports whether arg names only paths inside the
// workspace: no absolute or home-relative path and no parent traversal.
func isWorkspaceRelativeArg(arg string) bool {
	if strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~") {
		return false
	}
	// Compare whole segments: "./..." is Go's package wildcard, not traversal.
	for _, segment := range strings.Split(arg, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

// isVerificationCommandByte reports whether b may appear in a verification
// command. It is an allowlist: bash -lc treats newline, carriage return,
// backslash, quotes, and every control operator as syntax, so anything
// outside plain words, spaces, and path/flag punctuation is rejected.
func isVerificationCommandByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte(" -_./=:,@+%*", b) >= 0
}

// IsVerificationCommand reports whether command is a known
// build/vet/test/lint prefix with nothing chained after it. The command may
// hold only allowlisted bytes (see isVerificationCommandByte), so no shell
// separator, escape, quote, or substitution can smuggle a second command
// past the prefix, and it may not carry a flag that executes another
// program (see verificationCommandExecFlags).
func IsVerificationCommand(command string) bool {
	trimmed := strings.TrimSpace(command)
	fields := strings.Fields(trimmed)
	for len(fields) > 0 && (fields[0] == "GOWORK=off" || fields[0] == "CGO_ENABLED=0" || fields[0] == "CGO_ENABLED=1") {
		fields = fields[1:]
	}
	if len(fields) == 0 || verificationNoOpCommands[fields[0]] {
		return false
	}
	// Only spaces are accepted below; inspect the original bytes before normalizing.
	for i := 0; i < len(command); i++ {
		if !isVerificationCommandByte(command[i]) {
			return false
		}
	}
	trimmed = strings.Join(fields, " ")
	for _, field := range strings.Fields(trimmed) {
		name, value, hasValue := strings.Cut(strings.ToLower(field), "=")
		for _, flag := range verificationCommandExecFlags {
			if name == flag || name == "-"+flag {
				return false
			}
		}
		if !strings.HasPrefix(field, "-") {
			if !isWorkspaceRelativeArg(field) {
				return false
			}
			continue
		}
		// A flag may carry a path only as a relative "=value": an attached
		// short-flag value such as make's -f/tmp/x would dodge the name check.
		if strings.Contains(name, "/") || (hasValue && !isWorkspaceRelativeArg(value)) {
			return false
		}
	}
	for _, field := range fields[1:] {
		if field == "-V" || field == "--co" {
			return false
		}
		if (fields[0] == "go" || fields[0] == "make") && field == "-n" {
			return false
		}
		if fields[0] == "npm" && field == "-v" {
			return false
		}
	}
	if fields[0] == "make" {
		if len(fields) < 2 {
			return false
		}
		for _, field := range fields[2:] {
			if strings.Contains(field, "=") {
				return false
			}
			if strings.HasPrefix(field, "-") && !strings.HasPrefix(field, "--") && strings.ContainsAny(field[1:], "nqt") {
				return false
			}
		}
	}
	for _, prefix := range verificationShellCommandPrefixes {
		if trimmed == prefix || strings.HasPrefix(trimmed, prefix+" ") {
			return true
		}
	}
	return false
}
