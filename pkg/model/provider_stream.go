package model

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// MaxProviderStreamBytes bounds one provider response, including ignored lines.
const MaxProviderStreamBytes = 64 << 20

// MaxProviderStreamFrameBytes bounds one SSE or NDJSON line before its newline.
const MaxProviderStreamFrameBytes = 1 << 20

// Stream size violations are terminal to avoid repeating a billable request.
var (
	ErrProviderStreamTooLarge      = fmt.Errorf("model: provider stream exceeds %d-byte limit", MaxProviderStreamBytes)
	ErrProviderStreamFrameTooLarge = fmt.Errorf("model: provider stream frame exceeds %d-byte limit", MaxProviderStreamFrameBytes)
)

func newProviderStreamReader(source io.Reader) *providerResponseReader {
	return &providerResponseReader{source: source, remaining: MaxProviderStreamBytes, limitErr: ErrProviderStreamTooLarge}
}

type providerStreamScanner struct {
	*bufio.Scanner
	reader *providerResponseReader
}

func newProviderStreamScanner(source io.Reader) *providerStreamScanner {
	reader := newProviderStreamReader(source)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), MaxProviderStreamFrameBytes+1)
	return &providerStreamScanner{Scanner: scanner, reader: reader}
}

func (s *providerStreamScanner) Scan() bool {
	// Scanner can return buffered tokens after a read returned bytes and an error.
	// Never accept a completion marker from a body that exceeded its budget.
	return s.Scanner.Scan() && s.reader.remaining >= 0
}

func (s *providerStreamScanner) Err() error {
	if s.reader.remaining < 0 {
		return ErrProviderStreamTooLarge
	}
	if errors.Is(s.Scanner.Err(), bufio.ErrTooLong) {
		return ErrProviderStreamFrameTooLarge
	}
	return s.Scanner.Err()
}
