package builtin

import "strings"

type generatedText struct {
	text  strings.Builder
	limit int64
	err   error
}

func (b *generatedText) append(parts ...string) bool {
	if b.err != nil {
		return false
	}
	for _, part := range parts {
		if int64(len(part)) > b.limit-int64(b.text.Len()) {
			b.err = mutationBudgetError(b.limit)
			return false
		}
		b.text.WriteString(part)
	}
	return true
}

func (b *generatedText) result() (string, error) {
	if b.err != nil {
		return "", b.err
	}
	return b.text.String(), nil
}
