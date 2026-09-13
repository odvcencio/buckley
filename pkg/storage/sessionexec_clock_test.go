package storage

import (
	"context"
	"testing"
)

func TestSessionExecClock_DefaultAndOverrideAcrossTransactions(t *testing.T) {
	for _, override := range []bool{false, true} {
		name := "database"
		if override {
			name = "override"
		}
		t.Run(name, func(t *testing.T) {
			store, _ := openAdversarialEffectStore(t, "clock-"+name)
			const fixedMillis int64 = 1234567890000
			if override {
				store.sessionExecClock = func() int64 { return fixedMillis }
			}
			for _, transaction := range []struct {
				name string
				run  func(context.Context, func(*sessionExecConn) error) error
			}{
				{"write", store.withSessionExecWrite},
				{"observation", store.withSessionExecObservation},
			} {
				t.Run(transaction.name, func(t *testing.T) {
					err := transaction.run(context.Background(), func(db *sessionExecConn) error {
						var before, after int64
						if err := db.queryRow("SELECT " + sessionExecNowMillisSQL).Scan(&before); err != nil {
							return err
						}
						now, err := sessionExecNowMillis(db)
						if err != nil {
							return err
						}
						if err := db.queryRow("SELECT " + sessionExecNowMillisSQL).Scan(&after); err != nil {
							return err
						}
						if override {
							if now != fixedMillis {
								t.Errorf("clock = %d, want override %d", now, fixedMillis)
							}
						} else if now < before || now > after {
							t.Errorf("clock = %d, outside SQLite interval [%d, %d]", now, before, after)
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}
