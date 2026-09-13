package main

import "testing"

func TestMinPositiveLimit(t *testing.T) {
	tests := []struct {
		name  string
		input []int
		want  int
	}{
		{name: "empty input", input: nil, want: 0},
		{name: "empty slice", input: []int{}, want: 0},
		{name: "zero", input: []int{0}, want: 0},
		{name: "all zeros", input: []int{0, 0, 0}, want: 0},
		{name: "all negatives", input: []int{-5, -1, -10}, want: 0},
		{name: "positive only", input: []int{7}, want: 7},
		{name: "mixed positives", input: []int{9, 3, 5}, want: 3},
		{name: "zeros and negatives mixed with positives", input: []int{0, -4, 8, -1, 5, 0}, want: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := minPositiveLimit(tt.input...); got != tt.want {
				t.Errorf("minPositiveLimit(%v) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
