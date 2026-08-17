package storage

import (
	"testing"
	"time"
)

// Timestamps are stored as TEXT and ordered as TEXT, so the serialized form has
// to sort the same way the instants do.
//
// time.RFC3339Nano removes trailing zeros. A time whose nanoseconds end in zero
// then serializes shorter than its neighbours, and the shorter string sorts
// after the longer one because "Z" is above any digit in ASCII. Message history
// came back out of order whenever that happened, and LIMIT/OFFSET paging over
// the same ORDER BY could repeat or skip a row.

func TestSQLiteTimestampSortsChronologically(t *testing.T) {
	base := time.Date(2026, 8, 17, 7, 3, 0, 0, time.UTC)

	// 17433400ns ends in two zeros; 17433497ns does not. The first instant is
	// earlier, so its serialized form must also be smaller.
	earlier := sqliteTimestamp(base.Add(17433400 * time.Nanosecond))
	later := sqliteTimestamp(base.Add(17433497 * time.Nanosecond))

	if !(earlier < later) {
		t.Fatalf("earlier timestamp does not sort first:\n  earlier = %q\n  later   = %q", earlier, later)
	}
}

func TestSQLiteTimestampWidthIsFixed(t *testing.T) {
	base := time.Date(2026, 8, 17, 7, 3, 0, 0, time.UTC)
	widths := map[int]string{}
	for _, ns := range []int{0, 1, 10, 1000, 100000000, 123456789, 999999999, 17433400} {
		s := sqliteTimestamp(base.Add(time.Duration(ns)))
		widths[len(s)] = s
	}
	if len(widths) != 1 {
		t.Fatalf("serialized timestamps vary in width, so text ordering is unreliable: %v", widths)
	}
}

// TestSQLiteTimestampOrderingIsTotal walks a range of nanosecond values, many
// of which end in zero, and checks that sorting the strings agrees with sorting
// the instants.
func TestSQLiteTimestampOrderingIsTotal(t *testing.T) {
	base := time.Date(2026, 8, 17, 7, 3, 0, 0, time.UTC)
	prev := sqliteTimestamp(base)
	for ns := 1; ns < 2000; ns++ {
		cur := sqliteTimestamp(base.Add(time.Duration(ns) * 500000))
		if !(prev < cur) {
			t.Fatalf("ordering breaks at ns=%d:\n  prev = %q\n  cur  = %q", ns, prev, cur)
		}
		prev = cur
	}
}
