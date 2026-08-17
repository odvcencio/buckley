package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/policy"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/types"
)

func TestNewPermissionMiddleware_AskApprovalHandlerGatesExecution(t *testing.T) {
	var called int
	var observed PermissionApprovalRequest
	gate := &PermissionGate{
		WorkspaceRoot: "/workspace",
		Posture:       policy.PostureInteractive,
		ApprovalHandler: func(_ context.Context, req PermissionApprovalRequest) (PermissionApprovalResponse, error) {
			observed = req
			return PermissionApprovalResponse{Approved: true}, nil
		},
		RequireApproval: true,
	}
	exec := NewPermissionMiddleware(gate)(func(_ *ExecutionContext) (*builtin.Result, error) {
		called++
		return &builtin.Result{Success: true}, nil
	})
	result, err := exec(&ExecutionContext{
		Context:  context.Background(),
		ToolName: "run_shell",
		CallID:   "call-ask",
		Params:   map[string]any{"command": "rm -rf /etc/passwd"},
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("approved execution result=%#v err=%v", result, err)
	}
	if called != 1 {
		t.Fatalf("next called %d times, want 1", called)
	}
	if observed.ID != "call-ask" || observed.Tool != "run_shell" || observed.Scope == "" {
		t.Fatalf("unexpected structured approval request: %+v", observed)
	}
}

func TestNewPermissionMiddleware_AskApprovalRejectionNeverExecutes(t *testing.T) {
	called := false
	gate := &PermissionGate{
		WorkspaceRoot: "/workspace",
		Posture:       policy.PostureInteractive,
		ApprovalHandler: func(context.Context, PermissionApprovalRequest) (PermissionApprovalResponse, error) {
			return PermissionApprovalResponse{}, nil
		},
		RequireApproval: true,
	}
	exec := NewPermissionMiddleware(gate)(func(_ *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})
	result, err := exec(&ExecutionContext{
		ToolName: "run_shell",
		Params:   map[string]any{"command": "rm -rf /etc/passwd"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called || result == nil || result.Success || result.Data["approval"] != "denied" {
		t.Fatalf("rejected execution called=%v result=%#v", called, result)
	}
}

func TestNewPermissionMiddleware_AskWithoutSurfaceFailsClosedWhenRequired(t *testing.T) {
	called := false
	gate := &PermissionGate{
		WorkspaceRoot:   "/workspace",
		Posture:         policy.PostureInteractive,
		RequireApproval: true,
	}
	exec := NewPermissionMiddleware(gate)(func(_ *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})
	result, err := exec(&ExecutionContext{
		ToolName: "run_shell",
		Params:   map[string]any{"command": "rm -rf /etc/passwd"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called || result == nil || result.Success || result.Data["approval"] != "unavailable" {
		t.Fatalf("missing-surface execution called=%v result=%#v", called, result)
	}
}

func TestPermissionApprovalScope_UsesStableRuleAndFailsSafeWithoutOne(t *testing.T) {
	stable := policy.PermissionDecision{
		Action: policy.PermissionAsk,
		Layer:  "built-in defaults (arbiter)",
		Rule: policy.PermissionRule{
			ID:                   "builtin.ask-shell-rm-rf",
			Tool:                 "run_shell",
			ArgPattern:           "*rm -rf*",
			Action:               policy.PermissionAsk,
			OutsideWorkspaceOnly: true,
		},
		Matched: true,
	}
	first := policy.PermissionRequest{Tool: "run_shell", Category: "shell", Arg: "rm -rf /one", Posture: policy.PostureInteractive}
	second := first
	second.Arg = "rm -rf /two"
	firstScope := permissionApprovalScope(&ExecutionContext{ToolName: "run_shell"}, first, stable)
	secondScope := permissionApprovalScope(&ExecutionContext{ToolName: "run_shell"}, second, stable)
	if firstScope != secondScope || !strings.HasPrefix(firstScope, "permission-scope:v1:") {
		t.Fatalf("stable governed rule scopes differ: %q != %q", firstScope, secondScope)
	}

	missingRule := policy.PermissionDecision{Action: policy.PermissionAsk, Layer: "external evaluator", Matched: true}
	if permissionApprovalScope(nil, first, missingRule) == permissionApprovalScope(nil, second, missingRule) {
		t.Fatal("unidentified asks shared an always-allow scope")
	}
}

func TestDerivePermissionRequest_RunShell(t *testing.T) {
	req, ok := derivePermissionRequest("run_shell", map[string]any{"command": "rm -rf ./tmp"}, "/workspace", "interactive")
	if !ok {
		t.Fatal("expected run_shell to derive a request")
	}
	if req.Tool != "run_shell" || req.Category != "shell" || req.Arg != "rm -rf ./tmp" {
		t.Fatalf("unexpected request: %+v", req)
	}
	if !req.WorkspaceRelative {
		t.Fatalf("expected a relative-path command to be workspace-relative, got %+v", req)
	}
}

func TestDerivePermissionRequest_RunShellOutsideWorkspace(t *testing.T) {
	req, ok := derivePermissionRequest("run_shell", map[string]any{"command": "rm -rf /etc/passwd"}, "/workspace", "interactive")
	if !ok {
		t.Fatal("expected run_shell to derive a request")
	}
	if req.WorkspaceRelative {
		t.Fatalf("expected an absolute path outside the workspace to be non-relative, got %+v", req)
	}
}

func TestDerivePermissionRequest_RunShellEmptyCommand(t *testing.T) {
	if _, ok := derivePermissionRequest("run_shell", map[string]any{"command": "   "}, "/workspace", ""); ok {
		t.Fatal("expected an empty command not to derive a request")
	}
}

func TestDerivePermissionRequest_FileTool(t *testing.T) {
	req, ok := derivePermissionRequest("read_file", map[string]any{"path": "/workspace/.env"}, "/workspace", "interactive")
	if !ok {
		t.Fatal("expected read_file to derive a request")
	}
	if req.Category != string(policy.CategoryFileRead) {
		t.Fatalf("expected file_read category, got %q", req.Category)
	}
	if !req.WorkspaceRelative {
		t.Fatalf("expected a path under the workspace to be relative, got %+v", req)
	}
}

func TestDerivePermissionRequest_WriteToolCategory(t *testing.T) {
	req, ok := derivePermissionRequest("write_file", map[string]any{"path": "notes.txt"}, "", "")
	if !ok {
		t.Fatal("expected write_file to derive a request")
	}
	if req.Category != string(policy.CategoryFileWrite) {
		t.Fatalf("expected file_write category, got %q", req.Category)
	}
}

func TestDerivePermissionRequest_NoRelevantArg(t *testing.T) {
	if _, ok := derivePermissionRequest("list_directory", map[string]any{}, "", ""); ok {
		t.Fatal("expected a tool with no path/command argument not to derive a request")
	}
}

func TestNewPermissionMiddleware_NilGatePassesThrough(t *testing.T) {
	called := false
	mw := NewPermissionMiddleware(nil)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})
	if _, err := exec(&ExecutionContext{ToolName: "run_shell", Params: map[string]any{"command": "rm -rf /"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected next to be called when gate is nil")
	}
}

func TestNewPermissionMiddleware_DenyBlocksExecution(t *testing.T) {
	called := false
	gate := &PermissionGate{Posture: "interactive"}
	mw := NewPermissionMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})
	result, err := exec(&ExecutionContext{
		ToolName: "read_file",
		Params:   map[string]any{"path": ".env"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("expected next not to be called for a denied .env read")
	}
	if result == nil || result.Success {
		t.Fatalf("expected a denial result, got %#v", result)
	}
	if !strings.Contains(result.Error, "permission denied") {
		t.Fatalf("expected a permission-denied error, got %q", result.Error)
	}
}

func TestNewPermissionMiddleware_AskParksUnderUnattendedPosture(t *testing.T) {
	sink := policy.NewParkedDecisionLog()
	gate := &PermissionGate{
		WorkspaceRoot:    "/workspace",
		Posture:          policy.PostureUnattended,
		ParkAskDecisions: true,
		ParkedSink:       sink,
	}
	called := false
	mw := NewPermissionMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})

	result, err := exec(&ExecutionContext{
		ToolName: "run_shell",
		CallID:   "call-1",
		Params:   map[string]any{"command": "rm -rf /etc/passwd"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("expected next not to be called for a parked ask decision")
	}
	if result == nil || result.Success {
		t.Fatalf("expected a non-success parked result, got %#v", result)
	}
	parked, _ := result.Data["parked"].(bool)
	if !parked {
		t.Fatalf("expected result.Data[parked]=true, got %#v", result.Data)
	}

	items := sink.List()
	if len(items) != 1 {
		t.Fatalf("expected one parked decision, got %d", len(items))
	}
	if items[0].Tool != "run_shell" || items[0].ID != "call-1" {
		t.Fatalf("unexpected parked decision: %+v", items[0])
	}
}

func TestNewPermissionMiddleware_AskPassesThroughWhenNotParking(t *testing.T) {
	gate := &PermissionGate{WorkspaceRoot: "/workspace", Posture: policy.PostureInteractive, ParkAskDecisions: false}
	called := false
	mw := NewPermissionMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})

	// A destructive command outside the workspace triggers the built-in
	// "ask" rule, but the interactive posture (ParkAskDecisions=false) must
	// defer to the existing approval chain rather than blocking here.
	if _, err := exec(&ExecutionContext{
		ToolName: "run_shell",
		Params:   map[string]any{"command": "rm -rf /etc/passwd"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected next to be called when the posture doesn't park ask decisions")
	}
}

func TestNewPermissionMiddleware_AllowPassesThrough(t *testing.T) {
	gate := &PermissionGate{Posture: policy.PostureInteractive}
	called := false
	mw := NewPermissionMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})
	if _, err := exec(&ExecutionContext{
		ToolName: "run_shell",
		Params:   map[string]any{"command": "go test ./..."},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected next to be called for an unmatched command")
	}
}

func TestNewPermissionMiddleware_PostureLayerComposesWithBuiltins(t *testing.T) {
	sink := policy.NewParkedDecisionLog()
	gate := &PermissionGate{
		Layers:           []policy.PermissionLayer{policy.UnattendedPostureLayer()},
		Posture:          policy.PostureUnattended,
		ParkAskDecisions: true,
		ParkedSink:       sink,
	}
	mw := NewPermissionMiddleware(gate)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{Success: true}, nil
	})

	result, err := exec(&ExecutionContext{
		ToolName: "run_shell",
		Params:   map[string]any{"command": "git push origin main"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected git push to be parked under the unattended posture layer, got %#v", result)
	}
	parked, _ := result.Data["parked"].(bool)
	if !parked {
		t.Fatalf("expected a parked result, got %#v", result)
	}
}

func TestEmptyWorkspaceRootTreatsPathsAsOutside(t *testing.T) {
	if isWorkspaceRelative("/etc/passwd", "") {
		t.Fatal("empty workspace root must not classify paths as workspace-relative")
	}
	if isWorkspaceRelative("relative/file.go", "  ") {
		t.Fatal("blank workspace root must not classify paths as workspace-relative")
	}
}

func TestIsWorkspaceRelativeResolvesAgainstWorkspaceRoot(t *testing.T) {
	workspaceRoot := t.TempDir()
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "relative path inside workspace", path: "nested/file.go", want: true},
		{name: "relative traversal outside workspace", path: "../outside.txt", want: false},
		{name: "absolute path inside workspace", path: filepath.Join(workspaceRoot, "nested", "file.go"), want: true},
		{name: "absolute path outside workspace", path: filepath.Join(filepath.Dir(workspaceRoot), "outside.txt"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isWorkspaceRelative(tt.path, workspaceRoot); got != tt.want {
				t.Fatalf("isWorkspaceRelative(%q, %q) = %t, want %t", tt.path, workspaceRoot, got, tt.want)
			}
		})
	}
}

func TestWorkspaceContainmentRejectsSymlinkEscapes(t *testing.T) {
	workspaceRoot := t.TempDir()
	outsideRoot := t.TempDir()
	link := filepath.Join(workspaceRoot, "link")
	if err := os.Symlink(outsideRoot, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	for _, path := range []string{
		link,
		filepath.Join(link, "existing-or-new.txt"),
		filepath.Join(workspaceRoot, "link", "missing", "leaf.txt"),
	} {
		if isWorkspaceRelative(path, workspaceRoot) {
			t.Errorf("symlink escape %q was classified as workspace-relative", path)
		}
	}

	for _, command := range []string{
		"rm -rf " + link + "/missing.txt",
		"rm -rf link/missing.txt",
		"rm -rf link/missing/leaf.txt",
	} {
		if isShellCommandWorkspaceRelative(command, workspaceRoot) {
			t.Errorf("symlink escape command %q was classified as workspace-relative", command)
		}
	}

	spacedLink := filepath.Join(workspaceRoot, "out link")
	if err := os.Symlink(outsideRoot, spacedLink); err != nil {
		t.Fatalf("create spaced symlink: %v", err)
	}
	for _, command := range []string{
		`rm -rf "out link/file"`,
		`rm -rf out\ link/file`,
		`/bin/rm -rf -- "out link/missing/leaf.txt"`,
	} {
		if isShellCommandWorkspaceRelative(command, workspaceRoot) {
			t.Errorf("quoted or escaped symlink command %q was classified as workspace-relative", command)
		}
	}
}

func TestWorkspaceContainmentRejectsWindowsAbsolutePaths(t *testing.T) {
	workspaceRoot := t.TempDir()
	for _, path := range []string{`C:\outside`, `C:/outside`, `\\server\share\outside`, `//server/share/outside`} {
		if isWorkspaceRelative(path, workspaceRoot) {
			t.Errorf("Windows absolute path %q was classified as workspace-relative", path)
		}
		if isShellCommandWorkspaceRelative("rm -rf "+path, workspaceRoot) {
			t.Errorf("Windows absolute command operand %q was classified as workspace-relative", path)
		}
	}
}

func TestShellContainmentInspectsOperandsNotExecutableOrOptions(t *testing.T) {
	workspaceRoot := t.TempDir()
	for _, command := range []string{
		`/bin/rm -rf safe/file.txt`,
		`/bin/rm --recursive --force --preserve-root=all "safe dir/file.txt"`,
		`/bin/rm -rf -v -- -literal-name`,
	} {
		if !isShellCommandWorkspaceRelative(command, workspaceRoot) {
			t.Errorf("inside-workspace command %q was classified as outside", command)
		}
	}

	if isShellCommandWorkspaceRelative(`/bin/rm -rf /outside/file.txt`, workspaceRoot) {
		t.Fatal("outside operand was hidden by an absolute executable")
	}
	if isShellCommandWorkspaceRelative(`rm -rf "unterminated`, workspaceRoot) {
		t.Fatal("ambiguous shell quoting must fail closed")
	}
	if isShellCommandWorkspaceRelative(`rm -rf safe/*`, workspaceRoot) {
		t.Fatal("unresolved shell expansion must fail closed")
	}
}

func TestShellContainmentRejectsNestedExecution(t *testing.T) {
	workspaceRoot := t.TempDir()
	for _, command := range []string{
		`sh -c 'rm -rf /outside'`,
		`bash -lc "rm -rf /outside"`,
		`zsh -c rm\ -rf\ /outside`,
		`eval 'rm -rf /outside'`,
		`ssh example.test 'rm -rf /outside'`,
		`env sh -c 'rm -rf /outside'`,
		`/usr/bin/env bash -lc "rm -rf /outside"`,
		`sudo zsh -c 'rm -rf /outside'`,
		`/usr/bin/sudo sh -c 'rm -rf /outside'`,
		`command git push origin main --force`,
		`/usr/bin/command -- sh -c 'rm -rf /outside'`,
		`command nohup git push origin main --force`,
		`nohup sh -c 'rm -rf /outside'`,
		`/usr/bin/nohup -- git push origin main --force`,
		`xargs git push origin main --force`,
		`xargs -n 1 sh -c 'rm -rf /outside'`,
		`command -v git`,
		`nohup --help git status`,
	} {
		if isShellCommandWorkspaceRelative(command, workspaceRoot) {
			t.Errorf("nested command %q was classified as workspace-relative", command)
		}
	}

	for _, command := range []string{
		`command git status`,
		`command -p -- git add safe/file.txt`,
		`nohup go test ./...`,
		`/usr/bin/nohup -- git diff -- safe/file.txt`,
	} {
		if !isShellCommandWorkspaceRelative(command, workspaceRoot) {
			t.Errorf("bounded local wrapper %q was classified as outside", command)
		}
	}
}

func TestShellContainmentRejectsRemoteOperandsAndNetworkLaunchers(t *testing.T) {
	workspaceRoot := t.TempDir()
	for _, command := range []string{
		"curl https://example.test/archive.tar.gz",
		"wget http://example.test/install.sh",
		"ssh build.example.test ./deploy.sh",
		"scp ./artifact.tgz deploy@example.test:/srv/artifact.tgz",
		"rsync ./dist/ deploy@example.test:/srv/dist/",
		"git clone git@github.com:owner/repo.git",
		"git fetch origin",
		"cat https://example.test/secret",
		"echo git@github.com:owner/repo.git",
	} {
		if isShellCommandWorkspaceRelative(command, workspaceRoot) {
			t.Errorf("remote/network command %q was classified as workspace-relative", command)
		}
	}
}

func TestShellContainmentPreservesLocalPathsAndWindowsDriveSyntax(t *testing.T) {
	workspaceRoot := t.TempDir()
	for _, command := range []string{
		"go test ./...",
		"cat ./notes.txt",
		"echo local:name",
		`type C:\\workspace\\notes.txt`,
	} {
		if command == `type C:\\workspace\\notes.txt` {
			// A foreign Windows absolute path cannot be proven inside this
			// Unix workspace, but it must not be mistaken for a URI/remote.
			if shellTokenIsRemote(`C:\\workspace\\notes.txt`) {
				t.Errorf("Windows drive operand was classified as remote")
			}
			continue
		}
		if !isShellCommandWorkspaceRelative(command, workspaceRoot) {
			t.Errorf("ordinary local command %q was classified as outside", command)
		}
	}
	for _, token := range []string{`C:\\workspace\\notes.txt`, `C:/workspace/notes.txt`, `./dir/name:with-colon`} {
		if shellTokenIsRemote(token) {
			t.Errorf("local token %q was classified as remote", token)
		}
	}
}

func TestShellContainmentRejectsGitPushButPreservesLocalGit(t *testing.T) {
	workspaceRoot := t.TempDir()
	for _, command := range []string{
		`git push origin main --force`,
		`git push -f origin main`,
		`/usr/bin/git push origin main --force`,
		`git -C . push origin main -f`,
		`git -c k=v push origin main --force`,
		`git --no-pager push origin main --force`,
		`env git push origin main --force`,
		`/usr/bin/env git push origin main --force`,
		`sudo git push origin main -f`,
		`/usr/bin/sudo git push origin main -f`,
		`git --unknown-global-option push origin main`,
		`git -C`,
		`git -c push`,
	} {
		if isShellCommandWorkspaceRelative(command, workspaceRoot) {
			t.Errorf("external git command %q was classified as workspace-relative", command)
		}
	}

	for _, command := range []string{
		`git status`,
		`git add safe/file.txt`,
		`git diff -- safe/file.txt`,
		`git add push`,
		`git diff -- push`,
		`git commit -m push`,
		`/usr/bin/git add push`,
		`git --no-pager add push`,
	} {
		if !isShellCommandWorkspaceRelative(command, workspaceRoot) {
			t.Errorf("local git command %q was classified as outside", command)
		}
	}
}

func TestPermissionMiddlewareWrapperAndControlCommandsUseGovernedPolicy(t *testing.T) {
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("rules.NewEngine: %v", err)
	}
	evaluators := []struct {
		name      string
		evaluator types.RuleEvaluator
	}{
		{name: "go glob fallback"},
		{name: "arbiter engine", evaluator: rules.NewEngineAdapter(engine)},
	}
	commands := []string{
		`env sh -c 'rm -rf /outside'`,
		`/usr/bin/sudo sh -c 'rm -rf /outside'`,
		`env git push origin main --force`,
		`/usr/bin/sudo git push -f origin main`,
		`command sh -c 'rm -rf /outside'`,
		`nohup git push origin main --force`,
		`xargs sh -c 'rm -rf /outside'`,
		"rm -rf /outside\n:",
		"rm -rf /outside\r:",
		"rm -rf /outside\x00:",
	}

	for _, strategy := range evaluators {
		strategy := strategy
		t.Run(strategy.name, func(t *testing.T) {
			for _, command := range commands {
				command := command
				t.Run(command, func(t *testing.T) {
					called := false
					gate := &PermissionGate{
						WorkspaceRoot:    t.TempDir(),
						Posture:          policy.PostureUnattended,
						ParkAskDecisions: true,
						Evaluator:        strategy.evaluator,
					}
					exec := NewPermissionMiddleware(gate)(func(ctx *ExecutionContext) (*builtin.Result, error) {
						called = true
						return &builtin.Result{Success: true}, nil
					})

					result, err := exec(&ExecutionContext{
						ToolName: "run_shell",
						CallID:   "wrapper-control",
						Params:   map[string]any{"command": command},
					})
					if err != nil {
						t.Fatalf("middleware returned error: %v", err)
					}
					if called {
						t.Fatal("governed ask decision reached downstream execution")
					}
					if result == nil || result.Success || result.Data["parked"] != true {
						t.Fatalf("expected parked governed decision, got %#v", result)
					}
				})
			}
		})
	}
}

func TestPermissionMiddlewareCallerAllowCannotSuppressBuiltinAskInjection(t *testing.T) {
	allowLayer := policy.PermissionLayer{
		Name:  "caller",
		Rules: []policy.PermissionRule{{Tool: "run_shell", ArgPattern: "go test *", Action: policy.PermissionAllow}},
	}
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("rules.NewEngine: %v", err)
	}
	for _, strategy := range []struct {
		name      string
		evaluator types.RuleEvaluator
	}{
		{name: "go fallback"},
		{name: "arbiter", evaluator: rules.NewEngineAdapter(engine)},
	} {
		strategy := strategy
		t.Run(strategy.name, func(t *testing.T) {
			for _, separator := range []string{"\n", "\r", "\x00", ";", "&&", "||"} {
				separator := separator
				t.Run(separator, func(t *testing.T) {
					called := false
					gate := &PermissionGate{
						Layers:           []policy.PermissionLayer{allowLayer},
						WorkspaceRoot:    t.TempDir(),
						Posture:          policy.PostureUnattended,
						ParkAskDecisions: true,
						ParkedSink:       policy.NewParkedDecisionLog(),
						Evaluator:        strategy.evaluator,
					}
					exec := NewPermissionMiddleware(gate)(func(ctx *ExecutionContext) (*builtin.Result, error) {
						called = true
						return &builtin.Result{Success: true}, nil
					})
					result, err := exec(&ExecutionContext{
						ToolName: "run_shell",
						CallID:   "control-injection",
						Params: map[string]any{
							"command": "go test ./..." + separator + "rm -rf /",
						},
					})
					if err != nil {
						t.Fatalf("middleware returned error: %v", err)
					}
					if called || result == nil || result.Success || result.Data["parked"] != true {
						t.Fatalf("caller allow bypassed built-in ask for %q: called=%t result=%#v", separator, called, result)
					}
				})
			}
		})
	}
}

func TestShellContainmentRejectsRawControlSeparators(t *testing.T) {
	workspaceRoot := t.TempDir()
	for _, command := range []string{
		"rm -rf safe/file\nrm -rf /outside",
		"rm -rf safe/file\rrm -rf /outside",
		"rm -rf safe/file\x00/outside",
		"rm -rf safe/file\fsuffix",
		"rm\t-rf safe/file",
	} {
		if isShellCommandWorkspaceRelative(command, workspaceRoot) {
			t.Errorf("command containing raw control %q was classified as workspace-relative", command)
		}
	}

	req, ok := derivePermissionRequest("run_shell", map[string]any{
		"command": "rm -rf safe/file\n",
	}, workspaceRoot, "safe")
	if !ok {
		t.Fatal("expected permission request for non-empty command")
	}
	if req.WorkspaceRelative {
		t.Fatal("trailing raw newline was trimmed before containment inspection")
	}
}

func TestEmptyWorkspaceRootShellCommandsAreOutside(t *testing.T) {
	if isShellCommandWorkspaceRelative("rm -rf /etc/passwd", "") {
		t.Fatal("empty workspace root must not classify shell commands as workspace-relative")
	}
	if isShellCommandWorkspaceRelative("ls", "") {
		t.Fatal("empty workspace root must fail safe for every command")
	}
}

func TestShellTraversalTokensAreOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	if isShellCommandWorkspaceRelative("rm -rf "+root+"/../etc", root) {
		t.Fatal("dot-dot traversal through the workspace root must classify as outside")
	}
	if isShellCommandWorkspaceRelative("cat ../../etc/passwd", root) {
		t.Fatal("relative traversal must classify as outside")
	}
	if !isShellCommandWorkspaceRelative("cat "+root+"/sub/file.go", root) {
		t.Fatal("clean inside path must stay workspace-relative")
	}
}
