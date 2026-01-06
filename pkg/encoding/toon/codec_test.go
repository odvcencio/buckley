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
