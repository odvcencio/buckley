package tool

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/anmitsu/go-shlex"

	"m31labs.dev/buckley/pkg/policy"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/types"
)

// PermissionGate supplies the layered glob-permission configuration
// consulted by NewPermissionMiddleware: the posture/project/user rule
// layers in priority order (posture first), the workspace root used to
// compute whether an argument resolves inside the workspace, the active
// posture name, an optional rules engine for the arbiter-backed built-in
// defaults path, and an optional sink that collects parked "ask" decisions.
type PermissionGate struct {
	// Layers are evaluated in priority order (highest first, e.g. posture,
	// project, user); the built-in defaults layer is always consulted last.
	Layers []policy.PermissionLayer
	// WorkspaceRoot is the absolute path used to compute
	// PermissionRequest.WorkspaceRelative for file and shell arguments.
	WorkspaceRoot string
	// Posture is the active posture name (see policy.SelectPosture).
	Posture string
	// ParkAskDecisions routes "ask" decisions to ParkedSink instead of
	// blocking on human approval. Set for postures with nobody present to
	// answer (e.g. unattended).
	ParkAskDecisions bool
	// Evaluator, when non-nil, lets built-in defaults evaluate via the
	// embedded rules engine; a nil evaluator falls back to the deterministic
	// Go glob layer (see policy.EvaluateBuiltinDefaults).
	Evaluator types.RuleEvaluator
	// ParkedSink receives parked decisions; nil discards them.
	ParkedSink policy.ParkedDecisionSink
	// ApprovalHandler resolves interactive "ask" decisions. When nil, ask
	// decisions preserve the caller's existing approval chain unless
	// RequireApproval is set.
	ApprovalHandler PermissionApprovalHandler
	// RequireApproval makes an interactive ask fail closed when no approval
	// handler is installed. TUI registries set this because they own the
	// approval surface; headless callers leave it false to preserve their
	// runner-owned approval path.
	RequireApproval bool
}

// PermissionApprovalRequest is the structured request passed to an
// interactive approval surface. Scope is derived from the matched policy
// rule, tool category, and workspace classification; it is intentionally
// narrower than a global "allow everything" switch.
type PermissionApprovalRequest struct {
	ID         string
	Tool       string
	Params     map[string]any
	Permission policy.PermissionRequest
	Decision   policy.PermissionDecision
	Scope      string
}

// PermissionApprovalResponse is the result returned by an approval surface.
// AlwaysAllow is advisory to the surface and may only be remembered for the
// request's explicit Scope.
type PermissionApprovalResponse struct {
	Approved    bool
	AlwaysAllow bool
}

// PermissionApprovalHandler resolves one interactive permission request.
// Implementations may block the calling worker while the UI remains
// responsive; the context is cancelled when the tool loop is cancelled.
type PermissionApprovalHandler func(context.Context, PermissionApprovalRequest) (PermissionApprovalResponse, error)

