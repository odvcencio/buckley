package scrollback

// SemanticMark identifies a conversational turn on the rendered scroll axis.
type SemanticMark struct {
	Row    int
	Source string
}

// SemanticMarks returns the first rendered row for each user or assistant turn.
// Buckley's chat renderer inserts a blank separator for each conversational
// message, which gives the scrollbar stable bookmark positions after wrapping.
func (b *Buffer) SemanticMarks() []SemanticMark {
	b.mu.RLock()
	defer b.mu.RUnlock()

	marks := make([]SemanticMark, 0, len(b.lines)/4)
	row := 0
	lastSource := ""
	for _, line := range b.lines {
		conversationSource := line.Source == "user" || line.Source == "assistant"
		if conversationSource && (line.Content == "" || line.Source != lastSource) {
			if len(marks) == 0 || marks[len(marks)-1].Row != row || marks[len(marks)-1].Source != line.Source {
				marks = append(marks, SemanticMark{Row: row, Source: line.Source})
			}
		}
		if conversationSource && line.Content != "" {
			lastSource = line.Source
		}
		row += len(line.Wrapped)
	}
	return marks
}

// ScrollToFraction moves the viewport to a relative point on the scroll axis.
func (b *Buffer) ScrollToFraction(numerator, denominator int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if denominator <= 0 {
		return
	}
	if numerator < 0 {
		numerator = 0
	}
	if numerator > denominator {
		numerator = denominator
	}
	maxScroll := max(0, b.totalRows-b.height)
	b.scrollTop = (maxScroll * numerator) / denominator
	b.clampScroll()
	if b.scrollTop >= maxScroll {
		b.scrollMode = ScrollModeFollow
	} else {
		b.scrollMode = ScrollModeManual
	}
}
