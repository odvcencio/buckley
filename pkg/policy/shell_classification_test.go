package policy

import (
	"strings"
	"testing"
	"time"
)

func TestClassifyShellCommand_AgentHarnessInvocations(t *testing.T) {
	for _, command := range []string{
		"buckley -p inspect",
		"/home/user/.local/bin/codex exec task",
		"env MODEL=x opencode run",
		"go test ./... && claude -p fix",
		"(aider --yes)",
		`"/opt/agent tools/codex" exec task`,
		"MODEL=x codex exec task",
		"env -i MODEL=x codex exec task",
		"sudo -u worker codex exec task",
		"timeout -s TERM 30s codex exec task",
		"nice -n 5 codex exec task",
		"command -- codex exec task",
		"exec -a worker codex exec task",
		"printf 'finished';codex exec task",
		"go test ./...\nclaude -p fix",
		"if true; then codex exec task; fi",
		"# explanatory comment\ncodex exec task",
	} {
		if got := ClassifyShellCommand(command); got != CommandClassAgentHarness {
			t.Errorf("ClassifyShellCommand(%q) = %q", command, got)
		}
	}
}

func TestClassifyShellCommand_ReferencesAreNotInvocations(t *testing.T) {
	for _, command := range []string{
		"go test ./cmd/buckley",
		"rg buckley pkg",
		"printf 'use codex for this task'",
		"git diff -- pkg/tool/builtin/delegate.go",
		`printf 'first; codex task'`,
		`rg '&& codex exec' README.md`,
		`printf '%s' '(claude --help)'`,
		"command -v codex",
		"env --help codex",
		"sudo -v codex",
		"# example: go test; codex exec task",
		"printf done # example; codex exec task",
	} {
		if got := ClassifyShellCommand(command); got != "" {
			t.Errorf("ClassifyShellCommand(%q) = %q", command, got)
		}
	}
}

// Each command here launches an agent behind syntax that a plain split on spaces
// misreads: a # that is part of a word, a quote with an escaped quote inside, a
// substitution, a line continuation, a redirection, or a character that is not a
// blank to bash.
func TestClassifyShellCommand_LaunchesBehindShellSyntax(t *testing.T) {
	for _, command := range []string{
		"echo a\\ #b; codex exec task",
		"echo a\\\t#b; codex exec task",
		"echo $(true)#b; codex exec task",
		"echo \"a\"#b; codex exec task",
		`echo $'a\'b'; codex exec task`,
		"echo <(codex exec task)",
		"sudo -u worker \\\n  codex exec task",
		"timeout 5 \\\n  codex exec task",
		"env \\\n  FOO=1 claude -p fix",
		"cod\\\nex exec task",
		"codex \\\n exec task",
		">/dev/null codex exec task",
		"2>&1 claude -p fix",
		"</dev/null aider --yes",
		"echo ok >/dev/null; claude -p fix",
		"echo ok &>/dev/null; claude -p fix",
		"echo ok 2>&1 | claude -p fix",
		"echo x\r; codex exec task",
		"echo x\u00a0; codex exec task",
		"# note \\\ncodex exec task",
	} {
		if got := ClassifyShellCommand(command); got != CommandClassAgentHarness {
			t.Errorf("ClassifyShellCommand(%q) = %q", command, got)
		}
	}
}

// These only mention an agent. In each, the name is an argument, part of a
// comment, or a redirection target, and no command starts with it.
func TestClassifyShellCommand_MentionsBehindShellSyntaxAreNotLaunches(t *testing.T) {
	for _, command := range []string{
		"echo a\\ #b codex exec task",
		"echo $'codex exec task'",
		`echo $"codex exec task"`,
		"echo ${X:-codex exec task}",
		`echo "$(date)" codex`,
		"echo ok >codex",
		"echo ok 2>codex",
		"echo ok </dev/null # codex exec task",
		"echo ok # codex exec task \\\n",
		"cat <<< 'codex exec task'",
		"echo x\r codex exec task",
	} {
		if got := ClassifyShellCommand(command); got != "" {
			t.Errorf("ClassifyShellCommand(%q) = %q", command, got)
		}
	}
}

