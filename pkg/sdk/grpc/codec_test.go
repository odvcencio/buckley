package grpcsdk

import (
	"reflect"
	"testing"
)

func TestJSONCodecRoundTrip(t *testing.T) {
	codec := jsonCodec{}
	type sample struct {
		Message string
		Count   int
	}
	input := sample{
		Message: "hello",
		Count:   3,
	}

	data, err := codec.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded sample
	if err := codec.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if !reflect.DeepEqual(input, decoded) {
		t.Fatalf("round trip mismatch: got %+v want %+v", decoded, input)
	}

	if name := codec.Name(); name != "json" {
		t.Fatalf("Name() = %s, want json", name)
	}
}
