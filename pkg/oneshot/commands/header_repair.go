package commands

import (
	"encoding/json"
	"strings"

	"m31labs.dev/buckley/pkg/commitmsg"
)

// Repair fixes mechanical errors in a generate_commit call without another
// model call: header format, bullets over the word limit and bullets over the
// count limit. It returns the original arguments unless the result validates.
func (d CommitDefinition) Repair(raw json.RawMessage) (json.RawMessage, []string) {
	body, bodyRepairs := repairBodyArguments(raw)
	header, headerRepairs := repairHeaderArguments(body, "subject", commitmsg.HeaderLimit, d.Validate)
	repairs := append(bodyRepairs, headerRepairs...)
	if len(repairs) == 0 || d.Validate(header) != nil {
		return raw, nil
	}
	return header, repairs
}

func repairBodyArguments(raw json.RawMessage) (json.RawMessage, []string) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields["body"] == nil {
		return raw, nil
	}
	var body StringList
	if json.Unmarshal(fields["body"], &body) != nil {
		return raw, nil
	}
	repaired, repairs := commitmsg.RepairBody(body)
	if len(repairs) == 0 {
		return raw, nil
	}
	fields["body"], _ = json.Marshal(repaired)
	out, err := json.Marshal(fields)
	if err != nil {
		return raw, nil
	}
	return out, repairs
}

func (d PRDefinition) Repair(raw json.RawMessage) (json.RawMessage, []string) {
	return repairHeaderArguments(raw, "title", 100, d.Validate)
}

func repairHeaderArguments(raw json.RawMessage, subjectKey string, limit int, validate func(json.RawMessage) error) (json.RawMessage, []string) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return raw, nil
	}
	var action, scope, subject string
	if json.Unmarshal(fields["action"], &action) != nil || json.Unmarshal(fields[subjectKey], &subject) != nil {
		return raw, nil
	}
	if value, ok := fields["scope"]; ok && json.Unmarshal(value, &scope) != nil {
		return raw, nil
	}
	trimmedAction := strings.TrimSpace(action)
	scope, subject, repairs := commitmsg.RepairHeader(trimmedAction, scope, subject, limit)
	if trimmedAction != action {
		fields["action"], _ = json.Marshal(trimmedAction)
		repairs = append(repairs, "trimmed type whitespace")
	}
	if len(repairs) == 0 {
		return raw, nil
	}
	fields["scope"], _ = json.Marshal(scope)
	fields[subjectKey], _ = json.Marshal(subject)
	repaired, err := json.Marshal(fields)
	if err != nil || validate(repaired) != nil {
		return raw, nil
	}
	return repaired, repairs
}
