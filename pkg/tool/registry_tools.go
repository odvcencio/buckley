package tool

// Register registers a tool
func (r *Registry) Register(t Tool) {
	if r == nil || t == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

// Remove unregisters a tool by name.
func (r *Registry) Remove(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
	delete(r.toolKinds, name)
}

// SetToolKind associates an ACP tool_call kind with a tool name.
// Valid kinds are defined in pkg/acp/types.go (read, edit, delete, execute, etc.).
func (r *Registry) SetToolKind(toolName, kind string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolKinds[toolName] = kind
}

// ToolKind returns the ACP tool_call kind for a tool, or empty string if not set.
func (r *Registry) ToolKind(toolName string) string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.toolKinds[toolName]
}

// Filter removes tools that do not match the predicate.
func (r *Registry) Filter(keep func(Tool) bool) {
	if r == nil || keep == nil {
		return
	}
	tools := r.snapshotToolMap()
	var remove []string
	for name, t := range tools {
		if !keep(t) {
			remove = append(remove, name)
		}
	}
	if len(remove) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range remove {
		delete(r.tools, name)
	}
}

// Get returns a tool by name
func (r *Registry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools
func (r *Registry) List() []Tool {
	return r.snapshotTools()
}

// Hooks returns the registry hook manager.
func (r *Registry) Hooks() *HookRegistry {
	if r == nil {
		return nil
	}
	return r.hooks
}

// Use registers a middleware on the registry.
func (r *Registry) Use(mw Middleware) {
	if r == nil || mw == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middlewares = append(r.middlewares, mw)
	r.rebuildExecutorLocked()
}
