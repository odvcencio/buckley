package storage

import (
	"errors"
	"fmt"
	"testing"
)

type sqliteCodeTestError struct {
	code int
}

func (e sqliteCodeTestError) Error() string {
	return fmt.Sprintf("sqlite code %d", e.code)
}

func (e sqliteCodeTestError) Code() int {
	return e.code
}

type cyclicSQLiteError struct {
	code     int
	children []error
}

func (e *cyclicSQLiteError) Error() string {
	return "cyclic SQLite error"
}

func (e *cyclicSQLiteError) Code() int {
	return e.code
}

func (e *cyclicSQLiteError) Unwrap() []error {
	return e.children
}

type cyclicErrorSlice []error

func (cyclicErrorSlice) Error() string {
	return "cyclic error slice"
}

func (e cyclicErrorSlice) Unwrap() []error {
	return e
}

type interfaceErrorWrapper struct {
	child error
}

func (interfaceErrorWrapper) Error() string {
	return "interface error wrapper"
}

func (e interfaceErrorWrapper) Unwrap() error {
	return e.child
}

func TestIsSQLiteBusyError_PrimaryAndExtendedCodes(t *testing.T) {
	tests := []struct {
		name string
		code int
		want bool
	}{
		{name: "busy", code: 5, want: true},
		{name: "locked", code: 6, want: true},
		{name: "busy recovery", code: 261, want: true},
		{name: "busy timeout", code: 773, want: true},
		{name: "locked shared cache", code: 262, want: true},
		{name: "generic error", code: 1, want: false},
		{name: "constraint", code: 19, want: false},
		{name: "constraint unique", code: 2067, want: false},
	}

	wrappers := []struct {
		name string
		wrap func(error) error
	}{
		{name: "direct", wrap: func(err error) error { return err }},
		{name: "wrapped", wrap: func(err error) error { return fmt.Errorf("outer: %w", err) }},
		{name: "joined", wrap: func(err error) error { return errors.Join(errors.New("other"), err) }},
	}

	for _, test := range tests {
		for _, wrapper := range wrappers {
			t.Run(test.name+"/"+wrapper.name, func(t *testing.T) {
				err := wrapper.wrap(sqliteCodeTestError{code: test.code})
				if got := IsSQLiteBusyError(err); got != test.want {
					t.Fatalf("IsSQLiteBusyError(code %d) = %v, want %v", test.code, got, test.want)
				}
			})
		}
	}
}

func TestIsSQLiteBusyError_JoinedCodesAreOrderIndependent(t *testing.T) {
	permanent := sqliteCodeTestError{code: 1}
	busy := sqliteCodeTestError{code: 5}
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "permanent then busy", err: errors.Join(permanent, busy)},
		{name: "busy then permanent", err: errors.Join(busy, permanent)},
		{
			name: "nested joins and ordinary wrappers",
			err: fmt.Errorf("outer: %w", errors.Join(
				permanent,
				fmt.Errorf("middle: %w", errors.Join(
					sqliteCodeTestError{code: 19},
					fmt.Errorf("inner: %w", sqliteCodeTestError{code: 773}),
				)),
			)),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !IsSQLiteBusyError(test.err) {
				t.Fatal("IsSQLiteBusyError() = false, want true")
			}
		})
	}
}

func TestIsSQLiteBusyError_JoinedPermanentCodes(t *testing.T) {
	err := errors.Join(
		sqliteCodeTestError{code: 1},
		fmt.Errorf("wrapped: %w", sqliteCodeTestError{code: 19}),
		errors.Join(sqliteCodeTestError{code: 2067}, errors.New("plain")),
	)
	if IsSQLiteBusyError(err) {
		t.Fatal("IsSQLiteBusyError() = true, want false")
	}
}

func TestIsSQLiteBusyError_CyclesAreBounded(t *testing.T) {
	t.Run("comparable wrapper with noncomparable child", func(t *testing.T) {
		wrapped := interfaceErrorWrapper{
			child: cyclicErrorSlice{sqliteCodeTestError{code: 5}},
		}
		if !IsSQLiteBusyError(wrapped) {
			t.Fatal("IsSQLiteBusyError() = false, want true")
		}
	})

	t.Run("comparable cycle with busy sibling", func(t *testing.T) {
		cycle := &cyclicSQLiteError{code: 1}
		cycle.children = []error{cycle, sqliteCodeTestError{code: 262}}
		if !IsSQLiteBusyError(cycle) {
			t.Fatal("IsSQLiteBusyError() = false, want true")
		}
	})

	t.Run("comparable permanent cycle", func(t *testing.T) {
		cycle := &cyclicSQLiteError{code: 1}
		cycle.children = []error{cycle}
		if IsSQLiteBusyError(cycle) {
			t.Fatal("IsSQLiteBusyError() = true, want false")
		}
	})

	t.Run("noncomparable permanent cycle", func(t *testing.T) {
		cycle := make(cyclicErrorSlice, 1)
		cycle[0] = cycle
		if IsSQLiteBusyError(cycle) {
			t.Fatal("IsSQLiteBusyError() = true, want false")
		}
	})
}

func TestIsSQLiteBusyError_NoSQLiteCode(t *testing.T) {
	if IsSQLiteBusyError(nil) {
		t.Fatal("IsSQLiteBusyError(nil) = true, want false")
	}
	if IsSQLiteBusyError(errors.New("not a SQLite error")) {
		t.Fatal("IsSQLiteBusyError(plain error) = true, want false")
	}
}
