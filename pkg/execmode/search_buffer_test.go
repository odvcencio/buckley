package execmode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReadBoundedFileInto_ReusesStorage(t *testing.T) {
	buffer := bytes.Repeat([]byte("stale"), 32)
	for _, tc := range []struct {
		name string
		hint int64
		size int
	}{
		{"stable", 7, 7},
		{"shrunk", 30, 3},
		{"grown", 1, 20},
		{"empty", 0, 0},
		{"empty_grown", 0, 40},
		{"past_eof", -10, 0},
		{"exact_limit", 31, 32},
		{"over_limit", 32, 40},
		{"huge_hint", math.MaxInt64, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := strings.Repeat("n", tc.size)
			result, err := readBoundedFileInto(strings.NewReader(source), tc.hint, 32, buffer)
			if err != nil || string(result) != source[:min(tc.size, 32)] || cap(result) > 32 {
				t.Fatalf("stale or unbounded read: bytes=%d cap=%d error=%v", len(result), cap(result), err)
			}
			if len(result) > 0 && &result[0] != &buffer[0] {
				t.Fatal("allocated despite sufficient caller-owned capacity")
			}
		})
	}
}

func TestReadBoundedFileInto_GrowsWithinBudget(t *testing.T) {
	var buffer []byte
	for _, size := range []int{0, 3, 7, 8, 30, 31, 32, 80, 1} {
		source := strings.Repeat("x", size)
		var err error
		buffer, err = readBoundedFileInto(strings.NewReader(source), 0, 32, buffer)
		if err != nil || string(buffer) != source[:min(size, 32)] || cap(buffer) > 32 {
			t.Fatalf("size=%d bytes=%d cap=%d error=%v", size, len(buffer), cap(buffer), err)
		}
	}
	reader := io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(io.ErrClosedPipe))
	buffer, err := readBoundedFileInto(reader, 0, 32, buffer)
	if !errors.Is(err, io.ErrClosedPipe) || cap(buffer) > 32 {
		t.Fatalf("read error lost: %v", err)
	}
	buffer, err = readBoundedFileInto(strings.NewReader("fresh"), 20, 32, buffer)
	if err != nil || string(buffer) != "fresh" {
		t.Fatalf("partial data escaped into next file: %q %v", buffer, err)
	}
}

func TestBrokerSearchText_BufferDoesNotAliasResults(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"000.txt": "first needle before\n",
		"001.txt": strings.Repeat("x", 256<<10) + "needle large\n",
		"002.txt": "second needle\n",
		"003.txt": "",
		"004.txt": "\x00needle binary\n",
		"005.txt": "last needle\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	broker, err := NewBroker(root, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]any{"pattern": "needle", "glob": "*.txt"}
	first, err := broker.searchText(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var decoded excerptSearchResult
	if err := json.Unmarshal(before, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Matches) != 4 || decoded.Capped {
		t.Fatal("buffer exposed stale empty/binary file data")
	}
	want := []string{"first needle before", "needle large", "second needle", "last needle"}
	for i, hit := range decoded.Matches {
		if hit.Line != 1 || !strings.HasSuffix(hit.Text, want[i]) || hit.TextTruncated != (i == 1) {
			t.Fatalf("earlier match overwritten: %+v", hit)
		}
	}
	for name, content := range map[string]string{"000.txt": "changed needle\n", "001.txt": "", "002.txt": "no hit\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	second, err := broker.searchText(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Matches) != 2 || decoded.Matches[0].Text != "changed needle" || decoded.Matches[1].Text != "last needle" {
		t.Fatal("search reused stale file contents")
	}
	after, err := json.Marshal(first)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("another search mutated retained results")
	}
}
