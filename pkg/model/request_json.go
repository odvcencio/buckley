package model

import "encoding/json"

func marshalChatRequest(req ChatRequest) ([]byte, error) {
	type requestJSON ChatRequest
	type messageJSON Message
	var messages []messageJSON
	if req.Messages != nil {
		messages = make([]messageJSON, len(req.Messages))
		for i, message := range req.Messages {
			messages[i] = messageJSON(message)
		}
	}

	// Shadow the prefix through Messages to preserve field order while letting
	// the outer encoder write messages directly, without Message.MarshalJSON.
	return json.Marshal(struct {
		Model    string        `json:"model"`
		Models   []string      `json:"models,omitempty"`
		Messages []messageJSON `json:"messages"`
		*requestJSON
	}{req.Model, req.Models, messages, (*requestJSON)(&req)})
}
