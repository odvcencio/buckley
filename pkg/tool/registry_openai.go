package tool

// ToOpenAIFunctions converts all tools to OpenAI function calling format
func (r *Registry) ToOpenAIFunctions() []map[string]any {
	tools := r.snapshotTools()
	functions := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		functions = append(functions, ToOpenAIFunction(t))
	}
	return functions
}

// ToOpenAIFunctionsFiltered converts only allowed tools to OpenAI function format.
// A nil allowed list returns all tools; an explicitly empty list returns none.
func (r *Registry) ToOpenAIFunctionsFiltered(allowed []string) []map[string]any {
	if allowed == nil {
		return r.ToOpenAIFunctions()
	}
	tools := r.snapshotTools()
	functions := make([]map[string]any, 0, len(allowed))
	for _, t := range tools {
		if IsToolAllowed(t.Name(), allowed) {
			functions = append(functions, ToOpenAIFunction(t))
		}
	}
	return functions
}
