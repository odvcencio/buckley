package builtin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/sandbox"
)

const (
	interactiveTerminalEnv  = "BUCKLEY_INTERACTIVE_TERMINAL"
	shellDefaultTimeoutEnv  = "BUCKLEY_SHELL_TIMEOUT_SECONDS"
	tmuxWindowName          = "Buckley interactive"
	defaultShellTimeoutSecs = 120
	maxShellTimeoutSecs     = 600
)

// ShellCommandTool runs bash commands (assumed to be sandboxed in container)
type ShellCommandTool struct {
	workDirAware
	containerEnabled bool
	composeFile      string
	containerService string
	containerWorkDir string
	sandboxConfig    sandbox.Config
	sandboxEnabled   bool
}

// ConfigureContainerMode enables docker compose execution for commands.
func (t *ShellCommandTool) ConfigureContainerMode(composeFile, service, workDir string) {
	if t == nil {
		return
	}
	if composeFile == "" {
		t.containerEnabled = false
		t.composeFile = ""
		t.containerService = ""
		t.containerWorkDir = ""
		return
	}
	t.containerEnabled = true
	t.composeFile = composeFile
	t.containerService = service
	if t.containerService == "" {
		t.containerService = "dev"
	}
	t.containerWorkDir = workDir
}

func (t *ShellCommandTool) Name() string {
	return "run_shell"
}

func (t *ShellCommandTool) Description() string {
	return "Execute a shell command and return stdout, stderr, and exit code."
}

func (t *ShellCommandTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"command": {
				Type:        "string",
				Description: "Shell command to execute",
			},
			"timeout_seconds": {
				Type:        "integer",
				Description: "Timeout in seconds (default 120, max 600)",
				Default:     defaultShellTimeoutSeconds(),
			},
			"interactive": {
				Type:        "boolean",
				Description: "Run in interactive terminal mode",
				Default:     false,
			},
		},
		Required: []string{"command"},
	}
}

func defaultShellTimeoutSeconds() int {
	if raw := strings.TrimSpace(os.Getenv(shellDefaultTimeoutEnv)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			if parsed > maxShellTimeoutSecs {
				return maxShellTimeoutSecs
			}
			if parsed > 0 {
				return parsed
			}
		}
	}
	return defaultShellTimeoutSecs
}

// SetSandboxConfig configures command sandboxing.
func (t *ShellCommandTool) SetSandboxConfig(cfg sandbox.Config) {
	if t == nil {
		return
	}
	t.sandboxConfig = cfg
	t.sandboxEnabled = true
}

