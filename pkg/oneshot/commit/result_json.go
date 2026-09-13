package commit

import "encoding/json"

// UnmarshalJSON accepts a scalar body as one bullet, while preserving the
// canonical array representation and the normal validation of every field.
func (cr *CommitResult) UnmarshalJSON(data []byte) error {
	type resultAlias CommitResult
	var decoded resultAlias
	wire := struct {
		*resultAlias
		Body json.RawMessage `json:"body"`
	}{resultAlias: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if len(wire.Body) > 0 {
		if err := json.Unmarshal(wire.Body, &decoded.Body); err != nil {
			var body string
			if scalarErr := json.Unmarshal(wire.Body, &body); scalarErr != nil {
				return err
			}
			decoded.Body = []string{body}
		}
	}
	*cr = CommitResult(decoded)
	return nil
}
