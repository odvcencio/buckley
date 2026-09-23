package completioncheck

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestDecide_Boundaries(t *testing.T) {
	base := Evidence{Action: .95, Unfinished: .95, Authorized: .95, NeedsUser: .01, Model: Model}
	for _, tt := range []struct {
		name    string
		change  func(*Evidence)
		want    string
		invalid bool
	}{
		{"unfinished", func(*Evidence) {}, "continue", false},
		{"complete", func(e *Evidence) { e.Unfinished = .01 }, "stop", false},
		{"discussion", func(e *Evidence) { e.Action = .05 }, "stop", false},
		{"approval", func(e *Evidence) { e.NeedsUser = .95 }, "stop", false},
		{"uncertain", func(e *Evidence) { e.Unfinished = .84 }, "stop", false},
		{"no authorization", func(e *Evidence) { e.Authorized = .3 }, "stop", false},
		{"threshold", func(e *Evidence) { e.Action = .85; e.Unfinished = .85; e.Authorized = .85; e.NeedsUser = .2 }, "continue", false},
		{"needs user boundary", func(e *Evidence) { e.NeedsUser = .21 }, "stop", false},
		{"nan", func(e *Evidence) { e.Action = math.NaN() }, "", true},
		{"range", func(e *Evidence) { e.Unfinished = 1.1 }, "", true},
		{"wrong model", func(e *Evidence) { e.Model = "other/model" }, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := base
			tt.change(&e)
			got, err := Decide(e)
			if (err != nil) != tt.invalid || (!tt.invalid && got.Action != tt.want) {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

type panicJudge struct{}

func (panicJudge) Judge(context.Context, Input) (Evidence, error) { panic("must not call provider") }
func TestCheck_RejectsIncompleteContext(t *testing.T) {
	for _, in := range []Input{{}, {Request: "fix", Response: strings.Repeat("x", 12289)}, {Request: strings.Repeat("x", 8193), Response: "done"}} {
		if _, err := Check(context.Background(), panicJudge{}, in); err == nil {
			t.Fatal("expected input rejection")
		}
	}
}
