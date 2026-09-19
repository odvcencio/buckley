package builtin

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestReadFileToolRejectsOverflowingLineNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{math.Ldexp(1, strconv.IntSize-1), math.Inf(1), math.NaN(), int(math.MaxInt), int64(math.MaxInt), json.Number(strconv.FormatInt(int64(math.MaxInt), 10))} {
		t.Run(fmt.Sprintf("%T_%v", value, value), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("read_file panicked instead of returning an error: %v", r)
				}
			}()
			result, err := (&ReadFileTool{}).Execute(map[string]any{"path": path, "start_line": value})
			if err != nil || result == nil || result.Success || result.Error == "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestReadFileLineNumberPreservesIntegerBounds(t *testing.T) {
	for _, value := range []any{1, int64(1), float64(1), float32(1), json.Number("1")} {
		if got, err := readFileLineNumber("start_line", value); err != nil || got != 1 {
			t.Fatalf("valid small %T(%v) decoded as %d, err=%v", value, value, got, err)
		}
	}
	belowLimit := math.Nextafter(math.Ldexp(1, strconv.IntSize-1), 0)
	if math.Trunc(belowLimit) == belowLimit {
		if got, err := readFileLineNumber("start_line", belowLimit); err != nil || got != int(belowLimit) {
			t.Fatalf("representable boundary decoded as %d, err=%v", got, err)
		}
	}
	for _, value := range []any{int(math.MaxInt), int64(math.MaxInt), json.Number(strconv.FormatInt(int64(math.MaxInt), 10))} {
		got, err := readFileLineNumber("end_line", value)
		if err != nil || got != math.MaxInt {
			t.Fatalf("%T(%v) decoded as %d, err=%v", value, value, got, err)
		}
	}
	for _, value := range []any{math.Ldexp(1, strconv.IntSize-1), float32(math.Inf(1)), math.NaN(), 0, -1, int64(-1), json.Number("9223372036854775808"), json.Number("1.5")} {
		if got, err := readFileLineNumber("start_line", value); err == nil {
			t.Fatalf("accepted invalid %T(%v) as %d", value, value, got)
		}
	}
}

func TestReadFilePageBoundsDoNotOverflow(t *testing.T) {
	start, end, explicit, err := readFilePage(map[string]any{"start_line": math.MaxInt - 10}, math.MaxInt)
	if err != nil || start != math.MaxInt-10 || end != math.MaxInt || !explicit {
		t.Fatalf("page=%d-%d explicit=%v err=%v", start, end, explicit, err)
	}
	start, end, _, err = readFilePage(map[string]any{"start_line": 1, "end_line": math.MaxInt}, 250)
	if err != nil || start != 1 || end != 100 {
		t.Fatalf("bounded page=%d-%d err=%v", start, end, err)
	}
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("line\n", 250)), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := (&ReadFileTool{}).Execute(map[string]any{"path": path, "end_line": math.MaxInt})
	if err != nil || result == nil || !result.Success || !result.ShouldAbridge {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	page := result.DisplayData["page"].(map[string]any)
	if page["end_line"] != 100 || page["next_start_line"] != 101 {
		t.Fatalf("bad continuation: %+v", page)
	}
}