// A commit message in a here-document inside "$(...)" is text. A line in it that
// starts with an agent's name is not a launch, and an apostrophe in it must not
// swallow the commands that follow.
func TestClassifyShellCommand_HereDocumentInQuotedSubstitution(t *testing.T) {
	for _, message := range []string{
		"fix: don't crash",
		"fix: don't crash, and can't leave\n",
		"Claude Code reviewed it\ncodex exec was not used\nbuckley goal approve is a command",
		"it's (maybe) 'quoted' and \"nested\" (",
	} {
		commit := "git commit -m \"$(cat <<'EOF'\n" + message + "\nEOF\n)\""
		if got := ClassifyShellCommand(commit); got != "" {
			t.Errorf("ClassifyShellCommand(%q) = %q", commit, got)
		}
		if writesBuckleyState(commit) {
			t.Errorf("writesBuckleyState(%q) = true", commit)
		}
		for _, after := range []string{" && codex exec task", "; claude -p fix", "\nbuckley pr"} {
			if got := ClassifyShellCommand(commit + after); got != CommandClassAgentHarness {
				t.Errorf("ClassifyShellCommand(%q) = %q", commit+after, got)
			}
		}
		if !writesBuckleyState(commit + "\nbuckley goal approve run-1") {
			t.Errorf("writesBuckleyState(%q) = false", commit+"\nbuckley goal approve run-1")
		}
	}
}

// Markdown in a here-document is full of backquotes. An agent named in one is
// text, not a launch, and a backquote still ends the word it sits in.
func TestClassifyShellCommand_BackquotesAreText(t *testing.T) {
	for _, command := range []string{
		"cat > docs/agents.md <<'EOF'\nRun `codex exec task` to start a task.\nThen `buckley goal approve run-1` resolves it.\nEOF",
		"echo \"use `codex exec` here\"",
		"printf '%s' `echo claude`",
		"git commit -m 'docs: explain `claude -p`'",
	} {
		if got := ClassifyShellCommand(command); got != "" {
			t.Errorf("ClassifyShellCommand(%q) = %q", command, got)
		}
		if writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = true", command)
		}
	}
	// A launch after a backquote is still a launch, also when the backquote is
	// alone, as in a Markdown code fence.
	for _, command := range []string{
		"echo `date`; codex exec task",
		"cat > README.md <<'EOF'\n```sh\nmake test\n```\nEOF\ncodex exec task",
		"cat > README.md <<'EOF'\n```\nEOF\nbuckley goal approve run-1",
		"echo `",
	} {
		want := CommandClassAgentHarness
		if command == "echo `" {
			want = ""
		}
		if got := ClassifyShellCommand(command); got != want {
			t.Errorf("ClassifyShellCommand(%q) = %q, want %q", command, got, want)
		}
	}
	if !writesBuckleyState("cat > README.md <<'EOF'\n```\nEOF\nbuckley goal approve run-1") {
		t.Error("a lone backquote hid a goal approve")
	}
}

func TestClassifyShellCommand_DatabaseMigrationGooseIsNotAnAgent(t *testing.T) {
	for _, command := range []string{
		"goose up",
		"goose -dir migrations postgres \"$DATABASE_URL\" up",
		"/usr/local/bin/goose status",
		"env GOOSE_DRIVER=postgres goose up",
		"go build ./... && goose up",
	} {
		if got := ClassifyShellCommand(command); got != "" {
			t.Errorf("ClassifyShellCommand(%q) = %q", command, got)
		}
	}
}

