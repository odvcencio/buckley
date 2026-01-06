package builtin

import (
	"fmt"
	"os"
	"strings"
)

// TerminalEditorTool opens a file in a terminal text editor (vim, nano, etc.).
type TerminalEditorTool struct{ workDirAware }

func (t *TerminalEditorTool) Name() string {
	return "edit_file_terminal"
}

func (t *TerminalEditorTool) Description() string {
	return "Open a file in a terminal text editor (vim by default, respects BUCKLEY_TERMINAL_EDITOR/VISUAL/EDITOR). Launches in a separate terminal/tmux pane so the user can edit directly."
}

func (t *TerminalEditorTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the file to edit",
			},
			"editor": {
				Type:        "string",
				Description: "Optional terminal editor command (overrides environment/default)",
			},
		},
		Required: []string{"path"},
	}
}

func (t *TerminalEditorTool) Execute(params map[string]any) (*Result, error) {
	rawPath, ok := params["path"].(string)
	if !ok || strings.TrimSpace(rawPath) == "" {
		return &Result{Success: false, Error: "path parameter must be a non-empty string"}, nil
	}

	absPath, err := resolvePath(t.workDir, rawPath)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	if info, err := os.Stat(absPath); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("cannot access %s: %v", absPath, err)}, nil
	} else if info.IsDir() {
		return &Result{Success: false, Error: "path points to a directory, expected a file"}, nil
	}

	editor := strings.TrimSpace(getString(params["editor"]))
	if editor == "" {
		editor = firstNonEmpty(
			os.Getenv("BUCKLEY_TERMINAL_EDITOR"),
			os.Getenv("VISUAL"),
			os.Getenv("EDITOR"),
			"vim",
		)
	}

	shell := &ShellCommandTool{}
	command := fmt.Sprintf("%s %s", editor, shellEscapeSingleQuotes(absPath))
	result, err := shell.runInteractiveCommand(command, 0)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to open editor: %v", err)}, nil
	}

	note := "Edited file in current terminal."
	if result.UsedExternal {
		note = result.Note
	}

	data := map[string]any{
		"path":     absPath,
		"editor":   editor,
		"note":     note,
		"tmux":     result.UsedTmux,
		"launcher": result.Launcher,
	}

	return &Result{
		Success:       true,
		Data:          data,
		DisplayData:   map[string]any{"message": fmt.Sprintf("Opened %s in %s", absPath, editor)},
		ShouldAbridge: true,
	}, nil
}

func getString(value any) string {
	if value == nil {
		return ""
	}
	if str, ok := value.(string); ok {
		return str
	}
	return fmt.Sprintf("%v", value)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