// NewPermissionMiddleware evaluates every tool call against the layered
// glob-permission rules (pkg/policy): posture, project, user, and built-in
// defaults, in that priority order, with built-in defaults always
// consulted. A deny blocks the call outright, in every approval mode. An
// "ask" is parked (see PermissionGate.ParkAskDecisions) under postures
// where nobody is present to answer. Interactive callers may supply an
// ApprovalHandler; callers without one preserve the existing coarser
// approval chain unless RequireApproval asks this middleware to fail closed.
func NewPermissionMiddleware(gate *PermissionGate) Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			if gate == nil || ctx == nil {
				return next(ctx)
			}
			req, ok := derivePermissionRequest(ctx.ToolName, ctx.Params, gate.WorkspaceRoot, gate.Posture)
			if !ok {
				return next(ctx)
			}

			dec := policy.EvaluatePermissionLayersWithBuiltins(gate.Evaluator, req, gate.Layers...)
			switch dec.Action {
			case policy.PermissionDeny:
				return &builtin.Result{
					Success: false,
					Error:   fmt.Sprintf("permission denied by %s rule: %s %q", dec.Layer, ctx.ToolName, req.Arg),
				}, nil
			case policy.PermissionAsk, policy.PermissionPark:
				if gate.ParkAskDecisions || dec.Action == policy.PermissionPark {
					parked := policy.ParkedDecision{
						ID:        ctx.CallID,
						Tool:      ctx.ToolName,
						Arg:       req.Arg,
						Layer:     dec.Layer,
						Rule:      dec.Rule,
						Posture:   gate.Posture,
						CreatedAt: time.Now(),
					}
					if gate.ParkedSink != nil {
						gate.ParkedSink.RecordParkedDecision(parked)
					}
					return &builtin.Result{
						Success: false,
						Error:   fmt.Sprintf("parked under posture %q: %s %q requires approval", gate.Posture, ctx.ToolName, req.Arg),
						Data: map[string]any{
							"parked":  true,
							"posture": gate.Posture,
							"layer":   dec.Layer,
						},
					}, nil
				}

				if gate.ApprovalHandler == nil {
					if gate.RequireApproval {
						return &builtin.Result{
							Success: false,
							Error:   fmt.Sprintf("approval unavailable: no approval surface for %s %q", ctx.ToolName, req.Arg),
							Data: map[string]any{
								"approval": "unavailable",
								"scope":    permissionApprovalScope(ctx, req, dec),
							},
						}, nil
					}
					return next(ctx)
				}

				approvalCtx := context.Background()
				if ctx.Context != nil {
					approvalCtx = ctx.Context
				}
				approval, err := gate.ApprovalHandler(approvalCtx, PermissionApprovalRequest{
					ID:         ctx.CallID,
					Tool:       ctx.ToolName,
					Params:     ctx.Params,
					Permission: req,
					Decision:   dec,
					Scope:      permissionApprovalScope(ctx, req, dec),
				})
				if err != nil {
					return &builtin.Result{
						Success: false,
						Error:   fmt.Sprintf("approval unavailable: %v", err),
						Data: map[string]any{
							"approval": "unavailable",
						},
					}, nil
				}
				if !approval.Approved {
					return &builtin.Result{
						Success: false,
						Error:   fmt.Sprintf("approval denied: %s %q was not approved", ctx.ToolName, req.Arg),
						Data: map[string]any{
							"approval": "denied",
						},
					}, nil
				}
				return next(ctx)
			default:
				return next(ctx)
			}
		}
	}
}

// permissionApprovalScope identifies the policy boundary an "always allow"
// decision may cover. Stable rule metadata lets the UI approve repeated
// operations matching the same governed rule. If an evaluator supplies no
// rule identity or pattern, the concrete argument is included so unrelated
// asks cannot collapse into one remembered approval.
func permissionApprovalScope(ctx *ExecutionContext, req policy.PermissionRequest, dec policy.PermissionDecision) string {
	toolName := req.Tool
	if ctx != nil && strings.TrimSpace(ctx.ToolName) != "" {
		toolName = strings.TrimSpace(ctx.ToolName)
	}
	exactArg := ""
	if strings.TrimSpace(dec.Rule.ID) == "" && strings.TrimSpace(dec.Rule.ArgPattern) == "" {
		exactArg = req.Arg
	}
	fields := []string{
		"permission-scope-v1",
		toolName,
		req.Category,
		req.Posture,
		dec.Layer,
		string(dec.Action),
		dec.Rule.ID,
		dec.Rule.Tool,
		dec.Rule.ArgPattern,
		dec.Rule.CommandClass,
		req.CommandClass,
		strconv.FormatBool(dec.Rule.OutsideWorkspaceOnly),
		strconv.FormatBool(req.WorkspaceRelative),
		exactArg,
	}
	hash := sha256.New()
	var size [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(field))
	}
	return "permission-scope:v1:" + hex.EncodeToString(hash.Sum(nil))
}

// derivePermissionRequest builds a policy.PermissionRequest from a tool
// call, or returns ok=false when the tool carries no argument relevant to
// glob-permission evaluation (command string or file path).
func derivePermissionRequest(toolName string, params map[string]any, workspaceRoot, posture string) (policy.PermissionRequest, bool) {
	toolName = strings.TrimSpace(toolName)

	switch toolName {
	case "run_shell":
		rawCmd, _ := params["command"].(string)
		cmd := strings.TrimSpace(rawCmd)
		if cmd == "" {
			return policy.PermissionRequest{}, false
		}
		return policy.PermissionRequest{
			Tool:              toolName,
			Category:          "shell",
			Arg:               cmd,
			CommandClass:      policy.ClassifyShellCommand(rawCmd),
			WorkspaceRelative: isShellCommandWorkspaceRelative(rawCmd, workspaceRoot),
			Posture:           posture,
		}, true
	case "run_code":
		rawCmd, err := builtin.DynamicCodeCommand(params)
		cmd := strings.TrimSpace(rawCmd)
		if err != nil || cmd == "" {
			return policy.PermissionRequest{}, false
		}
		return policy.PermissionRequest{
			Tool:              toolName,
			Category:          "shell",
			Arg:               cmd,
			WorkspaceRelative: isShellCommandWorkspaceRelative(rawCmd, workspaceRoot),
			Posture:           posture,
		}, true
	}

	path := extractPathArg(params)
	if path == "" {
		return policy.PermissionRequest{}, false
	}
	return policy.PermissionRequest{
		Tool:              toolName,
		Category:          filePathCategory(toolName),
		Arg:               path,
		WorkspaceRelative: isWorkspaceRelative(path, workspaceRoot),
		Posture:           posture,
	}, true
}