func TestWritesBuckleyState_TrustWrites(t *testing.T) {
	for _, command := range []string{
		"buckley trust allow .",
		"buckley trust deny /srv/repo",
		"buckley trust reset",
		"buckley trust trust .",
		"buckley trust restrict .",
		"buckley trust forget .",
		"buckley trust rm .",
		"buckley trust ALLOW .",
		"buckley TRUST allow .",
		"buckley trust allow --help",
		"/usr/local/bin/buckley trust allow .",
		"buckley --quiet trust allow .",
		"buckley --config /tmp/x.yaml trust allow .",
		"buckley -c x.yaml trust allow .",
		"buckley trust --config x.yaml allow .",
		"buckley trust --no-color deny .",
		"buckley trust 'allow' .",
		"env HOME=/srv buckley trust allow .",
		"sudo -u worker buckley trust allow .",
		"timeout 5 buckley trust reset .",
		"nohup buckley trust allow . &",
		"cd repo && buckley trust allow .",
		"go test ./...; buckley trust deny .",
		"echo ok | buckley trust allow .",
		"codex exec task; buckley trust allow .",
		"(buckley trust allow .)",
		"if true; then buckley trust allow .; fi",
		`buckley trust "$VERB" .`,
		"buckley trust $VERB .",
		"buckley trust $(echo allow) .",
		"buckley trust `echo allow` .",
		"buckley trust {allow,deny} .",
		"buckley trust al* .",
		"buckley --config x.yaml trust ${VERB:-allow} .",
	} {
		if !writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = false", command)
		}
	}
}

// The text cannot show what a subcommand written as an expansion becomes, so it
// counts as a possible write. Bash can turn each command here into
// `buckley trust allow .` or `buckley trust deny .`. An earlier version of the
// check let all of them through.
func TestWritesBuckleyState_ExpandedSubcommandIsAWrite(t *testing.T) {
	for _, command := range []string{
		`buckley $'trust' allow .`,
		`buckley $"trust" allow .`,
		`buckley "$(printf trust)" allow .`,
		"buckley $(echo trust) allow .",
		"buckley `echo trust` allow .",
		"buckley ${T:-trust} allow .",
		"T=trust; buckley $T allow .",
		`X="trust allow ."; buckley $X`,
		"buckley {trust,allow} .",
		"buckley [t]rust allow .",
		"buckley tru* allow .",
		"buckley tr?st allow .",
		// Startup flags and their values come before the subcommand.
		`buckley -c x.yaml $T allow .`,
		`buckley --config=x.yaml $T allow .`,
		`buckley -q $T allow .`,
		`buckley --model "$M" $'trust' deny .`,
		`buckley -p "$(cat t.md)" $T allow .`,
		"buckley --max-elapsed 5m $(echo trust) allow .",
		"sudo -u worker buckley $T allow .",
		"cd repo && FOO=1 buckley $'trust' allow .",
	} {
		if !writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = false", command)
		}
	}
}

// buckley takes -c/--config and its value anywhere on the line, so the first
// word after "trust" can be the config path instead of the verb.
func TestWritesBuckleyState_ExpandedVerbBehindConfigFlagIsAWrite(t *testing.T) {
	for _, command := range []string{
		`buckley trust -c x.yaml "$V" .`,
		"buckley trust --config x.yaml ${V:-allow} .",
		`buckley trust -c x "$(echo allow)" .`,
		"buckley trust -c x $(echo allow) .",
		"buckley trust -c x `echo allow` .",
		`buckley trust --config x.yaml -c y.yaml "$V" .`,
		`buckley trust --quiet -c x.yaml $V .`,
		`buckley -c x.yaml trust -c y.yaml {allow,deny} .`,
		`buckley trust -c "$CFG" "$V" .`,
	} {
		if !writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = false", command)
		}
	}
}

