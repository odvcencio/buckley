package model

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

func TestJSONStringBytes_AllBytes(t *testing.T) {
	for value := 0; value < 256; value++ {
		s := string([]byte{byte(value)})
		want := len(s) + 2
		if value < 128 {
			encoded, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			want = len(encoded)
		}
		if got := jsonStringBytes(s); got != want {
			t.Fatalf("byte %d: got %d want %d", value, got, want)
		}
	}
}

func TestJSONStringBytes_ASCIIStrings(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, length := range []int{0, 1, 31, 128, 4096, 65536} {
		input := make([]byte, length)
		for i := range input {
			input[i] = byte(rng.Intn(128))
		}
		encoded, err := json.Marshal(string(input))
		if err != nil {
			t.Fatal(err)
		}
		if got := jsonStringBytes(string(input)); got != len(encoded) {
			t.Fatalf("length %d: got %d want %d", length, got, len(encoded))
		}
	}
}

func TestJSONStringBytes_PreservesNonASCIIApproximation(t *testing.T) {
	for _, text := range []string{"é ☃", "\u2028\u2029", "\xff\xfe", strings.Repeat("é ☃ \u2028 \xff", 1024)} {
		if got, want := jsonStringBytes(text), len(text)+2; got != want {
			t.Fatalf("UTF-8 byte estimate changed: got %d want %d", got, want)
		}
	}
}
