package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/agentcoord"
)

const (
	DefaultSubagentListLimit = 10
	MaxSubagentListLimit     = 20
	subagentListPageBytes    = 12 * 1024
)

type subagentListCursor struct {
	StartedAt time.Time `json:"started_at"`
	RunID     string    `json:"run_id"`
}

func (t *SubagentTool) list(ctx context.Context, coordinator agentcoord.Coordinator, params map[string]any) (*Result, error) {
	limit := DefaultSubagentListLimit
	if raw, ok := params["limit"]; ok {
		var err error
		limit, err = strconv.Atoi(fmt.Sprint(raw))
		if err != nil || limit < 1 || limit > MaxSubagentListLimit {
			return &Result{Success: false, Error: fmt.Sprintf("list limit must be an integer between 1 and %d", MaxSubagentListLimit)}, nil
		}
	}
	session, _, _, _ := t.runtimeContext()
	filter := agentcoord.RunFilter{ParentSessionID: session, Limit: limit + 1, NewestFirst: true}
	if raw, ok := params["cursor"]; ok {
		cursor, valid := raw.(string)
		if !valid || len(cursor) > 4096 {
			return &Result{Success: false, Error: "invalid subagent list cursor"}, nil
		}
		if cursor = strings.TrimSpace(cursor); cursor != "" {
			encoded, err := base64.RawURLEncoding.DecodeString(cursor)
			var page subagentListCursor
			if err != nil || json.Unmarshal(encoded, &page) != nil || page.StartedAt.IsZero() || page.RunID == "" {
				return &Result{Success: false, Error: "invalid subagent list cursor"}, nil
			}
			filter.BeforeStartedAt, filter.BeforeRunID = page.StartedAt, page.RunID
		}
	}
	runs, err := coordinator.List(ctx, filter)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	views := make([]map[string]any, min(len(runs), limit))
	for i := range views {
		views[i] = subagentRunView(runs[i], 128)
	}
	for shown := len(views); ; shown-- {
		more := len(runs) > shown
		display := map[string]any{"runs": views[:shown], "count": shown, "has_more": more}
		if more && shown > 0 {
			last := runs[shown-1]
			cursor, err := json.Marshal(subagentListCursor{StartedAt: last.StartedAt, RunID: last.ID})
			if err != nil {
				return nil, fmt.Errorf("encode subagent list cursor: %w", err)
			}
			display["next_cursor"] = base64.RawURLEncoding.EncodeToString(cursor)
		}
		encoded, err := json.Marshal(display)
		if err != nil {
			return nil, fmt.Errorf("encode subagent list page: %w", err)
		}
		if len(encoded) <= subagentListPageBytes {
			data := map[string]any{"runs": runs[:shown], "count": shown, "has_more": more}
			if cursor, ok := display["next_cursor"]; ok {
				data["next_cursor"] = cursor
			}
			return &Result{Success: true, Data: data, DisplayData: display, ShouldAbridge: true}, nil
		}
		if shown <= 1 {
			return &Result{Success: false, Error: "subagent list entry exceeds the page budget"}, nil
		}
	}
}

func subagentRunView(run agentcoord.Run, summaryBytes int) map[string]any {
	view := map[string]any{"id": run.ID, "state": run.State}
	for key, value := range map[string]string{
		"agent":        subagentDisplayText(run.Task.Agent, 128),
		"task_preview": subagentDisplayText(run.Task.Task, 160),
		"summary":      subagentDisplayText(run.Result.Summary, summaryBytes),
		"error":        subagentDisplayText(run.Result.Error, 1024),
	} {
		if value != "" {
			view[key] = value
		}
	}
	if !run.StartedAt.IsZero() {
		view["started_at"] = run.StartedAt
	}
	if !run.FinishedAt.IsZero() {
		view["finished_at"] = run.FinishedAt
	}
	if refs := run.Result.EvidenceRefs; len(refs) > 0 {
		// Terminal raw and typed reports are appended after task/mail evidence.
		view["evidence_refs"] = append([]string(nil), refs[max(0, len(refs)-2):]...)
		view["evidence_count"] = len(refs)
	}
	if len(run.Claims) > 0 {
		view["claim_count"] = len(run.Claims)
	}
	if run.MailboxCount > 0 {
		view["mailbox_count"] = run.MailboxCount
	}
	return view
}

func subagentDisplayText(value string, limit int) string {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "\uFFFD")
	if len(value) <= limit {
		return strings.Clone(value)
	}
	end := limit - 3
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "..."
}
