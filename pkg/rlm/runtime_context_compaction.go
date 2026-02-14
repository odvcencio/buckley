package rlm

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"strconv"

	"github.com/odvcencio/buckley/pkg/model"
)

func (r *Runtime) collectScratchpadSummaries(ctx context.Context, limit int) []EntrySummary {
	if r == nil || r.scratchpad == nil {
		return nil
	}
	summaries, err := r.scratchpad.ListSummaries(ctx, limit)
	if err != nil {
		return nil
	}
	return summaries
}

func (r *Runtime) compactMessages(messages []model.Message) []model.Message {
	if len(messages) == 0 {
		return messages
	}
	if coordinatorMaxMessages <= 0 || len(messages) <= coordinatorMaxMessages {
		return messages
	}
	if coordinatorKeepMessages <= 0 || len(messages) <= coordinatorKeepMessages+1 {
		return messages
	}

	system := messages[0]
	splitIdx := len(messages) - coordinatorKeepMessages

	for splitIdx < len(messages)-1 {
		msg := messages[splitIdx]
		if msg.Role == "tool" {
			splitIdx++
			continue
		}
		if splitIdx > 1 {
			prev := messages[splitIdx-1]
			if prev.Role == "assistant" && len(prev.ToolCalls) > 0 {
				splitIdx--
				break
			}
		}
		break
	}

	if splitIdx <= 1 {
		return messages
	}

	recent := messages[splitIdx:]
	removed := messages[1:splitIdx]

	toolCount := 0
	for _, msg := range removed {
		if msg.Role == "tool" {
			toolCount++
		}
	}

	compactNote := fmt.Sprintf("[Compacted %d earlier messages (%d tool results).]", len(removed), toolCount)
	out := make([]model.Message, 0, 2+len(recent))
	out = append(out, system, model.Message{Role: "assistant", Content: compactNote})
	out = append(out, recent...)
	return out
}

func (r *Runtime) shouldIncludeHistory(history []IterationHistory) bool {
	if r == nil || len(history) == 0 {
		return false
	}
	hash := hashHistorySnapshot(history)

	r.contextMu.Lock()
	defer r.contextMu.Unlock()

	if r.historySnapshotSet && r.historySnapshotHash == hash {
		return false
	}
	r.historySnapshotHash = hash
	r.historySnapshotSet = true
	return true
}

func (r *Runtime) shouldIncludeScratchpad(summaries []EntrySummary) bool {
	if r == nil || len(summaries) == 0 {
		return false
	}
	hash := hashScratchpadSnapshot(summaries)

	r.contextMu.Lock()
	defer r.contextMu.Unlock()

	if r.scratchpadSnapshotSet && r.scratchpadSnapshotHash == hash {
		return false
	}
	r.scratchpadSnapshotHash = hash
	r.scratchpadSnapshotSet = true
	return true
}

func hashHistorySnapshot(history []IterationHistory) uint64 {
	h := fnv.New64a()
	for _, item := range history {
		writeHashInt(h, int64(item.Iteration))
		writeHashBool(h, item.Compacted)
		writeHashInt(h, int64(item.TokensUsed))
		writeHashString(h, item.Summary)
		for _, d := range item.Delegations {
			writeHashString(h, d.TaskID)
			writeHashString(h, d.Weight)
			writeHashString(h, d.WeightUsed)
			writeHashString(h, d.Model)
			writeHashBool(h, d.Escalated)
			writeHashBool(h, d.Success)
			writeHashInt(h, int64(d.ToolCallsCount))
			writeHashString(h, d.Summary)
		}
	}
	return h.Sum64()
}

func hashScratchpadSnapshot(summaries []EntrySummary) uint64 {
	h := fnv.New64a()
	for _, summary := range summaries {
		writeHashString(h, summary.Key)
		writeHashString(h, string(summary.Type))
		writeHashString(h, summary.Summary)
		writeHashString(h, summary.CreatedBy)
		writeHashInt(h, summary.CreatedAt.UnixNano())
	}
	return h.Sum64()
}

func writeHashString(h io.Writer, value string) {
	if value == "" {
		_, _ = h.Write([]byte{0})
		return
	}
	_, _ = io.WriteString(h, value)
	_, _ = h.Write([]byte{0})
}

func writeHashInt(h io.Writer, value int64) {
	buf := strconv.AppendInt(make([]byte, 0, 20), value, 10)
	_, _ = h.Write(buf)
	_, _ = h.Write([]byte{0})
}

func writeHashBool(h io.Writer, value bool) {
	if value {
		_, _ = h.Write([]byte{1})
		return
	}
	_, _ = h.Write([]byte{0})
}
