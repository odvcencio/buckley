package runledger

// RunOverview is a run-tree node for navigation and inspection. Run contains
// metadata without budget or outcome bodies. Status and EventCount describe
// the complete event stream without retaining its payloads or task history.
// Full replay remains available through LoadGoalTree and ListEvents.
type RunOverview struct {
	Run        AgentRun
	Status     string
	EventCount int
	Children   []*RunOverview
}
