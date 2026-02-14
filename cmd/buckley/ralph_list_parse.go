package main

import (
	"bufio"
	"encoding/json"
	"os"

	"github.com/odvcencio/buckley/pkg/ralph"
)

// parseSessionLog reads a ralph log file and extracts session info.
func parseSessionLog(path string) (sessionInfo, error) {
	var info sessionInfo
	f, err := os.Open(path)
	if err != nil {
		return info, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var evt ralph.LogEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			continue
		}

		switch evt.Event {
		case "session_start":
			info.StartTime = evt.Timestamp
			if p, ok := evt.Data["prompt"].(string); ok {
				info.Prompt = p
			}
			info.Status = "running"
		case "session_end":
			info.EndTime = evt.Timestamp
			if reason, ok := evt.Data["reason"].(string); ok {
				info.Status = reason
			}
			if iters, ok := evt.Data["iterations"].(float64); ok {
				info.Iters = int(iters)
			}
			if cost, ok := evt.Data["total_cost"].(float64); ok {
				info.Cost = cost
			}
		case "iteration_end":
			info.Iters = evt.Iteration
			// The executor writes "session_total_cost"; fall back to "cost"
			if cost, ok := evt.Data["session_total_cost"].(float64); ok {
				info.Cost = cost
			} else if cost, ok := evt.Data["cost"].(float64); ok {
				info.Cost = cost
			}
		}
	}

	return info, scanner.Err()
}
