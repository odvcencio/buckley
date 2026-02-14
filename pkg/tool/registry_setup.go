package tool

import (
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/embeddings"
	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func (r *Registry) registerBuiltins(cfg registryOptions) {
	register := func(tool Tool) {
		if cfg.builtinFilter == nil || cfg.builtinFilter(tool) {
			r.Register(tool)
		}
	}

	// Register built-in file tools
	register(&builtin.ReadFileTool{})
	register(&builtin.WriteFileTool{})
	register(&builtin.ListDirectoryTool{})
	register(&builtin.PatchFileTool{})
	register(&builtin.SearchTextTool{})
	register(&builtin.SearchReplaceTool{})
	register(&builtin.FindFilesTool{})
	register(&builtin.FileExistsTool{})
	register(&builtin.ExcelTool{})

	// Register built-in edit tools (with diff preview)
	register(&builtin.EditFileTool{})
	register(&builtin.InsertTextTool{})
	register(&builtin.DeleteLinesTool{})

	// Register built-in git tools
	register(&builtin.GitStatusTool{})
	register(&builtin.GitDiffTool{})
	register(&builtin.GitLogTool{})
	register(&builtin.GitBlameTool{})
	register(&builtin.ListMergeConflictsTool{})
	register(&builtin.MarkResolvedTool{})
	register(&builtin.HeadlessBrowseTool{})
	register(&builtin.BrowserStartTool{})
	register(&builtin.BrowserNavigateTool{})
	register(&builtin.BrowserObserveTool{})
	register(&builtin.BrowserStreamTool{})
	register(&builtin.BrowserActTool{})
	register(&builtin.BrowserClipboardReadTool{})
	register(&builtin.BrowserClipboardWriteTool{})
	register(&builtin.BrowserCloseTool{})
	register(&builtin.ShellCommandTool{})

	// Delegation tools with guardrails (depth limits, rate limits, recursion prevention)
	// See pkg/tool/builtin/delegation_guard.go for safety implementation
	register(&builtin.CodexTool{})
	register(&builtin.ClaudeTool{})
	register(&builtin.BuckleyTool{})
	register(&builtin.SubagentTool{})

	// Register built-in code navigation tools
	register(&builtin.FindSymbolTool{})
	register(&builtin.FindReferencesTool{})
	register(&builtin.GetFunctionSignatureTool{})

	// Register built-in refactoring tools
	register(&builtin.RenameSymbolTool{})
	register(&builtin.ExtractFunctionTool{})

	// Register built-in code quality tools
	register(&builtin.AnalyzeComplexityTool{})
	register(&builtin.FindDuplicatesTool{})

	// Register built-in testing tools
	register(&builtin.RunTestsTool{})
	register(&builtin.GenerateTestTool{})

	// Register built-in documentation tools
	register(&builtin.GenerateDocstringTool{})
	register(&builtin.ExplainCodeTool{})

	// Register built-in skill authoring tool
	register(&builtin.CreateSkillTool{})

	// Register terminal editor helper
	register(&builtin.TerminalEditorTool{})

	// Register fluffy-ui agent tool for AI-driven UI automation
	register(&builtin.FluffyAgentTool{})

	// Note: TODO tool is registered separately with SetTodoStore()
}

// applyDefaultKinds sets ACP tool_call kinds for built-in tools.
func (r *Registry) applyDefaultKinds() {
	kinds := map[string]string{
		// File read tools
		"read_file":      "read",
		"list_directory": "read",
		"find_files":     "search",
		"file_exists":    "read",
		"search_text":    "search",
		"excel":          "read",
		"lookup_context": "read",

		// File edit tools
		"write_file":     "edit",
		"edit_file":      "edit",
		"insert_text":    "edit",
		"delete_lines":   "delete",
		"patch_file":     "edit",
		"search_replace": "edit",

		// Git tools
		"git_status": "read",
		"git_diff":   "read",
		"git_log":    "read",
		"git_blame":  "read",

		// Code navigation
		"find_symbol":            "search",
		"find_references":        "search",
		"get_function_signature": "read",

		// Refactoring
		"rename_symbol":    "edit",
		"extract_function": "edit",

		// Analysis
		"analyze_complexity": "read",
		"find_duplicates":    "search",

		// Testing
		"run_tests":     "execute",
		"generate_test": "edit",

		// Documentation
		"generate_docstring": "edit",
		"explain_code":       "think",

		// Shell
		"run_shell": "execute",

		// Browser
		"browse_url":              "fetch",
		"browser_start":           "execute",
		"browser_navigate":        "fetch",
		"browser_observe":         "read",
		"browser_stream":          "read",
		"browser_act":             "execute",
		"browser_clipboard_read":  "read",
		"browser_clipboard_write": "edit",
		"browser_close":           "execute",

		// Delegation
		"codex":    "execute",
		"claude":   "execute",
		"buckley":  "execute",
		"subagent": "execute",

		// Misc
		"create_skill":    "edit",
		"terminal_editor": "execute",
		"todo":            "edit",
		"fluffy_agent":    "execute",
	}

	for name, kind := range kinds {
		if _, exists := r.tools[name]; exists {
			r.toolKinds[name] = kind
		}
	}
}

// EnableTelemetry wires telemetry events for selected built-in tools.
func (r *Registry) EnableTelemetry(hub *telemetry.Hub, sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.telemetryHub = hub
	r.telemetrySession = sessionID
}

// EnableMissionControl configures mission-control-backed approvals for mutating tools.
// When requireApproval is true, write_file/apply_patch will block until approved.
func (r *Registry) EnableMissionControl(store *mission.Store, agentID string, requireApproval bool, timeout time.Duration) {
	if store == nil {
		return
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.missionStore = store
	r.missionAgent = agentID
	r.missionTimeout = timeout
	r.requireMissionApproval = requireApproval
}

// UpdateMissionSession updates the active session used when recording pending changes.
func (r *Registry) UpdateMissionSession(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.missionSession = strings.TrimSpace(sessionID)
}

// UpdateMissionAgent updates the agent identifier recorded alongside pending changes.
func (r *Registry) UpdateMissionAgent(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.missionAgent = strings.TrimSpace(agentID)
}

// UpdateTelemetrySession updates the active session used for telemetry fan-out.
func (r *Registry) UpdateTelemetrySession(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.telemetrySession = sessionID
}

// SetTodoStore initializes the TODO tool with a storage backend
func (r *Registry) SetTodoStore(store builtin.TodoStore) {
	r.Register(&builtin.TodoTool{Store: store})
}

// SetCompactionManager registers the compact_context tool.
func (r *Registry) SetCompactionManager(compactor builtin.Compactor) {
	if r == nil || compactor == nil {
		return
	}
	r.Register(builtin.NewCompactContextTool(compactor))
}

// GetTodoTool returns the registered TodoTool, or nil if not registered
func (r *Registry) GetTodoTool() *builtin.TodoTool {
	t, ok := r.Get("todo")
	if !ok {
		return nil
	}
	if todoTool, ok := t.(*builtin.TodoTool); ok {
		return todoTool
	}
	return nil
}

// ConfigureTodoPlanning enables planning capabilities on the TodoTool
func (r *Registry) ConfigureTodoPlanning(llmClient builtin.PlanningClient, planningModel string) {
	if todoTool := r.GetTodoTool(); todoTool != nil {
		todoTool.LLMClient = llmClient
		todoTool.PlanningModel = planningModel
	}
}

// EnableSemanticSearch registers semantic search tools
func (r *Registry) EnableSemanticSearch(searcher *embeddings.Searcher) {
	if searcher == nil {
		return
	}
	r.Register(builtin.NewSemanticSearchTool(searcher))
	r.Register(builtin.NewIndexManagementTool(searcher))
}

// EnableCodeIndex registers context lookup tools backed by storage.
func (r *Registry) EnableCodeIndex(store *storage.Store) {
	if store == nil {
		return
	}
	r.Register(&builtin.LookupContextTool{Store: store})
	if tool, ok := r.Get("find_symbol"); ok {
		if fs, ok := tool.(*builtin.FindSymbolTool); ok {
			fs.Store = store
			return
		}
	}
	r.Register(&builtin.FindSymbolTool{Store: store})
}
