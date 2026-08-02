package goalloop

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

// QueueItem is one runnable task in priority order, joined from the
// ledger's event-derived status and the task's latest checkpoint.
type QueueItem struct {
	TaskID   string
	Title    string
	Priority int
	Status   string
	// NextActions comes from the latest checkpoint; the scheduler
	// prefers tasks whose next action is cheap verification when debt
	// is high (G7 refines this; G6 orders by priority then creation).
	NextActions []taskstate.NextAction
}

// BuildQueue rebuilds the next-action queue for a goal run purely from
// durable state: task.created events order and describe the tasks, the
// latest checkpoint per task carries live status and next actions.
// Blocked, parked, and completed tasks stay out of the queue. This is
// the restart path too: a crashed process calls BuildQueue and continues
// (design section 5.3).
func (l *Loop) BuildQueue(ctx context.Context, runID string) ([]QueueItem, error) {
	events, err := l.ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID})
	if err != nil {
		return nil, fmt.Errorf("goalloop: list events: %w", err)
	}
	state, err := runledger.MaterializeRun(runID, events)
	if err != nil {
		return nil, fmt.Errorf("goalloop: materialize run: %w", err)
	}

	type ordered struct {
		item QueueItem
		seq  int64
	}
	var items []ordered
	for taskID, task := range state.Tasks {
		created, ok := firstCreated(task.Events)
		if !ok || payloadString(created.Payload, "kind") != "task" {
			continue
		}

		item := QueueItem{
			TaskID:   taskID,
			Title:    payloadString(created.Payload, "title"),
			Priority: payloadInt(created.Payload, "priority"),
			Status:   taskstate.StatusPending,
		}
		if resumed, err := l.checkpoints.Resume(ctx, taskID); err == nil {
			item.Status = resumed.State.Status
			item.NextActions = resumed.State.NextActions
		} else if !errors.Is(err, taskstate.ErrNoCheckpoint) {
			return nil, fmt.Errorf("goalloop: resume %s: %w", taskID, err)
		}

		switch item.Status {
		case taskstate.StatusCompleted, taskstate.StatusBlocked, taskstate.StatusParked:
			continue
		}
		items = append(items, ordered{item: item, seq: created.Sequence})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].item.Priority != items[j].item.Priority {
			return items[i].item.Priority < items[j].item.Priority
		}
		return items[i].seq < items[j].seq
	})

	queue := make([]QueueItem, 0, len(items))
	for _, o := range items {
		queue = append(queue, o.item)
	}
	return queue, nil
}

func firstCreated(events []runledger.Event) (runledger.Event, bool) {
	for _, ev := range events {
		if ev.Type == runledger.EventTaskCreated {
			return ev, true
		}
	}
	return runledger.Event{}, false
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	s, _ := payload[key].(string)
	return s
}

func payloadInt(payload map[string]any, key string) int {
	if payload == nil {
		return 0
	}
	switch v := payload[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}