func (t *ShellCommandTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *ShellCommandTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	cmd, ok := params["command"].(string)
	if !ok || strings.TrimSpace(cmd) == "" {
		return &Result{Success: false, Error: "command parameter must be a non-empty string"}, nil
	}

	defaultTimeout := defaultShellTimeoutSeconds()
	timeout := parseInt(params["timeout_seconds"], defaultTimeout)
	explicitTimeout := params["timeout_seconds"] != nil
	if timeout <= 0 || timeout > maxShellTimeoutSecs {
		timeout = defaultTimeout
	}

	interactive := parseBoolParam(params["interactive"], false)
	if interactive && !explicitTimeout {
		timeout = 0 // allow long-lived interactive sessions unless user overrides
	}

	if t.sandboxEnabled && !t.containerEnabled {
		sandboxCfg := t.sandboxConfigForCommand()
		if err := sandbox.New(sandboxCfg).Validate(cmd); err != nil {
			return &Result{Success: false, Error: "sandbox blocked command: " + err.Error()}, nil
		}
	}

	if interactive {
		if t.containerEnabled {
			return &Result{
				Success: false,
				Error:   "interactive shell sessions are not supported when container execution is enabled",
			}, nil
		}
		if ctx == nil {
			ctx = context.Background()
		}
		info, err := t.runInteractiveCommand(ctx, cmd, timeout)
		if err != nil {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("failed to run interactive command: %v", err),
			}, nil
		}
		note := info.Note
		data := map[string]any{
			"command":     cmd,
			"interactive": true,
			"note":        note,
			"tmux":        info.UsedTmux,
			"launcher":    info.Launcher,
		}
		return &Result{
			Success:       true,
			Data:          data,
			DisplayData:   map[string]any{"message": note},
			ShouldAbridge: true,
		}, nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := timeoutContext(ctx, timeout)
	defer cancel()

	var command *exec.Cmd
	if t.containerEnabled {
		args := []string{"compose", "-f", t.composeFile, "exec", "-T"}
		for _, pair := range envPairs(t.env) {
			args = append(args, "-e", pair)
		}
		service := t.containerService
		if service == "" {
			service = "dev"
		}
		args = append(args, service, "bash", "-lc", cmd)
		command = exec.CommandContext(ctx, "docker", args...)
	} else {
		command = exec.CommandContext(ctx, "bash", "-lc", cmd)
	}
	if strings.TrimSpace(t.workDir) != "" {
		command.Dir = strings.TrimSpace(t.workDir)
	}
	command.Env = mergeEnv(command.Env, t.env)
	stdout := newLimitedBuffer(t.maxOutputBytes)
	stderr := newLimitedBuffer(t.maxOutputBytes)
	command.Stdout = stdout
	command.Stderr = stderr

	err := command.Run()
	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("command timed out after %ds\n%s", timeout, strings.TrimSpace(stderr.String())),
			}, nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("command failed: %v\n%s", err, strings.TrimSpace(stderr.String())),
			}, nil
		}
	}

	stdoutStr := strings.TrimRight(stdout.String(), "\n")
	stderrStr := strings.TrimRight(stderr.String(), "\n")
	data := map[string]any{
		"command":   cmd,
		"stdout":    stdoutStr,
		"stderr":    stderrStr,
		"exit_code": exitCode,
	}
	if stdout.Truncated() {
		data["stdout_truncated"] = true
	}
	if stderr.Truncated() {
		data["stderr_truncated"] = true
	}

	result := &Result{
		Success: err == nil,
		Data:    data,
		Error: func() string {
			if err != nil {
				return fmt.Sprintf("command exited with code %d", exitCode)
			}
			return ""
		}(),
	}
	if stdout.Truncated() || stderr.Truncated() {
		display := map[string]any{
			"command":          cmd,
			"stdout":           stdoutStr,
			"stderr":           stderrStr,
			"exit_code":        exitCode,
			"stdout_truncated": stdout.Truncated(),
			"stderr_truncated": stderr.Truncated(),
		}
		result.ShouldAbridge = true
		result.DisplayData = display
	}

	return result, nil
}

func (t *ShellCommandTool) sandboxConfigForCommand() sandbox.Config {
	cfg := t.sandboxConfig
	cfg.AllowedPaths = append([]string{}, cfg.AllowedPaths...)
	cfg.DeniedPaths = append([]string{}, cfg.DeniedPaths...)
	cfg.AllowedCommands = append([]string{}, cfg.AllowedCommands...)
	cfg.DeniedCommands = append([]string{}, cfg.DeniedCommands...)

	workDir := strings.TrimSpace(t.workDir)
	if cfg.WorkspacePath == "" && workDir != "" {
		cfg.WorkspacePath = workDir
	}
	if workDir != "" {
		if len(cfg.AllowedPaths) == 0 {
			cfg.AllowedPaths = []string{workDir}
		} else if !containsPath(cfg.AllowedPaths, workDir) {
			cfg.AllowedPaths = append(cfg.AllowedPaths, workDir)
		}
	}

	return cfg
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if strings.TrimSpace(path) == target {
			return true
		}
	}
	return false
}

type interactiveLaunchResult struct {
	Note         string
	UsedExternal bool
	UsedTmux     bool
	Launcher     string
}