// The switch lets a model launch agents. It does not let the model approve its
// own parked work, mint itself an API token, or store remote credentials.
func TestWritesBuckleyState_ApprovalAndCredentialWrites(t *testing.T) {
	for _, command := range []string{
		"buckley goal approve run-1",
		"buckley goal approve --deny run-1",
		"buckley goal approve --deny --reason 'not yet' --task t1 run-1",
		"buckley remote login --url https://b.example",
		"buckley remote login --url https://b.example --no-browser --label ci",
		"buckley remote tokens create --url https://b.example --scope operator",
		"buckley remote tokens create --url https://b.example --name ci --owner me --scope operator",
		"buckley remote tokens revoke --url https://b.example --id tok-1",
		// The words ignore case and quotes.
		"buckley GOAL APPROVE run-1",
		"buckley Goal Approve run-1",
		"buckley REMOTE LOGIN --url https://b.example",
		"buckley Remote Tokens Create --url https://b.example --scope operator",
		"buckley remote TOKENS REVOKE --url https://b.example --id tok-1",
		"buckley goal 'approve' run-1",
		`buckley "goal" "approve" run-1`,
		`buckley remote "tokens" "create" --scope operator`,
		// The program can come by path or behind a wrapper, and anywhere in a line.
		"BUCKLEY goal approve run-1",
		"/usr/local/bin/buckley goal approve run-1",
		"/home/user/.local/bin/buckley remote login --url https://b.example",
		"./buckley remote tokens create --scope operator",
		"MODEL=x buckley goal approve run-1",
		"env HOME=/srv buckley goal approve run-1",
		"sudo -u worker buckley goal approve run-1",
		"sudo -u worker buckley remote login --url https://b.example",
		"timeout 5 buckley remote tokens revoke --id tok-1",
		"nohup buckley goal approve run-1 &",
		"cd repo && buckley goal approve run-1",
		"go test ./...; buckley remote tokens revoke --id tok-1",
		"echo ok | buckley goal approve run-1",
		"codex exec task; buckley remote login --url https://b.example",
		"(buckley goal approve run-1)",
		"if true; then buckley remote login --url https://b.example; fi",
	} {
		if !writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = false", command)
		}
	}
}

// buckley takes -c/--config and the flags without a value from anywhere on the
// line, and the other startup flags before the subcommand. They can sit before
// the words and between them.
func TestWritesBuckleyState_StartupFlagsAroundStateCommands(t *testing.T) {
	for _, command := range []string{
		"buckley -c x.yaml goal approve run-1",
		"buckley --config x.yaml remote login --url https://b.example",
		"buckley --config=x.yaml goal approve run-1",
		"buckley --quiet remote tokens create --scope operator",
		"buckley --model m --max-elapsed 5m goal approve run-1",
		"buckley goal --quiet approve run-1",
		"buckley goal -q approve run-1",
		"buckley goal --no-color approve run-1",
		"buckley goal -c x.yaml approve run-1",
		"buckley goal --config x.yaml approve run-1",
		"buckley goal --config=x.yaml approve run-1",
		"buckley remote --quiet login --url https://b.example",
		"buckley remote -c x.yaml login --url https://b.example",
		"buckley remote --quiet tokens create --scope operator",
		"buckley remote tokens --quiet create --scope operator",
		"buckley remote tokens -c x.yaml revoke --id tok-1",
		"buckley remote tokens --config=x.yaml revoke --id tok-1",
		"buckley -c a.yaml remote -c b.yaml tokens --quiet -c c.yaml create --scope operator",
		`buckley -c "$CFG" goal -c "$CFG" approve run-1`,
	} {
		if !writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = false", command)
		}
	}
}

