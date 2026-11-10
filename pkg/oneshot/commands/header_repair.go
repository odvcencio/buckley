package commands

import (
	"encoding/json"
	"strings"

	"m31labs.dev/buckley/pkg/commitmsg"
)

// Repair fixes mechanical errors in a generate_commit call without another
// model call: header format, a header over the length limit, bullets over the
// word limit and bullets over the count limit.
//
// It returns what it fixed even when another problem remains, such as a
// subject that repeats the verb. The framework validates the result. Before,
// Repair gave back the original arguments unless everything validated, so a
// header of 77 characters beside any other problem was not cut, the model was
// told about the length only, and the run could end with "header exceeds 72
// characters (77)" although the length was never the obstacle.
func (d CommitDefinition) Repair(raw json.RawMessage) (json.RawMessage, []string) {
	body, bodyRepairs := repairBodyArguments(raw)
	header, headerRepairs := repairHeaderArguments(body, "subject", commitmsg.HeaderLimit)
	return header, append(bodyRepairs, headerRepairs...)
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
	return repairHeaderArguments(raw, "title", 100)
}

func repairHeaderArguments(raw json.RawMessage, subjectKey string, limit int) (json.RawMessage, []string) {
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
	if err != nil {
		return raw, nil
	}
	return repaired, repairs
}

// ShortenFirst reports whether err is only a bullet over the word limit or a
// header over the character limit. The framework then asks the model to
// shorten it once (the retry prompt names the limit and the current length),
// and code cuts it only if the next answer is still too long.
func (d CommitDefinition) ShortenFirst(err error) bool {
	return err != nil && (commitmsg.IsBulletTooLong(err) || commitmsg.IsHeaderTooLong(err))
}