func (t *ShellCommandTool) runInteractiveCommand(ctx context.Context, cmd string, timeout int) (*interactiveLaunchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	wrapped := wrapInteractiveCommand(cmd)

	if inTmux() {
		if err := runTmuxInteractive(ctx, wrapped, timeout); err == nil {
			return &interactiveLaunchResult{
				Note:         fmt.Sprintf("Interactive shell running in tmux window \"%s\".", tmuxWindowName),
				UsedExternal: true,
				UsedTmux:     true,
				Launcher:     "tmux",
			}, nil
		}
	}

	if launcher := findTerminalLauncher(); launcher != nil {
		if err := launcher.run(ctx, wrapped, timeout); err == nil {
			return &interactiveLaunchResult{
				Note:         fmt.Sprintf("Interactive session opened via %s.", launcher.name),
				UsedExternal: true,
				Launcher:     launcher.name,
			}, nil
		}
	}

	if err := runAttachedInteractive(ctx, wrapped, timeout); err != nil {
		return nil, err
	}
	return &interactiveLaunchResult{
		Note:     "Running interactive session in current terminal.",
		Launcher: "current_terminal",
	}, nil
}

type terminalLauncher struct {
	name           string
	binary         string
	args           []string
	requireDisplay bool
	runFn          func(ctx context.Context, shellCmd string, timeout int) error
}

func (tl *terminalLauncher) run(ctx context.Context, shellCmd string, timeout int) error {
	if tl == nil {
		return fmt.Errorf("no launcher configured")
	}
	if tl.runFn != nil {
		return tl.runFn(ctx, shellCmd, timeout)
	}
	if tl.requireDisplay && !hasGUIEnvironment() {
		return fmt.Errorf("display not available")
	}
	if _, err := exec.LookPath(tl.binary); err != nil {
		return err
	}
	args := make([]string, 0, len(tl.args))
	placeholderFound := false
	for _, arg := range tl.args {
		if arg == "%s" {
			args = append(args, shellCmd)
			placeholderFound = true
		} else {
			args = append(args, arg)
		}
	}
	if !placeholderFound {
		args = append(args, shellCmd)
	}

	ctx, cancel := timeoutContext(ctx, timeout)
	defer cancel()

	command := exec.CommandContext(ctx, tl.binary, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	return command.Run()
}

func findTerminalLauncher() *terminalLauncher {
	if custom := strings.TrimSpace(os.Getenv(interactiveTerminalEnv)); custom != "" {
		if launcher := parseCustomLauncher(custom); launcher != nil {
			return launcher
		}
	}

	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd":
		for _, candidate := range linuxTerminalCandidates() {
			if _, err := exec.LookPath(candidate.binary); err == nil {
				return candidate
			}
		}
	case "darwin":
		if _, err := exec.LookPath("osascript"); err == nil {
			return &terminalLauncher{
				name:   "mac_osascript_terminal",
				binary: "osascript",
				runFn:  runAppleScriptTerminal,
			}
		}
	}
	return nil
}

func linuxTerminalCandidates() []*terminalLauncher {
	return []*terminalLauncher{
		{
			name:           "x-terminal-emulator",
			binary:         "x-terminal-emulator",
			requireDisplay: true,
			args:           []string{"-T", "Buckley Interactive", "-e", "bash", "-lc", "%s"},
		},
		{
			name:           "gnome-terminal",
			binary:         "gnome-terminal",
			requireDisplay: true,
			args:           []string{"--title", "Buckley Interactive", "--", "bash", "-lc", "%s"},
		},
		{
			name:           "konsole",
			binary:         "konsole",
			requireDisplay: true,
			args:           []string{"-e", "bash", "-lc", "%s"},
		},
		{
			name:           "xfce4-terminal",
			binary:         "xfce4-terminal",
			requireDisplay: true,
			args:           []string{"-T", "Buckley Interactive", "-e", "bash", "-lc", "%s"},
		},
		{
			name:           "kitty",
			binary:         "kitty",
			requireDisplay: true,
			args:           []string{"bash", "-lc", "%s"},
		},
		{
			name:           "alacritty",
			binary:         "alacritty",
			requireDisplay: true,
			args:           []string{"-e", "bash", "-lc", "%s"},
		},
		{
			name:           "wezterm",
			binary:         "wezterm",
			requireDisplay: true,
			args:           []string{"start", "--", "bash", "-lc", "%s"},
		},
		{
			name:           "xterm",
			binary:         "xterm",
			requireDisplay: true,
			args:           []string{"-T", "Buckley Interactive", "-e", "bash", "-lc", "%s"},
		},
	}
}

