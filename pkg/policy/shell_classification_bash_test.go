package policy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// The guard reads command text and never runs it, so every way in which bash
// reads a line differently from a plain split on spaces is a way to hide a
// command from it. These tests hold the guard to what bash does. Each row is a
// command line and the arguments that bash hands to buckley for it. The test
// checks that claim by running the line in bash, with a stub in place of
// buckley, and then checks the guard's verdict.

// bashRow is a command line and the arguments that bash passes to buckley.
type bashRow struct {
	command string
	argv    []string
}

// buckleyStub returns a function that runs a command line in bash and returns
// the arguments of each call it makes to buckley. A stub that only records its
// arguments stands in for the program, so nothing else runs. The function takes
// the test that is running, so a subtest can use it.
func buckleyStub(t *testing.T) func(t *testing.T, command string) [][]string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bash is not available on windows")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	stub := "#!/bin/sh\n" +
		`{ printf '\001'; for arg in "$@"; do printf '%s\000' "$arg"; done; printf '\002'; } >> "$STUB_LOG"` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "buckley"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write the buckley stub: %v", err)
	}
	return func(t *testing.T, command string) [][]string {
		t.Helper()
		if err := os.WriteFile(log, nil, 0o600); err != nil {
			t.Fatalf("reset the stub log: %v", err)
		}
		cmd := exec.Command(bash, "--noprofile", "--norc", "-c", command)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir, "STUB_LOG=" + log}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bash -c %q: %v\n%s", command, err, out)
		}
		raw, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("read the stub log: %v", err)
		}
		var calls [][]string
		for _, record := range bytes.Split(raw, []byte{2}) {
			if len(record) == 0 {
				continue
			}
			// Each argument ends with a NUL byte, so the last element is empty.
			fields := bytes.Split(bytes.TrimPrefix(record, []byte{1}), []byte{0})
			call := make([]string, 0, len(fields))
			for _, field := range fields[:len(fields)-1] {
				call = append(call, string(field))
			}
			calls = append(calls, call)
		}
		return calls
	}
}

// checkGuardAgainstBash checks each row against bash, then checks that the
// guard calls the line a state write exactly when wantWrite says so.
func checkGuardAgainstBash(t *testing.T, rows []bashRow, wantWrite bool) {
	t.Helper()
	runBash := buckleyStub(t)
	for _, row := range rows {
		t.Run(row.command, func(t *testing.T) {
			if calls := runBash(t, row.command); len(calls) != 1 || !slices.Equal(calls[0], row.argv) {
				t.Fatalf("bash passes %q to buckley, but the row says [%q]: fix the row", calls, row.argv)
			}
			if got := writesBuckleyState(row.command); got != wantWrite {
				t.Errorf("writesBuckleyState(%q) = %t, want %t; bash passes %q", row.command, got, wantWrite, row.argv)
			}
		})
	}
}

// An empty word is still a word. A flag that takes a value takes "" or " " as
// its value, so the words after it keep their places.
func TestWritesBuckleyState_EmptyWordIsStillAWord(t *testing.T) {
	checkGuardAgainstBash(t, []bashRow{
		{`buckley -c "" goal approve run-1`, []string{"-c", "", "goal", "approve", "run-1"}},
		{`buckley -c '' goal approve run-1`, []string{"-c", "", "goal", "approve", "run-1"}},
		{`buckley -c " " goal approve run-1`, []string{"-c", " ", "goal", "approve", "run-1"}},
		{`buckley -c ""'' goal approve run-1`, []string{"-c", "", "goal", "approve", "run-1"}},
		{`buckley --config "" remote login --url https://b.example`, []string{"--config", "", "remote", "login", "--url", "https://b.example"}},
		{`buckley -p "" remote tokens create --url https://b.example --scope operator`, []string{"-p", "", "remote", "tokens", "create", "--url", "https://b.example", "--scope", "operator"}},
		{`buckley goal -c "" approve run-1`, []string{"goal", "-c", "", "approve", "run-1"}},
		{`buckley remote tokens -c " " revoke --url https://b.example --id tok-1`, []string{"remote", "tokens", "-c", " ", "revoke", "--url", "https://b.example", "--id", "tok-1"}},
		{`buckley trust -c "" allow .`, []string{"trust", "-c", "", "allow", "."}},
		{`buckley -c "" trust allow .`, []string{"-c", "", "trust", "allow", "."}},
	}, true)
}

