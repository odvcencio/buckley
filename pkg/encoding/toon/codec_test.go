package toon

import "testing"

type sample struct {
	Message string
	Count   int
}

func TestCodecProducesToonPayload(t *testing.T) {
	codec := New(true)
	value := sample{Message: "hello", Count: 3}

	data, err := codec.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	if string(data) == "" || data[0] == '{' {
		t.Fatalf("expected TOON output, got %q", string(data))
	}
}

func TestCodecJSONRoundTrip(t *testing.T) {
	codec := New(false)
	value := sample{Message: "json", Count: 1}

	data, err := codec.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded sample
	if err := codec.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decoded != value {
		t.Fatalf("round trip mismatch: %+v vs %+v", decoded, value)
	}
}

func TestContainsTOON_DetectsArrayHeader(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "array header",
			input:    "results[3]{key,type,summary}:",
			expected: true,
		},
		{
			name:     "object header",
			input:    "data{success,error}:",
			expected: true,
		},
		{
			name:     "plain text",
			input:    "This is a normal response.",
			expected: false,
		},
		{
			name:     "json",
			input:    `{"success": true, "data": []}`,
			expected: false,
		},
		{
			name:     "mixed content",
			input:    "Here are the results:\nresults[2]{name,value}:\n  alice,100\n  bob,200",
			expected: true,
		},
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainsTOON(tt.input)
			if got != tt.expected {
				t.Errorf("ContainsTOON(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSanitizeOutput_RemovesTOONFragments(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain text unchanged",
			input:    "This is a normal response.",
			expected: "This is a normal response.",
		},
		{
			name:     "removes array header and data",
			input:    "Here are results:\nresults[2]{name,value}:\n  alice,100\n  bob,200\nDone!",
			expected: "Here are results:\nDone!",
		},
		{
			name:     "removes object header",
			input:    "Status:\ndata{success,error}:\n  true,null\nAll good.",
			expected: "Status:\nAll good.",
		},
		{
			name:     "empty input",
			input:    "",
			expected: "",
		},
		{
			name:     "pure TOON",
			input:    "results[1]{key}:\n  test",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeOutput(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeOutput() =\n%q\nwant\n%q", got, tt.expected)
			}
		})
	}
}

func TestFormatForDisplay_FormatsReadably(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{
			name:     "plain text unchanged",
			input:    "Normal response",
			contains: "Normal response",
		},
		{
			name:     "mixed content sanitized",
			input:    "Result:\ndata{a,b}:\n  1,2\nEnd",
			contains: "End",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatForDisplay(tt.input)
			if got == "" && tt.contains != "" {
				t.Errorf("FormatForDisplay() returned empty, want content containing %q", tt.contains)
			}
		})
	}
}