// The text cannot show what a word written as an expansion becomes, so it
// counts as the word a state command needs.
func TestWritesBuckleyState_ExpandedStateCommandWordIsAWrite(t *testing.T) {
	for _, command := range []string{
		"buckley goal $SUB run-1",
		`buckley goal "$SUB" run-1`,
		`buckley goal "$(echo approve)" run-1`,
		"buckley goal $(echo approve) run-1",
		"buckley goal `echo approve` run-1",
		"buckley goal ${SUB:-approve} run-1",
		"buckley goal {approve,status} run-1",
		"buckley goal appr* run-1",
		"buckley remote $SUB --url https://b.example",
		`buckley remote "$(echo login)" --url https://b.example`,
		"buckley remote tokens $SUB --scope operator",
		`buckley remote tokens "$(echo create)" --scope operator`,
		"buckley remote tokens ${SUB:-revoke} --id tok-1",
		"buckley remote tokens {create,list} --scope operator",
		// Startup flags and their values come first.
		"buckley goal --quiet $SUB run-1",
		"buckley goal -c x.yaml $SUB run-1",
		`buckley -c "$CFG" goal $SUB run-1`,
		"buckley remote --quiet $SUB --url https://b.example",
		"buckley remote tokens -c x.yaml $SUB --id tok-1",
		`buckley remote -c "$CFG" tokens -c "$CFG" "$(echo create)"`,
		"sudo -u worker buckley goal $SUB run-1",
		// A flag word with an expansion may become -c and take the next word as
		// its value, so the words after it are not the ones buckley reads.
		"buckley -c$(:) x.yaml goal approve run-1",
		"buckley -c`:` x.yaml goal approve run-1",
		"buckley --config$EMPTY x.yaml remote tokens create --scope operator",
		`buckley -c"$E" x.yaml remote login --url https://b.example`,
		"buckley goal -c$(:) x.yaml approve run-1",
		"buckley remote tokens -c$(:) x.yaml revoke --id tok-1",
		`buckley trust -c$(:) x.yaml "$V" .`,
		"buckley trust -c${E} x.yaml allow-later .",
		`buckley -c$(:) x.yaml trust "$V" .`,
	} {
		if !writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = false", command)
		}
	}
}

// A backslash and newline, with indentation after it, is only blank space to the
// shell, so it must not hide the next word.
func TestWritesBuckleyState_LineContinuationBetweenWords(t *testing.T) {
	for _, command := range []string{
		"buckley goal \\\n  approve run-1",
		"buckley remote \\\n  login --url https://b.example",
		"buckley remote tokens \\\n  create --scope operator",
		"buckley remote \\\n  tokens \\\n  revoke --id tok-1",
		"buckley -c \\\n  x.yaml goal approve run-1",
		"buckley goal -c \\\n  x.yaml approve run-1",
		"buckley \\\n  $SUB approve run-1",
		"buckley goal \\\n  $SUB run-1",
		"buckley trust -c \\\n  x.yaml \"$VERB\" .",
		"buckley trust \\\n  allow .",
	} {
		if !writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = false", command)
		}
	}
	for _, command := range []string{
		"buckley goal \\\n  status run-1",
		"buckley remote tokens \\\n  list --url https://b.example",
		"buckley -c \\\n  approve goal status run-1",
		"buckley commit \\\n  -m \"$MSG\"",
	} {
		if writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = true", command)
		}
	}
}

func TestWritesBuckleyState_OtherCommandsAreNotWrites(t *testing.T) {
	for _, command := range []string{
		"buckley commit --yes --minimal-output",
		"buckley pr --dry-run",
		"buckley --version",
		"buckley trust",
		"buckley trust status",
		"buckley trust status .",
		`buckley trust status "$PWD"`,
		"buckley trust status $(pwd)",
		"buckley trust --help",
		"buckley trust help",
		"buckley config show",
		"buckley commit --hint trust",
		`buckley -p "run buckley trust allow ."`,
		`buckley commit -m "trust allow"`,
		"codex exec task",
		"codex trust allow .",
		"echo buckley trust allow .",
		"printf 'buckley trust allow .'",
		"rg 'trust allow' docs",
		"go test ./cmd/buckley -run Trust",
		"git diff -- cmd/buckley/project_trust.go",
		"# buckley trust allow .",
		"goose up",
		// An expansion in a flag value is not an unreadable subcommand.
		`buckley commit -m "$MSG"`,
		`buckley -p "$(cat t.md)"`,
		"buckley -p $(cat t.md)",
		`buckley --model "$MODEL" -p "fix it"`,
		`buckley -m "$MODEL" commit`,
		`buckley --config "$CFG" doctor`,
		`buckley --max-elapsed "$LIMIT" --budget "$BUDGET" -p "fix it"`,
		`buckley pr --title "$TITLE" --body "$BODY"`,
		`buckley trust --config "$CFG" status`,
		`buckley trust -c "$CFG" status "$PWD"`,
		`buckley trust --quiet status ${PROJECT:-.}`,
		// A flag word with an expansion after a subcommand that writes nothing.
		`buckley pr --title="$TITLE"`,
		`buckley commit --hint=$(git log -1 --format=%s)`,
		`buckley trust status --format=$FMT`,
	} {
		if writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = true", command)
		}
	}
}

