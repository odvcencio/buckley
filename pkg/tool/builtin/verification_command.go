package builtin

import (
	"fmt"
	"strconv"
	"strings"
)

// IsVerificationCall uses the shell's own interactive-value parser so a
// terminal launch cannot count as a completed foreground check.
func IsVerificationCall(params map[string]any) bool {
	command, _ := params["command"].(string)
	return !parseBoolParam(params["interactive"], false) && IsVerificationCommand(command)
}

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

// VerificationCommandHelp states what a verification command may look like.
// The run_verification description and every rejection message use it, so a
// model learns the contract from the tool instead of by trial and error.
func VerificationCommandHelp() string {
	return "Accepted commands start with " + strings.Join(verificationShellCommandPrefixes, ", ") +
		". GOWORK=off, CGO_ENABLED=0 or 1, env, and nice -n N (0 to 19) may come first. " +
		"Give one command in plain words: no quotes, $, ;, &, |, <, >, backslash, absolute paths, or .. paths."
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

// isVerificationEnvAssignment reports whether field is one of the few
// environment assignments a verification command may start with.
func isVerificationEnvAssignment(field string) bool {
	return field == "GOWORK=off" || field == "CGO_ENABLED=0" || field == "CGO_ENABLED=1"
}

// isNiceLevel reports whether field is a nice level that only lowers priority.
func isNiceLevel(field string) bool {
	level, err := strconv.Atoi(field)
	return err == nil && level >= 0 && level <= 19 && field == strconv.Itoa(level)
}

// stripVerificationWrappers drops the leading words that only shape the
// environment or the scheduling priority of the check that follows them:
// allowed env assignments, an env word that carries them, and nice -n N. It
// follows shell grammar: a bare assignment is valid first, or right after env,
// but not after nice, where the shell would run it as a program name.
func stripVerificationWrappers(fields []string) []string {
	bareAssignment := true
	for len(fields) > 0 {
		switch {
		case bareAssignment && isVerificationEnvAssignment(fields[0]):
			fields = fields[1:]
		case fields[0] == "env" && len(fields) > 1 && isVerificationEnvAssignment(fields[1]):
			fields = fields[1:]
			bareAssignment = true
		case fields[0] == "nice" && len(fields) > 2 && fields[1] == "-n" && isNiceLevel(fields[2]):
			fields = fields[3:]
			bareAssignment = false
		default:
			return fields
		}
	}
	return fields
}

// IsVerificationCommand reports whether command is a known
// build/vet/test/lint prefix with nothing chained after it. The command may
// hold only allowlisted bytes (see isVerificationCommandByte), so no shell
// separator, escape, quote, or substitution can smuggle a second command
// past the prefix, and it may not carry a flag that executes another
// program (see verificationCommandExecFlags).
func IsVerificationCommand(command string) bool {
	return VerificationCommandRejection(command) == ""
}

// VerificationCommandRejection returns "" when command is an accepted
// verification command (see IsVerificationCommand). Otherwise it returns one
// plain sentence that names the first rule the command breaks, so a caller can
// tell the model what to change instead of only that the call failed.
func VerificationCommandRejection(command string) string {
	fields := stripVerificationWrappers(strings.Fields(strings.TrimSpace(command)))
	if len(fields) == 0 {
		return "the command is empty"
	}
	if verificationNoOpCommands[fields[0]] {
		return fmt.Sprintf("%s proves nothing, so it is not a check", fields[0])
	}
	trimmed := strings.Join(fields, " ")
	knownPrefix := false
	for _, prefix := range verificationShellCommandPrefixes {
		if trimmed == prefix || strings.HasPrefix(trimmed, prefix+" ") {
			knownPrefix = true
			break
		}
	}
	if !knownPrefix {
		return unacceptedVerificationCommand(fields)
	}
	// Only spaces are accepted below; inspect the original bytes before normalizing.
	for i := 0; i < len(command); i++ {
		if !isVerificationCommandByte(command[i]) {
			return fmt.Sprintf("it contains the character %q, which is shell syntax; use plain words only", string(command[i]))
		}
	}
	for _, field := range strings.Fields(trimmed) {
		name, value, hasValue := strings.Cut(strings.ToLower(field), "=")
		for _, flag := range verificationCommandExecFlags {
			if name == flag || name == "-"+flag {
				return fmt.Sprintf("the flag %s can run another program, read outside the workspace, or run no check", field)
			}
		}
		if !strings.HasPrefix(field, "-") {
			if !isWorkspaceRelativeArg(field) {
				return fmt.Sprintf("the path %s is absolute or leaves the workspace; use a path inside it", field)
			}
			continue
		}
		// A flag may carry a path only as a relative "=value": an attached
		// short-flag value such as make's -f/tmp/x would dodge the name check.
		if strings.Contains(name, "/") || (hasValue && !isWorkspaceRelativeArg(value)) {
			return fmt.Sprintf("the flag %s carries a path outside the workspace", field)
		}
	}
	for _, field := range fields[1:] {
		if field == "-V" || field == "--co" {
			return fmt.Sprintf("%s only prints information and runs no check", field)
		}
		if (fields[0] == "go" || fields[0] == "make") && field == "-n" {
			return "-n is a dry run and runs no check"
		}
		if fields[0] == "npm" && field == "-v" {
			return "-v only prints the npm version and runs no check"
		}
	}
	if fields[0] == "make" {
		if len(fields) < 2 {
			return "make needs a target: test, check, build, vet, or lint"
		}
		for _, field := range fields[2:] {
			if strings.Contains(field, "=") {
				return fmt.Sprintf("the make variable %s can replace the recipe shell; run the target without variables", field)
			}
			if strings.HasPrefix(field, "-") && !strings.HasPrefix(field, "--") && strings.ContainsAny(field[1:], "nqt") {
				return fmt.Sprintf("the make flag %s is a dry run, a question, or a touch, and runs no check", field)
			}
		}
	}
	return ""
}

// unacceptedVerificationCommand explains why a command whose first words are
// not an accepted check is rejected, with a pointer to the right family when
// the first word names one. It never echoes an env-style word, which can
// carry a secret.
func unacceptedVerificationCommand(fields []string) string {
	head := fields[0]
	if name, _, hasValue := strings.Cut(head, "="); hasValue {
		return fmt.Sprintf("%s=... is not accepted here; only GOWORK=off and CGO_ENABLED=0 or 1 are allowed, first or after env, for example GOWORK=off nice -n 10 go test ./...", name)
	}
	switch head {
	case "make":
		return "make needs one of the targets test, check, build, vet, or lint"
	case "go":
		return "go needs one of build, vet, or test"
	case "npm":
		return "npm needs one of test, run test, run build, or run lint"
	case "cargo":
		return "cargo needs one of build, test, check, or clippy"
	case "python", "python3":
		return "use pytest or python -m pytest for Python tests; python -c, python -m unittest, and scripts are not accepted checks"
	}
	if !plainVerificationWord(head) {
		return "the command does not start with an accepted check"
	}
	return fmt.Sprintf("%s is not an accepted check; ad hoc commands such as test, grep, diff, and git do not count as verification (run them with run_shell if you need them)", head)
}

// plainVerificationWord reports whether word holds only bytes that are safe to
// echo back in an error message.
func plainVerificationWord(word string) bool {
	if word == "" || len(word) > 40 {
		return false
	}
	for i := 0; i < len(word); i++ {
		if !isVerificationCommandByte(word[i]) || word[i] == '=' {
			return false
		}
	}
	return true
}