// A substitution is one word of the command around it, and the shell does not
// end that command where the substitution holds a ;, a |, an & or a parenthesis.
func TestWritesBuckleyState_SubstitutionIsOneWord(t *testing.T) {
	checkGuardAgainstBash(t, []bashRow{
		{"buckley -c $(echo x) goal approve run-1", []string{"-c", "x", "goal", "approve", "run-1"}},
		{"buckley --config $(echo x)/y.yaml remote login --url https://b.example", []string{"--config", "x/y.yaml", "remote", "login", "--url", "https://b.example"}},
		{"buckley -m $(echo m) remote tokens create --url https://b.example --scope operator", []string{"-m", "m", "remote", "tokens", "create", "--url", "https://b.example", "--scope", "operator"}},
		{"buckley --config=$(echo x) goal approve run-1", []string{"--config=x", "goal", "approve", "run-1"}},
		{"buckley -c $((1+1)) goal approve run-1", []string{"-c", "2", "goal", "approve", "run-1"}},
		{"buckley -c `echo x | cat` goal approve run-1", []string{"-c", "x", "goal", "approve", "run-1"}},
		{"buckley -c ${X:-$(echo a)} goal approve run-1", []string{"-c", "a", "goal", "approve", "run-1"}},
		{"buckley -c $(true && echo x) goal approve run-1", []string{"-c", "x", "goal", "approve", "run-1"}},
		{"buckley -c $(echo x; echo) goal approve run-1", []string{"-c", "x", "goal", "approve", "run-1"}},
		{"buckley --model $(echo m) trust allow .", []string{"--model", "m", "trust", "allow", "."}},
		{"buckley trust -c $(echo x) allow .", []string{"trust", "-c", "x", "allow", "."}},
		{`buckley -c "$(echo x | cat)" goal approve run-1`, []string{"-c", "x", "goal", "approve", "run-1"}},
		{`buckley -c "$(echo "x y")" goal approve run-1`, []string{"-c", "x y", "goal", "approve", "run-1"}},
		{`buckley -c "${X:-$(echo "a b")}" goal approve run-1`, []string{"-c", "a b", "goal", "approve", "run-1"}},
		{"buckley -c $(true)#b goal approve run-1", []string{"-c", "#b", "goal", "approve", "run-1"}},
		{"buckley $\\\n(echo goal) approve run-1", []string{"goal", "approve", "run-1"}},
	}, true)
}

// A # starts a comment only at the start of a word. After an escaped blank it is
// part of the word, and so is the text after it.
func TestWritesBuckleyState_HashAfterEscapedBlankIsNotAComment(t *testing.T) {
	checkGuardAgainstBash(t, []bashRow{
		{`buckley -c a\ #b goal approve run-1`, []string{"-c", "a #b", "goal", "approve", "run-1"}},
		{`buckley -m a\ #b remote login --url https://b.example`, []string{"-m", "a #b", "remote", "login", "--url", "https://b.example"}},
		{`buckley trust -c a\ #b allow .`, []string{"trust", "-c", "a #b", "allow", "."}},
		{"buckley -c a\\\t#b goal approve run-1", []string{"-c", "a\t#b", "goal", "approve", "run-1"}},
		{`buckley -c "a"#b goal approve run-1`, []string{"-c", "a#b", "goal", "approve", "run-1"}},
		{`buckley -c ''#b goal approve run-1`, []string{"-c", "#b", "goal", "approve", "run-1"}},
		{"buckley -c a\\\n#b goal approve run-1", []string{"-c", "a#b", "goal", "approve", "run-1"}},
	}, true)
}

// A backslash and newline vanish before the shell reads words, in the middle of
// a word as well as between words.
func TestWritesBuckleyState_ContinuationInsideAWord(t *testing.T) {
	checkGuardAgainstBash(t, []bashRow{
		{"buckley goal app\\\nrove run-1", []string{"goal", "approve", "run-1"}},
		{"buckley go\\\nal approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley remote lo\\\ngin --url https://b.example", []string{"remote", "login", "--url", "https://b.example"}},
		{"buckley remote tokens cre\\\nate --url https://b.example --scope operator", []string{"remote", "tokens", "create", "--url", "https://b.example", "--scope", "operator"}},
		{"buckley goal \"app\\\nrove\" run-1", []string{"goal", "approve", "run-1"}},
		{"buckley tr\\\nust allow .", []string{"trust", "allow", "."}},
		{"buckley goal app\\\n\\\nrove run-1", []string{"goal", "approve", "run-1"}},
		{"env \\\n  FOO=1 buckley goal approve run-1", []string{"goal", "approve", "run-1"}},
		// A comment ends at the newline, whatever precedes it.
		{"# note \\\nbuckley goal approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley goal approve run-1 # note \\\n", []string{"goal", "approve", "run-1"}},
	}, true)
}

// bash splits words on the space and the tab only. The other characters that
// Unicode and the C library call white space, among them the carriage return,
// stay inside the word.
func TestWritesBuckleyState_OnlySpaceAndTabEndAWord(t *testing.T) {
	checkGuardAgainstBash(t, []bashRow{
		{"buckley -c x\r goal approve run-1", []string{"-c", "x\r", "goal", "approve", "run-1"}},
		{"buckley -m x\r remote login --url https://b.example", []string{"-m", "x\r", "remote", "login", "--url", "https://b.example"}},
		{"buckley --config=\rx goal approve run-1", []string{"--config=\rx", "goal", "approve", "run-1"}},
		{"buckley -c x  goal approve run-1", []string{"-c", "x ", "goal", "approve", "run-1"}},
		{"buckley -c x　 goal approve run-1", []string{"-c", "x　", "goal", "approve", "run-1"}},
		{"buckley -c x\u000b goal approve run-1", []string{"-c", "x\u000b", "goal", "approve", "run-1"}},
		{"buckley -c x\u000c goal approve run-1", []string{"-c", "x\u000c", "goal", "approve", "run-1"}},
		{"buckley -c x\u0085 goal approve run-1", []string{"-c", "x\u0085", "goal", "approve", "run-1"}},
		{"buckley trust -c x\r allow .", []string{"trust", "-c", "x\r", "allow", "."}},
	}, true)
}