// These goal and remote commands read or run work; they approve nothing and
// store no credentials.
func TestWritesBuckleyState_GoalAndRemoteReadsAreNotWrites(t *testing.T) {
	for _, command := range []string{
		"buckley goal status run-1",
		"buckley goal status --watch run-1",
		"buckley goal list",
		"buckley goal report run-1",
		"buckley goal run --backend claude run-1",
		`buckley goal start "fix the flaky test"`,
		"buckley goal preflight",
		"buckley goal audit run-1",
		"buckley goal replay --json run-1",
		"buckley goal worker --endpoint localhost:50001",
		"buckley remote attach --url https://b.example --session s1",
		"buckley remote sessions --url https://b.example",
		"buckley remote console --url https://b.example --session s1",
		"buckley remote tokens list --url https://b.example",
		// The subcommand is the word after the program, not any later word.
		"buckley goal start approve",
		`buckley goal start "approve the release"`,
		"buckley goal status approve",
		"buckley remote attach --url https://b.example --session login",
		"buckley remote tokens list --url https://b.example --token create",
		// Startup flags before and between the words do not change that.
		"buckley -c x.yaml goal status run-1",
		"buckley --quiet remote sessions --url https://b.example",
		"buckley goal --quiet list",
		"buckley goal -c x.yaml report run-1",
		"buckley remote -c x.yaml attach --url https://b.example --session s1",
		"buckley remote tokens --quiet list --url https://b.example",
		"buckley remote tokens -c x.yaml list --url https://b.example",
		// The value of -c/--config is not a subcommand word, whatever it says.
		"buckley goal -c approve status run-1",
		"buckley remote -c login sessions --url https://b.example",
		"buckley remote tokens --config create list --url https://b.example",
		// An expansion in a flag value or an argument is not an unreadable word.
		`buckley goal status "$RUN"`,
		`buckley goal run --backend "$BACKEND" "$RUN"`,
		`buckley goal start "$STATEMENT"`,
		`buckley goal -c "$CFG" status run-1`,
		`buckley goal --config "$CFG" report run-1`,
		`buckley remote -c "$CFG" sessions --url "$URL"`,
		`buckley remote attach --url "$URL" --session "$SESSION"`,
		`buckley remote tokens list --url "$URL" --token "$TOKEN"`,
		// No subcommand, or only a request for help.
		"buckley goal",
		"buckley remote",
		"buckley remote tokens",
		"buckley goal --help",
		"buckley remote tokens --help",
		// References, other programs, and other buckley commands.
		"codex goal approve run-1",
		"codex remote login --url https://b.example",
		"echo buckley goal approve run-1",
		"printf 'buckley remote login'",
		"rg 'goal approve' docs",
		"git diff -- cmd/buckley/goal.go",
		"# buckley goal approve run-1",
		`buckley commit -m "approve the goal"`,
		"buckley commit --hint remote",
		`buckley -p "goal approve run-1"`,
		`buckley pr --title "remote login" --body "$BODY"`,
		"buckley --model goal commit",
	} {
		if writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = true", command)
		}
	}
}