// extractPathArg looks up the path argument used by file tools.
func extractPathArg(params map[string]any) string {
	if params == nil {
		return ""
	}
	if p, ok := params["path"].(string); ok && strings.TrimSpace(p) != "" {
		return strings.TrimSpace(p)
	}
	if p, ok := params["file_path"].(string); ok && strings.TrimSpace(p) != "" {
		return strings.TrimSpace(p)
	}
	return ""
}

// filePathCategory classifies a file tool as a read or write category
// based on its name (write/edit/delete/insert/patch/rename/extract/create
// imply a write).
func filePathCategory(toolName string) string {
	name := strings.ToLower(toolName)
	writeMarkers := []string{"write", "edit", "delete", "insert", "patch", "rename", "extract", "create"}
	for _, marker := range writeMarkers {
		if strings.Contains(name, marker) {
			return string(policy.CategoryFileWrite)
		}
	}
	return string(policy.CategoryFileRead)
}

// isWorkspaceRelative reports whether path resolves inside workspaceRoot.
// An empty workspaceRoot means there is no confirmed workspace, so NOTHING
// counts as workspace-relative: OutsideWorkspaceOnly protections must fire
// when the workspace is unknown, never silently stand down (fail-safe).
func isWorkspaceRelative(path, workspaceRoot string) bool {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	path = strings.TrimSpace(path)
	if workspaceRoot == "" || path == "" {
		return false
	}
	// URI and remote-reference operands are not local workspace paths. Keep
	// Windows drive paths distinct from URI schemes (the colon in C:\\... is
	// a drive separator, not a remote destination).
	if shellTokenIsRemote(path) {
		return false
	}
	// filepath.IsAbs follows the host OS. Keep foreign Windows absolute paths
	// outside even when this process is running on Unix, where C:\\... and
	// UNC paths would otherwise be treated as ordinary relative names.
	if isWindowsAbsolutePath(path) && runtime.GOOS != "windows" {
		return false
	}
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(absRoot, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	resolvedRoot, ok := effectivePath(absRoot)
	if !ok {
		return false
	}
	resolvedPath, ok := effectivePath(absPath)
	if !ok {
		return false
	}
	return pathWithinWorkspace(resolvedPath, resolvedRoot)
}

// isShellCommandWorkspaceRelative reports whether cmd appears confined to
// the workspace: it flags a command as non-workspace-relative when it
// references an absolute path (or "~") outside workspaceRoot. This is a
// heuristic (commands can reach outside the workspace in ways a token scan
// can't see), matching the existing approach in pkg/approval.
func isShellCommandWorkspaceRelative(cmd, workspaceRoot string) bool {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		// No confirmed workspace: fail-safe, matching isWorkspaceRelative.
		return false
	}
	if strings.Contains(cmd, "~") {
		return false
	}
	if shellCommandHasRawControl(cmd) {
		return false
	}
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return false
	}
	if shellHasOutsideWindowsOperand(cmd, absRoot) {
		return false
	}

	tokens, err := shlex.Split(cmd, true)
	if err != nil || len(tokens) == 0 {
		return false
	}
	for _, token := range tokens {
		if shellTokenIsAmbiguous(token) {
			return false
		}
	}

	executable := shellExecutableIndex(tokens)
	if executable < 0 {
		return false
	}
	if shellCommandContainmentUnknown(tokens, executable) {
		return false
	}
	options := true
	for _, token := range tokens[executable+1:] {
		if options && token == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(token, "-") && token != "-" {
			continue
		}
		if !isWorkspaceRelative(token, absRoot) {
			return false
		}
	}
	return true
}

