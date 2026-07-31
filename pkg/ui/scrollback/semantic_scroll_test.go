package scrollback

import "testing"

func TestSemanticMarksTrackConversationTurns(t *testing.T) {
	buffer := NewBuffer(40, 4)
	buffer.AppendMessage([]Line{{Content: "", Source: "user"}, {Content: "hello", Source: "user"}})
	buffer.AppendMessage([]Line{{Content: "", Source: "assistant"}, {Content: "answer", Source: "assistant"}})
	marks := buffer.SemanticMarks()
	if len(marks) != 2 {
		t.Fatalf("marks = %+v, want two turns", marks)
	}
	if marks[0].Source != "user" || marks[1].Source != "assistant" {
		t.Fatalf("unexpected marks: %+v", marks)
	}
}

func TestScrollToFractionMovesViewport(t *testing.T) {
	buffer := NewBuffer(20, 4)
	for i := 0; i < 20; i++ {
		buffer.AppendLine("line", LineStyle{}, "system")
	}
	buffer.ScrollToFraction(1, 2)
	top, total, height := buffer.ScrollPosition()
	if top <= 0 || top >= total-height {
		t.Fatalf("top = %d, want an interior position (total=%d height=%d)", top, total, height)
	}
}
