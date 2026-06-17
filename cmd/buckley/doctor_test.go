package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/chatcheck"
)

func TestRunDoctorCommandUnknown(t *testing.T) {
	err := runDoctorCommand([]string{"bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown doctor command") {
		t.Fatalf("err=%v want unknown doctor command", err)
	}
}

func TestPrintChatCheckResult(t *testing.T) {
	var out bytes.Buffer
	printChatCheckResult(&out, &chatcheck.Result{
		Turns: []chatcheck.TurnResult{{
			Index:      1,
			Model:      "test-model",
			Latency:    1500 * time.Millisecond,
			CharLength: 32,
			Finish:     "stop",
		}},
	})
	got := out.String()
	for _, want := range []string{"[ok] turn 1", "1.5s", "32 chars", "model=test-model", "finish=stop"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %s", want, got)
		}
	}
}
