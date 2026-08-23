package runledger

import (
	"context"
	"errors"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
)

func TestMonitorUnavailableSentinel(t *testing.T) {
	var store *SQLiteStore
	tests := []struct {
		name string
		call func() error
	}{
		{name: "get routine", call: func() error {
			_, err := store.GetRoutineStatus(context.Background(), "session-01", "run-01")
			return err
		}},
		{name: "list routines", call: func() error {
			_, err := store.ListRoutineStatuses(context.Background(), agentcoord.RoutineQuery{SessionID: "session-01"})
			return err
		}},
		{name: "list mailbox", call: func() error {
			_, err := store.ListMailboxStatuses(context.Background(), agentcoord.MailboxStatusQuery{SessionID: "session-01", RunID: "run-01"})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, ErrMonitorUnavailable) {
				t.Fatalf("error=%v want ErrMonitorUnavailable", err)
			}
		})
	}
}
