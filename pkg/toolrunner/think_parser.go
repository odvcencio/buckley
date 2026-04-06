package toolrunner

// ThinkTagParser parses streaming content for <think> tags,
// routing reasoning content and regular text to separate callbacks.
type ThinkTagParser struct {
	onReasoning    func(string)
	onText         func(string)
	onReasoningEnd func()

	buffer       []byte
	inThinkTag   bool
	hasReasoning bool
}

// NewThinkTagParser creates a parser that routes content to callbacks.
func NewThinkTagParser(onReasoning, onText func(string), onReasoningEnd func()) *ThinkTagParser {
	return &ThinkTagParser{
		onReasoning:    onReasoning,
		onText:         onText,
		onReasoningEnd: onReasoningEnd,
	}
}

// Write processes a chunk of streaming content.
func (p *ThinkTagParser) Write(chunk string) {
	for i := 0; i < len(chunk); i++ {
		c := chunk[i]
		p.buffer = append(p.buffer, c)

		if p.inThinkTag {
			if p.bufferEndsWith("</think>") {
				content := string(p.buffer[:len(p.buffer)-8])
				if content != "" {
					p.onReasoning(content)
					p.hasReasoning = true
				}
				p.buffer = p.buffer[:0]
				p.inThinkTag = false
				if p.hasReasoning {
					p.onReasoningEnd()
					p.hasReasoning = false
				}
			}
		} else {
			if p.bufferEndsWith("<think>") {
				content := string(p.buffer[:len(p.buffer)-7])
				if content != "" {
					p.onText(content)
				}
				p.buffer = p.buffer[:0]
				p.inThinkTag = true
			}
		}
	}

	p.flushSafeContent()
}

func (p *ThinkTagParser) bufferEndsWith(suffix string) bool {
	if len(p.buffer) < len(suffix) {
		return false
	}
	return string(p.buffer[len(p.buffer)-len(suffix):]) == suffix
}

func (p *ThinkTagParser) flushSafeContent() {
	maxPartial := 8
	if len(p.buffer) <= maxPartial {
		return
	}

	safeLen := len(p.buffer) - maxPartial
	safe := string(p.buffer[:safeLen])
	p.buffer = p.buffer[safeLen:]

	if safe != "" {
		if p.inThinkTag {
			p.onReasoning(safe)
			p.hasReasoning = true
		} else {
			p.onText(safe)
		}
	}
}

// Flush emits any remaining buffered content.
func (p *ThinkTagParser) Flush() {
	if len(p.buffer) == 0 {
		return
	}

	content := string(p.buffer)
	p.buffer = p.buffer[:0]

	if content != "" {
		if p.inThinkTag {
			p.onReasoning(content)
			p.hasReasoning = true
		} else {
			p.onText(content)
		}
	}

	if p.inThinkTag && p.hasReasoning {
		p.onReasoningEnd()
		p.hasReasoning = false
	}
	p.inThinkTag = false
}
