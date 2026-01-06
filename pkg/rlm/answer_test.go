package rlm

import "testing"

func TestAnswerNormalize(t *testing.T) {
	answer := Answer{
		Confidence: 1.5,
		Iteration:  -2,
		TokensUsed: -10,
	}
	answer.Normalize()

	if answer.Confidence != 1 {
		t.Fatalf("expected confidence clamped to 1, got %v", answer.Confidence)
	}
	if answer.Iteration != 0 {
		t.Fatalf("expected iteration to clamp to 0, got %d", answer.Iteration)
	}
	if answer.TokensUsed != 0 {
		t.Fatalf("expected tokens to clamp to 0, got %d", answer.TokensUsed)
	}
}
