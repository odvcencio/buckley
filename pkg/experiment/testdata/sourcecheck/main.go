// sourcecheck is a trusted command-criterion fixture. It compares model output
// on stdin with caller-supplied golden source rows; it never executes that input.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type row struct {
	Item      string `json:"item"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Quote     string `json:"quote"`
}

func readRows(r io.Reader) ([]row, error) {
	const maxBytes = 256 * 1024
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil || len(body) > maxBytes {
		return nil, fmt.Errorf("unreadable or oversized source rows")
	}
	var rawRows []json.RawMessage
	// Reject unknown fields and trailing prose instead of silently ignoring them.
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rawRows); err != nil {
		return nil, fmt.Errorf("source rows must be a plain JSON array: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("trailing source output")
	}
	rows := make([]row, len(rawRows))
	for i, raw := range rawRows {
		fields := json.NewDecoder(bytes.NewReader(raw))
		opening, err := fields.Token()
		if err != nil || opening != json.Delim('{') {
			return nil, fmt.Errorf("source row must be an object")
		}
		seen := make(map[string]bool)
		for fields.More() {
			key, err := fields.Token()
			if err != nil {
				return nil, err
			}
			name := strings.ToLower(key.(string))
			if seen[name] {
				return nil, fmt.Errorf("duplicate source row field")
			}
			seen[name] = true
			var value json.RawMessage
			if err := fields.Decode(&value); err != nil {
				return nil, err
			}
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&rows[i]); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func fail(message any) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

func main() {
	if len(os.Args) != 2 {
		fail("usage: sourcecheck TRUSTED_GOLD_JSON < model-output.json")
	}
	file, err := os.Open(os.Args[1])
	if err != nil {
		fail(err)
	}
	defer file.Close()
	expected, err := readRows(file)
	if err != nil || len(expected) == 0 {
		fail("invalid or empty trusted source rows")
	}
	actual, err := readRows(os.Stdin)
	if err != nil {
		fail(err)
	}
	if len(actual) != len(expected) {
		fail("source item coverage mismatch")
	}
	remaining := make(map[string]row, len(expected))
	for _, want := range expected {
		if _, exists := remaining[want.Item]; exists || want.Item == "" || want.Path == "" || want.StartLine < 1 || want.EndLine < want.StartLine || want.Quote == "" {
			fail("invalid trusted source row")
		}
		remaining[want.Item] = want
	}
	for _, got := range actual {
		want, exists := remaining[got.Item]
		if !exists || got != want {
			fail("source item, citation, or literal quote mismatch")
		}
		delete(remaining, got.Item)
	}
	fmt.Printf("verified %d source items\n", len(actual))
}