// shellCommandHasRawControl rejects control characters before lexical
// splitting can turn them into ordinary whitespace. A command containing a
// hidden separator is not provably confined to one workspace operation.
func shellCommandHasRawControl(cmd string) bool {
	for _, char := range cmd {
		if char < ' ' || char == '\x7f' {
			return true
		}
	}
	return false
}

// shellCommandContainmentUnknown identifies execution forms whose operands
// are commands, remote destinations, or refs rather than filesystem paths.
// The middleware cannot prove workspace containment for these forms, so it
// leaves WorkspaceRelative false for the governed policy layer to evaluate.
func shellCommandContainmentUnknown(tokens []string, executable int) bool {
	return shellCommandContainmentUnknownAt(tokens, executable, 0)
}

const maxShellWrapperDepth = 8

func shellCommandContainmentUnknownAt(tokens []string, executable, depth int) bool {
	if executable < 0 || executable >= len(tokens) || depth >= maxShellWrapperDepth {
		return true
	}
	name := shellExecutableName(tokens[executable])
	if isNetworkLauncher(name) {
		return true
	}
	switch name {
	case "sh", "bash", "zsh", "eval", "ssh", "env", "sudo":
		return true
	case "command", "nohup":
		nested, ok := shellWrappedExecutableIndex(tokens, executable, name)
		if !ok {
			return true
		}
		return shellCommandContainmentUnknownAt(tokens, nested, depth+1)
	case "xargs":
		// xargs can append operands read from stdin that are absent from this
		// command text. No nested target can therefore prove containment.
		return true
	case "git":
		subcommand, ok := gitSubcommand(tokens, executable)
		if !ok {
			return true
		}
		switch subcommand {
		case "push", "clone", "fetch", "pull", "submodule":
			return true
		default:
			return false
		}
	}
	return false
}

// shellWrappedExecutableIndex unwraps the bounded option grammar for wrappers
// that execute one visible command. Unsupported or incomplete options fail
// closed instead of guessing where the nested executable begins.
func shellWrappedExecutableIndex(tokens []string, executable int, wrapper string) (int, bool) {
	for i := executable + 1; i < len(tokens); i++ {
		token := tokens[i]
		if token == "--" {
			i++
			return i, i < len(tokens)
		}
		switch wrapper {
		case "command":
			if token == "-p" {
				continue
			}
		case "nohup":
			// nohup has no execution-affecting short options; its GNU long
			// options terminate without launching the following token.
		}
		if strings.HasPrefix(token, "-") && token != "-" {
			return -1, false
		}
		return i, true
	}
	return -1, false
}

// isNetworkLauncher identifies commands whose operands can leave the local
// workspace even when all visible operands happen to look like local paths.
// The classifier intentionally keeps this list explicit: adding a launcher
// is a policy decision, while ordinary local commands remain path-based.
func isNetworkLauncher(name string) bool {
	switch name {
	case "curl", "wget", "http", "httpie", "aria2c", "ssh", "scp", "sftp", "rsync", "rclone", "ftp", "nc", "ncat", "netcat", "telnet", "socat":
		return true
	default:
		return false
	}
}

// gitSubcommand parses the bounded global-option grammar needed before a Git
// subcommand. Unknown or incomplete options are ambiguous and fail closed.
func gitSubcommand(tokens []string, executable int) (string, bool) {
	for i := executable + 1; i < len(tokens); i++ {
		token := tokens[i]
		switch token {
		case "-C":
			if i+1 >= len(tokens) || tokens[i+1] == "" || strings.HasPrefix(tokens[i+1], "-") {
				return "", false
			}
			i++
		case "-c":
			if i+1 >= len(tokens) || !isGitConfigAssignment(tokens[i+1]) {
				return "", false
			}
			i++
		case "-p", "--paginate", "-P", "--no-pager", "--bare", "--no-replace-objects", "--literal-pathspecs", "--glob-pathspecs", "--noglob-pathspecs", "--icase-pathspecs", "--no-optional-locks":
			continue
		default:
			if strings.HasPrefix(token, "-") {
				return "", false
			}
			return token, true
		}
	}
	return "", true
}

func isGitConfigAssignment(token string) bool {
	equals := strings.IndexByte(token, '=')
	return equals > 0 && equals < len(token)-1
}

