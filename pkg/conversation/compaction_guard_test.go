package conversation

import "testing"

func TestSelectCompactionSegmentsProtectsSystemMessages(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "steering/persona"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "more"},
		{Role: "assistant", Content: "reply"},
	}

	toSummarize, toKeep, err := selectCompactionSegments(msgs)
	if err != nil {
		t.Fatalf("selectCompactionSegments error: %v", err)
	}

	for _, msg := range toSummarize {
		if msg.Role == "system" {
			t.Fatalf("system messages should not be summarized")
		}
	}

	protectedFound := false
	for _, msg := range toKeep {
		if msg.Role == "system" && msg.Content == "steering/persona" {
			protectedFound = true
		}
	}
	if !protectedFound {
		t.Fatalf("expected steering/system message to be retained in toKeep")
	}
}
