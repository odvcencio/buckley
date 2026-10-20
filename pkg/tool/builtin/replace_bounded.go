package builtin

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type regexpScan struct {
	re, continuation      *regexp.Regexp
	input                 string
	position, previousEnd int
}

func newRegexpScan(re *regexp.Regexp, input string) (*regexpScan, error) {
	parsed, err := syntax.Parse(re.String(), syntax.Perl)
	if err != nil {
		return nil, fmt.Errorf("preparing replacement pattern: %w", err)
	}
	// A preceding rune preserves ^, multiline and word-boundary context when
	// scanning a suffix. The lazy prefix finds the first match after that rune.
	continuation, err := regexp.Compile(`\A(?s:..*?)(` + parsed.String() + `)`)
	if err != nil {
		return nil, fmt.Errorf("preparing bounded replacement scan: %w", err)
	}
	return &regexpScan{re: re, continuation: continuation, input: input, previousEnd: -1}, nil
}

func (s *regexpScan) reset() { s.position, s.previousEnd = 0, -1 }

func (s *regexpScan) next() []int {
	for s.position <= len(s.input) {
		var match []int
		if s.position == 0 {
			match = s.re.FindStringSubmatchIndex(s.input)
		} else {
			_, width := utf8.DecodeLastRuneInString(s.input[:s.position])
			base := s.position - width
			if found := s.continuation.FindStringSubmatchIndex(s.input[base:]); found != nil {
				match = found[2:]
				for i, index := range match {
					if index >= 0 {
						match[i] += base
					}
				}
			}
		}
		if match == nil {
			return nil
		}
		accept := true
		if match[1] == s.position {
			accept = match[0] != s.previousEnd
			_, width := utf8.DecodeRuneInString(s.input[s.position:])
			if width == 0 {
				s.position = len(s.input) + 1
			} else {
				s.position += width
			}
		} else {
			s.position = match[1]
		}
		s.previousEnd = match[1]
		if accept {
			return match
		}
	}
	return nil
}

type replacementPart struct {
	literal string
	group   int
}

func replacementParts(re *regexp.Regexp, template string, expand bool) []replacementPart {
	if !expand || !strings.Contains(template, "$") {
		return []replacementPart{{literal: template, group: -1}}
	}
	var parts []replacementPart
	var literal strings.Builder
	flush := func() {
		if literal.Len() > 0 {
			parts = append(parts, replacementPart{literal: literal.String(), group: -1})
			literal.Reset()
		}
	}
	for i := 0; i < len(template); {
		if template[i] != '$' {
			literal.WriteByte(template[i])
			i++
			continue
		}
		if i+1 < len(template) && template[i+1] == '$' {
			literal.WriteByte('$')
			i += 2
			continue
		}
		start := i + 1
		braced := start < len(template) && template[start] == '{'
		if braced {
			start++
		}
		end := start
		for end < len(template) {
			r, width := utf8.DecodeRuneInString(template[end:])
			if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				break
			}
			end += width
		}
		if end == start || (braced && (end == len(template) || template[end] != '}')) {
			literal.WriteByte('$')
			i++
			continue
		}
		name := template[start:end]
		group, err := strconv.Atoi(name)
		if err != nil || len(name) > 9 || (len(name) > 1 && name[0] == '0') {
			group = re.SubexpIndex(name)
		}
		flush()
		if group >= 0 && group <= re.NumSubexp() {
			parts = append(parts, replacementPart{group: group})
		}
		i = end
		if braced {
			i++
		}
	}
	flush()
	return parts
}

func replaceRegexpBounded(re *regexp.Regexp, input, replacement string, maxReplacements int, limit int64) (string, int, error) {
	scan, err := newRegexpScan(re, input)
	if err != nil {
		return "", 0, err
	}
	first := scan.next()
	if first == nil {
		return input, 0, nil
	}
	parts := replacementParts(re, replacement, maxReplacements <= 0)
	count, removed, inserted := 0, int64(0), int64(0)
	for match := first; match != nil; match = scan.next() {
		count++
		removed += int64(match[1] - match[0])
		for _, part := range parts {
			length := len(part.literal)
			if part.group >= 0 {
				index := part.group * 2
				length = 0
				if match[index] >= 0 {
					length = match[index+1] - match[index]
				}
			}
			if int64(length) > limit-inserted {
				return "", 0, mutationBudgetError(limit)
			}
			inserted += int64(length)
		}
		if maxReplacements > 0 && count == maxReplacements {
			break
		}
	}
	if count == 0 {
		return input, 0, nil
	}
	remaining := int64(len(input)) - removed
	if remaining > limit-inserted {
		return "", 0, mutationBudgetError(limit)
	}
	size := remaining + inserted
	var output strings.Builder
	output.Grow(int(size))
	scan.reset()
	end := 0
	for i := 0; i < count; i++ {
		match := scan.next()
		output.WriteString(input[end:match[0]])
		for _, part := range parts {
			if part.group < 0 {
				output.WriteString(part.literal)
			} else {
				index := part.group * 2
				if match[index] >= 0 {
					output.WriteString(input[match[index]:match[index+1]])
				}
			}
		}
		end = match[1]
	}
	output.WriteString(input[end:])
	return output.String(), count, nil
}