func shellExecutableName(token string) string {
	token = strings.ReplaceAll(token, `\`, "/")
	if slash := strings.LastIndexByte(token, '/'); slash >= 0 {
		token = token[slash+1:]
	}
	token = strings.ToLower(token)
	return strings.TrimSuffix(token, ".exe")
}

func shellExecutableIndex(tokens []string) int {
	for i, token := range tokens {
		if !isShellAssignment(token) {
			return i
		}
	}
	return -1
}

func isShellAssignment(token string) bool {
	equals := strings.IndexByte(token, '=')
	if equals <= 0 {
		return false
	}
	for i := 0; i < equals; i++ {
		char := token[i]
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '_' || (i > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

// shellTokenIsAmbiguous rejects shell control syntax and expansions that a
// lexical splitter cannot safely map to filesystem operands. Outside-only
// permission rules must stay active when interpretation is uncertain.
func shellTokenIsAmbiguous(token string) bool {
	return strings.ContainsAny(token, "\r\n;&|<>`$(){}*?[") || strings.HasPrefix(token, "#")
}

// shellTokenIsRemote recognizes URI schemes and scp-style remote operands.
// It deliberately does not treat an arbitrary colon as remote syntax: local
// labels and Windows drive paths are common command operands and should not
// be rejected merely because they contain ':'.
func shellTokenIsRemote(token string) bool {
	token = strings.TrimSpace(strings.Trim(token, `"'`))
	if token == "" || isWindowsAbsolutePath(token) {
		return false
	}
	if strings.Contains(token, "://") {
		return true
	}
	if colon := strings.IndexByte(token, ':'); colon > 0 {
		// URI schemes without // (for example ssh:host or mailto:...) are
		// recognized only from the standard network-capable scheme set.
		scheme := strings.ToLower(token[:colon])
		switch scheme {
		case "http", "https", "ftp", "ftps", "ssh", "scp", "sftp", "rsync", "git", "file", "ws", "wss", "data", "mailto":
			return true
		}
	}
	return isSCPStyleRemote(token)
}

func isSCPStyleRemote(token string) bool {
	if isWindowsAbsolutePath(token) {
		return false
	}
	at := strings.IndexByte(token, '@')
	if at <= 0 {
		return false
	}
	colon := strings.IndexByte(token[at+1:], ':')
	if colon < 1 {
		return false
	}
	colon += at + 1
	if colon+1 >= len(token) || strings.ContainsAny(token[:colon], `/\\`) {
		return false
	}
	for _, part := range []string{token[:at], token[at+1 : colon]} {
		if part == "" || strings.ContainsAny(part, " \t\r\n") {
			return false
		}
	}
	return true
}

// shellHasOutsideWindowsOperand preserves drive and UNC spelling before the
// POSIX lexer consumes backslashes as escapes. It is intentionally limited to
// operands: a Windows-style executable path is not itself a destructive
// target, just as /bin/rm is not.
func shellHasOutsideWindowsOperand(cmd, workspaceRoot string) bool {
	rawTokens := strings.Fields(cmd)
	executableSeen := false
	options := true
	for _, raw := range rawTokens {
		token := strings.Trim(raw, `"'`)
		if token == "" {
			continue
		}
		if !executableSeen {
			if isShellAssignment(token) {
				continue
			}
			executableSeen = true
			continue
		}
		if options && token == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(token, "-") && token != "-" {
			continue
		}
		if isWindowsAbsolutePath(token) && !isWorkspaceRelative(token, workspaceRoot) {
			return true
		}
	}
	return false
}

// isWindowsAbsolutePath recognizes Windows drive-absolute and UNC paths even
// when filepath is operating with Unix semantics. On Windows, filepath.IsAbs
// remains the source of truth so native drive and UNC paths are handled by
// the platform implementation.
func isWindowsAbsolutePath(path string) bool {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return true
	}
	return len(path) >= 3 &&
		((path[0] >= 'a' && path[0] <= 'z') || (path[0] >= 'A' && path[0] <= 'Z')) &&
		path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

// effectivePath resolves the existing portion of path through symlinks and
// appends any missing leaf components. EvalSymlinks alone rejects a
// nonexistent target, which would let a path such as workspace/link/new
// evade containment when link points outside the workspace.
func effectivePath(path string) (string, bool) {
	path = filepath.Clean(path)
	if path == "" {
		return "", false
	}

	var missing []string
	current := path
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", false
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return "", false
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), true
		} else if !os.IsNotExist(err) {
			// Permission, loop, and other lookup errors cannot establish
			// containment. Fail closed instead of guessing.
			return "", false
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func pathWithinWorkspace(path, workspaceRoot string) bool {
	rel, err := filepath.Rel(filepath.Clean(workspaceRoot), filepath.Clean(path))
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
