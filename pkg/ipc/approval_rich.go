package ipc

import (
	"encoding/json"
	"strings"
	"time"

	ipcpb "github.com/odvcencio/buckley/pkg/ipc/proto"
	"github.com/odvcencio/buckley/pkg/touch"
)

type approvalRichFields struct {
	operationType string
	description   string
	command       string
	filePath      string
	diffLines     []touch.DiffLine
	addedLines    int32
	removedLines  int32
	ranges        []touch.LineRange
}

func extractApprovalRichFields(toolName, toolInput string) approvalRichFields {
	rich := touch.ExtractFromJSON(toolName, toolInput)
	return approvalRichFields{
		operationType: rich.OperationType,
		description:   rich.Description,
		command:       rich.Command,
		filePath:      rich.FilePath,
		diffLines:     rich.DiffLines,
		addedLines:    rich.AddedLines,
		removedLines:  rich.RemovedLines,
		ranges:        rich.Ranges,
	}
}

func approvalDiffLinesToProto(lines []touch.DiffLine) []*ipcpb.DiffLine {
	if len(lines) == 0 {
		return nil
	}
	out := make([]*ipcpb.DiffLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, &ipcpb.DiffLine{
			Type:    line.Type,
			Content: line.Content,
		})
	}
	return out
}

func enrichToolEventPayload(payload map[string]any) {
	if payload == nil {
		return
	}
	toolName, _ := payload["toolName"].(string)
	rawArgs := toolArgsFromPayload(payload)
	if strings.TrimSpace(toolName) == "" || strings.TrimSpace(rawArgs) == "" {
		return
	}
	rich := touch.ExtractFromJSON(toolName, rawArgs)
	if rich.OperationType != "" {
		payload["operationType"] = rich.OperationType
		payload["expiresAt"] = time.Now().Add(touch.TTLForOperation(rich.OperationType))
	}
	if rich.Description != "" {
		payload["description"] = rich.Description
	}
	if rich.Command != "" {
		payload["command"] = rich.Command
	}
	if rich.FilePath != "" {
		payload["filePath"] = rich.FilePath
	}
	if len(rich.DiffLines) > 0 {
		payload["diffLines"] = rich.DiffLines
	}
	if len(rich.Ranges) > 0 {
		payload["ranges"] = rich.Ranges
	}
	if rich.AddedLines != 0 {
		payload["addedLines"] = rich.AddedLines
	}
	if rich.RemovedLines != 0 {
		payload["removedLines"] = rich.RemovedLines
	}
}

func toolArgsFromPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if raw, ok := payload["arguments"].(string); ok {
		return raw
	}
	if raw, ok := payload["args"].(string); ok {
		return raw
	}
	if raw, ok := payload["arguments"].(map[string]any); ok {
		if encoded, err := json.Marshal(raw); err == nil {
			return string(encoded)
		}
	}
	return ""
}
