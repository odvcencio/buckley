package toon

import (
	"encoding/json"
	"math"
	"testing"
)

func TestCodecMarshalString_PreservesEncodingAndByteOwnership(t *testing.T) {
	var missing *sample
	for _, tc := range []struct {
		name  string
		value any
		toon  string
	}{
		{"nil", nil, "null"},
		{"typed nil", missing, "null"},
		{"primitive", "hello", "hello"},
		{"number", 3, "3"},
		{"object", map[string]any{"message": "hello", "count": 3}, "count: 3\nmessage: hello"},
		{"struct", sample{Message: "hello", Count: 3}, "Count: 3\nMessage: hello"},
		{"records", []map[string]any{{"name": "one", "value": 1}, {"name": "two", "value": 2}}, "[2]{name,value}:\n  one,1\n  two,2"},
		{"empty object", map[string]any{}, ""},
		{"unsupported", make(chan int), "null"},
		{"nonfinite", math.Inf(1), "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, useToon := range []bool{false, true} {
				codec := New(useToon)
				want := tc.toon
				if !useToon {
					data, err := json.Marshal(tc.value)
					if err != nil {
						encoded, gotErr := codec.MarshalString(tc.value)
						if gotErr == nil || gotErr.Error() != err.Error() || encoded != "" {
							t.Fatalf("JSON error changed: %q %v", encoded, gotErr)
						}
						continue
					}
					want = string(data)
				}
				encoded, err := codec.MarshalString(tc.value)
				if err != nil || encoded != want {
					t.Fatalf("toon=%t: got %q %v, want %q", useToon, encoded, err, want)
				}
				data, err := codec.Marshal(tc.value)
				if err != nil || string(data) != want {
					t.Fatalf("byte encoding changed: %q %v", data, err)
				}
				if len(data) > 0 {
					data[0] ^= 1
					if encoded != want {
						t.Fatal("byte mutation changed the encoded string")
					}
				}
			}
		})
	}
}