// <(...) and >(...) are words too. They give a file name, so a flag takes one as
// its value.
func TestWritesBuckleyState_ProcessSubstitutionIsOneWord(t *testing.T) {
	for _, command := range []string{
		"buckley -c <(echo x) goal approve run-1",
		"buckley --config <(echo x; echo y) remote login --url https://b.example",
		"buckley -c >(cat) goal approve run-1",
		"buckley -m <(true | cat) remote tokens revoke --url https://b.example --id tok-1",
		"buckley trust -c <(echo x) allow .",
		"buckley --config=<(echo x) goal approve run-1",
	} {
		if !writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = false", command)
		}
	}
	for _, command := range []string{
		"buckley -c <(echo x) goal status run-1",
		"buckley -p <(echo x) commit",
		"buckley trust -c <(echo x) status",
	} {
		if writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = true", command)
		}
	}
}

// Outside quotes, the text inside a $(...), ${...}, <(...) or >(...) is a command
// list of its own.
func TestWritesBuckleyState_SubstitutionHoldsCommands(t *testing.T) {
	for _, command := range []string{
		"echo $(buckley goal approve run-1)",
		"X=$(buckley remote login --url https://b.example)",
		"echo ${X:-$(buckley remote tokens create --scope operator)}",
		"cat <(buckley trust allow .)",
		"echo $(echo $(buckley goal approve run-1))",
		"buckley -c $(buckley goal approve run-1) status",
		"echo $(true; buckley trust allow .)",
		"echo $( buckley goal approve run-1 )",
	} {
		if !writesBuckleyState(command) {
			t.Errorf("writesBuckleyState(%q) = false", command)
		}
	}
}

// A substitution that nests too deep for the reader counts as a match, which is
// the safe error, and the check still ends quickly.
func TestShellReader_DeeplyNestedSubstitutionIsAMatch(t *testing.T) {
	for _, nest := range []string{"$(echo ", "<(echo ", "${X:-$(echo "} {
		deep := "echo " + strings.Repeat(nest, 200) + "x"
		if got := ClassifyShellCommand(deep); got != CommandClassAgentHarness {
			t.Errorf("ClassifyShellCommand(200 levels of %q) = %q", nest, got)
		}
		if !writesBuckleyState(deep) {
			t.Errorf("writesBuckleyState(200 levels of %q) = false", nest)
		}
	}
}

// A long run of expansions that never close, inside double quotes, must not
// make the check slow. Each one reads to the end of the text, and a limit on that
// work keeps the total linear: 40 KB used to take about half a minute.
func TestShellReader_UnclosedExpansionsInQuotesTakeLinearTime(t *testing.T) {
	for _, open := range []string{"$(", "${", "`", "$(a ${b `"} {
		command := "echo \"" + strings.Repeat(open, 40_000/len(open))
		start := time.Now()
		ClassifyShellCommand(command)
		writesBuckleyState(command)
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("reading %d bytes of %q took %v", len(command), open, took)
		}
	}
}

// The reader never panics and never stops short, whatever the text.
func FuzzWritesBuckleyState(f *testing.F) {
	for _, seed := range []string{
		"buckley goal approve run-1",
		`buckley -c "" goal approve run-1`,
		"buckley -c $(echo x) goal approve run-1",
		"buckley -c a\\ #b goal approve run-1",
		"buckley goal app\\\nrove run-1",
		"buckley goal >&2 approve run-1",
		`echo $'a\'b'; buckley trust allow .`,
		"echo `echo \\`x\\``",
		"cat <<EOF\nit's\nEOF\nbuckley goal approve run-1",
		"${",
		"$(",
		"`",
		"\"$(",
		"'",
		"<(",
		">&",
		"&>",
		"\\",
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, command string) {
		// A state write is an agent launch, so the class must follow.
		if writesBuckleyState(command) && ClassifyShellCommand(command) != CommandClassAgentHarness {
			t.Fatalf("writesBuckleyState(%q) is true but ClassifyShellCommand is empty", command)
		}
	})
}
