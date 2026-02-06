package widgets

func clonePlanTasks(tasks []PlanTask) []PlanTask {
	if len(tasks) == 0 {
		return nil
	}
	cloned := make([]PlanTask, len(tasks))
	copy(cloned, tasks)
	return cloned
}

func cloneRunningTools(tools []RunningTool) []RunningTool {
	if len(tools) == 0 {
		return nil
	}
	cloned := make([]RunningTool, len(tools))
	copy(cloned, tools)
	return cloned
}

func cloneToolHistory(history []ToolHistoryEntry) []ToolHistoryEntry {
	if len(history) == 0 {
		return nil
	}
	cloned := make([]ToolHistoryEntry, len(history))
	copy(cloned, history)
	return cloned
}

func cloneTouchSummaries(touches []TouchSummary) []TouchSummary {
	if len(touches) == 0 {
		return nil
	}
	cloned := make([]TouchSummary, len(touches))
	for i, touch := range touches {
		cloned[i] = TouchSummary{
			Path:      touch.Path,
			Operation: touch.Operation,
		}
		if len(touch.Ranges) > 0 {
			ranges := make([]TouchRange, len(touch.Ranges))
			copy(ranges, touch.Ranges)
			cloned[i].Ranges = ranges
		}
	}
	return cloned
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func cloneExperimentVariants(variants []ExperimentVariant) []ExperimentVariant {
	if len(variants) == 0 {
		return nil
	}
	cloned := make([]ExperimentVariant, len(variants))
	copy(cloned, variants)
	return cloned
}

func cloneScratchpadEntries(entries []RLMScratchpadEntry) []RLMScratchpadEntry {
	if len(entries) == 0 {
		return nil
	}
	cloned := make([]RLMScratchpadEntry, len(entries))
	copy(cloned, entries)
	return cloned
}

func cloneRLMStatus(status *RLMStatus) *RLMStatus {
	if status == nil {
		return nil
	}
	cloned := *status
	return &cloned
}

func cloneCircuitStatus(status *CircuitStatus) *CircuitStatus {
	if status == nil {
		return nil
	}
	cloned := *status
	return &cloned
}

func cloneAgentSummaries(agents []AgentSummary) []AgentSummary {
	if len(agents) == 0 {
		return nil
	}
	cloned := make([]AgentSummary, len(agents))
	copy(cloned, agents)
	return cloned
}

func cloneFileLockSummaries(locks []FileLockSummary) []FileLockSummary {
	if len(locks) == 0 {
		return nil
	}
	cloned := make([]FileLockSummary, len(locks))
	copy(cloned, locks)
	return cloned
}