func parseCustomLauncher(def string) *terminalLauncher {
	parts := strings.Fields(def)
	if len(parts) == 0 {
		return nil
	}
	return &terminalLauncher{
		name:   "custom",
		binary: parts[0],
		args: func() []string {
			args := make([]string, len(parts)-1)
			for i := 1; i < len(parts); i++ {
				if parts[i] == "{{cmd}}" {
					args[i-1] = "%s"
				} else {
					args[i-1] = parts[i]
				}
			}
			return args
		}(),
		requireDisplay: false,
	}
}

func inTmux() bool {
	return strings.TrimSpace(os.Getenv("TMUX")) != ""
}

func runTmuxInteractive(ctx context.Context, shellCmd string, timeout int) error {
	token := fmt.Sprintf("buckley-interactive-%d", time.Now().UnixNano())
	windowCmd := fmt.Sprintf("trap 'tmux wait-for -S %[1]s' EXIT; %s", token, shellCmd)

	ctx, cancel := timeoutContext(ctx, timeout)
	defer cancel()

	newWindow := exec.CommandContext(ctx, "tmux", "new-window", "-dn", tmuxWindowName, "bash", "-lc", windowCmd)
	newWindow.Stdout = os.Stdout
	newWindow.Stderr = os.Stderr
	newWindow.Stdin = os.Stdin
	if err := newWindow.Run(); err != nil {
		return err
	}

	waitCmd := exec.CommandContext(ctx, "tmux", "wait-for", token)
	waitCmd.Stdout = os.Stdout
	waitCmd.Stderr = os.Stderr
	waitCmd.Stdin = os.Stdin
	return waitCmd.Run()
}

func runAppleScriptTerminal(ctx context.Context, shellCmd string, timeout int) error {
	escaped := escapeAppleScript(shellCmd)
	script := fmt.Sprintf(`tell application "Terminal"
	activate
	set newTab to do script "%s"
	delay 0.5
	repeat
		try
			if busy of newTab is false then exit repeat
		on error
			exit repeat
		end try
		delay 1
	end repeat
end tell`, escaped)

	ctx, cancel := timeoutContext(ctx, timeout)
	defer cancel()

	command := exec.CommandContext(ctx, "osascript", "-e", script)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func runAttachedInteractive(ctx context.Context, shellCmd string, timeout int) error {
	ctx, cancel := timeoutContext(ctx, timeout)
	defer cancel()

	fmt.Println("No GUI terminal detected. Running interactive command in current terminal...")
	command := exec.CommandContext(ctx, "bash", "-lc", shellCmd)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	return command.Run()
}

func wrapInteractiveCommand(cmd string) string {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	prompt := `printf '\nCommand finished. Close this window or press Enter to exit...\n'; read -r _`
	return fmt.Sprintf("cd %s && %s; %s", shellEscapeSingleQuotes(wd), cmd, prompt)
}

func shellEscapeSingleQuotes(input string) string {
	return "'" + strings.ReplaceAll(input, "'", `'\''`) + "'"
}

func escapeAppleScript(cmd string) string {
	replacer := strings.NewReplacer(
		`"`, `\"`,
		`\`, `\\`,
	)
	return replacer.Replace(cmd)
}

func hasGUIEnvironment() bool {
	if runtime.GOOS == "darwin" {
		return true
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "freebsd" || runtime.GOOS == "openbsd" {
		if os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" {
			return true
		}
	}
	return false
}

func parseBoolParam(value any, defaultVal bool) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		default:
			return defaultVal
		}
	default:
		return defaultVal
	}
}

func timeoutContext(parent context.Context, timeout int) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, time.Duration(timeout)*time.Second)
}