// A redirection, with its file descriptor and its target, is not an argument.
func TestWritesBuckleyState_RedirectionIsNotAnArgument(t *testing.T) {
	checkGuardAgainstBash(t, []bashRow{
		{"buckley >/dev/null goal approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley goal 2>&1 approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley remote tokens </dev/null create --url https://b.example --scope operator", []string{"remote", "tokens", "create", "--url", "https://b.example", "--scope", "operator"}},
		{"buckley goal &>/dev/null approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley goal >&2 approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley goal 2> /dev/null approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley goal <<<x approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley goal >>/dev/null approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley goal >|/dev/null approve run-1", []string{"goal", "approve", "run-1"}},
		{"2>&1 buckley goal approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley goal >\\\n&2 approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley trust >/dev/null allow .", []string{"trust", "allow", "."}},
		// >&- and <&- close a descriptor. They take no target, so the next word stays an argument.
		{"buckley trust 3>&-\\\nrm .", []string{"trust", "rm", "."}},
		{"buckley goal >&-approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley goal 0<&- approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley goal 3>&1- approve run-1", []string{"goal", "approve", "run-1"}},
		{"buckley -c x.yaml 2>/dev/null goal approve run-1", []string{"-c", "x.yaml", "goal", "approve", "run-1"}},
	}, true)
}

// Inside $'...' a backslash escapes the quote, so the quoted text goes on past
// it. Reading the quote as closed there leaves the rest of the line unread.
func TestWritesBuckleyState_AnsiCQuoteWithEscapedQuote(t *testing.T) {
	checkGuardAgainstBash(t, []bashRow{
		{`buckley -c $'a\'b' goal approve run-1`, []string{"-c", "a'b", "goal", "approve", "run-1"}},
		{`echo $'a\'b'; buckley goal approve run-1`, []string{"goal", "approve", "run-1"}},
		{`buckley $'goal' approve run-1`, []string{"goal", "approve", "run-1"}},
	}, true)
}

// The same syntax in commands that change no state. The guard must let each of
// these run, or it denies work the operator asked to allow.
func TestWritesBuckleyState_ShellSyntaxAroundOtherCommands(t *testing.T) {
	checkGuardAgainstBash(t, []bashRow{
		{`buckley -c "" goal status run-1`, []string{"-c", "", "goal", "status", "run-1"}},
		{`buckley -c " " commit --yes`, []string{"-c", " ", "commit", "--yes"}},
		{"buckley -c $(echo x) goal status run-1", []string{"-c", "x", "goal", "status", "run-1"}},
		{"buckley -c `echo x | cat` remote tokens list --url https://b.example", []string{"-c", "x", "remote", "tokens", "list", "--url", "https://b.example"}},
		{"buckley -p $(echo hello) commit", []string{"-p", "hello", "commit"}},
		{"buckley commit -m $(echo fix)", []string{"commit", "-m", "fix"}},
		{`buckley commit -m "$(echo "fix goal approve")"`, []string{"commit", "-m", "fix goal approve"}},
		{`buckley commit -m a\ #b`, []string{"commit", "-m", "a #b"}},
		{"buckley goal app\\\nrove-all run-1", []string{"goal", "approve-all", "run-1"}},
		{"buckley goal sta\\\ntus run-1", []string{"goal", "status", "run-1"}},
		{"buckley -c x\r goal status run-1", []string{"-c", "x\r", "goal", "status", "run-1"}},
		{"buckley goal status run-1 >/dev/null", []string{"goal", "status", "run-1"}},
		{"buckley goal 2>&1 status run-1", []string{"goal", "status", "run-1"}},
		{"buckley goal >approve status run-1", []string{"goal", "status", "run-1"}},
		{"buckley goal status run-1 # buckley goal approve run-1", []string{"goal", "status", "run-1"}},
		{"buckley goal status run-1 #\\\n", []string{"goal", "status", "run-1"}},
		{"buckley commit -m 'fix\\\napprove'", []string{"commit", "-m", "fix\\\napprove"}},
		{"buckley trust status $(echo .)", []string{"trust", "status", "."}},
		{"buckley trust -c $(echo x) status .", []string{"trust", "-c", "x", "status", "."}},
		{`buckley trust -c "" status`, []string{"trust", "-c", "", "status"}},
	}, false)
}
