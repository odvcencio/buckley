package main

import "errors"

type exitCoder interface {
	ExitCode() int
}

type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e exitError) Unwrap() error {
	return e.err
}

func (e exitError) ExitCode() int {
	if e.code == 0 {
		return 1
	}
	return e.code
}

func withExitCode(err error, code int) error {
	if err == nil {
		return nil
	}
	return exitError{code: code, err: err}
}

func exitCodeForError(err error) int {
	if err == nil {
		return 0
	}
	var coded exitCoder
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return 1
}
