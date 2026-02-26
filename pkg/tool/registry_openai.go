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
// If allowed is empty, all tools are returned.
func (r *Registry) ToOpenAIFunctionsFiltered(allowed []string) []map[string]any {
	if len(allowed) == 0 {
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
